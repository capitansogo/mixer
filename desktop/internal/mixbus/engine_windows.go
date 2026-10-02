//go:build windows

// Package mixbus is the "Аудио-микс" engine: it captures the microphone
// plus the audio of chosen applications (per-process loopback, so the apps
// keep playing to your headphones untouched), mixes them with individual
// gains and writes the result into a virtual cable (VB-Cable's
// "CABLE Input"). Discord/OBS/etc. then use "CABLE Output" as their mic.
//
// Threading: one engine goroutine, locked to an OS thread in the COM MTA,
// owns every audio object and runs the mix loop on the output device's
// event clock. Process-loopback activation (which can take tens of ms) runs
// on short-lived helper goroutines so it never stalls the mix. The public
// methods only touch the mutex-protected settings/gains and the published
// status.
package mixbus

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

// MicID is the source id of the microphone; apps use their exe name.
const MicID = "mic"

const (
	scanEvery     = 2 * time.Second
	retryFailedIn = 10 * time.Second
	retrySession  = 2 * time.Second
	publishEvery  = 40 * time.Millisecond
)

var errQuit = errors.New("quit")

// AppSource is one application mixed into the bus.
type AppSource struct {
	Exe   string  `json:"exe"`
	Gain  float32 `json:"gain"` // fader position 0..1
	Muted bool    `json:"muted"`
}

// Settings is the full engine configuration.
type Settings struct {
	Enabled      bool
	MicDevice    string // endpoint id, "" = auto
	OutputDevice string // endpoint id, "" = auto (VB-Cable)
	MicGain      float32
	MicMuted     bool
	Apps         []AppSource
}

// SourceState is the live view of one source for the GUI.
type SourceState struct {
	ID     string  `json:"id"`
	Gain   float32 `json:"gain"`
	Muted  bool    `json:"muted"`
	Active bool    `json:"active"` // mic open / app process found and captured
	Level  float32 `json:"level"`  // post-gain peak, 0..1
	Error  string  `json:"error"`
}

// Status is everything the GUI shows about the bus.
type Status struct {
	Enabled  bool          `json:"enabled"`
	Running  bool          `json:"running"`
	Mic      string        `json:"mic"`
	Output   string        `json:"output"`
	Error    string        `json:"error"`
	OutLevel float32       `json:"outLevel"`
	Sources  []SourceState `json:"sources"`
}

type Engine struct {
	mu    sync.Mutex
	set   Settings
	ver   uint64             // bumped when the session must be rebuilt
	gains map[string]float32 // effective amplitude per source id

	// published by the engine goroutine
	running  bool
	micName  string
	outName  string
	errMsg   string
	active   map[string]bool
	appErr   map[string]string
	levels   map[string]float32
	outLevel float32

	wake chan struct{}
	quit chan struct{}
	done chan struct{}
}

func New() *Engine {
	e := &Engine{
		gains:  map[string]float32{},
		active: map[string]bool{},
		appErr: map[string]string{},
		levels: map[string]float32{},
		wake:   make(chan struct{}, 1),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go e.run()
	return e
}

func (e *Engine) Close() {
	select {
	case <-e.quit:
	default:
		close(e.quit)
	}
	<-e.done
}

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func normalizeApps(in []AppSource) []AppSource {
	seen := make(map[string]bool, len(in))
	out := make([]AppSource, 0, len(in))
	for _, a := range in {
		a.Exe = strings.ToLower(strings.TrimSpace(a.Exe))
		if a.Exe == "" || a.Exe == MicID || seen[a.Exe] {
			continue
		}
		seen[a.Exe] = true
		out = append(out, a)
	}
	return out
}

// Apply replaces the configuration. Device or on/off changes rebuild the
// session; app-list changes are picked up live by the next process scan.
func (e *Engine) Apply(s Settings) {
	s.Apps = normalizeApps(s.Apps)
	e.mu.Lock()
	old := e.set
	e.set = s
	if old.Enabled != s.Enabled || old.MicDevice != s.MicDevice || old.OutputDevice != s.OutputDevice {
		e.ver++
	}
	e.recomputeGainsLocked()
	e.mu.Unlock()
	e.poke()
}

func (e *Engine) recomputeGainsLocked() {
	g := make(map[string]float32, len(e.set.Apps)+1)
	eff := func(pos float32, muted bool) float32 {
		if muted {
			return 0
		}
		return GainCurve(pos)
	}
	g[MicID] = eff(e.set.MicGain, e.set.MicMuted)
	for _, a := range e.set.Apps {
		g[a.Exe] = eff(a.Gain, a.Muted)
	}
	e.gains = g
}

// SetGain changes one source's fader position (0..1) without touching the
// session. Returns false if there is no such source.
func (e *Engine) SetGain(id string, pos float32) bool {
	pos = clamp01(pos)
	id = strings.ToLower(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == MicID {
		e.set.MicGain = pos
		e.recomputeGainsLocked()
		return true
	}
	for i := range e.set.Apps {
		if e.set.Apps[i].Exe == id {
			e.set.Apps[i].Gain = pos
			e.recomputeGainsLocked()
			return true
		}
	}
	return false
}

// SetMuted mutes/unmutes one source. Returns false if there is no such source.
func (e *Engine) SetMuted(id string, muted bool) bool {
	id = strings.ToLower(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == MicID {
		e.set.MicMuted = muted
		e.recomputeGainsLocked()
		return true
	}
	for i := range e.set.Apps {
		if e.set.Apps[i].Exe == id {
			e.set.Apps[i].Muted = muted
			e.recomputeGainsLocked()
			return true
		}
	}
	return false
}

// Status returns a snapshot for the GUI.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{
		Enabled:  e.set.Enabled,
		Running:  e.running,
		Mic:      e.micName,
		Output:   e.outName,
		Error:    e.errMsg,
		OutLevel: e.outLevel,
		Sources:  make([]SourceState, 0, len(e.set.Apps)+1),
	}
	st.Sources = append(st.Sources, SourceState{
		ID: MicID, Gain: e.set.MicGain, Muted: e.set.MicMuted,
		Active: e.active[MicID], Level: e.levels[MicID],
	})
	for _, a := range e.set.Apps {
		st.Sources = append(st.Sources, SourceState{
			ID: a.Exe, Gain: a.Gain, Muted: a.Muted,
			Active: e.active[a.Exe], Level: e.levels[a.Exe], Error: e.appErr[a.Exe],
		})
	}
	return st
}

// Level returns the current post-gain peak of one source (for LED meters).
func (e *Engine) Level(id string) float32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.levels[strings.ToLower(id)]
}

func (e *Engine) publishState(running bool, mic, out, errMsg string) {
	e.mu.Lock()
	e.running, e.micName, e.outName, e.errMsg = running, mic, out, errMsg
	if !running {
		e.active = map[string]bool{}
		e.levels = map[string]float32{}
		e.outLevel = 0
	}
	e.mu.Unlock()
}

// ---------- engine goroutine ----------

func (e *Engine) run() {
	defer close(e.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := initCOM(); err != nil {
		e.publishState(false, "", "", "COM: "+err.Error())
		<-e.quit
		return
	}
	defer ole.CoUninitialize()

	mmde, err := newEnumerator()
	if err != nil {
		e.publishState(false, "", "", err.Error())
		<-e.quit
		return
	}
	defer mmde.Release()

	for {
		e.mu.Lock()
		s, ver := e.set, e.ver
		e.mu.Unlock()

		if !s.Enabled {
			e.publishState(false, "", "", "")
			select {
			case <-e.quit:
				return
			case <-e.wake:
				continue
			}
		}

		err := e.session(mmde, s, ver)
		if err == errQuit {
			e.publishState(false, "", "", "")
			return
		}
		if err != nil {
			e.publishState(false, "", "", err.Error())
			select {
			case <-e.quit:
				return
			case <-e.wake:
			case <-time.After(retrySession):
			}
		}
	}
}

type openResult struct {
	exe string
	pid uint32
	c   *capture
	err error
}

// session runs one mix session until the settings require a rebuild
// (returns nil), the engine quits (errQuit) or the output fails.
func (e *Engine) session(mmde *wca.IMMDeviceEnumerator, s Settings, ver uint64) error {
	outDev, outName, err := pickOutput(mmde, s.OutputDevice)
	if err != nil {
		return err
	}
	out, err := openRenderer(outDev)
	outDev.Release()
	if err != nil {
		return err
	}
	defer out.close()

	var mic *capture
	micName, micErr := "", ""
	if micDev, name, err := pickMic(mmde, s.MicDevice); err != nil {
		micErr = err.Error()
	} else {
		micName = name
		if mic, err = openMicCapture(micDev); err != nil {
			micErr = "микрофон: " + err.Error()
		}
		micDev.Release()
	}
	defer func() {
		if mic != nil {
			mic.close()
		}
	}()

	apps := map[string]map[uint32]*capture{}
	appErr := map[string]string{}
	failed := map[uint32]time.Time{}
	pending := map[uint32]bool{}
	opened := make(chan openResult, 8)
	sessDone := make(chan struct{})
	defer func() {
		close(sessDone)
		for _, caps := range apps {
			for _, c := range caps {
				c.close()
			}
		}
		for {
			select {
			case r := <-opened:
				if r.c != nil {
					r.c.close()
				}
			default:
				return
			}
		}
	}()

	e.publishState(true, micName, outName, micErr)

	scan := func(now time.Time) {
		e.mu.Lock()
		want := make(map[string]struct{}, len(e.set.Apps))
		for _, a := range e.set.Apps {
			want[a.Exe] = struct{}{}
		}
		e.mu.Unlock()

		for exe, caps := range apps {
			if _, ok := want[exe]; !ok {
				for _, c := range caps {
					c.close()
				}
				delete(apps, exe)
				delete(appErr, exe)
			}
		}
		roots := findRootPIDs(want)
		for exe := range want {
			pids := roots[exe]
			caps := apps[exe]
			if caps == nil {
				caps = map[uint32]*capture{}
				apps[exe] = caps
			}
			alive := make(map[uint32]bool, len(pids))
			for _, pid := range pids {
				alive[pid] = true
			}
			for pid, c := range caps {
				if !alive[pid] {
					c.close()
					delete(caps, pid)
				}
			}
			for _, pid := range pids {
				if caps[pid] != nil || pending[pid] {
					continue
				}
				if t, ok := failed[pid]; ok && now.Sub(t) < retryFailedIn {
					continue
				}
				pending[pid] = true
				go openAsync(exe, pid, opened, sessDone)
			}
			if len(pids) == 0 {
				delete(appErr, exe) // not running is not an error
			}
		}
	}

	mix := make([]float32, 0, int(out.bufFrames)*Channels)
	acc := map[string]float32{}
	var accOut float32
	var lastScan, lastPublish time.Time

	for {
		select {
		case <-e.quit:
			return errQuit
		case <-e.wake:
			e.mu.Lock()
			rebuild := e.ver != ver
			e.mu.Unlock()
			if rebuild {
				return nil
			}
			lastScan = time.Time{} // app list may have changed: rescan now
		default:
		}

		_, _ = windows.WaitForSingleObject(out.ev, 200)
		now := time.Now()

		if now.Sub(lastScan) >= scanEvery {
			lastScan = now
			scan(now)
		}

	collect:
		for {
			select {
			case r := <-opened:
				delete(pending, r.pid)
				if r.err != nil {
					failed[r.pid] = now
					appErr[r.exe] = r.err.Error()
					continue
				}
				caps := apps[r.exe]
				if caps == nil { // removed from the list meanwhile
					r.c.close()
					continue
				}
				delete(appErr, r.exe)
				caps[r.pid] = r.c
			default:
				break collect
			}
		}

		if mic != nil {
			if err := mic.drain(); err != nil {
				mic.close()
				mic = nil
				micErr = "микрофон отключился: " + err.Error()
				e.publishState(true, micName, outName, micErr)
			}
		}
		for exe, caps := range apps {
			for pid, c := range caps {
				if err := c.drain(); err != nil {
					c.close()
					delete(caps, pid)
					failed[pid] = now
					appErr[exe] = err.Error()
				}
			}
		}

		n, err := out.writable()
		if err != nil {
			return err // output device went away: rebuild the session
		}
		if n > 0 {
			mix = mix[:int(n)*Channels]
			clear(mix)

			e.mu.Lock()
			gains := e.gains
			e.mu.Unlock()

			if mic != nil {
				if p := mic.q.mixInto(mix, gains[MicID]); p > acc[MicID] {
					acc[MicID] = p
				}
			}
			for exe, caps := range apps {
				g := gains[exe]
				for _, c := range caps {
					if p := c.q.mixInto(mix, g); p > acc[exe] {
						acc[exe] = p
					}
				}
			}
			softClip(mix)
			for _, v := range mix {
				if v < 0 {
					v = -v
				}
				if v > accOut {
					accOut = v
				}
			}
			if err := out.write(mix); err != nil {
				return err
			}
		}

		if now.Sub(lastPublish) >= publishEvery {
			lastPublish = now
			active := make(map[string]bool, len(apps)+1)
			active[MicID] = mic != nil
			for exe, caps := range apps {
				active[exe] = len(caps) > 0
			}
			errs := make(map[string]string, len(appErr))
			for k, v := range appErr {
				errs[k] = v
			}
			e.mu.Lock()
			e.levels = acc
			e.outLevel = accOut
			e.active = active
			e.appErr = errs
			e.mu.Unlock()
			acc = map[string]float32{}
			accOut = 0
		}
	}
}

func openAsync(exe string, pid uint32, out chan<- openResult, done <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var r openResult
	if err := initCOM(); err != nil {
		r = openResult{exe: exe, pid: pid, err: err}
	} else {
		c, err := openLoopbackCapture(pid)
		r = openResult{exe: exe, pid: pid, c: c, err: err}
		defer ole.CoUninitialize()
	}
	select {
	case out <- r:
	case <-done:
		if r.c != nil {
			r.c.close()
		}
	}
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

// ---------- device listing for the GUI ----------

// Devices is the choice offered in the GUI.
type Devices struct {
	Inputs  []Device `json:"inputs"`
	Outputs []Device `json:"outputs"`
}

// ListDevices enumerates capture and render endpoints on a temporary COM
// thread, so it is safe to call from any goroutine.
func ListDevices() (Devices, error) {
	type res struct {
		d   Devices
		err error
	}
	ch := make(chan res, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := initCOM(); err != nil {
			ch <- res{err: err}
			return
		}
		defer ole.CoUninitialize()
		mmde, err := newEnumerator()
		if err != nil {
			ch <- res{err: err}
			return
		}
		defer mmde.Release()
		var d Devices
		if d.Inputs, err = listDevices(mmde, wca.ECapture); err != nil {
			ch <- res{err: err}
			return
		}
		d.Outputs, err = listDevices(mmde, wca.ERender)
		ch <- res{d: d, err: err}
	}()
	r := <-ch
	return r.d, r.err
}
