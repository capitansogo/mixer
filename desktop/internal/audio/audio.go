// Package audio wraps go-wca (Windows Core Audio bindings) with a small
// human-friendly API: list sessions, set volume by master/exe-name/system/mic,
// read peak meters.
//
// The heavy lifting is done through a long-lived Client that caches the
// device enumerator, the default render/capture endpoints and their
// volume/meter interfaces, plus a pid → exe-name cache. A Snapshot
// enumerates the render sessions exactly once and lets the caller apply
// any number of volume writes / peak reads against that one enumeration.
package audio

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

// Special target keywords understood by Snapshot.SetVolume / Peak.
const (
	TargetMaster     = "master"
	TargetSystem     = "system"
	TargetMic        = "mic"
	TargetForeground = "game"
)

// deviceRefreshInterval bounds how long we keep using a cached default
// endpoint before re-asking Windows which device is the default. Switching
// the default output in Windows takes effect for us within this window.
const deviceRefreshInterval = 2 * time.Second

// nameCacheTTL is how long a pid → exe-name mapping is trusted. PIDs are
// recycled by Windows, but never within a couple of seconds of the
// process exiting, and an exited process' session disappears from the
// enumeration anyway (we also drop entries for pids no longer seen).
const nameCacheTTL = 15 * time.Second

type Session struct {
	PID      uint32  `json:"pid"`
	Name     string  `json:"name"`   // basename of the process exe, e.g. "chrome.exe"
	Volume   float32 `json:"volume"` // 0.0..1.0
	IsSystem bool    `json:"isSystem"`
}

// InitCOM must be called once per goroutine that uses this package.
// Pair with Uninit() via defer at goroutine exit.
func InitCOM() error {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		// S_FALSE (already initialised) returns as OleError — accept it.
		if oe, ok := err.(*ole.OleError); ok && oe.Code() == 1 {
			return nil
		}
		return err
	}
	return nil
}

func Uninit() { ole.CoUninitialize() }

// ---------- Client ----------

// endpoint is one cached default device plus the interfaces we need on it.
type endpoint struct {
	flow uint32 // wca.ERender / wca.ECapture
	role uint32
	id   string
	dev  *wca.IMMDevice
	asm  *wca.IAudioSessionManager2 // render only
	aev  *wca.IAudioEndpointVolume
	ami  *wca.IAudioMeterInformation
}

func (e *endpoint) release() {
	if e.ami != nil {
		e.ami.Release()
		e.ami = nil
	}
	if e.aev != nil {
		e.aev.Release()
		e.aev = nil
	}
	if e.asm != nil {
		e.asm.Release()
		e.asm = nil
	}
	if e.dev != nil {
		e.dev.Release()
		e.dev = nil
	}
	e.id = ""
}

type nameEntry struct {
	name  string
	until time.Time
}

// Client is a long-lived Core Audio handle. It must be used from the
// single goroutine that created it (COM apartment affinity) and that
// goroutine must have called InitCOM first.
type Client struct {
	mmde        *wca.IMMDeviceEnumerator
	render      endpoint
	capture     endpoint
	lastRefresh time.Time
	names       map[uint32]nameEntry
}

// NewClient creates the device enumerator. Endpoints are resolved lazily
// on first use and re-validated every deviceRefreshInterval.
func NewClient() (*Client, error) {
	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator, &mmde,
	); err != nil {
		return nil, fmt.Errorf("CoCreateInstance(MMDeviceEnumerator): %w", err)
	}
	c := &Client{
		mmde:    mmde,
		render:  endpoint{flow: wca.ERender, role: wca.EMultimedia},
		capture: endpoint{flow: wca.ECapture, role: wca.ECommunications},
		names:   make(map[uint32]nameEntry, 32),
	}
	return c, nil
}

// Close releases every cached COM object.
func (c *Client) Close() {
	c.render.release()
	c.capture.release()
	if c.mmde != nil {
		c.mmde.Release()
		c.mmde = nil
	}
}

// Invalidate forces the next call to re-resolve the default devices.
// Call it after an operation failed with a device error.
func (c *Client) Invalidate() { c.lastRefresh = time.Time{} }

// refresh re-resolves the default endpoints if the cache is stale. A
// missing capture device (no microphone) is not an error: the endpoint
// just stays unresolved and mic targets become no-ops.
func (c *Client) refresh() error {
	now := time.Now()
	if now.Sub(c.lastRefresh) < deviceRefreshInterval {
		return nil
	}
	c.lastRefresh = now
	if err := c.refreshEndpoint(&c.render); err != nil {
		return err
	}
	_ = c.refreshEndpoint(&c.capture)
	return nil
}

func (c *Client) refreshEndpoint(e *endpoint) error {
	var dev *wca.IMMDevice
	if err := c.mmde.GetDefaultAudioEndpoint(e.flow, e.role, &dev); err != nil {
		e.release()
		return fmt.Errorf("GetDefaultAudioEndpoint: %w", err)
	}
	var id string
	if err := dev.GetId(&id); err == nil && id == e.id && e.dev != nil {
		dev.Release()
		return nil // same device as before, keep cached interfaces
	}

	e.release()
	e.dev = dev
	e.id = id

	if err := dev.Activate(wca.IID_IAudioEndpointVolume, wca.CLSCTX_ALL, nil, &e.aev); err != nil {
		e.release()
		return fmt.Errorf("Activate(IAudioEndpointVolume): %w", err)
	}
	if err := dev.Activate(wca.IID_IAudioMeterInformation, wca.CLSCTX_ALL, nil, &e.ami); err != nil {
		e.ami = nil // meters are optional
	}
	if e.flow == wca.ERender {
		if err := dev.Activate(wca.IID_IAudioSessionManager2, wca.CLSCTX_ALL, nil, &e.asm); err != nil {
			e.release()
			return fmt.Errorf("Activate(IAudioSessionManager2): %w", err)
		}
	}
	return nil
}

// processName returns the (cached) lower-case basename of the executable
// owning pid, or "" if it cannot be resolved (system pids, protected
// processes). Negative results are cached too so we do not hammer
// OpenProcess on processes we will never be able to open.
func (c *Client) processName(pid uint32, now time.Time) string {
	if pid == 0 {
		return ""
	}
	if e, ok := c.names[pid]; ok && now.Before(e.until) {
		return e.name
	}
	name := lookupProcessName(pid)
	c.names[pid] = nameEntry{name: name, until: now.Add(nameCacheTTL)}
	return name
}

// pruneNames drops cache entries for pids that no longer have a session.
func (c *Client) pruneNames(seen map[uint32]struct{}) {
	for pid := range c.names {
		if _, ok := seen[pid]; !ok {
			delete(c.names, pid)
		}
	}
}

func lookupProcessName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	var buf [windows.MAX_LONG_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:size])))
}

// ---------- Snapshot ----------

type session struct {
	pid      uint32
	name     string
	isSystem bool
	ctrl     *wca.IAudioSessionControl
	ctrl2    *wca.IAudioSessionControl2
}

// Snapshot is one enumeration of the render sessions. Create it, run any
// number of SetVolume / Peak calls, then Release it. Never keep one across
// frames: sessions come and go.
type Snapshot struct {
	c        *Client
	sessions []session
}

// Snapshot enumerates the sessions on the default render endpoint once.
func (c *Client) Snapshot() (*Snapshot, error) {
	if err := c.refresh(); err != nil {
		return nil, err
	}
	if c.render.asm == nil {
		c.Invalidate()
		return nil, fmt.Errorf("no default render device")
	}

	var enum *wca.IAudioSessionEnumerator
	if err := c.render.asm.GetSessionEnumerator(&enum); err != nil {
		c.Invalidate()
		return nil, fmt.Errorf("GetSessionEnumerator: %w", err)
	}
	defer enum.Release()

	var count int
	if err := enum.GetCount(&count); err != nil {
		return nil, err
	}

	now := time.Now()
	s := &Snapshot{c: c, sessions: make([]session, 0, count)}
	seen := make(map[uint32]struct{}, count)
	for i := 0; i < count; i++ {
		var ctrl *wca.IAudioSessionControl
		if enum.GetSession(i, &ctrl) != nil {
			continue
		}
		disp, err := ctrl.QueryInterface(wca.IID_IAudioSessionControl2)
		if err != nil {
			ctrl.Release()
			continue
		}
		ctrl2 := (*wca.IAudioSessionControl2)(unsafe.Pointer(disp))

		var pid uint32
		_ = ctrl2.GetProcessId(&pid)
		isSystem := ctrl2.IsSystemSoundsSession() == nil // S_OK means yes

		name := ""
		if !isSystem {
			name = c.processName(pid, now)
		}
		seen[pid] = struct{}{}
		s.sessions = append(s.sessions, session{
			pid: pid, name: name, isSystem: isSystem, ctrl: ctrl, ctrl2: ctrl2,
		})
	}
	c.pruneNames(seen)
	return s, nil
}

// Release frees the session interfaces held by the snapshot.
func (s *Snapshot) Release() {
	for i := range s.sessions {
		if s.sessions[i].ctrl2 != nil {
			s.sessions[i].ctrl2.Release()
		}
		if s.sessions[i].ctrl != nil {
			s.sessions[i].ctrl.Release()
		}
	}
	s.sessions = nil
}

// Sessions returns a GUI-friendly list (name, pid, current volume).
func (s *Snapshot) Sessions() []Session {
	out := make([]Session, 0, len(s.sessions))
	for _, ses := range s.sessions {
		var level float32
		if disp, err := ses.ctrl2.QueryInterface(wca.IID_ISimpleAudioVolume); err == nil {
			vol := (*wca.ISimpleAudioVolume)(unsafe.Pointer(disp))
			_ = vol.GetMasterVolume(&level)
			vol.Release()
		}
		out = append(out, Session{PID: ses.pid, Name: ses.name, Volume: level, IsSystem: ses.isSystem})
	}
	return out
}

// SetVolume applies level (0..1) to one target. Targets:
//   - "master" → default render endpoint volume
//   - "mic"    → default capture endpoint volume
//   - "system" → the system-sounds session
//   - "*.exe"  → every session whose process basename matches
//
// "game" must be resolved to an exe name by the caller first.
// Raising a level above zero also un-mutes the target: raising a slider
// while Windows keeps the thing muted feels broken.
// Returns how many sessions/endpoints were written.
func (s *Snapshot) SetVolume(target string, level float32) (int, error) {
	level = clamp01(level)
	switch target {
	case TargetMaster:
		return s.setEndpointVolume(&s.c.render, level)
	case TargetMic:
		return s.setEndpointVolume(&s.c.capture, level)
	}

	target = strings.ToLower(target)
	matched := 0
	for _, ses := range s.sessions {
		var ok bool
		if target == TargetSystem {
			ok = ses.isSystem
		} else {
			ok = !ses.isSystem && ses.name != "" && ses.name == target
		}
		if !ok {
			continue
		}
		disp, err := ses.ctrl2.QueryInterface(wca.IID_ISimpleAudioVolume)
		if err != nil {
			continue
		}
		vol := (*wca.ISimpleAudioVolume)(unsafe.Pointer(disp))
		if level > 0 {
			_ = vol.SetMute(false, nil)
		}
		if err := vol.SetMasterVolume(level, nil); err == nil {
			matched++
		}
		vol.Release()
	}
	return matched, nil
}

func (s *Snapshot) setEndpointVolume(e *endpoint, level float32) (int, error) {
	if e.aev == nil {
		if e.flow == wca.ECapture {
			return 0, nil // no microphone — silently ignore
		}
		s.c.Invalidate()
		return 0, fmt.Errorf("no default device")
	}
	if level > 0 {
		_ = e.aev.SetMute(false, nil)
	}
	if err := e.aev.SetMasterVolumeLevelScalar(level, nil); err != nil {
		s.c.Invalidate() // device may have gone away
		return 0, err
	}
	return 1, nil
}

// Peaks returns the current peak meter value (0..1) for each slider,
// picking the loudest target attached to that slider. Session meters are
// read once per snapshot, so the cost does not grow with the number of
// targets.
//
// Targets resolve like SetVolume; "game" uses currentForeground.
func (s *Snapshot) Peaks(mapping map[int][]string, currentForeground string, n int) []float32 {
	out := make([]float32, n)

	var masterPeak, micPeak, systemPeak float32
	if s.c.render.ami != nil {
		_ = s.c.render.ami.GetPeakValue(&masterPeak)
	}
	if s.c.capture.ami != nil {
		_ = s.c.capture.ami.GetPeakValue(&micPeak)
	}

	// Collect the set of exe names anyone is interested in so we only
	// query meters for sessions that matter.
	wanted := make(map[string]struct{}, 8)
	wantSystem := false
	for i := 0; i < n; i++ {
		for _, t := range mapping[i] {
			switch t {
			case TargetMaster, TargetMic:
			case TargetSystem:
				wantSystem = true
			case TargetForeground:
				if currentForeground != "" {
					wanted[currentForeground] = struct{}{}
				}
			default:
				wanted[strings.ToLower(t)] = struct{}{}
			}
		}
	}

	peakByExe := make(map[string]float32, len(wanted))
	for _, ses := range s.sessions {
		if ses.isSystem {
			if !wantSystem {
				continue
			}
		} else if _, ok := wanted[ses.name]; !ok {
			continue
		}
		disp, err := ses.ctrl2.QueryInterface(wca.IID_IAudioMeterInformation)
		if err != nil {
			continue
		}
		ami := (*wca.IAudioMeterInformation)(unsafe.Pointer(disp))
		var peak float32
		if ami.GetPeakValue(&peak) == nil {
			if ses.isSystem {
				if peak > systemPeak {
					systemPeak = peak
				}
			} else if peak > peakByExe[ses.name] {
				peakByExe[ses.name] = peak
			}
		}
		ami.Release()
	}

	for i := 0; i < n; i++ {
		var best float32
		for _, t := range mapping[i] {
			var v float32
			switch t {
			case TargetMaster:
				v = masterPeak
			case TargetMic:
				v = micPeak
			case TargetSystem:
				v = systemPeak
			case TargetForeground:
				if currentForeground != "" {
					v = peakByExe[currentForeground]
				}
			default:
				v = peakByExe[strings.ToLower(t)]
			}
			if v > best {
				best = v
			}
		}
		out[i] = clamp01(best)
	}
	return out
}

// ListSessions is a convenience for one-off callers (the GUI's session
// picker): it builds a temporary Client, takes one snapshot and tears
// everything down. The caller must have called InitCOM on this goroutine.
func ListSessions() ([]Session, error) {
	c, err := NewClient()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	snap, err := c.Snapshot()
	if err != nil {
		return nil, err
	}
	defer snap.Release()
	return snap.Sessions(), nil
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
