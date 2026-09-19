//go:build windows

// Package winui answers one question Wails v2 cannot: is our main window
// actually on screen right now? Wails hides the window on close (tray
// mode) without telling the Go side, and WebView2 keeps executing JS
// while hidden, so we ask user32 directly and stop streaming UI events
// when nobody can see them.
package winui

import (
	"os"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procFindWindowExW            = user32.NewProc("FindWindowExW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procIsWindow                 = user32.NewProc("IsWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")

	ownPID = uint32(os.Getpid())

	// cached HWND of the Wails main window (0 = not found yet)
	mainHwnd atomic.Uintptr
)

// wailsWindowClass is the default class name Wails v2 registers for its
// main window on Windows (options.Windows.WindowClassName overrides it).
const wailsWindowClass = "wailsWindow"

func findMainWindow() uintptr {
	if h := mainHwnd.Load(); h != 0 {
		ok, _, _ := procIsWindow.Call(h)
		if ok != 0 {
			return h
		}
		mainHwnd.Store(0)
	}
	cls, err := windows.UTF16PtrFromString(wailsWindowClass)
	if err != nil {
		return 0
	}
	var after uintptr
	for {
		h, _, _ := procFindWindowExW.Call(0, after, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var pid uint32
		procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid == ownPID {
			mainHwnd.Store(h)
			return h
		}
		after = h
	}
}

// MainWindowVisible reports whether the Wails main window is shown and
// not minimised. If the window cannot be located (very early in startup)
// it returns true so we never wrongly starve a visible UI.
func MainWindowVisible() bool {
	h := findMainWindow()
	if h == 0 {
		return true
	}
	vis, _, _ := procIsWindowVisible.Call(h)
	if vis == 0 {
		return false
	}
	iconic, _, _ := procIsIconic.Call(h)
	return iconic == 0
}
