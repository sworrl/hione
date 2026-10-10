export interface DAC { filter: string; gain: string; dre: string; hp: string }

export interface DeviceStatus {
  target: string; ok: boolean; error?: string; updated: string;
  dac?: DAC; dac_label?: string; wired_output: string; volume: string;
  sources: Record<string, string>;
}

export interface LogEvent { type: string; time: string; msg?: string }

export interface RunSummary {
  id: string; kind: string; status: string; error?: string; steps: number; done: number; started: string;
}

export interface State {
  capture: string; cap_rate: number; cap_format: string; device: string; level_dbfs: number;
  busy: boolean; capture_error: string; runs: RunSummary[]; logs: LogEvent[]; filters: string[];
  device_status?: DeviceStatus;
}

export interface Curve { hz: number[]; db: number[]; phase?: number[]; gd?: number[] }

export interface Tone {
  freq_hz: number; level_dbfs: number; thd_db: number; thd_pct: number; thdn_db: number; thdn_pct: number;
  thdn_a_db: number; resid_dbfs: number; resid_a_dbfs: number; harmonics_db: number[];
}

export interface Noise { dbfs: number; dbfs_a: number; dbfs_wide: number; peak_dbfs: number; hum_dbfs: number; hum_hz: number }

export interface Channel {
  name: string; fr: Curve; fr_comp?: Curve; gain_db: number; polarity: string;
  ir: number[] | null; ir_start_ms: number; sweep_ir: number[]; sweep_ir_start_ms: number;
  tone?: Tone; low?: Tone; noise?: Noise; noise_re_dut_fs_db: number; noise_a_re_dut_fs_db: number;
  snr_a_db: number; dr_a_db: number; xtalk_db: number; full_scale_dbv: number;
  clipped?: string[];
}

export interface Result {
  found: boolean; message?: string; sync_confidence: number; start_s: number; drift_ppm: number;
  cap_rate: number; play_rate: number; peak_dbfs: number; balance_db: number; lr_swapped: boolean;
  channels: Channel[]; notes?: string[];
}

export interface Step {
  run_id: string; index: number; label: string; group: string; dac: DAC; play_rate: number;
  status: string; error?: string; result?: Result; started: string;
}

export interface LevelResult { pass: boolean; tone_dbfs: number[]; peak_dbfs: number[]; test_level_dbfs: number; message: string }

export interface Run {
  id: string; spec: { kind: string; plan: string; rates: number[]; jack: string };
  status: string; error?: string; started: string; finished?: string; dir: string;
  steps: Step[]; user_dac?: DAC; level?: LevelResult; device?: Record<string, string>; capture: Record<string, string>;
}

export interface ChartInfo { id: string; title: string; caption: string }

export interface Progress { run_id: string; label: string; phase: string; fraction: number; segment?: string }

/** One live frame from the binary WebSocket. */
export interface LiveFrame {
  rate: number; time: number; clip: [boolean, boolean];
  peak: [number, number]; rms: [number, number];
  specL: Float32Array; specR: Float32Array;
}

export const FILTER_LABEL: Record<string, string> = {
  fast_rolloff_low_latency: 'Fast, low latency',
  fast_rolloff_phase_compensated: 'Fast, phase compensated',
  slow_rolloff_low_latency: 'Slow, low latency',
  slow_rolloff_phase_compensated: 'Slow, phase compensated',
  nos: 'NOS',
};
