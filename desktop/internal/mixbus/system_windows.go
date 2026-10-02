//go:build windows

package mixbus

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

// ---------- COM ----------

func initCOM() error {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		if oe, ok := err.(*ole.OleError); ok && oe.Code() == 1 { // S_FALSE
			return nil
		}
		return err
	}
	return nil
}

func newEnumerator() (*wca.IMMDeviceEnumerator, error) {
	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator, &mmde,
	); err != nil {
		return nil, fmt.Errorf("CoCreateInstance(MMDeviceEnumerator): %w", err)
	}
	return mmde, nil
}

// ---------- devices ----------

// Device is an audio endpoint as shown in the GUI.
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Virtual bool   `json:"virtual"` // looks like a virtual cable / mixer bus
}

// IsVirtualName reports whether an endpoint name looks like a virtual
// audio cable (VB-Cable, Voicemeeter, VAC…). Used to auto-pick the output
// and to keep such devices out of the microphone choice — capturing the
// cable we are writing into would feed the mix back into itself.
func IsVirtualName(name string) bool {
	n := strings.ToLower(name)
	for _, k := range []string{"cable", "voicemeeter", "virtual"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}

func deviceName(dev *wca.IMMDevice) string {
	var ps *wca.IPropertyStore
	if err := dev.OpenPropertyStore(wca.STGM_READ, &ps); err != nil {
		return ""
	}
	defer ps.Release()
	var pv wca.PROPVARIANT
	if err := ps.GetValue(&wca.PKEY_Device_FriendlyName, &pv); err != nil || pv.Val == 0 {
		return ""
	}
	return pv.String()
}

func defaultID(mmde *wca.IMMDeviceEnumerator, flow uint32) string {
	var dev *wca.IMMDevice
	if err := mmde.GetDefaultAudioEndpoint(flow, wca.EConsole, &dev); err != nil {
		return ""
	}
	defer dev.Release()
	var id string
	_ = dev.GetId(&id)
	return id
}

// listDevices enumerates the active endpoints of one direction.
func listDevices(mmde *wca.IMMDeviceEnumerator, flow uint32) ([]Device, error) {
	var coll *wca.IMMDeviceCollection
	if err := mmde.EnumAudioEndpoints(flow, wca.DEVICE_STATE_ACTIVE, &coll); err != nil {
		return nil, fmt.Errorf("EnumAudioEndpoints: %w", err)
	}
	defer coll.Release()

	def := defaultID(mmde, flow)
	var count uint32
	if err := coll.GetCount(&count); err != nil {
		return nil, err
	}
	out := make([]Device, 0, count)
	for i := uint32(0); i < count; i++ {
		var dev *wca.IMMDevice
		if coll.Item(i, &dev) != nil {
			continue
		}
		var id string
		_ = dev.GetId(&id)
		name := deviceName(dev)
		dev.Release()
		out = append(out, Device{ID: id, Name: name, Default: id == def, Virtual: IsVirtualName(name)})
	}
	return out, nil
}

// openDeviceByID returns the endpoint with the given id (or nil).
func openDeviceByID(mmde *wca.IMMDeviceEnumerator, flow uint32, id string) (*wca.IMMDevice, string) {
	var coll *wca.IMMDeviceCollection
	if err := mmde.EnumAudioEndpoints(flow, wca.DEVICE_STATE_ACTIVE, &coll); err != nil {
		return nil, ""
	}
	defer coll.Release()
	var count uint32
	_ = coll.GetCount(&count)
	for i := uint32(0); i < count; i++ {
		var dev *wca.IMMDevice
		if coll.Item(i, &dev) != nil {
			continue
		}
		var did string
		_ = dev.GetId(&did)
		if did == id {
			return dev, deviceName(dev)
		}
		dev.Release()
	}
	return nil, ""
}

// pickOutput resolves the output endpoint: the configured id if it is
// still present, otherwise the first device that looks like VB-Cable's
// "CABLE Input", otherwise any other virtual cable.
func pickOutput(mmde *wca.IMMDeviceEnumerator, wantID string) (*wca.IMMDevice, string, error) {
	if wantID != "" {
		if dev, name := openDeviceByID(mmde, wca.ERender, wantID); dev != nil {
			return dev, name, nil
		}
	}
	devs, err := listDevices(mmde, wca.ERender)
	if err != nil {
		return nil, "", err
	}
	best := ""
	for _, d := range devs {
		n := strings.ToLower(d.Name)
		if strings.Contains(n, "cable input") {
			best = d.ID
			break
		}
		if best == "" && d.Virtual {
			best = d.ID
		}
	}
	if best == "" {
		return nil, "", errNoCable
	}
	dev, name := openDeviceByID(mmde, wca.ERender, best)
	if dev == nil {
		return nil, "", errNoCable
	}
	return dev, name, nil
}

// pickMic resolves the microphone: the configured id if present, else the
// Windows default recording device — unless that default is itself a
// virtual cable (people often make "CABLE Output" their default mic so
// every app picks it up), in which case the first real microphone wins.
func pickMic(mmde *wca.IMMDeviceEnumerator, wantID string) (*wca.IMMDevice, string, error) {
	if wantID != "" {
		if dev, name := openDeviceByID(mmde, wca.ECapture, wantID); dev != nil {
			return dev, name, nil
		}
	}
	devs, err := listDevices(mmde, wca.ECapture)
	if err != nil {
		return nil, "", err
	}
	pick := ""
	for _, d := range devs {
		if d.Default && !d.Virtual {
			pick = d.ID
		}
	}
	if pick == "" {
		for _, d := range devs {
			if !d.Virtual {
				pick = d.ID
				break
			}
		}
	}
	if pick == "" {
		return nil, "", errNoMic
	}
	dev, name := openDeviceByID(mmde, wca.ECapture, pick)
	if dev == nil {
		return nil, "", errNoMic
	}
	return dev, name, nil
}

// ---------- processes ----------

// findRootPIDs returns, for every wanted exe name (lower-case basename),
// the PIDs of its "root" processes: instances whose parent is not the same
// executable. Chrome, Spotify, Discord… are process trees where the audio
// is rendered by a child; capturing the root with INCLUDE_TREE gets it all.
func findRootPIDs(want map[string]struct{}) map[string][]uint32 {
	out := make(map[string][]uint32, len(want))
	if len(want) == 0 {
		return out
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)

	type proc struct {
		name   string
		parent uint32
	}
	procs := make(map[uint32]proc, 256)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := strings.ToLower(filepath.Base(windows.UTF16ToString(e.ExeFile[:])))
		procs[e.ProcessID] = proc{name: name, parent: e.ParentProcessID}
	}
	for pid, p := range procs {
		if _, ok := want[p.name]; !ok || pid == 0 {
			continue
		}
		if parent, ok := procs[p.parent]; ok && parent.name == p.name {
			continue // child of the same app — covered by the root's tree
		}
		out[p.name] = append(out[p.name], pid)
	}
	return out
}
