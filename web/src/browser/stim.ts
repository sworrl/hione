// The test signal: a port of internal/stim. A plan of timed segments described in continuous
// time, rendered at the playback rate for the device and re-rendered at the capture rate as the
// analysis reference. Rendering without dither matches the Go code sample for sample (to a few
// ulps); the TPDF dither uses the same amplitude but a different (seeded) PRNG, since Go's
// math/rand sequence is not reproduced here.

import { Sweep, fromDB, goRound } from './dsp';

export type Kind = 'silence' | 'sync' | 'sweep' | 'tone' | 'impulses' | 'multitone';

/** Segment is one timed part of the plan. */
export interface Segment {
  kind: Kind;
  name: string;
  start: number; // s
  dur: number; // s
  chan: 'both' | 'L' | 'R';
  level: number; // peak level of the segment, dBFS
  freqs?: number[];
  ratio?: number[];
  sweep?: Sweep;
  count?: number; // impulses
  every?: number; // s
  tag?: string; // thd, fr, ir, noise, dr, xtalk-l, ...
}

export type PlanName = 'quick' | 'full' | 'level';

export interface BuildOptions {
  name: PlanName;
  level: number; // dBFS, peak level of the loudest segment
  playRate: number; // device playback rate (bounds the sweep's top frequency)
  sweepLen?: number; // seconds, default 6
}

/** SyncSweep is the short marker sweep at the start. */
export function syncSweep(level: number): Sweep {
  return new Sweep(300, 6000, 0.5, fromDB(level), 0.02);
}

/** EndSync is the downward marker sweep at the end. */
export function endSync(level: number): Sweep {
  return new Sweep(6000, 300, 0.5, fromDB(level), 0.02);
}

const toneFade = 0.02;

function fade(t: number, dur: number): number {
  if (t < toneFade) return 0.5 - 0.5 * Math.cos((Math.PI * t) / toneFade);
  if (t > dur - toneFade) return 0.5 - 0.5 * Math.cos((Math.PI * Math.max(0, dur - t)) / toneFade);
  return 1;
}

/** Deterministic PRNG for the dither (xoshiro128**, 53-bit doubles in [0, 1)). */
class Rng {
  private s0: number;
  private s1: number;
  private s2: number;
  private s3: number;
  constructor(seed: number) {
    // splitmix32 to expand the seed
    let x = seed >>> 0;
    const next = (): number => {
      x = (x + 0x9e3779b9) >>> 0;
      let z = x;
      z = Math.imul(z ^ (z >>> 16), 0x85ebca6b) >>> 0;
      z = Math.imul(z ^ (z >>> 13), 0xc2b2ae35) >>> 0;
      return (z ^ (z >>> 16)) >>> 0;
    };
    this.s0 = next();
    this.s1 = next();
    this.s2 = next();
    this.s3 = next();
  }
  private u32(): number {
    const r = Math.imul(((Math.imul(this.s1, 5) << 7) | (Math.imul(this.s1, 5) >>> 25)) >>> 0, 9) >>> 0;
    const t = (this.s1 << 9) >>> 0;
    this.s2 = (this.s2 ^ this.s0) >>> 0;
    this.s3 = (this.s3 ^ this.s1) >>> 0;
    this.s1 = (this.s1 ^ this.s2) >>> 0;
    this.s0 = (this.s0 ^ this.s3) >>> 0;
    this.s2 = (this.s2 ^ t) >>> 0;
    this.s3 = ((this.s3 << 11) | (this.s3 >>> 21)) >>> 0;
    return r;
  }
  float64(): number {
    const hi = this.u32() >>> 5; // 27 bits
    const lo = this.u32() >>> 6; // 26 bits
    return (hi * 67108864 + lo) / 9007199254740992;
  }
}

/** Plan is the whole test signal. */
export class Plan {
  segments: Segment[] = [];
  total = 0;
  syncLead = 0; // start of the first sync
  syncTail = 0; // start of the end sync

  constructor(
    public name: string,
    public level: number,
  ) {}

  /** Find returns the first segment with tag. */
  find(tag: string): Segment | undefined {
    return this.segments.find((s) => s.tag === tag);
  }

  /** FindAll returns all segments with tag. */
  findAll(tag: string): Segment[] {
    return this.segments.filter((s) => s.tag === tag);
  }

  /** DryLevelDB is the peak of the plan in dBFS. */
  dryLevelDB(): number {
    let m = -999;
    for (const s of this.segments) if (s.level > m) m = s.level;
    return m;
  }

  /** Render produces the stereo signal at rate. ditherBits > 0 adds TPDF dither at that word
   *  length (amplitude as in Go: (u1 - u2) * 2^-(bits-1)). */
  render(rate: number, ditherBits = 0): Float64Array[] {
    const n = Math.ceil(this.total * rate);
    const out = [new Float64Array(n), new Float64Array(n)];
    const L = out[0];
    const R = out[1];
    for (const s of this.segments) {
      const a = Math.ceil(s.start * rate);
      const b = Math.min(Math.ceil((s.start + s.dur) * rate), n);
      const toL = s.chan !== 'R';
      const toR = s.chan !== 'L';
      const amp = fromDB(s.level);
      const put = (j: number, v: number): void => {
        if (toL) L[j] = v;
        if (toR) R[j] = v;
      };
      switch (s.kind) {
        case 'sync':
        case 'sweep': {
          const sw = s.sweep as Sweep;
          for (let j = a; j < b; j++) put(j, sw.at(j / rate - s.start));
          break;
        }
        case 'tone': {
          const f = (s.freqs as number[])[0];
          for (let j = a; j < b; j++) {
            const t = j / rate - s.start;
            put(j, amp * fade(t, s.dur) * Math.sin(2 * Math.PI * f * t));
          }
          break;
        }
        case 'multitone': {
          const ratio = s.ratio as number[];
          const freqs = s.freqs as number[];
          let sum = 0;
          for (const r of ratio) sum += r;
          for (let j = a; j < b; j++) {
            const t = j / rate - s.start;
            const env = fade(t, s.dur);
            let v = 0;
            for (let i = 0; i < freqs.length; i++) v += (ratio[i] / sum) * Math.sin(2 * Math.PI * freqs[i] * t);
            put(j, amp * env * v);
          }
          break;
        }
        case 'impulses': {
          const every = goRound((s.every as number) * rate);
          const count = s.count as number;
          const half = Math.trunc(every / 2);
          for (let j = a; j < b; j++) {
            const k = j - a;
            const hit = k >= 0 && every > 0 && k % every === half && Math.trunc(k / every) < count;
            put(j, hit ? amp : 0);
          }
          break;
        }
        default:
          for (let j = a; j < b; j++) put(j, 0);
      }
    }
    if (ditherBits > 0) {
      const r = new Rng(1);
      const lsb = Math.pow(2, -(ditherBits - 1));
      for (const c of out) {
        for (let j = 0; j < c.length; j++) c[j] += (r.float64() - r.float64()) * lsb;
      }
    }
    return out;
  }
}

/** goG formats like Go's %g for the plain numbers used in segment names. */
function goG(f: number): string {
  return String(f);
}

/** Build makes a plan. Throws on an invalid level or unknown plan name. */
export function build(o: BuildOptions): Plan {
  if (o.level > 0) throw new Error(`level ${o.level.toFixed(1)} dBFS is above full scale`);
  const sweepLen = o.sweepLen ? o.sweepLen : 6;
  const p = new Plan(o.name, o.level);
  let t = 0;
  const add = (s: Omit<Segment, 'start' | 'chan'> & { chan?: Segment['chan'] }): void => {
    const seg: Segment = { ...s, start: t, chan: s.chan ?? 'both' };
    p.segments.push(seg);
    t += s.dur;
  };
  const gap = (d: number): void => add({ kind: 'silence', name: 'gap', dur: d, level: -999 });
  const sync = (name: string): void => {
    const sw = name === 'end sync' ? endSync(o.level) : syncSweep(o.level);
    add({ kind: 'sync', name, dur: sw.T, level: o.level, sweep: sw });
  };
  const f2 = Math.min(0.475 * o.playRate, 40000);
  switch (o.name) {
    case 'level':
      gap(0.3);
      add({ kind: 'tone', name: 'level check 1 kHz', dur: 60, level: o.level, freqs: [1000], tag: 'level' });
      p.total = t;
      return p;
    case 'quick':
    case 'full':
      break;
    default:
      throw new Error(`unknown plan "${String(o.name)}" (quick, full, level)`);
  }
  gap(1.5);
  p.syncLead = t;
  sync('start sync');
  gap(0.5);
  add({ kind: 'tone', name: '1 kHz', dur: 2.5, level: o.level, freqs: [1000], tag: 'thd' });
  gap(0.3);
  const sw = new Sweep(10, f2, sweepLen, fromDB(o.level), 0.05);
  add({ kind: 'sweep', name: 'sweep', dur: sw.T, level: o.level, sweep: sw, tag: 'fr' });
  gap(1.0);
  add({ kind: 'impulses', name: 'impulses', dur: 32 * 0.05, level: o.level, count: 32, every: 0.05, tag: 'ir' });
  gap(0.3);
  add({ kind: 'silence', name: 'digital silence', dur: 3, level: -999, tag: 'noise' });
  add({ kind: 'tone', name: '1 kHz at -60 dB', dur: 2.5, level: o.level - 60, freqs: [1000], tag: 'dr' });
  gap(0.3);
  add({ kind: 'tone', name: 'left only', dur: 1.5, chan: 'L', level: o.level, freqs: [1000], tag: 'xtalk-l' });
  add({ kind: 'tone', name: 'right only', dur: 1.5, chan: 'R', level: o.level, freqs: [1000], tag: 'xtalk-r' });
  gap(0.3);
  if (o.name === 'full') {
    for (const f of [20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 15000, 20000]) {
      if (f > f2) break;
      add({ kind: 'tone', name: `${goG(f)} Hz`, dur: 1.0, level: o.level, freqs: [f], tag: 'thd-freq' });
    }
    gap(0.3);
    for (let d = 0.0; d >= -80; d -= 10) {
      const ds = (d >= 0 ? '+' : '-') + Math.abs(d).toFixed(0);
      add({ kind: 'tone', name: `1 kHz ${ds} dB`, dur: 1.0, level: o.level + d, freqs: [1000], tag: 'thd-level' });
    }
    gap(0.3);
    add({ kind: 'multitone', name: 'SMPTE 60 Hz + 7 kHz 4:1', dur: 2, level: o.level, freqs: [60, 7000], ratio: [4, 1], tag: 'imd-smpte' });
    if (f2 > 20500) {
      add({ kind: 'multitone', name: 'CCIF 19 + 20 kHz 1:1', dur: 2, level: o.level, freqs: [19000, 20000], ratio: [1, 1], tag: 'imd-ccif' });
    }
    add({ kind: 'tone', name: 'polarity and L/R id, left 440 Hz', dur: 0.8, chan: 'L', level: o.level, freqs: [440], tag: 'id-l' });
    add({ kind: 'tone', name: 'polarity and L/R id, right 880 Hz', dur: 0.8, chan: 'R', level: o.level, freqs: [880], tag: 'id-r' });
    gap(0.3);
  }
  p.syncTail = t;
  sync('end sync');
  gap(0.7);
  p.total = t;
  return p;
}
