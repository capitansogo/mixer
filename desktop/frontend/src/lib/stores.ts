import { writable } from 'svelte/store';

export type Calibration = { min: number; max: number };

export type MixSource = { exe: string; gain: number; muted: boolean };

export type AudioMix = {
  enabled: boolean;
  micDevice: string;
  outputDevice: string;
  micGain: number;
  micMuted: boolean;
  sources: MixSource[];
};

export type Config = {
  sliderMapping: Record<number, string[]>;
  comPort: string;
  baudRate: number;
  invertSliders: boolean;
  noiseReduction: number;
  calibration: Record<number, Calibration>;
  ledMode: number;
  audioMix: AudioMix;
};

/** Live state of one Аудио-микс source ("mic" or an exe name). */
export type MixSourceState = {
  id: string;
  gain: number;
  muted: boolean;
  active: boolean;
  level: number;
  error: string;
};

export type MixStatus = {
  enabled: boolean;
  running: boolean;
  mic: string;
  output: string;
  error: string;
  outLevel: number;
  sources: MixSourceState[];
};

export type MixDevice = { id: string; name: string; default: boolean; virtual: boolean };

/** Slider-target prefix that drives an Аудио-микс gain instead of a Windows volume. */
export const MIX_PREFIX = 'mix:';

export type AudioSession = {
  pid: number;
  name: string;
  volume: number;
  isSystem: boolean;
};

export type DeviceState = { theme: number; brightness: number; mode: number };

export const values = writable<number[]>([0, 0, 0, 0, 0]);
export const connected = writable<boolean>(false);
export const reconnecting = writable<boolean>(false);
/** Last STATE report from the firmware (theme/brightness/mode); null until the device answered. */
export const deviceState = writable<DeviceState | null>(null);
export const status = writable<string>('');
export const selectedPort = writable<string>('');
export const ports = writable<string[]>([]);
export const configPath = writable<string>('');
export const cfg = writable<Config | null>(null);
