/**
 * Hione in the browser: the hosted page measures with the visitor's own audio input, and nothing
 * leaves their machine. There is no device control here. The visitor sets the player's mode
 * themselves and plays the test file on it; the page waits for the file's start marker, records
 * the whole test signal, and analyzes it in a worker with the same maths as the Go engine.
 *
 * Events use the shapes the backend's WebSocket sends (log, progress, step, run, level), so the
 * page's views work the same in both modes.
 */
import type { LevelResult, LogEvent, Progress, Result, Run, RunSummary, State, Step } from '../types';
import { Capture, type InputInfo } from './capture';
import { levelAt, peak } from './dsp';
import { LiveMeter } from './live';
import { build } from './stim';
import type { PlanName, Req } from './worker';

export const TEST_LEVEL = -12;
const MIN_CONFIDENCE = 8; // analysis.MinConfidence
const MAX_PEAK = -2; // engine.MaxPeak: the test level must peak below this at the input
const MIN_TONE = -50; // engine.MinTone

export type EngineEvent =
  | { type: 'log'; time: string; msg: string }
  | { type: 'progress'; progress: Progress | null }
  | { type: 'step'; step: Step }
  | { type: 'run'; run: RunSummary }
  | { type: 'level'; msg: string };

export interface MeasureSpec {
  label: string;
  plan: 'quick' | 'full';
  playRate: number;
  /** Optional, for a HiBy M500 the visitor set by hand: groups the curves like the matrix does. */
  filter?: string;
  gain?: string;
}

type Pending = { resolve: (v: any) => void; reject: (e: Error) => void };
// Omit distributed over the union, so each request keeps its own fields.
type DistOmit<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never;
type ReqBody = DistOmit<Req, 'id'>;

export class BrowserEngine {
  readonly capture = new Capture();
  meter: LiveMeter | null = null;
  runs: Run[] = [];
  logs: LogEvent[] = [];
  busy = false;
  captureError = '';
  private listeners = new Set<(e: EngineEvent) => void>();
  private frameListeners = new Set<(f: ReturnType<LiveMeter['feed']> & object) => void>();
  private worker: Worker | null = null;
  private seq = 0;
  private pending = new Map<number, Pending>();
  private abort: AbortController | null = null;
  private stimCache = new Map<string, string>();

  constructor() {
    if (new URLSearchParams(location.search).has('debug')) (window as unknown as Record<string, unknown>).__hioneEngine = this;
  }

  on(fn: (e: EngineEvent) => void) { this.listeners.add(fn); return () => { this.listeners.delete(fn); }; }
  onFrame(fn: (f: NonNullable<ReturnType<LiveMeter['feed']>>) => void) { this.frameListeners.add(fn); return () => { this.frameListeners.delete(fn); }; }
  private emit(e: EngineEvent) { this.listeners.forEach(fn => fn(e)); }

  log(msg: string) {
    const l = { type: 'log', time: new Date().toISOString(), msg };
    this.logs = [...this.logs.slice(-400), l];
    this.emit(l as EngineEvent);
  }

  private call<T>(req: ReqBody, transfer: Transferable[] = []): Promise<T> {
    if (!this.worker) {
      this.worker = new Worker(new URL('./worker.ts', import.meta.url), { type: 'module' });
      this.worker.onmessage = (ev: MessageEvent) => {
        const p = this.pending.get(ev.data.id);
        if (!p) return;
        this.pending.delete(ev.data.id);
        if (ev.data.ok) p.resolve(ev.data.value); else p.reject(new Error(ev.data.error));
      };
    }
    const id = ++this.seq;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.worker!.postMessage({ ...req, id }, transfer);
    });
  }

  // ---- input ----

  async open(deviceId?: string): Promise<InputInfo> {
    this.captureError = '';
    try {
      const info = await this.capture.open(deviceId);
      this.meter = new LiveMeter(info.rate);
      this.capture.on((l, r) => {
        const f = this.meter?.feed(l, r);
        if (f) this.frameListeners.forEach(fn => fn(f));
      });
      this.log(`Input: ${info.label}, ${info.rate / 1000} kHz, ${info.channels < 2 ? 'mono' : 'stereo'}, 32-bit float.`);
      if (info.channels < 2) this.log('This input is mono, so crosstalk and channel balance will not mean anything. A stereo line input is needed for those.');
      if (info.processing.length) this.log(`The browser left this on despite being asked not to: ${info.processing.join(', ')}. Results will be affected.`);
      if (info.rate < 96000) this.log(`The input runs at ${info.rate / 1000} kHz, so the response above ${Math.round(info.rate * 0.45 / 1000)} kHz cannot be seen. Set the input to 96 or 192 kHz in your system's sound settings if it supports it.`);
      return info;
    } catch (e) {
      this.captureError = (e as Error).message || String(e);
      throw e;
    }
  }

  state(): State {
    const i = this.capture.info;
    return {
      capture: i ? i.label : 'no input yet',
      cap_rate: i ? i.rate : 0,
      cap_format: 'F32',
      device: 'you press play',
      level_dbfs: TEST_LEVEL,
      busy: this.busy,
      capture_error: this.captureError,
      runs: this.runs.map(summary),
      logs: this.logs,
      filters: [],
    };
  }

  run(id: string): Run | undefined { return this.runs.find(r => r.id === id); }

  stop() { this.abort?.abort(); }

  // ---- test files ----

  /** A blob URL for the test file at a playback rate (rendered once per page, 24-bit WAV). */
  async stimulusURL(plan: PlanName, rate: number): Promise<string> {
    const k = `${plan}@${rate}`;
    const have = this.stimCache.get(k);
    if (have) return have;
    const v = await this.call<{ wav: Uint8Array }>({ type: 'stim', plan, level: TEST_LEVEL, rate });
    const url = URL.createObjectURL(new Blob([v.wav], { type: 'audio/wav' }));
    this.stimCache.set(k, url);
    return url;
  }

  static stimulusName(plan: PlanName, rate: number) { return `hione-v2-${plan}-${-TEST_LEVEL}dB-${rate}.wav`; }

  // ---- sessions ----

  private session(): Run {
    let r = this.runs[0];
    if (!r || r.status !== 'open') {
      const now = new Date();
      const id = now.toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
      const i = this.capture.info;
      r = {
        id, spec: { kind: 'browser', plan: 'quick', rates: [], jack: '' }, status: 'open', started: now.toISOString(), dir: '',
        steps: [], device: { device: 'you press play', where: 'measured in the browser' },
        capture: i ? { device: i.label, rate: String(i.rate), format: 'F32', channels: String(i.channels) } : {},
      };
      this.runs = [r, ...this.runs];
      this.emit({ type: 'run', run: summary(r) });
    }
    return r;
  }

  newSession() {
    const r = this.runs[0];
    if (r && r.status === 'open') { r.status = 'done'; r.finished = new Date().toISOString(); this.emit({ type: 'run', run: summary(r) }); }
  }

  // ---- capture helpers ----

  /** Collects capture blocks until stopped; keeps at most `keep` samples while `trim` is on. */
  private recorder() {
    const chunks: [Float32Array[], Float32Array[]] = [[], []];
    let len = 0, offset = 0;
    const off = this.capture.on((l, r) => { chunks[0].push(l); chunks[1].push(r); len += l.length; });
    return {
      get length() { return len; },
      get offset() { return offset; },
      /** Drop everything before absolute sample index abs. */
      dropBefore(abs: number) {
        let cut = abs - offset;
        while (cut > 0 && chunks[0].length) {
          const n = chunks[0][0].length;
          if (n > cut) { chunks[0][0] = chunks[0][0].subarray(cut); chunks[1][0] = chunks[1][0].subarray(cut); len -= cut; offset += cut; break; }
          chunks[0].shift(); chunks[1].shift(); len -= n; offset += n; cut -= n;
        }
      },
      /** The last n samples per channel, as float64. */
      tail(n: number): Float64Array[] { return [0, 1].map(c => flatten(chunks[c], len, Math.max(0, len - n))); },
      all(): Float64Array[] { return [0, 1].map(c => flatten(chunks[c], len, 0)); },
      stop: off,
    };
  }

  private async wait(ms: number, signal: AbortSignal) {
    await new Promise<void>((res, rej) => {
      const t = setTimeout(res, ms);
      signal.addEventListener('abort', () => { clearTimeout(t); rej(new DOMException('Stopped', 'AbortError')); }, { once: true });
    });
  }

  // ---- level check ----

  /**
   * The visitor plays the level file (1 kHz at the test level). The page waits up to five
   * minutes for the tone, then reads it every couple of seconds until it passes or they stop.
   * It never changes anything on the device; if it is too loud, it says so and keeps listening.
   */
  async levelCheck(): Promise<LevelResult> {
    if (!this.capture.info) throw new Error('Choose an audio input first.');
    if (this.busy) throw new Error('Already measuring.');
    this.busy = true;
    this.abort = new AbortController();
    const signal = this.abort.signal;
    const rate = this.capture.info.rate;
    const run = this.session();
    const rec = this.recorder();
    const res: LevelResult = { pass: false, tone_dbfs: [-999, -999], peak_dbfs: [-999, -999], test_level_dbfs: TEST_LEVEL, message: '' };
    const prog = (phase: string, f: number) => this.emit({ type: 'progress', progress: { run_id: run.id, label: 'Level check', phase, fraction: f } });
    try {
      this.log(`Level check: play the level file on your device now. It is a 1 kHz tone at ${TEST_LEVEL} dBFS.`);
      const deadline = Date.now() + 5 * 60_000;
      let found = false;
      while (!found) {
        if (Date.now() > deadline) { res.message = 'No 1 kHz tone arrived at the input in five minutes. Check the cable, the input you chose and that the level file is playing.'; break; }
        prog('waiting for the tone', 0);
        await this.wait(500, signal);
        const b = rec.tail(Math.round(0.5 * rate));
        for (const x of b) {
          const lv = levelAt(x, rate, 1000);
          if (lv > -90 && lv > db(rmsOf(x) * Math.SQRT2) - 3) found = true;
        }
        rec.dropBefore(rec.offset + rec.length - Math.round(2 * rate));
      }
      let quietLogged = false;
      while (found) {
        prog('reading the level', 0.5);
        await this.wait(2200, signal);
        const b = rec.tail(Math.round(1.5 * rate));
        rec.dropBefore(rec.offset + rec.length - Math.round(2 * rate));
        for (let c = 0; c < 2; c++) { res.tone_dbfs[c] = levelAt(b[c], rate, 1000); res.peak_dbfs[c] = db(peak(b[c])); }
        const pk = Math.max(...res.peak_dbfs), tone = Math.max(...res.tone_dbfs);
        this.emit({ type: 'level', msg: `${res.tone_dbfs[0].toFixed(1)},${res.tone_dbfs[1].toFixed(1)}` });
        if (tone < -90) { res.message = 'The tone stopped. Play the level file again and rerun the check.'; break; }
        if (pk > MAX_PEAK) { this.log(`Too loud: peak ${pk.toFixed(1)} dBFS at the input, needs ${MAX_PEAK} or lower. Turn the device volume down a step; still listening.`); continue; }
        if (tone < MIN_TONE) {
          if (!quietLogged) this.log(`Too quiet: the tone reads ${tone.toFixed(1)} dBFS at the input. Turn the device volume up, or the input gain in your system settings; still listening.`);
          quietLogged = true;
          continue;
        }
        res.pass = true;
        res.message = `Level OK: the test level reads ${tone.toFixed(1)} dBFS at the input, peak ${pk.toFixed(1)} dBFS.`;
        if (tone < -30) res.message += ' That is on the quiet side, so noise results will be limited by the input. A higher device volume helps.';
        break;
      }
    } catch (e) {
      if ((e as Error).name !== 'AbortError') throw e;
      res.message = 'Level check stopped.';
    } finally {
      rec.stop();
      this.busy = false;
      this.abort = null;
      this.emit({ type: 'progress', progress: null });
    }
    run.level = res;
    this.log(res.message);
    this.emit({ type: 'run', run: summary(run) });
    return res;
  }

  // ---- one measurement ----

  /**
   * Record one pass of the test file. The page listens for the start marker (a short upward
   * sweep), then records until the whole plan and 3 s spare have arrived, the same rule as the
   * Go engine, and analyzes it in the worker.
   */
  async measure(spec: MeasureSpec): Promise<Step> {
    if (!this.capture.info) throw new Error('Choose an audio input first.');
    if (this.busy) throw new Error('Already measuring.');
    this.busy = true;
    this.abort = new AbortController();
    const signal = this.abort.signal;
    const capRate = this.capture.info.rate;
    const run = this.session();
    if (!run.spec.rates.includes(spec.playRate)) run.spec.rates = [...run.spec.rates, spec.playRate];
    run.spec.plan = spec.plan;
    const step: Step = {
      run_id: run.id, index: run.steps.length, label: spec.label, group: spec.filter ? 'filters' : 'single',
      dac: { filter: spec.filter || '', gain: spec.gain || '', dre: '', hp: '' }, play_rate: spec.playRate,
      status: 'running', started: new Date().toISOString(),
    };
    run.steps = [...run.steps, step];
    this.emit({ type: 'step', step });
    this.emit({ type: 'run', run: summary(run) });
    const plan = build({ name: spec.plan, level: TEST_LEVEL, playRate: spec.playRate });
    const rec = this.recorder();
    const prog = (phase: string, f: number) => this.emit({ type: 'progress', progress: { run_id: run.id, label: spec.label, phase, fraction: f } });
    try {
      this.log(`${spec.label}: play ${BrowserEngine.stimulusName(spec.plan, spec.playRate)} on your device now, from the start. Recording waits for its start marker.`);
      let syncAbs = -1, syncConf = 0, need = 0, lastCheck = 0;
      const deadline = Date.now() + 5 * 60_000 + plan.total * 1000;
      for (;;) {
        await this.wait(250, signal);
        if (Date.now() > deadline) throw new Error(syncAbs < 0 ? 'the test signal never arrived at the input' : 'recording timed out');
        const total = rec.offset + rec.length;
        const checkMarker = syncAbs < 0 || total - syncAbs < 3 * capRate;
        if (checkMarker && total - lastCheck >= 0.25 * capRate && rec.length > 0.8 * capRate) {
          lastCheck = total;
          const win = Math.min(rec.length, Math.round(2.5 * capRate));
          const a = rec.offset + rec.length - win;
          const ch = rec.tail(win);
          const m = await this.call<{ pos: number; conf: number; refLen: number }>({ type: 'marker', ch, level: TEST_LEVEL, rate: capRate }, ch.map(x => x.buffer));
          const whole = m.pos + m.refLen < win - 0.05 * capRate;
          if (syncAbs < 0 && m.conf >= MIN_CONFIDENCE && whole) {
            syncAbs = a + m.pos; syncConf = m.conf;
            rec.dropBefore(Math.round(syncAbs - (plan.syncLead + 0.3) * capRate));
            this.log(`Test signal found (sync confidence ${isFinite(m.conf) ? m.conf.toFixed(0) : 'above 999'}).`);
          } else if (syncAbs >= 0 && m.conf > 1.5 * syncConf && m.conf >= MIN_CONFIDENCE && a + m.pos > syncAbs + 0.05 * capRate && whole) {
            this.log(`Clearer start marker found ${((a + m.pos - syncAbs) / capRate).toFixed(2)} s later (confidence ${isFinite(m.conf) ? m.conf.toFixed(0) : 'above 999'}), using it.`);
            syncAbs = a + m.pos; syncConf = m.conf;
          }
          if (syncAbs >= 0) need = Math.round(syncAbs + (plan.total - plan.syncLead + 3) * capRate * 1.001) - rec.offset;
        }
        if (syncAbs < 0) {
          rec.dropBefore(rec.offset + rec.length - Math.round(4 * capRate));
          prog('waiting for the test file', 0);
        } else {
          prog('recording', Math.min(1, rec.length / need));
          if (rec.length >= need) break;
        }
      }
      rec.stop();
      prog('analyzing', 1);
      const data = rec.all();
      // ?debug keeps a copy of the raw recording for inspection from the console.
      if (new URLSearchParams(location.search).has('debug')) (window as unknown as Record<string, unknown>).__hioneRec = data.map(x => x.slice());
      const v = await this.call<{ result: Result; ms: number }>({ type: 'analyze', rec: data, plan: spec.plan, level: TEST_LEVEL, playRate: spec.playRate, capRate }, data.map(x => x.buffer));
      step.result = v.result;
      if (!v.result.found) throw new Error(v.result.message || 'the test signal was not found in the recording');
      if (this.capture.mono) v.result.notes = [...(v.result.notes || []), 'Recorded from a mono input: crosstalk and balance are not meaningful.'];
      step.status = 'done';
      const L = v.result.channels[0];
      this.log(`  level ${fmt(L.tone?.level_dbfs)} dBFS, THD+N ${fmt(L.tone?.thdn_db)} dB, noise ${fmt(L.noise?.dbfs_a)} dBFS(A), drift ${fmt(v.result.drift_ppm)} ppm. Analyzed in ${(v.ms / 1000).toFixed(1)} s.`);
    } catch (e) {
      rec.stop();
      const err = e as Error;
      step.status = 'failed';
      step.error = err.name === 'AbortError' ? 'stopped' : err.message;
      this.log(`Step failed: ${step.error}`);
    } finally {
      this.busy = false;
      this.abort = null;
      this.emit({ type: 'progress', progress: null });
      this.emit({ type: 'step', step });
      this.emit({ type: 'run', run: summary(run) });
    }
    return step;
  }

  // ---- results files ----

  exportJSON(run: Run): Blob {
    return new Blob([JSON.stringify({ format: 'hione.run', version: 1, measured_in: 'browser', run }, null, 1)], { type: 'application/json' });
  }

  exportCSV(run: Run): Blob {
    const head = ['step', 'label', 'play_rate', 'channel', 'level_dbfs', 'thd_db', 'thdn_db', 'noise_dbfs', 'noise_a_dbfs', 'snr_a_db', 'dr_a_db', 'xtalk_db', 'balance_db', 'drift_ppm', 'polarity'];
    const rows = [head.join(',')];
    for (const s of run.steps) {
      const r = s.result;
      if (!r?.found) continue;
      for (const ch of r.channels) {
        rows.push([s.index + 1, JSON.stringify(s.label), s.play_rate, ch.name, ch.tone?.level_dbfs, ch.tone?.thd_db, ch.tone?.thdn_db, ch.noise?.dbfs, ch.noise?.dbfs_a,
          ch.snr_a_db, ch.dr_a_db, ch.xtalk_db, r.balance_db, r.drift_ppm, ch.polarity].map(v => (typeof v === 'number' ? v.toFixed(3) : v ?? '')).join(','));
      }
    }
    return new Blob([rows.join('\n') + '\n'], { type: 'text/csv' });
  }

  async importJSON(file: File): Promise<Run> {
    const j = JSON.parse(await file.text());
    const run: Run | undefined = j?.format === 'hione.run' ? j.run : (j?.steps && j?.id ? j : undefined);
    if (!run || !Array.isArray(run.steps)) throw new Error('That is not a Hione results file.');
    run.status = 'done';
    this.runs = [run, ...this.runs.filter(r => r.id !== run.id)];
    this.emit({ type: 'run', run: summary(run) });
    return run;
  }
}

function summary(r: Run): RunSummary {
  return { id: r.id, kind: r.spec.kind, status: r.status, error: r.error, steps: r.steps.length, done: r.steps.filter(s => s.status === 'done' || s.status === 'failed').length, started: r.started };
}

function flatten(chunks: Float32Array[], len: number, from: number): Float64Array {
  const out = new Float64Array(len - from);
  let pos = 0, o = 0;
  for (const c of chunks) {
    const a = Math.max(0, from - pos), b = c.length;
    if (a < b) { out.set(c.subarray(a, b), o); o += b - a; }
    pos += c.length;
  }
  return out;
}

const db = (v: number) => 20 * Math.log10(Math.max(v, 1e-15));
function rmsOf(x: Float64Array) { let s = 0; for (let i = 0; i < x.length; i++) s += x[i] * x[i]; return Math.sqrt(s / Math.max(1, x.length)); }
const fmt = (v: number | undefined) => (v === undefined || !isFinite(v) || v < -900 ? '-' : v.toFixed(1));
