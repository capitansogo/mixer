package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	maudio "mixer/internal/audio"
	"mixer/internal/autostart"
	mconfig "mixer/internal/config"
	"mixer/internal/notify"
	mserial "mixer/internal/serial"
	mtray "mixer/internal/tray"
	"mixer/internal/winui"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the bound object exposed to the Svelte frontend.
type App struct {
	ctx    context.Context
	mu     sync.RWMutex
	cfg    mconfig.Config
	reader *mserial.Reader

	// apply carries coalesced slider frames from pumpReader to applier.
	apply chan [mserial.NumSliders]int
	// resync tells applier to forget the last-applied levels so the next
	// frame re-applies every slider (after reconnect / mapping change).
	resync chan struct{}

	// Calibration state. When `calibrating` is true the applier just
	// records min/max into `cal` instead of routing audio.
	calMu       sync.Mutex
	calibrating bool
	cal         [mserial.NumSliders]mconfig.Calibration

	// Connection intent. wantPort is the port the user (or autostart)
	// asked for; while it is non-empty a lost link is re-established
	// automatically.
	connMu          sync.Mutex
	wantPort        string
	reconnectCancel context.CancelFunc
	reconnectGen    int

	errMu        sync.Mutex
	lastAudioErr time.Time
}

// changeThreshold suppresses redundant SetVolume calls. ESP32 ADC has
// ±15-30 raw-unit noise even with the firmware's median smoothing, so
// a threshold below ~0.03 (≈30 raw units) ghosts movements and would
// also trigger spurious auto-unmute on muted sessions.
const changeThreshold float32 = 0.03

// UI streaming. The firmware can push up to 50 frames/s; the GUI only
// needs ~30 fps and only when something actually moved.
const (
	uiFrameInterval = 33 * time.Millisecond
	uiMinDelta      = 2 // raw ADC units
	meterInterval   = 40 * time.Millisecond
	audioErrEvery   = 2 * time.Second
	reconnectMin    = 2 * time.Second
	reconnectMax    = 30 * time.Second
)

// DeviceState mirrors the firmware's "STATE:<theme>,<brightness>,<mode>"
// reply so the GUI can show what the hardware is actually doing.
type DeviceState struct {
	Theme      int `json:"theme"`
	Brightness int `json:"brightness"`
	Mode       int `json:"mode"`
}

// ConnectionInfo is what the GUI needs to render the port/status bar.
type ConnectionInfo struct {
	Connected    bool   `json:"connected"`
	Port         string `json:"port"`
	Reconnecting bool   `json:"reconnecting"`
}

func NewApp() *App {
	return &App{
		reader: mserial.New(),
		apply:  make(chan [mserial.NumSliders]int, 4),
		resync: make(chan struct{}, 1),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cfg, created, err := mconfig.Load()
	if err != nil {
		wruntime.LogErrorf(ctx, "config load: %v", err)
		cfg = mconfig.Default()
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()

	if created {
		path, _ := mconfig.Path()
		wruntime.LogInfof(ctx, "created default config at %s", path)
	}

	go a.pumpReader()
	go a.applier()
	go mtray.Run(mtray.Callbacks{
		OnOpen:   func() { wruntime.WindowShow(a.ctx) },
		OnReload: func() { _, _ = a.ReloadConfig() },
		OnQuit:   func() { wruntime.Quit(a.ctx) },
	})

	if cfg.ComPort != "" {
		a.connMu.Lock()
		a.wantPort = cfg.ComPort
		a.connMu.Unlock()
		go func() {
			if err := a.openPort(cfg.ComPort); err != nil {
				wruntime.EventsEmit(ctx, "serial-error", "auto-connect: "+err.Error())
				notify.Warn("Mixer", "Не удалось подключиться к "+cfg.ComPort+", пробую переподключиться…")
				a.startReconnect(cfg.ComPort)
				return
			}
			notify.Info("Mixer", "Подключено: "+cfg.ComPort)
		}()
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.connMu.Lock()
	a.wantPort = ""
	a.cancelReconnectLocked()
	a.connMu.Unlock()
	a.reader.Stop()
}

// ---------- Serial pump ----------

// pumpReader turns the raw frame stream into two coalesced consumers:
// the audio applier (latest frame every uiFrameInterval) and the GUI
// (same, but only when the window is visible and something moved).
func (a *App) pumpReader() {
	values := a.reader.ValuesCh()
	lines := a.reader.LinesCh()
	errs := a.reader.ErrorsCh()

	tick := time.NewTicker(uiFrameInterval)
	defer tick.Stop()

	var latest [mserial.NumSliders]int
	lastSent := [mserial.NumSliders]int{-1, -1, -1, -1, -1}
	applyPending, uiPending := false, false

	for {
		select {
		case <-a.ctx.Done():
			return
		case v := <-values:
			latest = v
			applyPending, uiPending = true, true
		case <-tick.C:
			if applyPending {
				applyPending = false
				select {
				case a.apply <- latest:
				default:
				}
			}
			if !uiPending {
				continue
			}
			if !frameDiffers(latest, lastSent, uiMinDelta) {
				uiPending = false
				continue
			}
			// Keep uiPending set while hidden so the first visible tick
			// flushes the current position.
			if !winui.MainWindowVisible() {
				continue
			}
			wruntime.EventsEmit(a.ctx, "slider-values", latest[:])
			lastSent = latest
			uiPending = false
		case l := <-lines:
			a.handleLine(l)
		case e := <-errs:
			if errors.Is(e, mserial.ErrDisconnected) {
				a.onLinkLost(e)
			} else {
				wruntime.EventsEmit(a.ctx, "serial-error", e.Error())
			}
		}
	}
}

func frameDiffers(a, b [mserial.NumSliders]int, delta int) bool {
	for i := range a {
		d := a[i] - b[i]
		if d >= delta || d <= -delta {
			return true
		}
	}
	return false
}

// handleLine interprets non-frame uplink lines from the firmware.
func (a *App) handleLine(line string) {
	switch {
	case strings.HasPrefix(line, "STATE:"):
		parts := strings.Split(line[len("STATE:"):], ",")
		if len(parts) != 3 {
			return
		}
		var st DeviceState
		var err error
		if st.Theme, err = strconv.Atoi(parts[0]); err != nil {
			return
		}
		if st.Brightness, err = strconv.Atoi(parts[1]); err != nil {
			return
		}
		if st.Mode, err = strconv.Atoi(parts[2]); err != nil {
			return
		}
		// Opening the port resets the ESP32, so the MODE we sent right after
		// Start may have landed during boot and been lost. The device tells
		// us what it is really doing; if that disagrees with config, push
		// the mode again (it answers with a matching STATE, so no loop).
		a.mu.RLock()
		want := a.cfg.LedMode
		a.mu.RUnlock()
		if st.Mode != want && a.reader.IsRunning() {
			_ = a.reader.Send(fmt.Sprintf("MODE:%d", want))
			st.Mode = want
		}
		wruntime.EventsEmit(a.ctx, "device-state", st)
	case line == "PONG":
		// port-probe reply, nothing to do
	}
}

// ---------- Connection management ----------

// openPort opens the reader on port and brings the device in sync with
// our config (LED mode) and the GUI (STATE request, volume resync).
func (a *App) openPort(port string) error {
	a.mu.RLock()
	baud := a.cfg.BaudRate
	mode := a.cfg.LedMode
	a.mu.RUnlock()

	if err := a.reader.Start(port, baud); err != nil {
		return err
	}
	_ = a.reader.Send(fmt.Sprintf("MODE:%d", mode))
	_ = a.reader.Send("GET")
	a.requestResync()
	wruntime.EventsEmit(a.ctx, "serial-connected", port)
	return nil
}

func (a *App) onLinkLost(err error) {
	wruntime.EventsEmit(a.ctx, "serial-disconnected", err.Error())

	a.connMu.Lock()
	port := a.wantPort
	a.connMu.Unlock()

	if port == "" {
		return
	}
	notify.Warn("Mixer", "Связь с "+port+" потеряна, пробую переподключиться…")
	a.startReconnect(port)
}

func (a *App) startReconnect(port string) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.reconnectCancel != nil {
		return // already running
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.reconnectCancel = cancel
	a.reconnectGen++
	gen := a.reconnectGen
	go a.reconnectLoop(ctx, gen, port)
}

// cancelReconnectLocked stops a running reconnect loop. Caller holds connMu.
func (a *App) cancelReconnectLocked() {
	if a.reconnectCancel != nil {
		a.reconnectCancel()
		a.reconnectCancel = nil
	}
}

func (a *App) reconnectLoop(ctx context.Context, gen int, port string) {
	defer func() {
		a.connMu.Lock()
		if a.reconnectGen == gen {
			a.reconnectCancel = nil
		}
		a.connMu.Unlock()
	}()

	delay := reconnectMin
	for {
		wruntime.EventsEmit(a.ctx, "serial-reconnecting", port)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		if err := a.openPort(port); err == nil {
			notify.Info("Mixer", "Подключено: "+port)
			return
		}
		if delay < reconnectMax {
			delay *= 2
			if delay > reconnectMax {
				delay = reconnectMax
			}
		}
	}
}

func (a *App) requestResync() {
	select {
	case a.resync <- struct{}{}:
	default:
	}
}

// ---------- Audio applier ----------

func (a *App) applier() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := maudio.InitCOM(); err != nil {
		wruntime.EventsEmit(a.ctx, "audio-error", "init COM: "+err.Error())
		return
	}
	defer maudio.Uninit()

	client, err := maudio.NewClient()
	if err != nil {
		wruntime.EventsEmit(a.ctx, "audio-error", err.Error())
		return
	}
	defer client.Close()

	var last [mserial.NumSliders]float32
	resetLevels := func() {
		for i := range last {
			last[i] = -1
		}
	}
	resetLevels()
	var lastForeground string

	meterTicker := time.NewTicker(meterInterval)
	defer meterTicker.Stop()

	var levels [mserial.NumSliders]float32
	var changed [mserial.NumSliders]bool

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.resync:
			resetLevels()
		case v := <-a.apply:
			if a.recordCalibration(v) {
				continue
			}
			any := false
			for i, raw := range v {
				norm := a.normalize(i, raw)
				changed[i] = absDiff(norm, last[i]) >= changeThreshold
				if changed[i] {
					last[i] = norm
					levels[i] = norm
					any = true
				}
			}
			if !any {
				continue
			}
			snap, err := client.Snapshot()
			if err != nil {
				a.audioError("sessions: " + err.Error())
				continue
			}
			for i := range v {
				if changed[i] {
					a.applySlider(client, snap, i, levels[i], &lastForeground)
				}
			}
			snap.Release()
		case <-meterTicker.C:
			a.pushMeter(client, &lastForeground)
		}
	}
}

// audioError forwards an audio-layer error to the GUI, at most once per
// audioErrEvery so a flapping device cannot flood the status line.
func (a *App) audioError(msg string) {
	a.errMu.Lock()
	now := time.Now()
	ok := now.Sub(a.lastAudioErr) >= audioErrEvery
	if ok {
		a.lastAudioErr = now
	}
	a.errMu.Unlock()
	if ok {
		wruntime.EventsEmit(a.ctx, "audio-error", msg)
	}
}

// resolveForeground refreshes *lastForeground from the current foreground
// window if it belongs to a process we can name. Our own window and
// unnameable (elevated) processes keep the previous value, so the "game"
// slider does not lose its target when the user alt-tabs into the mixer.
func resolveForeground(client *maudio.Client, lastForeground *string) {
	if pid := maudio.ForegroundPID(); pid != 0 {
		if name := client.ProcessName(pid); name != "" {
			*lastForeground = name
		}
	}
}

// pushMeter is a no-op unless we're connected, in meter mode, and have
// some mapping to read peaks for. It takes one session snapshot and
// emits a single "M:p1,p2,p3,p4,p5" downlink line.
func (a *App) pushMeter(client *maudio.Client, lastForeground *string) {
	a.mu.RLock()
	mode := a.cfg.LedMode
	mapping := a.cfg.SliderMapping
	a.mu.RUnlock()

	if mode != 2 || !a.reader.IsRunning() {
		return
	}
	if mappingHas(mapping, maudio.TargetForeground) {
		resolveForeground(client, lastForeground)
	}

	snap, err := client.Snapshot()
	if err != nil {
		return
	}
	peaks := snap.Peaks(mapping, *lastForeground, mserial.NumSliders)
	snap.Release()

	// 1023 mirrors the firmware's ADC range so the existing showLights()
	// math fills the bar as if it were a physical slider at that position.
	var sb [64]byte
	out := append(sb[:0], "M:"...)
	for i, p := range peaks {
		if i > 0 {
			out = append(out, ',')
		}
		out = strconv.AppendInt(out, int64(p*1023), 10)
	}
	_ = a.reader.Send(string(out))
}

func mappingHas(mapping map[int][]string, target string) bool {
	for _, ts := range mapping {
		for _, t := range ts {
			if t == target {
				return true
			}
		}
	}
	return false
}

// recordCalibration updates per-slider min/max when in calibration mode
// and emits progress to the GUI when something changed. Returns true if
// we are calibrating (so the caller skips audio side effects this frame).
func (a *App) recordCalibration(v [mserial.NumSliders]int) bool {
	a.calMu.Lock()
	if !a.calibrating {
		a.calMu.Unlock()
		return false
	}
	dirty := false
	for i, raw := range v {
		if raw < a.cal[i].Min {
			a.cal[i].Min = raw
			dirty = true
		}
		if raw > a.cal[i].Max {
			a.cal[i].Max = raw
			dirty = true
		}
	}
	snapshot := a.cal
	a.calMu.Unlock()

	if dirty {
		wruntime.EventsEmit(a.ctx, "calibration-progress", snapshot[:])
	}
	return true
}

// normalize converts a raw 0..1023 ADC reading into a 0..1 volume,
// using the per-slider calibration plus the global invert / noise
// reduction settings from config.
func (a *App) normalize(idx, raw int) float32 {
	a.mu.RLock()
	inv := a.cfg.InvertSliders
	dz := a.cfg.NoiseReduction
	cal, ok := a.cfg.Calibration[idx]
	a.mu.RUnlock()

	if !ok || cal.Max-cal.Min < 50 {
		cal = mconfig.Calibration{Min: 0, Max: 1023}
	}

	if raw < cal.Min+dz {
		raw = cal.Min
	}
	if raw > cal.Max {
		raw = cal.Max
	}

	v := float32(raw-cal.Min) / float32(cal.Max-cal.Min)
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	if inv {
		v = 1 - v
	}
	return v
}

func (a *App) applySlider(client *maudio.Client, snap *maudio.Snapshot, idx int, level float32, lastForeground *string) {
	a.mu.RLock()
	targets := append([]string(nil), a.cfg.SliderMapping[idx]...)
	a.mu.RUnlock()

	for _, t := range targets {
		name := t
		if t == maudio.TargetForeground {
			resolveForeground(client, lastForeground)
			if *lastForeground == "" {
				continue
			}
			name = *lastForeground
		}
		if _, err := snap.SetVolume(name, level); err != nil {
			a.audioError(name + ": " + err.Error())
		}
	}
}

func absDiff(a, b float32) float32 {
	if a > b {
		return a - b
	}
	return b - a
}

// ---------- Serial (bound) ----------

func (a *App) ListPorts() ([]string, error) { return mserial.ListPorts() }

// Connect opens port and remembers it as the wanted port, so a lost link
// is re-established automatically until Disconnect is called.
func (a *App) Connect(port string) error {
	a.connMu.Lock()
	a.wantPort = port
	a.cancelReconnectLocked()
	a.connMu.Unlock()

	if a.reader.IsRunning() {
		if a.reader.PortName() == port {
			wruntime.EventsEmit(a.ctx, "serial-connected", port)
			return nil
		}
		a.reader.Stop()
	}
	return a.openPort(port)
}

// Disconnect closes the port and stops any reconnect attempts.
func (a *App) Disconnect() {
	a.connMu.Lock()
	a.wantPort = ""
	a.cancelReconnectLocked()
	a.connMu.Unlock()

	a.reader.Stop()
	wruntime.EventsEmit(a.ctx, "serial-disconnected", "")
}

func (a *App) IsConnected() bool { return a.reader.IsRunning() }

func (a *App) GetConnectionInfo() ConnectionInfo {
	a.connMu.Lock()
	want := a.wantPort
	reconnecting := a.reconnectCancel != nil
	a.connMu.Unlock()

	info := ConnectionInfo{Connected: a.reader.IsRunning(), Reconnecting: reconnecting}
	if info.Connected {
		info.Port = a.reader.PortName()
	} else {
		info.Port = want
	}
	return info
}

func (a *App) SendCommand(cmd string) error { return a.reader.Send(cmd) }

// RequestDeviceState asks the firmware to re-send its STATE line.
func (a *App) RequestDeviceState() error { return a.reader.Send("GET") }

// ---------- Audio (bound) ----------

func (a *App) ListAudioSessions() ([]maudio.Session, error) {
	type result struct {
		sessions []maudio.Session
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := maudio.InitCOM(); err != nil {
			ch <- result{nil, err}
			return
		}
		defer maudio.Uninit()
		ss, err := maudio.ListSessions()
		ch <- result{ss, err}
	}()
	r := <-ch
	return r.sessions, r.err
}

// ---------- Config (bound) ----------

func (a *App) GetConfig() mconfig.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *App) SaveConfig(cfg mconfig.Config) error {
	if cfg.BaudRate == 0 {
		cfg.BaudRate = mconfig.DefaultBaud
	}
	if cfg.Calibration == nil {
		cfg.Calibration = mconfig.DefaultCalibration()
	}
	if cfg.SliderMapping == nil {
		cfg.SliderMapping = map[int][]string{}
	}
	if err := mconfig.Save(cfg); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	a.requestResync()
	return nil
}

func (a *App) ReloadConfig() (mconfig.Config, error) {
	cfg, _, err := mconfig.Load()
	if err != nil {
		return mconfig.Config{}, err
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	a.requestResync()
	wruntime.EventsEmit(a.ctx, "config-reloaded", cfg)
	return cfg, nil
}

func (a *App) ConfigPath() (string, error) { return mconfig.Path() }

// ---------- LED Mode (bound) ----------

// SetLEDMode pushes the new mode to the firmware (if connected) and
// persists it in config so the next launch starts there too.
//
//	0 — position (default)
//	1 — rainbow
//	2 — meter (PC drives strips with peak meters)
func (a *App) SetLEDMode(mode int) error {
	if mode < 0 || mode > 2 {
		return fmt.Errorf("led mode out of range: %d", mode)
	}
	if a.reader.IsRunning() {
		if err := a.reader.Send(fmt.Sprintf("MODE:%d", mode)); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.cfg.LedMode = mode
	cfgCopy := a.cfg
	a.mu.Unlock()
	return mconfig.Save(cfgCopy)
}

// ---------- Autostart (bound) ----------

func (a *App) GetAutostart() (bool, error) { return autostart.IsEnabled() }

func (a *App) SetAutostart(enable bool) error {
	if enable {
		return autostart.Enable()
	}
	return autostart.Disable()
}

// ---------- Calibration (bound) ----------

// StartCalibration freezes audio routing and starts recording per-slider
// extremes. The GUI should ask the user to move every slider to both
// hard stops, then call StopCalibration to persist the result.
func (a *App) StartCalibration() {
	a.calMu.Lock()
	a.calibrating = true
	for i := range a.cal {
		// Seed the search with "impossible" values so the first frame
		// always replaces both bounds.
		a.cal[i] = mconfig.Calibration{Min: 9999, Max: -1}
	}
	a.calMu.Unlock()
}

// StopCalibration saves the observed min/max into the config and exits
// calibration mode. Returns the persisted calibration map for the GUI.
func (a *App) StopCalibration() (map[int]mconfig.Calibration, error) {
	a.calMu.Lock()
	a.calibrating = false
	snapshot := a.cal
	a.calMu.Unlock()

	result := make(map[int]mconfig.Calibration, mserial.NumSliders)
	for i, c := range snapshot {
		if c.Max-c.Min < 50 {
			c = mconfig.Calibration{Min: 0, Max: 1023}
		}
		result[i] = c
	}

	a.mu.Lock()
	a.cfg.Calibration = result
	cfgCopy := a.cfg
	a.mu.Unlock()

	if err := mconfig.Save(cfgCopy); err != nil {
		return nil, err
	}
	a.requestResync()
	return result, nil
}

// CancelCalibration leaves calibration mode without saving.
func (a *App) CancelCalibration() {
	a.calMu.Lock()
	a.calibrating = false
	a.calMu.Unlock()
	a.requestResync()
}

// ResetCalibration restores the default 0..1023 range for all sliders
// and saves it to disk.
func (a *App) ResetCalibration() error {
	a.mu.Lock()
	a.cfg.Calibration = mconfig.DefaultCalibration()
	cfgCopy := a.cfg
	a.mu.Unlock()
	a.requestResync()
	return mconfig.Save(cfgCopy)
}
