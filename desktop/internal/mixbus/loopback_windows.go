//go:build windows

package mixbus

// Per-process loopback capture.
//
// Windows 10 2004+ can hand out an IAudioClient that captures only what a
// given process (and, with INCLUDE_TARGET_PROCESS_TREE, all of its child
// processes) renders — independent of which output device it plays to.
// That client cannot be obtained through IMMDevice::Activate; it comes from
// ActivateAudioInterfaceAsync on the virtual device "VAD\Process_Loopback",
// which go-wca does not wrap. The async API wants a COM completion handler,
// so a minimal one is hand-built below: a 4-slot vtable backed by Go
// callbacks, living in LocalAlloc'd memory so the GC never frees or scans
// it while Windows still holds a reference.

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

const (
	vtBlob = 65 // VT_BLOB

	activationTypeProcessLoopback = 1 // AUDIOCLIENT_ACTIVATION_TYPE_PROCESS_LOOPBACK
	loopbackModeIncludeTree       = 0 // PROCESS_LOOPBACK_MODE_INCLUDE_TARGET_PROCESS_TREE

	eNoInterface = 0x80004002
	activateWait = 5000 // ms
)

var (
	modMmdevapi                 = windows.NewLazySystemDLL("Mmdevapi.dll")
	procActivateAudioInterfaceA = modMmdevapi.NewProc("ActivateAudioInterfaceAsync")

	iidCompletionHandler = ole.NewGUID("{41D949AB-9862-444A-80F6-C261334DA5EB}")
	iidAgileObject       = ole.NewGUID("{94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90}")

	processLoopbackPath, _ = windows.UTF16PtrFromString(`VAD\Process_Loopback`)
)

// AUDIOCLIENT_ACTIVATION_PARAMS with the PROCESS_LOOPBACK_PARAMS arm.
type activationParams struct {
	activationType uint32
	targetPID      uint32
	loopbackMode   uint32
}

// PROPVARIANT holding a BLOB (x64 layout: 8-byte header, ULONG size,
// 4 bytes padding, pointer).
type propVariantBlob struct {
	vt       uint16
	_        [3]uint16
	cbSize   uint32
	_        uint32
	blobData uintptr
}

// ---------- IActivateAudioInterfaceCompletionHandler ----------

type completionHandler struct {
	vtbl  *handlerVtbl
	refs  int32
	event windows.Handle
}

type handlerVtbl struct {
	queryInterface    uintptr
	addRef            uintptr
	release           uintptr
	activateCompleted uintptr
}

var (
	vtblOnce   sync.Once
	sharedVtbl *handlerVtbl // global → never collected; Go's GC never moves
)

func handlerVtable() *handlerVtbl {
	vtblOnce.Do(func() {
		sharedVtbl = &handlerVtbl{
			queryInterface:    syscall.NewCallback(chQueryInterface),
			addRef:            syscall.NewCallback(chAddRef),
			release:           syscall.NewCallback(chRelease),
			activateCompleted: syscall.NewCallback(chActivateCompleted),
		}
	})
	return sharedVtbl
}

func chQueryInterface(this *completionHandler, riid *ole.GUID, ppv *uintptr) uintptr {
	if ole.IsEqualGUID(riid, ole.IID_IUnknown) ||
		ole.IsEqualGUID(riid, iidCompletionHandler) ||
		ole.IsEqualGUID(riid, iidAgileObject) {
		atomic.AddInt32(&this.refs, 1)
		*ppv = uintptr(unsafe.Pointer(this))
		return 0
	}
	*ppv = 0
	return eNoInterface
}

func chAddRef(this *completionHandler) uintptr {
	return uintptr(atomic.AddInt32(&this.refs, 1))
}

func chRelease(this *completionHandler) uintptr {
	n := atomic.AddInt32(&this.refs, -1)
	if n == 0 {
		windows.CloseHandle(this.event)
		windows.LocalFree(windows.Handle(unsafe.Pointer(this)))
	}
	return uintptr(n)
}

func chActivateCompleted(this *completionHandler, _ uintptr) uintptr {
	windows.SetEvent(this.event)
	return 0
}

func newCompletionHandler() (*completionHandler, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	const lptr = 0x0040 // LMEM_FIXED | LMEM_ZEROINIT
	mem, err := windows.LocalAlloc(lptr, uint32(unsafe.Sizeof(completionHandler{})))
	if err != nil {
		windows.CloseHandle(ev)
		return nil, err
	}
	h := (*completionHandler)(unsafe.Pointer(mem))
	h.vtbl = handlerVtable()
	h.refs = 1
	h.event = ev
	return h, nil
}

// ---------- activation ----------

// activateProcessLoopback returns an uninitialised IAudioClient that
// captures everything rendered by pid and its child processes. Must be
// called from a goroutine with COM initialised (MTA).
func activateProcessLoopback(pid uint32) (*wca.IAudioClient, error) {
	if err := procActivateAudioInterfaceA.Find(); err != nil {
		return nil, errors.New("ActivateAudioInterfaceAsync недоступен (нужна Windows 10 2004+)")
	}

	params := &activationParams{
		activationType: activationTypeProcessLoopback,
		targetPID:      pid,
		loopbackMode:   loopbackModeIncludeTree,
	}
	pv := &propVariantBlob{
		vt:       vtBlob,
		cbSize:   uint32(unsafe.Sizeof(*params)),
		blobData: uintptr(unsafe.Pointer(params)),
	}

	h, err := newCompletionHandler()
	if err != nil {
		return nil, err
	}
	ev := h.event
	// our own reference is dropped on every return path below
	defer chRelease(h)

	var op uintptr
	hr, _, _ := procActivateAudioInterfaceA.Call(
		uintptr(unsafe.Pointer(processLoopbackPath)),
		uintptr(unsafe.Pointer(wca.IID_IAudioClient)),
		uintptr(unsafe.Pointer(pv)),
		uintptr(unsafe.Pointer(h)),
		uintptr(unsafe.Pointer(&op)),
	)
	if uint32(hr) != 0 {
		return nil, fmt.Errorf("ActivateAudioInterfaceAsync: %w", ole.NewError(hr))
	}
	// IActivateAudioInterfaceAsyncOperation: QI, AddRef, Release, GetActivateResult
	opVtbl := *(**[4]uintptr)(unsafe.Pointer(op))
	defer syscall.SyscallN(opVtbl[2], op)

	if r, _ := windows.WaitForSingleObject(ev, activateWait); r != windows.WAIT_OBJECT_0 {
		return nil, errors.New("process loopback activation timed out")
	}
	runtime.KeepAlive(params)
	runtime.KeepAlive(pv)

	var activateHr uint32
	var unk uintptr
	hr, _, _ = syscall.SyscallN(opVtbl[3], op,
		uintptr(unsafe.Pointer(&activateHr)), uintptr(unsafe.Pointer(&unk)))
	if uint32(hr) != 0 {
		return nil, fmt.Errorf("GetActivateResult: %w", ole.NewError(hr))
	}
	if activateHr != 0 {
		if unk != 0 {
			(*ole.IUnknown)(unsafe.Pointer(unk)).Release()
		}
		return nil, fmt.Errorf("process loopback activation: %w", ole.NewError(uintptr(activateHr)))
	}
	if unk == 0 {
		return nil, errors.New("process loopback activation returned no interface")
	}
	return (*wca.IAudioClient)(unsafe.Pointer(unk)), nil
}
