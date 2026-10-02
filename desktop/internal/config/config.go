// Package config persists user preferences to %APPDATA%\mixer\config.yaml.
// The on-disk format matches the structure described in mixer/ТЗ.md §4.5.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	appDirName  = "mixer"
	fileName    = "config.yaml"
	DefaultBaud = 115200
)

// Calibration holds the observed raw-ADC extremes for one slider.
// Used to normalize so that the user's actual top/bottom position
// maps to 1.0/0.0 even if the pot physically can't reach 0 or 1023.
type Calibration struct {
	Min int `yaml:"min" json:"min"`
	Max int `yaml:"max" json:"max"`
}

// Config is the full set of user-tunable settings. Keep field tags
// lowercase + snake_case to match the human-friendly YAML in ТЗ.md.
type Config struct {
	// SliderMapping maps slider index (0..4) to a list of audio
	// targets. Each target is either a process basename ("chrome.exe")
	// or one of the special keywords from internal/audio:
	// "master", "system", "mic", "game".
	SliderMapping map[int][]string `yaml:"slider_mapping" json:"sliderMapping"`

	ComPort        string `yaml:"com_port"         json:"comPort"`
	BaudRate       int    `yaml:"baud_rate"        json:"baudRate"`
	InvertSliders  bool   `yaml:"invert_sliders"   json:"invertSliders"`
	NoiseReduction int    `yaml:"noise_reduction"  json:"noiseReduction"`

	// Calibration is per-slider raw min/max. Index 0..4. Defaults
	// 0..1023 (full ADC range) if not yet calibrated.
	Calibration map[int]Calibration `yaml:"calibration" json:"calibration"`

	// LedMode mirrors the firmware's MODE: command — 0 position,
	// 1 rainbow, 2 meter (PC pushes audio peaks to the LED strips).
	LedMode int `yaml:"led_mode" json:"ledMode"`

	// AudioMix configures the "Аудио-микс" bus: mic + app audio mixed
	// into a virtual cable that other apps use as their microphone.
	AudioMix AudioMix `yaml:"audio_mix" json:"audioMix"`
}

// MixSource is one application mixed into the Аудио-микс bus.
type MixSource struct {
	Exe   string  `yaml:"exe"   json:"exe"`
	Gain  float32 `yaml:"gain"  json:"gain"` // fader position 0..1
	Muted bool    `yaml:"muted" json:"muted"`
}

// AudioMix is the persisted state of the Аудио-микс page. Slider targets
// "mix:mic" / "mix:<exe>" in SliderMapping drive these gains from hardware.
type AudioMix struct {
	Enabled      bool        `yaml:"enabled"       json:"enabled"`
	MicDevice    string      `yaml:"mic_device"    json:"micDevice"`    // endpoint id, "" = auto
	OutputDevice string      `yaml:"output_device" json:"outputDevice"` // endpoint id, "" = auto (VB-Cable)
	MicGain      float32     `yaml:"mic_gain"      json:"micGain"`
	MicMuted     bool        `yaml:"mic_muted"     json:"micMuted"`
	Sources      []MixSource `yaml:"sources"       json:"sources"`
}

// DefaultAudioMix: off, mic at full, music-ish apps at half (≈ -12 dB).
func DefaultAudioMix() AudioMix {
	return AudioMix{
		MicGain: 1,
		Sources: []MixSource{
			{Exe: "spotify.exe", Gain: 0.5},
			{Exe: "chrome.exe", Gain: 0.5},
		},
	}
}

// Default returns a sensible starter config. Picked to mirror the v1
// hardcoded mapping that we used during initial bring-up so existing
// behavior is preserved on first launch.
func Default() Config {
	return Config{
		SliderMapping: map[int][]string{
			0: {"master"},
			1: {"chrome.exe", "msedge.exe", "firefox.exe"},
			2: {"discord.exe"},
			3: {"spotify.exe"},
			4: {"system"},
		},
		ComPort:        "",
		BaudRate:       DefaultBaud,
		InvertSliders:  false,
		NoiseReduction: 4,
		Calibration:    DefaultCalibration(),
		AudioMix:       DefaultAudioMix(),
	}
}

// DefaultCalibration returns a fresh 0..1023 calibration for every slider.
func DefaultCalibration() map[int]Calibration {
	m := make(map[int]Calibration, 5)
	for i := 0; i < 5; i++ {
		m[i] = Calibration{Min: 0, Max: 1023}
	}
	return m
}

// Path returns %APPDATA%\mixer\config.yaml on Windows (uses os.UserConfigDir).
func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appDirName, fileName), nil
}

// Load reads the config file, creating it with defaults if missing.
// The second return value is true if the file was just created.
func Load() (Config, bool, error) {
	p, err := Path()
	if err != nil {
		return Config{}, false, err
	}

	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if err := saveTo(p, cfg); err != nil {
			return cfg, false, fmt.Errorf("create default config: %w", err)
		}
		return cfg, true, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read %s: %w", p, err)
	}

	// Scalars fall back to defaults when absent from the file, but the
	// maps must NOT be pre-populated: yaml.v3 merges into an existing map
	// key by key, which would resurrect a binding the user deleted from
	// the file by hand. Start them nil and default only when missing.
	cfg := Default()
	cfg.SliderMapping = nil
	cfg.Calibration = nil
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", p, err)
	}
	if cfg.SliderMapping == nil {
		cfg.SliderMapping = Default().SliderMapping
	}
	if cfg.Calibration == nil {
		cfg.Calibration = DefaultCalibration()
	}
	if cfg.BaudRate == 0 {
		cfg.BaudRate = DefaultBaud
	}
	return cfg, false, nil
}

func Save(cfg Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	return saveTo(p, cfg)
}

func saveTo(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
