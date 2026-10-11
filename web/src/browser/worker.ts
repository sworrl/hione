/**
 * The heavy maths, off the page's thread: marker search while recording, the analysis of a
 * finished capture, and rendering test files. Everything stays on the visitor's machine.
 */
import { analyze, sanitize } from './analysis';
import { findMarker } from './dsp';
import { build, syncSweep } from './stim';
import { encodeWav } from './wav';

export type PlanName = 'quick' | 'full' | 'level';

export type Req =
  | { id: number; type: 'marker'; ch: Float64Array[]; level: number; rate: number }
  | { id: number; type: 'analyze'; rec: Float64Array[]; plan: PlanName; level: number; playRate: number; capRate: number }
  | { id: number; type: 'stim'; plan: PlanName; level: number; rate: number };

// The page's tsconfig carries the DOM lib, not the worker one, so type the worker scope here.
const ctx = self as unknown as { postMessage(m: unknown, transfer?: Transferable[]): void; onmessage: ((ev: MessageEvent<Req>) => void) | null };

const refs = new Map<string, Float64Array>();
function syncRef(level: number, rate: number): Float64Array {
  const k = `${level}@${rate}`;
  let r = refs.get(k);
  if (!r) { r = Float64Array.from(syncSweep(level).render(rate, 1)); refs.set(k, r); }
  return r;
}

ctx.onmessage = (ev: MessageEvent<Req>) => {
  const m = ev.data;
  try {
    if (m.type === 'marker') {
      const ref = syncRef(m.level, m.rate);
      let best = 0, conf = 0;
      for (const x of m.ch) {
        const [p, c] = findMarker(x, ref);
        if (c > conf) { best = p; conf = c; }
      }
      ctx.postMessage({ id: m.id, ok: true, value: { pos: best, conf, refLen: ref.length } });
    } else if (m.type === 'analyze') {
      const plan = build({ name: m.plan, level: m.level, playRate: m.playRate });
      const t0 = performance.now();
      const res = analyze(m.rec, plan, { playRate: m.playRate, capRate: m.capRate, dbvAtFS: NaN });
      sanitize(res);
      ctx.postMessage({ id: m.id, ok: true, value: { result: res, ms: performance.now() - t0 } });
    } else if (m.type === 'stim') {
      const plan = build({ name: m.plan, level: m.level, playRate: m.rate });
      const wav = encodeWav(plan.render(m.rate, 24), m.rate, 24);
      ctx.postMessage({ id: m.id, ok: true, value: { wav, total: plan.total, syncLead: plan.syncLead } }, [wav.buffer]);
    }
  } catch (e) {
    ctx.postMessage({ id: m.id, ok: false, error: String((e as Error)?.message || e) });
  }
};
