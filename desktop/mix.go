package main

// Аудио-микс: the GUI-facing side of internal/mixbus — bound methods for
// the "Аудио-микс" page, the hardware-slider targets "mix:mic" and
// "mix:<exe>", and the debounced persistence of fader positions.

import (
	"errors"
	"strings"
	"time"

	mconfig "mixer/internal/config"
	"mixer/internal/mixbus"
	"mixer/internal/winui"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// mixTargetPrefix marks a slider target that drives a mix gain instead
	// of a Windows session volume: "mix:mic", "mix:spotify.exe".
	mixTargetPrefix = "mix:"

	mixStatusInterval = 40 * time.Millisecond
	saveDebounce      = 1 * time.Second
)

func mixTarget(t string) (string, bool) {
	if !strings.HasPrefix(t, mixTargetPrefix) {
		return "", false
	}
	id := strings.ToLower(strings.TrimSpace(t[len(mixTargetPrefix):]))
	return id, id != ""
}

func mixSettings(m mconfig.AudioMix) mixbus.Settings {
	s := mixbus.Settings{
		Enabled:      m.Enabled,
		MicDevice:    m.MicDevice,
		OutputDevice: m.OutputDevice,
		MicGain:      m.MicGain,
		MicMuted:     m.MicMuted,
		Apps:         make([]mixbus.AppSource, 0, len(m.Sources)),
	}
	for _, src := range m.Sources {
		s.Apps = append(s.Apps, mixbus.AppSource{Exe: src.Exe, Gain: src.Gain, Muted: src.Muted})
	}
	return s
}

func normalizeExe(exe string) string {
	exe = strings.ToLower(strings.TrimSpace(exe))
	if exe != "" && !strings.HasSuffix(exe, ".exe") {
		exe += ".exe"
	}
	return exe
}

// pumpMix streams the bus status (levels, gains, activity) to the GUI
// while the Аудио-микс page is open and the window is visible.
func (a *App) pumpMix() {
	t := time.NewTicker(mixStatusInterval)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			if !a.mixWatch.Load() || !winui.MainWindowVisible() {
				continue
			}
			wruntime.EventsEmit(a.ctx, "mix-status", a.mix.Status())
		}
	}
}

// ---------- config helpers ----------

// updateMix mutates the AudioMix section, pushes it to the engine and
// saves the config right away. Returns the new config for the GUI store.
func (a *App) updateMix(fn func(m *mconfig.AudioMix) error) (mconfig.Config, error) {
	a.mu.Lock()
	m := a.cfg.AudioMix
	m.Sources = append([]mconfig.MixSource(nil), m.Sources...)
	if err := fn(&m); err != nil {
		a.mu.Unlock()
		return mconfig.Config{}, err
	}
	a.cfg.AudioMix = m
	cfg := a.cfg
	a.mu.Unlock()

	a.mix.Apply(mixSettings(m))
	a.cancelPendingSave()
	return cfg, mconfig.Save(cfg)
}

// setCfgGain records a fader position in the config (no save).
func (a *App) setCfgGain(id string, pos float32, unmute bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := &a.cfg.AudioMix
	if id == mixbus.MicID {
		m.MicGain = pos
		if unmute {
			m.MicMuted = false
		}
		return
	}
	src := make([]mconfig.MixSource, len(m.Sources))
	copy(src, m.Sources)
	for i := range src {
		if src[i].Exe == id {
			src[i].Gain = pos
			if unmute {
				src[i].Muted = false
			}
		}
	}
	m.Sources = src
}

func (a *App) scheduleSave() {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	a.saveTimer = time.AfterFunc(saveDebounce, func() {
		a.saveMu.Lock()
		a.saveTimer = nil
		a.saveMu.Unlock()
		a.mu.RLock()
		cfg := a.cfg
		a.mu.RUnlock()
		_ = mconfig.Save(cfg)
	})
}

func (a *App) cancelPendingSave() {
	a.saveMu.Lock()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
		a.saveTimer = nil
	}
	a.saveMu.Unlock()
}

// flushSave writes a pending debounced save immediately (on shutdown).
func (a *App) flushSave() {
	a.saveMu.Lock()
	pending := a.saveTimer != nil
	if pending {
		a.saveTimer.Stop()
		a.saveTimer = nil
	}
	a.saveMu.Unlock()
	if pending {
		a.mu.RLock()
		cfg := a.cfg
		a.mu.RUnlock()
		_ = mconfig.Save(cfg)
	}
}

// setMixGainFromSlider is called by the applier for "mix:*" targets.
// Like the Windows-session targets, raising a slider un-mutes the source.
func (a *App) setMixGainFromSlider(id string, level float32) {
	if !a.mix.SetGain(id, level) {
		return // no such source in the mix (yet)
	}
	unmute := level > 0
	if unmute {
		a.mix.SetMuted(id, false)
	}
	a.setCfgGain(id, level, unmute)
	a.scheduleSave()
}

// ---------- bound methods ----------

// WatchMix turns the mix-status event stream on while the page is open.
func (a *App) WatchMix(on bool) { a.mixWatch.Store(on) }

func (a *App) GetMixStatus() mixbus.Status { return a.mix.Status() }

func (a *App) ListMixDevices() (mixbus.Devices, error) { return mixbus.ListDevices() }

func (a *App) SetMixEnabled(on bool) (mconfig.Config, error) {
	return a.updateMix(func(m *mconfig.AudioMix) error {
		m.Enabled = on
		return nil
	})
}

// SetMixDevices sets the microphone and output endpoint ids ("" = auto).
func (a *App) SetMixDevices(mic, out string) (mconfig.Config, error) {
	return a.updateMix(func(m *mconfig.AudioMix) error {
		m.MicDevice, m.OutputDevice = mic, out
		return nil
	})
}

// SetMixGain moves one fader (0..1). Called at drag rate, so the config
// write is debounced.
func (a *App) SetMixGain(id string, gain float32) {
	id = strings.ToLower(id)
	if gain < 0 {
		gain = 0
	} else if gain > 1 {
		gain = 1
	}
	if !a.mix.SetGain(id, gain) {
		return
	}
	a.setCfgGain(id, gain, false)
	a.scheduleSave()
}

func (a *App) SetMixMuted(id string, muted bool) (mconfig.Config, error) {
	id = strings.ToLower(id)
	return a.updateMix(func(m *mconfig.AudioMix) error {
		if id == mixbus.MicID {
			m.MicMuted = muted
			return nil
		}
		for i := range m.Sources {
			if m.Sources[i].Exe == id {
				m.Sources[i].Muted = muted
				return nil
			}
		}
		return errors.New("нет такого источника: " + id)
	})
}

func (a *App) AddMixSource(exe string) (mconfig.Config, error) {
	exe = normalizeExe(exe)
	return a.updateMix(func(m *mconfig.AudioMix) error {
		if exe == "" || exe == mixbus.MicID+".exe" {
			return errors.New("пустое имя приложения")
		}
		for _, s := range m.Sources {
			if s.Exe == exe {
				return errors.New(exe + " уже в миксе")
			}
		}
		m.Sources = append(m.Sources, mconfig.MixSource{Exe: exe, Gain: 0.5})
		return nil
	})
}

func (a *App) RemoveMixSource(exe string) (mconfig.Config, error) {
	exe = strings.ToLower(strings.TrimSpace(exe))
	return a.updateMix(func(m *mconfig.AudioMix) error {
		out := m.Sources[:0]
		for _, s := range m.Sources {
			if s.Exe != exe {
				out = append(out, s)
			}
		}
		m.Sources = out
		return nil
	})
}
