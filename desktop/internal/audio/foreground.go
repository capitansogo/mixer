//go:build windows

package audio

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")

	ownPID = uint32(os.Getpid())
)

// ForegroundPID returns the pid of the process owning the foreground
// window, or 0 if:
//   - there is no foreground window (lock screen, etc.)
//   - the foreground window is the calling process itself (we never
//     want to route the "game" slider onto the mixer's own GUI)
func ForegroundPID() uint32 {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == ownPID {
		return 0
	}
	return pid
}

// ProcessName resolves a pid to its lower-case exe basename through the
// client's cache. "" if the process cannot be opened.
func (c *Client) ProcessName(pid uint32) string {
	return c.processName(pid, time.Now())
}
