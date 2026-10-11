// Capture analysis: a port of internal/analysis (Analyze) plus internal/engine's Sanitize. Finds
// the sync markers, corrects clock drift, runs the sweep, tone, noise and crosstalk analyses.
// The Result serializes with JSON.stringify to the same field names, order and structure as the
// Go Result's JSON. Not ported: the Loopback/ChainCal compensation (fr_comp stays absent).

import {
  type Curve,
  type NoiseResult,
  type ToneResult,
  analyzeNoise,
  analyzeTone,
  curveNormalize,
  db,
  deconvRef,
  deconvolveWith,
  fracShift,
  fromDB,
  goRound,
  levelAt,
  logGrid,
  findMarker,
  findMarkers,
  nextPow2,
  peak,
  peakIndex,
  responseOf,
  slice,
  window,
  type Spectrum,
} from './dsp';
import { type Plan, type Segment, endSync, syncSweep } from './stim';

export interface AnalyzeOptions {
  playRate: number;
  capRate: number;
  bandLo?: number; // THD+N and noise band, default 20 Hz
  bandHi?: number; // default min(20 kHz, 0.45 * capRate)
  /** dBV = dBFS + dbvAtFS. NaN (or absent) when not calibrated. */
  dbvAtFS?: number;
}

/** Point is one step of a stepped-sine sweep. */
export interface Point {
  x: number; // Hz or dBFS
  thdn_db: number;
  thd_db: number;
  level_dbfs: number;
}

/** Channel holds one channel's results (JSON names and order as in Go). */
export interface Channel {
  name: string;
  fr: Curve; // dB re 1 kHz
  fr_comp?: Curve; // never set in the browser
  gain_db: number;
  polarity: string; // normal or inverted
  ir: number[] | null;
  ir_start_ms: number;
  sweep_ir: number[] | null;
  sweep_ir_start_ms: number;
  tone?: ToneResult;
  low?: ToneResult;
  noise?: NoiseResult;
  noise_re_dut_fs_db: number;
  noise_a_re_dut_fs_db: number;
  snr_a_db: number;
  dr_a_db: number;
  xtalk_db: number;
  full_scale_dbv: number; // NaN before sanitize = uncalibrated
  thd_vs_freq?: Point[];
  thd_vs_level?: Point[];
  imd_smpte_db: number;
  imd_ccif_db: number;
  clipped?: string[];
}

/** Result of one run (JSON names and order as in Go). */
export interface Result {
  found: boolean;
  message?: string;
  sync_confidence: number;
  start_s: number;
  drift_ppm: number;
  cap_rate: number;
  play_rate: number;
  peak_dbfs: number;
  balance_db: number;
  lr_swapped: boolean;
  channels: Channel[] | null;
  idle_noise?: NoiseResult[];
  notes?: string[];
}

/** Matched-filter confidence above which a sync counts as found. */
export const MIN_CONFIDENCE = 8;

/** Go's %.Nf: like toFixed, but exact ties round half to even. */
function goFixed(x: number, digits: number): string {
  if (!Number.isFinite(x)) return Number.isNaN(x) ? 'NaN' : x > 0 ? '+Inf' : '-Inf';
  const scale = Math.pow(10, digits);
  const s = Math.abs(x) * scale;
  if (s - Math.floor(s) === 0.5 && s < 2 ** 52) {
    let k = Math.floor(s);
    if (k % 2 === 1) k += 1;
    const v = (k / scale).toFixed(digits);
    return (x < 0 && k !== 0 ? '-' : x < 0 ? '-' : '') + v;
  }
  return x.toFixed(digits);
}

function locateRef(rec: Float64Array[], ref: Float64Array): [number, number] {
  let bp = 0;
  let bc = 0;
  for (const x of rec) {
    const [p, c] = findMarker(x, ref);
    if (c > bc) {
      bp = p;
      bc = c;
    }
  }
  return [bp, bc];
}

/** Locate finds the start sync per channel and returns [start index, confidence] of the clearer
 *  one. */
export function locate(rec: Float64Array[], plan: Plan, capRate: number): [number, number] {
  return locateRef(rec, syncSweep(plan.level).render(capRate, 1));
}

function newChannel(name: string): Channel {
  // Keys in Go struct order; optional ones are removed again in finish() when empty.
  return {
    name,
    fr: { hz: null, db: null },
    fr_comp: undefined,
    gain_db: 0,
    polarity: '',
    ir: null,
    ir_start_ms: 0,
    sweep_ir: null,
    sweep_ir_start_ms: 0,
    tone: undefined,
    low: undefined,
    noise: undefined,
    noise_re_dut_fs_db: 0,
    noise_a_re_dut_fs_db: 0,
    snr_a_db: 0,
    dr_a_db: 0,
    xtalk_db: 0,
    full_scale_dbv: NaN,
    thd_vs_freq: undefined,
    thd_vs_level: undefined,
    imd_smpte_db: NaN,
    imd_ccif_db: NaN,
    clipped: undefined,
  };
}

/** Drops keys that Go's omitempty would drop (undefined, empty string, empty array). */
function omitEmpty(o: Record<string, unknown>, keys: string[]): void {
  for (const k of keys) {
    const v = o[k];
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) delete o[k];
  }
}

function finish(r: Result): Result {
  omitEmpty(r as unknown as Record<string, unknown>, ['message', 'idle_noise', 'notes']);
  for (const ch of r.channels ?? []) {
    omitEmpty(ch as unknown as Record<string, unknown>, ['fr_comp', 'tone', 'low', 'noise', 'thd_vs_freq', 'thd_vs_level', 'clipped']);
  }
  return r;
}

/** Analyze runs everything on a capture. rec is [left, right] (or one channel). */
export function analyze(rec: Float64Array[], plan: Plan, opts: AnalyzeOptions): Result {
  const capRate = opts.capRate;
  const playRate = opts.playRate;
  const bandLo = opts.bandLo ? opts.bandLo : 20;
  const bandHi = opts.bandHi ? opts.bandHi : Math.min(20000, capRate * 0.45);
  const dbvAtFS = opts.dbvAtFS === undefined || opts.dbvAtFS === null ? NaN : opts.dbvAtFS;
  const r: Result = {
    found: false,
    message: undefined,
    sync_confidence: 0,
    start_s: 0,
    drift_ppm: 0,
    cap_rate: capRate,
    play_rate: playRate,
    peak_dbfs: 0,
    balance_db: 0,
    lr_swapped: false,
    channels: null,
    idle_noise: undefined,
    notes: undefined,
  };
  const notes: string[] = [];
  // Go starts PeakDBFS at 0, so this is max(0, peak): a capture below full scale reads 0.
  for (const c of rec) r.peak_dbfs = Math.max(r.peak_dbfs, db(peak(c)));
  if (rec[0].length === 0) r.peak_dbfs = -999;
  const startRef = syncSweep(plan.level).render(capRate, 1);
  const endRef = endSync(plan.level).render(capRate, 1);
  // Start and end markers in one pass per channel (they share the capture's transforms); the
  // selection below is the same as Go's Locate followed by locateRef(endRef).
  const marks = rec.map((x) => findMarkers(x, [startRef, endRef]));
  const best = (i: number): [number, number] => {
    let bp = 0;
    let bc = 0;
    for (const m of marks) {
      const [p, c] = m[i];
      if (c > bc) {
        bp = p;
        bc = c;
      }
    }
    return [bp, bc];
  };
  let [start, conf] = best(0);
  r.sync_confidence = conf;
  if (conf < MIN_CONFIDENCE) {
    r.found = false;
    r.message = `The test signal was not found in the capture (sync confidence ${goFixed(conf, 1)}, needs ${MIN_CONFIDENCE}). Only the idle noise was measured.`;
    r.idle_noise = rec.map((c) => analyzeNoise(c, capRate, bandLo, bandHi));
    return finish(r);
  }
  r.found = true;
  const span = (plan.syncTail - plan.syncLead) * capRate;
  let drift = 0;
  const [endPos, endConf] = best(1);
  let anchored = false;
  if (endConf >= MIN_CONFIDENCE) {
    const exp = endPos - span;
    const win = 0.05 * capRate;
    const a = Math.trunc(exp - win);
    const b = Math.trunc(exp + win) + startRef.length;
    if (a >= 0 && b <= rec[0].length) {
      const cut = rec.map((x) => x.subarray(a, b));
      const [p, c2] = locateRef(cut, startRef);
      if (c2 >= MIN_CONFIDENCE / 2) {
        if (Math.abs(a + p - start) > 0.01 * capRate) {
          notes.push(`Playback restarted near the beginning (the start marker came twice, ${goFixed(Math.abs(a + p - start) / capRate, 2)} s apart). The second start was used.`);
        }
        start = a + p;
        drift = (endPos - start) / span - 1;
        anchored = true;
      }
    }
  }
  if (!anchored) {
    notes.push('The end marker was not found, so clock drift was not corrected and a restart of playback would go unnoticed.');
  }
  r.start_s = start / capRate;
  r.drift_ppm = drift * 1e6;
  const recIdx = (t: number): number => start + (t - plan.syncLead) * (1 + drift) * capRate;
  const seg = (c: number, s: Segment, guardA: number, guardB: number): Float64Array =>
    slice(rec[c], Math.trunc(recIdx(s.start + guardA)), Math.trunc(recIdx(s.start + s.dur - guardB)));

  // The sweep reference and its self-deconvolution depend only on the sweep, the drift and the
  // segment length, so they are computed once and shared by both channels.
  let sweepCache: { len: number; ref: ReturnType<typeof deconvRef>; refSpec: Spectrum } | null = null;

  const names = ['Left', 'Right'];
  const chans: Channel[] = [];
  r.channels = chans;
  for (let c = 0; c < rec.length && c < 2; c++) {
    const ch = newChannel(names[c]);
    chans.push(ch);
    // Clipping per segment.
    for (const s of plan.segments) {
      if (s.kind === 'silence') continue;
      if (peak(seg(c, s, 0, 0)) >= 0.999) (ch.clipped ??= []).push(s.name);
    }
    // 1 kHz tone: level, THD, THD+N.
    const sThd = plan.find('thd');
    if (sThd) {
      const tr = analyzeTone(seg(c, sThd, 0.25, 0.25), capRate, (sThd.freqs as number[])[0] / (1 + drift), bandLo, bandHi);
      ch.tone = tr;
      ch.gain_db = tr.level_dbfs - sThd.level;
      if (!Number.isNaN(dbvAtFS)) ch.full_scale_dbv = ch.gain_db + dbvAtFS;
    }
    // Sweep: impulse and frequency response.
    const sFr = plan.find('fr');
    if (sFr && sFr.sweep) {
      const sw = sFr.sweep;
      const pre = 0.2;
      const y = slice(rec[c], Math.trunc(recIdx(sFr.start - pre)), Math.trunc(recIdx(sFr.start + sFr.dur + 0.9)));
      if (!sweepCache || sweepCache.len !== y.length) {
        const x = sw.render(capRate, 1 + drift);
        const n = nextPow2(x.length + y.length);
        const ref = deconvRef(x, n);
        // The same deconvolution and window applied to the reference itself.
        const xr = new Float64Array(y.length);
        const off0 = goRound(pre * capRate);
        const cnt = Math.max(0, Math.min(xr.length - off0, x.length));
        if (cnt > 0) xr.set(x.subarray(0, cnt), off0);
        const hr = deconvolveWith(xr, ref, capRate, sw.F1, sw.F2);
        const [pkr] = peakIndex(hr, Math.trunc(0.1 * capRate), Math.trunc(0.5 * capRate));
        const [wr, offr] = window(hr, pkr, capRate, 0.005, 0.6);
        const refSpec = responseOf(wr, capRate, offr, Math.trunc(capRate));
        sweepCache = { len: y.length, ref, refSpec };
      }
      const h = deconvolveWith(y, sweepCache.ref, capRate, sw.F1, sw.F2);
      const [pk, pv] = peakIndex(h, Math.trunc(0.1 * capRate), Math.trunc(0.5 * capRate));
      ch.polarity = pv < 0 ? 'inverted' : 'normal';
      const [w, off] = window(h, pk, capRate, 0.005, 0.6);
      const spec = responseOf(w, capRate, off, Math.trunc(capRate));
      spec.divideBy(sweepCache.refSpec);
      const grid = logGrid(10, sw.F2, 48);
      ch.fr = spec.curveFrom(grid, 0);
      curveNormalize(ch.fr, 1000);
      // Short IR view from the sweep: -1 ms .. +3 ms.
      const ia = goRound(pk) - Math.trunc(0.001 * capRate);
      const ib = goRound(pk) + Math.trunc(0.003 * capRate);
      const sir = slice(h, ia, ib);
      const scale = 1 / Math.max(Math.abs(pv), 1e-12);
      const out: number[] = new Array(sir.length);
      for (let i = 0; i < sir.length; i++) out[i] = sir[i] * scale;
      ch.sweep_ir = out;
      ch.sweep_ir_start_ms = ((ia - pk) / capRate) * 1000;
    }
    // Impulses: average with fractional alignment.
    const sIr = plan.find('ir');
    if (sIr) {
      const [ir, irStart] = averageImpulses(rec[c], sIr, capRate, playRate, recIdx);
      ch.ir = ir;
      ch.ir_start_ms = irStart;
    }
    // Noise floor with digital silence playing.
    const sNoise = plan.find('noise');
    if (sNoise) {
      const nr = analyzeNoise(seg(c, sNoise, 0.3, 0.2), capRate, bandLo, bandHi);
      ch.noise = nr;
      ch.noise_re_dut_fs_db = nr.dbfs - ch.gain_db;
      ch.noise_a_re_dut_fs_db = nr.dbfs_a - ch.gain_db;
      ch.snr_a_db = -ch.noise_a_re_dut_fs_db;
    }
    // Dynamic range (AES17): THD+N of a -60 dB tone, A-weighted, re full scale.
    const sDr = plan.find('dr');
    if (sDr) {
      const tr = analyzeTone(seg(c, sDr, 0.25, 0.25), capRate, (sDr.freqs as number[])[0] / (1 + drift), bandLo, bandHi);
      ch.low = tr;
      ch.dr_a_db = -(tr.resid_a_dbfs - ch.gain_db);
    }
    // Stepped sines.
    for (const s of plan.findAll('thd-freq')) {
      const f = (s.freqs as number[])[0];
      const tr = analyzeTone(seg(c, s, 0.2, 0.1), capRate, f / (1 + drift), bandLo, bandHi);
      (ch.thd_vs_freq ??= []).push({ x: f, thdn_db: tr.thdn_db, thd_db: tr.thd_db, level_dbfs: tr.level_dbfs });
    }
    for (const s of plan.findAll('thd-level')) {
      const f = (s.freqs as number[])[0];
      const tr = analyzeTone(seg(c, s, 0.2, 0.1), capRate, f / (1 + drift), bandLo, bandHi);
      (ch.thd_vs_level ??= []).push({ x: s.level, thdn_db: tr.thdn_db, thd_db: tr.thd_db, level_dbfs: tr.level_dbfs });
    }
    const sSmpte = plan.find('imd-smpte');
    if (sSmpte) {
      const fq = sSmpte.freqs as number[];
      ch.imd_smpte_db = imdSMPTE(seg(c, sSmpte, 0.2, 0.2), capRate, fq[0] / (1 + drift), fq[1] / (1 + drift));
    }
    const sCcif = plan.find('imd-ccif');
    if (sCcif) {
      const fq = sCcif.freqs as number[];
      ch.imd_ccif_db = imdCCIF(seg(c, sCcif, 0.2, 0.2), capRate, fq[0] / (1 + drift), fq[1] / (1 + drift));
    }
  }
  // Crosstalk and L/R identification.
  if (chans.length === 2) {
    const sl = plan.find('xtalk-l');
    const sr = plan.find('xtalk-r');
    if (sl && sr) {
      const f = (sl.freqs as number[])[0] / (1 + drift);
      const ll = levelAt(seg(0, sl, 0.25, 0.25), capRate, f);
      const lr = levelAt(seg(1, sl, 0.25, 0.25), capRate, f);
      const rr = levelAt(seg(1, sr, 0.25, 0.25), capRate, f);
      const rl = levelAt(seg(0, sr, 0.25, 0.25), capRate, f);
      chans[0].xtalk_db = lr - ll;
      chans[1].xtalk_db = rl - rr;
      if (lr > ll && rl > rr) {
        r.lr_swapped = true;
        notes.push('Left and right look swapped: the left-only tone came out on the right input.');
      }
    }
    const t0 = chans[0].tone;
    const t1 = chans[1].tone;
    if (t0 && t1) r.balance_db = t0.level_dbfs - t1.level_dbfs;
  }
  r.notes = notes;
  return finish(r);
}

function averageImpulses(
  x: Float64Array,
  s: Segment,
  capRate: number,
  playRate: number,
  recIdx: (t: number) => number,
): [number[] | null, number] {
  const pre = Math.trunc(0.001 * capRate);
  const post = Math.trunc(0.003 * capRate);
  const every = goRound((s.every as number) * playRate);
  const a0 = Math.ceil(s.start * playRate);
  const acc = new Float64Array(pre + post);
  const count = s.count as number;
  let n = 0;
  for (let k = 0; k < count; k++) {
    const tp = (a0 + (k * every + Math.trunc(every / 2))) / playRate;
    const ci = recIdx(tp);
    const base = Math.floor(ci) - pre - 8;
    const w = slice(x, base, base + pre + post + 16);
    if (w.length < pre + post + 16) continue;
    const frac = ci - Math.floor(ci);
    const sh = fracShift(w, -frac);
    for (let i = 0; i < acc.length; i++) acc[i] += sh[i + 8];
    n++;
  }
  if (n === 0) return [null, 0];
  for (let i = 0; i < acc.length; i++) acc[i] /= n;
  const [pk, pv] = peakIndex(acc, 0, acc.length);
  const g = 1 / Math.max(Math.abs(pv), 1e-12);
  const out: number[] = new Array(acc.length);
  for (let i = 0; i < acc.length; i++) out[i] = acc[i] * g;
  return [out, (-pk / capRate) * 1000];
}

function imdSMPTE(x: Float64Array, rate: number, f1: number, f2: number): number {
  const a2 = fromDB(levelAt(x, rate, f2));
  let p = 0;
  for (let n = 1; n <= 3; n++) {
    for (const sgn of [-1, 1]) {
      const v = fromDB(levelAt(x, rate, f2 + sgn * n * f1));
      p += v * v;
    }
  }
  return db(Math.sqrt(p) / a2);
}

function imdCCIF(x: Float64Array, rate: number, f1: number, f2: number): number {
  const a1 = fromDB(levelAt(x, rate, f1));
  const a2 = fromDB(levelAt(x, rate, f2));
  let p = 0;
  for (const f of [f2 - f1, 2 * f1 - f2, 2 * f2 - f1]) {
    if (f <= 0 || f >= rate / 2) continue;
    const v = fromDB(levelAt(x, rate, f));
    p += v * v;
  }
  return db(Math.sqrt(p) / (a1 + a2));
}

/** NO_VALUE replaces NaN and Inf (readers treat anything at or below -900 as not measured). */
export const NO_VALUE = -999;

/** sanitize does what internal/engine's Sanitize does after Analyze: every NaN or infinite
 *  number anywhere in the result is replaced with -999, in place. Returns its argument. */
export function sanitize<T>(result: T): T {
  const walk = (v: unknown): unknown => {
    if (typeof v === 'number') return Number.isFinite(v) ? v : NO_VALUE;
    if (Array.isArray(v)) {
      for (let i = 0; i < v.length; i++) v[i] = walk(v[i]);
      return v;
    }
    if (v instanceof Float64Array || v instanceof Float32Array) {
      for (let i = 0; i < v.length; i++) if (!Number.isFinite(v[i])) v[i] = NO_VALUE;
      return v;
    }
    if (v !== null && typeof v === 'object') {
      const o = v as Record<string, unknown>;
      for (const k of Object.keys(o)) o[k] = walk(o[k]);
      return v;
    }
    return v;
  };
  walk(result);
  return result;
}
