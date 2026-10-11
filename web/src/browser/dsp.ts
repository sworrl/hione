// Signal processing for in-browser analysis: a port of internal/dsp (fft.go, util.go, sweep.go,
// response.go, tone.go, biquad.go). Numerical behaviour follows the Go code line by line: the
// same FFT sizes and zero padding, the same rounding (Go's math.Round rounds half away from
// zero, int() truncates toward zero), the same evaluation order of floating-point expressions.
// Only the FFT kernels and the transcendental functions differ, which moves results by a few
// ulps.
//
// Conventions:
//  - Signals are Float64Array. Functions that only read accept any ArrayLike<number>.
//  - Complex spectra are a pair of arrays, { re, im } (type Cplx), never interleaved.
//  - rfft(x, n) returns the n/2+1 coefficients X[k] = sum x[j] exp(-2*pi*i*j*k/n), unscaled,
//    like gonum's fourier.FFT.Coefficients. irfft scales by 1/n, so irfft(rfft(x, n), n) == x.
//  - Data objects that end up in the JSON result (Curve, ToneResult, NoiseResult) use the Go
//    JSON tag names as property names.

export type Signal = ArrayLike<number>;

export interface Cplx {
  re: Float64Array;
  im: Float64Array;
}

// ---------------------------------------------------------------------------------------------
// Go numeric semantics

/** Go's math.Round: half away from zero. */
export function goRound(x: number): number {
  const t = Math.trunc(x);
  if (Math.abs(x - t) >= 0.5) return t + Math.sign(x);
  return t;
}

/** Go's math.Log2: exact for powers of two. */
export function goLog2(x: number): number {
  if (!(x > 0) || !Number.isFinite(x)) return Math.log2(x);
  // frexp: x = frac * 2^exp, frac in [0.5, 1)
  let exp = Math.floor(Math.log2(x)) + 1;
  let frac = x / Math.pow(2, exp);
  // Math.log2 can be one off near powers of two; fix up so frac is in [0.5, 1).
  while (frac >= 1) {
    frac /= 2;
    exp++;
  }
  while (frac < 0.5) {
    frac *= 2;
    exp--;
  }
  if (frac === 0.5) return exp - 1;
  return Math.log(frac) * (1 / Math.LN2) + exp;
}

/** cmplx.Abs (math.Hypot, scaled form). */
export function hypot(re: number, im: number): number {
  let p = Math.abs(re);
  let q = Math.abs(im);
  if (p === Infinity || q === Infinity) return Infinity;
  if (Number.isNaN(p) || Number.isNaN(q)) return NaN;
  if (p < q) {
    const t = p;
    p = q;
    q = t;
  }
  if (p === 0) return 0;
  q = q / p;
  return p * Math.sqrt(1 + q * q);
}

/** Go runtime complex division (Smith's algorithm). Returns [re, im]. */
function cdiv(a: number, b: number, c: number, d: number, out: number[]): void {
  let e: number;
  let f: number;
  if (Math.abs(c) >= Math.abs(d)) {
    const ratio = d / c;
    const denom = c + ratio * d;
    e = (a + b * ratio) / denom;
    f = (b - a * ratio) / denom;
  } else {
    const ratio = c / d;
    const denom = d + ratio * c;
    e = (a * ratio + b) / denom;
    f = (b * ratio - a) / denom;
  }
  out[0] = e;
  out[1] = f;
}

// ---------------------------------------------------------------------------------------------
// FFT: mixed-radix (4, 2, 3, 5, generic) Stockham autosort on split re/im arrays, with a real
// transform of even length n done as a complex transform of length n/2. Plans (twiddle tables)
// are cached; work buffers are shared.

interface CPlan {
  n: number;
  factors: number[];
  twr: Float64Array; // exp(-2*pi*i*j/n), j < n
  twi: Float64Array;
}

interface RPlan {
  n: number; // real length (even)
  c: CPlan; // complex plan of n/2
  pr: Float64Array; // exp(-2*pi*i*k/n), k <= n/4
  pi: Float64Array;
}

// Plans and windows are kept in LRU caches bounded by bytes, so the handful of sizes one analysis
// uses (correlation blocks, deconvolution, tone lengths, noise blocks) stay resident.
const CACHE_BUDGET = 160 * 2 ** 20;

class LRU<V> {
  private m = new Map<number, { v: V; bytes: number }>();
  private total = 0;
  constructor(private budget: number) {}
  get(k: number): V | undefined {
    const e = this.m.get(k);
    if (!e) return undefined;
    this.m.delete(k);
    this.m.set(k, e);
    return e.v;
  }
  set(k: number, v: V, bytes: number): void {
    this.m.set(k, { v, bytes });
    this.total += bytes;
    while (this.total > this.budget && this.m.size > 1) {
      const [fk, fe] = this.m.entries().next().value as [number, { v: V; bytes: number }];
      this.m.delete(fk);
      this.total -= fe.bytes;
    }
  }
  clear(): void {
    this.m.clear();
    this.total = 0;
  }
}

const cplans = new LRU<CPlan>(CACHE_BUDGET);
const rplans = new LRU<RPlan>(CACHE_BUDGET / 2);
const bh7Cache = new LRU<Float64Array>(64 * 2 ** 20);

/** Fills re/im[j] = cos/-sin(2*pi*j/total) for j < count, using octant symmetry when total%8==0
 *  so that values on the axes and diagonals are exact. */
function fillTwiddles(re: Float64Array, im: Float64Array, count: number, total: number): void {
  if (total % 8 !== 0) {
    for (let j = 0; j < count; j++) {
      const a = (2 * Math.PI * j) / total;
      re[j] = Math.cos(a);
      im[j] = -Math.sin(a);
    }
    return;
  }
  const e = total / 8;
  const q = total / 4;
  // First octant directly, then reflect into the second octant and rotate into the others.
  const c = new Float64Array(q + 1);
  const s = new Float64Array(q + 1);
  for (let j = 0; j <= e; j++) {
    const a = (2 * Math.PI * j) / total;
    c[j] = Math.cos(a);
    s[j] = Math.sin(a);
  }
  for (let j = e + 1; j <= q; j++) {
    c[j] = s[q - j];
    s[j] = c[q - j];
  }
  for (let j = 0; j < count; j++) {
    const quad = Math.floor(j / q);
    const r = j - quad * q;
    let cv: number;
    let sv: number;
    switch (quad & 3) {
      case 0:
        cv = c[r];
        sv = s[r];
        break;
      case 1:
        cv = -s[r];
        sv = c[r];
        break;
      case 2:
        cv = -c[r];
        sv = -s[r];
        break;
      default:
        cv = s[r];
        sv = -c[r];
        break;
    }
    re[j] = cv;
    im[j] = -sv;
  }
}

function factorize(n: number): number[] {
  const f: number[] = [];
  while (n % 4 === 0) {
    f.push(4);
    n /= 4;
  }
  while (n % 2 === 0) {
    f.push(2);
    n /= 2;
  }
  for (const r of [3, 5]) {
    while (n % r === 0) {
      f.push(r);
      n /= r;
    }
  }
  for (let p = 7; p * p <= n; p += 2) {
    while (n % p === 0) {
      f.push(p);
      n /= p;
    }
  }
  if (n > 1) f.push(n);
  return f;
}

function cplan(n: number): CPlan {
  let p = cplans.get(n);
  if (!p) {
    const twr = new Float64Array(n);
    const twi = new Float64Array(n);
    fillTwiddles(twr, twi, n, n);
    p = { n, factors: factorize(n), twr, twi };
    cplans.set(n, p, 16 * n);
  }
  return p;
}

function rplan(n: number): RPlan {
  let p = rplans.get(n);
  if (!p) {
    const h = n / 2;
    const cnt = Math.floor(h / 2) + 1;
    const pr = new Float64Array(cnt);
    const pi = new Float64Array(cnt);
    fillTwiddles(pr, pi, cnt, n);
    p = { n, c: cplan(h), pr, pi };
    rplans.set(n, p, 16 * cnt);
  }
  return p;
}

// Shared work buffers (grown on demand; JS is single-threaded and nothing here re-enters).
let wbuf: Float64Array[] = [new Float64Array(0), new Float64Array(0), new Float64Array(0), new Float64Array(0)];
function work(n: number): Float64Array[] {
  if (wbuf[0].length < n) {
    wbuf = [new Float64Array(n), new Float64Array(n), new Float64Array(n), new Float64Array(n)];
  }
  return [wbuf[0].subarray(0, n), wbuf[1].subarray(0, n), wbuf[2].subarray(0, n), wbuf[3].subarray(0, n)];
}

/** Releases the cached FFT plans and work buffers (they can reach a few hundred MB after a
 *  192 kHz analysis). */
export function releaseFFTMemory(): void {
  cplans.clear();
  rplans.clear();
  bh7Cache.clear();
  wbuf = [new Float64Array(0), new Float64Array(0), new Float64Array(0), new Float64Array(0)];
}

/** Forward complex FFT in place on (xr, xi) using (yr, yi) as scratch. Returns true when the
 *  result ended up in (yr, yi). */
function cfft(p: CPlan, xr: Float64Array, xi: Float64Array, yr: Float64Array, yi: Float64Array): boolean {
  const n = p.n;
  const twr = p.twr;
  const twi = p.twi;
  let ar = xr;
  let ai = xi;
  let br = yr;
  let bi = yi;
  let s = 1;
  let len = n;
  let swapped = false;
  for (const r of p.factors) {
    const m = len / r;
    const sm = s * m;
    if (r === 4) {
      for (let pp = 0; pp < m; pp++) {
        const t1 = pp * s;
        const w1r = twr[t1], w1i = twi[t1];
        const w2r = twr[2 * t1], w2i = twi[2 * t1];
        const w3r = twr[3 * t1], w3i = twi[3 * t1];
        const ib = s * pp;
        const ob = s * 4 * pp;
        for (let q = 0; q < s; q++) {
          const i0 = ib + q, i1 = i0 + sm, i2 = i1 + sm, i3 = i2 + sm;
          const a0r = ar[i0], a0i = ai[i0], a1r = ar[i1], a1i = ai[i1];
          const a2r = ar[i2], a2i = ai[i2], a3r = ar[i3], a3i = ai[i3];
          const t0r = a0r + a2r, t0i = a0i + a2i;
          const u1r = a0r - a2r, u1i = a0i - a2i;
          const t2r = a1r + a3r, t2i = a1i + a3i;
          const t3r = a1i - a3i, t3i = a3r - a1r; // -i*(a1-a3)
          const b1r = u1r + t3r, b1i = u1i + t3i;
          const b2r = t0r - t2r, b2i = t0i - t2i;
          const b3r = u1r - t3r, b3i = u1i - t3i;
          const o0 = ob + q, o1 = o0 + s, o2 = o1 + s, o3 = o2 + s;
          br[o0] = t0r + t2r;
          bi[o0] = t0i + t2i;
          br[o1] = b1r * w1r - b1i * w1i;
          bi[o1] = b1r * w1i + b1i * w1r;
          br[o2] = b2r * w2r - b2i * w2i;
          bi[o2] = b2r * w2i + b2i * w2r;
          br[o3] = b3r * w3r - b3i * w3i;
          bi[o3] = b3r * w3i + b3i * w3r;
        }
      }
    } else if (r === 2) {
      for (let pp = 0; pp < m; pp++) {
        const t1 = pp * s;
        const w1r = twr[t1], w1i = twi[t1];
        const ib = s * pp;
        const ob = s * 2 * pp;
        for (let q = 0; q < s; q++) {
          const i0 = ib + q, i1 = i0 + sm;
          const a0r = ar[i0], a0i = ai[i0], a1r = ar[i1], a1i = ai[i1];
          const dr = a0r - a1r, di = a0i - a1i;
          const o0 = ob + q, o1 = o0 + s;
          br[o0] = a0r + a1r;
          bi[o0] = a0i + a1i;
          br[o1] = dr * w1r - di * w1i;
          bi[o1] = dr * w1i + di * w1r;
        }
      }
    } else if (r === 3) {
      const s3 = Math.sin((2 * Math.PI) / 3);
      for (let pp = 0; pp < m; pp++) {
        const t1 = pp * s;
        const w1r = twr[t1], w1i = twi[t1];
        const w2r = twr[2 * t1], w2i = twi[2 * t1];
        const ib = s * pp;
        const ob = s * 3 * pp;
        for (let q = 0; q < s; q++) {
          const i0 = ib + q, i1 = i0 + sm, i2 = i1 + sm;
          const a0r = ar[i0], a0i = ai[i0], a1r = ar[i1], a1i = ai[i1], a2r = ar[i2], a2i = ai[i2];
          const t1r = a1r + a2r, t1i = a1i + a2i;
          const t2r = a0r - 0.5 * t1r, t2i = a0i - 0.5 * t1i;
          // -i*s3*(a1-a2)
          const t3r = s3 * (a1i - a2i), t3i = -s3 * (a1r - a2r);
          const b1r = t2r + t3r, b1i = t2i + t3i;
          const b2r = t2r - t3r, b2i = t2i - t3i;
          const o0 = ob + q, o1 = o0 + s, o2 = o1 + s;
          br[o0] = a0r + t1r;
          bi[o0] = a0i + t1i;
          br[o1] = b1r * w1r - b1i * w1i;
          bi[o1] = b1r * w1i + b1i * w1r;
          br[o2] = b2r * w2r - b2i * w2i;
          bi[o2] = b2r * w2i + b2i * w2r;
        }
      }
    } else if (r === 5) {
      const c1 = Math.cos((2 * Math.PI) / 5), c2 = Math.cos((4 * Math.PI) / 5);
      const s1 = Math.sin((2 * Math.PI) / 5), s2 = Math.sin((4 * Math.PI) / 5);
      for (let pp = 0; pp < m; pp++) {
        const t1 = pp * s;
        const w1r = twr[t1], w1i = twi[t1];
        const w2r = twr[2 * t1], w2i = twi[2 * t1];
        const w3r = twr[3 * t1], w3i = twi[3 * t1];
        const w4r = twr[4 * t1], w4i = twi[4 * t1];
        const ib = s * pp;
        const ob = s * 5 * pp;
        for (let q = 0; q < s; q++) {
          const i0 = ib + q, i1 = i0 + sm, i2 = i1 + sm, i3 = i2 + sm, i4 = i3 + sm;
          const a0r = ar[i0], a0i = ai[i0];
          const p1r = ar[i1] + ar[i4], p1i = ai[i1] + ai[i4];
          const p2r = ar[i2] + ar[i3], p2i = ai[i2] + ai[i3];
          const m1r = ar[i1] - ar[i4], m1i = ai[i1] - ai[i4];
          const m2r = ar[i2] - ar[i3], m2i = ai[i2] - ai[i3];
          const u1r = a0r + c1 * p1r + c2 * p2r, u1i = a0i + c1 * p1i + c2 * p2i;
          const u2r = a0r + c2 * p1r + c1 * p2r, u2i = a0i + c2 * p1i + c1 * p2i;
          const v1r = s1 * m1r + s2 * m2r, v1i = s1 * m1i + s2 * m2i;
          const v2r = s2 * m1r - s1 * m2r, v2i = s2 * m1i - s1 * m2i;
          // b1 = u1 - i v1, b4 = u1 + i v1, b2 = u2 - i v2, b3 = u2 + i v2
          const b1r = u1r + v1i, b1i = u1i - v1r;
          const b4r = u1r - v1i, b4i = u1i + v1r;
          const b2r = u2r + v2i, b2i = u2i - v2r;
          const b3r = u2r - v2i, b3i = u2i + v2r;
          const o0 = ob + q, o1 = o0 + s, o2 = o1 + s, o3 = o2 + s, o4 = o3 + s;
          br[o0] = a0r + p1r + p2r;
          bi[o0] = a0i + p1i + p2i;
          br[o1] = b1r * w1r - b1i * w1i;
          bi[o1] = b1r * w1i + b1i * w1r;
          br[o2] = b2r * w2r - b2i * w2i;
          bi[o2] = b2r * w2i + b2i * w2r;
          br[o3] = b3r * w3r - b3i * w3i;
          bi[o3] = b3r * w3i + b3i * w3r;
          br[o4] = b4r * w4r - b4i * w4i;
          bi[o4] = b4r * w4i + b4i * w4r;
        }
      }
    } else {
      // Generic radix: plain DFT of length r per butterfly.
      const cr = new Float64Array(r * r);
      const ci = new Float64Array(r * r);
      for (let j = 0; j < r; j++) {
        for (let k = 0; k < r; k++) {
          const a = (2 * Math.PI * ((j * k) % r)) / r;
          cr[j * r + k] = Math.cos(a);
          ci[j * r + k] = -Math.sin(a);
        }
      }
      const tr = new Float64Array(r);
      const ti = new Float64Array(r);
      for (let pp = 0; pp < m; pp++) {
        const ib = s * pp;
        const ob = s * r * pp;
        for (let q = 0; q < s; q++) {
          for (let j = 0; j < r; j++) {
            tr[j] = ar[ib + q + j * sm];
            ti[j] = ai[ib + q + j * sm];
          }
          for (let k = 0; k < r; k++) {
            let vr = 0;
            let vi = 0;
            for (let j = 0; j < r; j++) {
              const wr = cr[j * r + k], wi = ci[j * r + k];
              vr += tr[j] * wr - ti[j] * wi;
              vi += tr[j] * wi + ti[j] * wr;
            }
            const t = pp * k * s;
            const o = ob + q + k * s;
            br[o] = vr * twr[t] - vi * twi[t];
            bi[o] = vr * twi[t] + vi * twr[t];
          }
        }
      }
    }
    const tr = ar;
    const ti = ai;
    ar = br;
    ai = bi;
    br = tr;
    bi = ti;
    swapped = !swapped;
    s *= r;
    len = m;
  }
  return swapped;
}

/** Real FFT into caller-provided outputs of length n/2+1 (see rfft). */
function rfftInto(x: Signal, n: number, outRe: Float64Array, outIm: Float64Array): void {
  if (n % 2 !== 0) {
    // Odd sizes never occur on the measurement path; plain complex transform.
    const p = cplan(n);
    const [zr, zi, wr, wi] = work(n);
    const L = Math.min(x.length, n);
    for (let j = 0; j < n; j++) {
      zr[j] = j < L ? x[j] : 0;
      zi[j] = 0;
    }
    const sw = cfft(p, zr, zi, wr, wi);
    const rr = sw ? wr : zr;
    const ri = sw ? wi : zi;
    for (let k = 0; k <= (n - 1) / 2; k++) {
      outRe[k] = rr[k];
      outIm[k] = ri[k];
    }
    return;
  }
  const h = n / 2;
  const p = rplan(n);
  const [zr, zi, wr, wi] = work(h);
  const L = Math.min(x.length, n);
  const pairs = L >> 1;
  for (let j = 0; j < pairs; j++) {
    zr[j] = x[2 * j];
    zi[j] = x[2 * j + 1];
  }
  let j0 = pairs;
  if (L & 1) {
    zr[pairs] = x[L - 1];
    zi[pairs] = 0;
    j0++;
  }
  if (j0 < h) {
    zr.fill(0, j0);
    zi.fill(0, j0);
  }
  const sw = cfft(p.c, zr, zi, wr, wi);
  const Zr = sw ? wr : zr;
  const Zi = sw ? wi : zi;
  const z0r = Zr[0], z0i = Zi[0];
  outRe[0] = z0r + z0i;
  outIm[0] = 0;
  outRe[h] = z0r - z0i;
  outIm[h] = 0;
  const pr = p.pr;
  const pi = p.pi;
  const half = h >> 1;
  for (let k = 1; k <= half; k++) {
    const j = h - k;
    const zkr = Zr[k], zki = Zi[k], zjr = Zr[j], zji = Zi[j];
    const er = 0.5 * (zkr + zjr), ei = 0.5 * (zki - zji);
    const or_ = 0.5 * (zki + zji), oi = -0.5 * (zkr - zjr);
    const cr = pr[k], ci = pi[k];
    const tr = cr * or_ - ci * oi, ti = cr * oi + ci * or_;
    outRe[j] = er - tr;
    outIm[j] = -(ei - ti);
    outRe[k] = er + tr;
    outIm[k] = ei + ti;
  }
}

/** RFFT returns the n/2+1 coefficients of x zero-padded (or truncated) to n samples, as a
 *  { re, im } pair (not interleaved). */
export function rfft(x: Signal, n: number): Cplx {
  const m = Math.floor(n / 2) + 1;
  const re = new Float64Array(m);
  const im = new Float64Array(m);
  rfftInto(x, n, re, im);
  return { re, im };
}

/** Inverse of rfftInto, scaled by 1/n, written into out (length >= n). The imaginary parts of
 *  the DC and Nyquist bins are ignored, as gonum's Sequence does. */
function irfftInto(Xr: Float64Array, Xi: Float64Array, n: number, out: Float64Array): void {
  const inv = 1 / n;
  if (n % 2 !== 0) {
    const p = cplan(n);
    const [zr, zi, wr, wi] = work(n);
    const m = Xr.length;
    for (let k = 0; k < n; k++) {
      if (k < m) {
        zr[k] = Xr[k];
        zi[k] = k === 0 ? 0 : -Xi[k];
      } else {
        zr[k] = Xr[n - k];
        zi[k] = Xi[n - k];
      }
    }
    const sw = cfft(p, zr, zi, wr, wi);
    const rr = sw ? wr : zr;
    for (let j = 0; j < n; j++) out[j] = rr[j] * inv;
    return;
  }
  const h = n / 2;
  const p = rplan(n);
  const [zr, zi, wr, wi] = work(h);
  const x0 = Xr[0];
  const xn = Xr[h];
  // Build conj(Z) so the forward transform computes the inverse.
  zr[0] = x0 + xn;
  zi[0] = -(x0 - xn);
  const pr = p.pr;
  const pi = p.pi;
  const half = h >> 1;
  for (let k = 1; k <= half; k++) {
    const j = h - k;
    const xkr = Xr[k], xki = Xi[k], xjr = Xr[j], xji = Xi[j];
    const er = xkr + xjr, ei = xki - xji;
    const dr = xkr - xjr, di = xki + xji;
    const cr = pr[k], ci = pi[k];
    const or_ = dr * cr + di * ci, oi = di * cr - dr * ci;
    // Z_j = (er + oi, -ei + or), stored conjugated
    zr[j] = er + oi;
    zi[j] = -(-ei + or_);
    // Z_k = (er - oi, ei + or)
    zr[k] = er - oi;
    zi[k] = -(ei + or_);
  }
  const sw = cfft(p.c, zr, zi, wr, wi);
  const Zr = sw ? wr : zr;
  const Zi = sw ? wi : zi;
  // z = conj(result); x[2t] = re z, x[2t+1] = im z
  for (let t = 0; t < h; t++) {
    out[2 * t] = Zr[t] * inv;
    out[2 * t + 1] = -Zi[t] * inv;
  }
}

/** IRFFT inverts RFFT, scaled so irfft(rfft(x, n), n) == x. */
export function irfft(X: Cplx, n: number): Float64Array {
  const out = new Float64Array(n);
  irfftInto(X.re, X.im, n, out);
  return out;
}

// ---------------------------------------------------------------------------------------------
// util.go

/** NextPow2 returns the smallest power of two >= n. */
export function nextPow2(n: number): number {
  let p = 1;
  while (p < n) p *= 2;
  return p;
}

/** Mag returns |X| for each coefficient. */
export function mag(X: Cplx): Float64Array {
  const m = new Float64Array(X.re.length);
  for (let i = 0; i < m.length; i++) m[i] = hypot(X.re[i], X.im[i]);
  return m;
}

/** DB converts an amplitude ratio to decibels with a floor. */
export function db(v: number): number {
  if (v < 1e-15) v = 1e-15;
  return 20 * Math.log10(v);
}

/** DBPow converts a power ratio to decibels with a floor. */
export function dbPow(p: number): number {
  if (p < 1e-30) p = 1e-30;
  return 10 * Math.log10(p);
}

/** FromDB converts decibels to an amplitude ratio. */
export function fromDB(d: number): number {
  return Math.pow(10, d / 20);
}

/** RMS of a signal. */
export function rms(x: Signal): number {
  if (x.length === 0) return 0;
  let s = 0;
  for (let i = 0; i < x.length; i++) s += x[i] * x[i];
  return Math.sqrt(s / x.length);
}

/** Peak returns max |x|. */
export function peak(x: Signal): number {
  let p = 0;
  for (let i = 0; i < x.length; i++) {
    const a = Math.abs(x[i]);
    if (a > p) p = a;
  }
  return p;
}

/** Hann window of length n (periodic=false). */
export function hann(n: number): Float64Array {
  const w = new Float64Array(n);
  for (let i = 0; i < n; i++) w[i] = 0.5 - 0.5 * Math.cos((2 * Math.PI * i) / (n - 1));
  return w;
}

const BH7 = [
  0.27105140069342, 0.43329793923448, 0.21812299954311, 0.06592544638803, 0.01081174209837,
  0.00077658482522, 0.00001388721735,
];

/** BlackmanHarris7 is a 7-term Blackman-Harris window (sidelobes near -180 dB). */
export function blackmanHarris7(n: number): Float64Array {
  const w = new Float64Array(n);
  for (let i = 0; i < n; i++) {
    const x = (2 * Math.PI * i) / n;
    let v = 0;
    let s = 1;
    for (let k = 0; k < BH7.length; k++) {
      v += s * BH7[k] * Math.cos(k * x);
      s = -s;
    }
    w[i] = v;
  }
  return w;
}

/** Cached BH7 window for internal use (callers must not modify it). */
function bh7(n: number): Float64Array {
  let w = bh7Cache.get(n);
  if (!w) {
    w = blackmanHarris7(n);
    bh7Cache.set(n, w, 8 * n);
  }
  return w;
}

/** Slice returns x[a:b] clamped to the bounds, as a view (no copy). Empty when a >= b. */
export function slice(x: Float64Array, a: number, b: number): Float64Array {
  if (a < 0) a = 0;
  if (b > x.length) b = x.length;
  if (a >= b) return new Float64Array(0);
  return x.subarray(a, b);
}

/** GoodLen returns the largest n <= max whose only prime factors are 2, 3 and 5 (even). */
export function goodLen(maxN: number): number {
  let best = 2;
  for (let p2 = 2; p2 <= maxN; p2 *= 2) {
    for (let p3 = p2; p3 <= maxN; p3 *= 3) {
      for (let p5 = p3; p5 <= maxN; p5 *= 5) {
        if (p5 > best) best = p5;
      }
    }
  }
  return best;
}

// ---------------------------------------------------------------------------------------------
// fft.go: correlation, marker search, fractional shift, resampling

/** Correlate returns c[k] = sum ref[i]*x[i+k] for lags 0..len(x)-1 (linear, not circular).
 *  Go does this with one zero-padded FFT of NextPow2(len(x)+len(ref)) points; here long inputs
 *  use overlap-save blocks, which computes the same linear correlation with a fraction of the
 *  memory and time (the results agree to rounding). */
export function correlate(x: Signal, ref: Signal): Float64Array {
  return correlateMulti(x, [ref])[0];
}

/** correlateMulti correlates x with several references of the same length, sharing the forward
 *  transforms of x. Each result equals correlate(x, refs[i]) exactly. */
export function correlateMulti(x: Signal, refs: Signal[]): Float64Array[] {
  const nx = x.length;
  const M = refs[0].length;
  for (const r of refs) if (r.length !== M) throw new Error('correlateMulti: references differ in length');
  const nGo = nextPow2(nx + M);
  let B = nextPow2(Math.max(4 * M, 1 << 16));
  if (B >= nGo || M === 0) {
    const n = nGo;
    const X = rfft(x, n);
    return refs.map((ref) => {
      const R = rfft(ref, n);
      const P = { re: new Float64Array(X.re.length), im: new Float64Array(X.re.length) };
      conjMulInto(X.re, X.im, R.re, R.im, P.re, P.im);
      const c = new Float64Array(n);
      irfftInto(P.re, P.im, n, c);
      return c.slice(0, nx);
    });
  }
  while (B - M + 1 < B / 2) B *= 2;
  const L = B - M + 1;
  const R = refs.map((ref) => rfft(ref, B));
  const half = B / 2 + 1;
  const Sr = new Float64Array(half);
  const Si = new Float64Array(half);
  const Pr = new Float64Array(half);
  const Pi = new Float64Array(half);
  const seg = new Float64Array(B);
  const blk = new Float64Array(B);
  const outs = refs.map(() => new Float64Array(nx));
  const xa = x as Float64Array;
  const isTyped = xa instanceof Float64Array;
  for (let k0 = 0; k0 < nx; k0 += L) {
    const end = Math.min(k0 + B, nx);
    if (isTyped) {
      seg.set(xa.subarray(k0, end));
    } else {
      for (let i = k0; i < end; i++) seg[i - k0] = x[i];
    }
    if (end - k0 < B) seg.fill(0, end - k0);
    rfftInto(seg, B, Sr, Si);
    const cnt = Math.min(L, nx - k0);
    for (let r = 0; r < refs.length; r++) {
      conjMulInto(Sr, Si, R[r].re, R[r].im, Pr, Pi);
      irfftInto(Pr, Pi, B, blk);
      outs[r].set(blk.subarray(0, cnt), k0);
    }
  }
  return outs;
}

/** (pr, pi) = X * conj(R), bin by bin, with Go's complex multiply. */
function conjMulInto(xr: Float64Array, xi: Float64Array, rr: Float64Array, ri: Float64Array, pr: Float64Array, pi: Float64Array): void {
  const n = xr.length;
  for (let k = 0; k < n; k++) {
    const a = xr[k], b = xi[k], c = rr[k], d = -ri[k];
    pr[k] = a * c - b * d;
    pi[k] = a * d + b * c;
  }
}

/** Marker search on a correlation (the second half of FindMarker). */
function markerFrom(c: Float64Array, refLen: number): [number, number] {
  const [p] = peakIndex(c, 0, c.length - refLen + 1);
  const pk = Math.abs(c[goRound(p)]);
  let s = 0;
  let cnt = 0;
  const excl = refLen;
  for (let i = 0; i < c.length; i += 7) {
    if (Math.abs(i - p) < excl) continue;
    s += Math.abs(c[i]);
    cnt++;
  }
  if (cnt === 0) return [p, 0];
  const floor = s / cnt;
  if (floor === 0) return [p, Infinity];
  return [p, pk / floor];
}

/** FindMarker locates ref inside x by matched filtering. Returns [fractional start index,
 *  confidence = peak over mean |c| away from the peak]. A clean capture gives well above 10,
 *  noise alone about 1 to 3. */
export function findMarker(x: Signal, ref: Signal): [number, number] {
  if (x.length < ref.length) return [0, 0];
  return markerFrom(correlate(x, ref), ref.length);
}

/** findMarkers is findMarker for several references of the same length at once (shares the
 *  transforms of x); each result equals findMarker(x, refs[i]). */
export function findMarkers(x: Signal, refs: Signal[]): [number, number][] {
  const M = refs[0].length;
  if (x.length < M) return refs.map(() => [0, 0] as [number, number]);
  return correlateMulti(x, refs).map((c) => markerFrom(c, M));
}

/** FracShift delays x by d samples (any real d) with a linear phase ramp. */
export function fracShift(x: Signal, d: number): Float64Array {
  const n = nextPow2(x.length * 2);
  const X = rfft(x, n);
  const re = X.re;
  const im = X.im;
  for (let k = 0; k < re.length; k++) {
    const w = (2 * Math.PI * k) / n;
    const a = -w * d;
    const c = Math.cos(a);
    const s = Math.sin(a);
    const xr = re[k], xi = im[k];
    re[k] = xr * c - xi * s;
    im[k] = xr * s + xi * c;
  }
  const out = new Float64Array(n);
  irfftInto(re, im, n, out);
  return out.slice(0, x.length);
}

function sinc(x: number): number {
  if (x === 0) return 1;
  return Math.sin(Math.PI * x) / (Math.PI * x);
}

function besselI0(x: number): number {
  let s = 1;
  let t = 1;
  for (let k = 1; k < 50; k++) {
    t *= x / 2 / k;
    s += t * t;
    if (t * t < 1e-17 * s) break;
  }
  return s;
}

/** Resample converts x from rate a to rate b with a windowed-sinc interpolator (Kaiser, beta 9,
 *  32 zero crossings each side, table-driven). */
export function resample(x: Signal, a: number, b: number): Float64Array {
  const n = Math.trunc((x.length * b) / a);
  const out = new Float64Array(n);
  const cut = Math.min(1, b / a) * 0.97;
  const half = 32;
  const per = 512;
  const span = half / cut;
  const tab = new Float64Array(half * per + 2);
  const i0b = besselI0(9);
  for (let i = 0; i < tab.length; i++) {
    const u = i / per / cut;
    const r = u / span;
    if (r >= 1) continue;
    tab[i] = (cut * sinc(cut * u) * besselI0(9 * Math.sqrt(1 - r * r))) / i0b;
  }
  const reach = Math.ceil(span);
  for (let j = 0; j < n; j++) {
    const t = (j * a) / b;
    const c = Math.floor(t);
    let s = 0;
    for (let i = c - reach + 1; i <= c + reach; i++) {
      if (i < 0 || i >= x.length) continue;
      const u = Math.abs(t - i) * cut * per;
      const k = Math.trunc(u);
      if (k >= tab.length - 1) continue;
      const fr = u - k;
      s += x[i] * (tab[k] * (1 - fr) + tab[k + 1] * fr);
    }
    out[j] = s;
  }
  return out;
}

// ---------------------------------------------------------------------------------------------
// sweep.go

/** Sweep is an exponential (log) sine sweep defined in continuous time. Field names match the
 *  Go struct (which has no JSON tags). */
export class Sweep {
  constructor(
    public F1: number,
    public F2: number,
    public T: number,
    public Amp: number,
    public Fade: number,
  ) {}

  /** L is the sweep rate constant T / ln(F2/F1). */
  L(): number {
    return this.T / Math.log(this.F2 / this.F1);
  }

  /** At returns the sweep value at time t seconds (0 outside [0, T]). */
  at(t: number): number {
    if (t < 0 || t > this.T) return 0;
    const L = this.L();
    return this.atL(t, L);
  }

  private atL(t: number, L: number): number {
    let v = this.Amp * Math.sin(2 * Math.PI * this.F1 * L * (Math.exp(t / L) - 1));
    if (this.Fade > 0) {
      if (t < this.Fade) {
        v *= 0.5 - 0.5 * Math.cos((Math.PI * t) / this.Fade);
      } else if (t > this.T - this.Fade) {
        v *= 0.5 - 0.5 * Math.cos((Math.PI * (this.T - t)) / this.Fade);
      }
    }
    return v;
  }

  /** Render samples the sweep at rate; stretch (1 + clock drift) stretches time. */
  render(rate: number, stretch = 1): Float64Array {
    if (stretch === 0) stretch = 1;
    const n = Math.ceil(this.T * rate * stretch);
    const out = new Float64Array(n);
    const L = this.L();
    const rs = rate * stretch;
    for (let i = 0; i < n; i++) {
      const t = i / rs;
      out[i] = t < 0 || t > this.T ? 0 : this.atL(t, L);
    }
    return out;
  }

  /** HarmonicDelay is how far before the linear impulse the k-th harmonic's impulse lands. */
  harmonicDelay(k: number): number {
    return this.L() * Math.log(k);
  }
}

/** Regularized spectral division of Y by X (see deconvolve), given X's spectrum and maxP.
 *  Writes H into Y in place and returns the inverse transform. */
function deconvolveSpectra(Y: Cplx, X: Cplx, maxP: number, n: number, rate: number, f1: number, f2: number): Float64Array {
  const xr = X.re, xi = X.im, yr = Y.re, yi = Y.im;
  const lo = f1 / 4;
  for (let k = 0; k < xr.length; k++) {
    const f = (k * rate) / n;
    let eps = 1e-10 * maxP;
    if (f < lo || f > f2) {
      const q = f < lo ? lo / Math.max(f, 1e-3) : f / f2;
      if (q > 4.0001) {
        // d > 2 octaves: 1e-10 * 10^(5d) > 1, so Go's min() yields exactly maxP.
        eps = maxP;
      } else {
        const d = goLog2(q);
        eps = maxP * Math.min(1, 1e-10 * Math.pow(10, 5 * d));
      }
    }
    const a = xr[k], b = xi[k];
    const den = a * a + b * b + eps;
    // Y * conj(X), then complex division by (den + 0i) as Go's runtime does it.
    const pr = yr[k] * a - yi[k] * -b;
    const pi = yr[k] * -b + yi[k] * a;
    const ratio = 0 / den;
    const denom = den + ratio * 0;
    yr[k] = (pr + pi * ratio) / denom;
    yi[k] = (pi - pr * ratio) / denom;
  }
  const out = new Float64Array(n);
  irfftInto(yr, yi, n, out);
  return out;
}

function maxPower(X: Cplx): number {
  let maxP = 0;
  for (let k = 0; k < X.re.length; k++) {
    const p = X.re[k] * X.re[k] + X.im[k] * X.im[k];
    if (p > maxP) maxP = p;
  }
  return maxP;
}

/** A precomputed reference spectrum for repeated deconvolutions with the same x and n. */
export interface DeconvRef {
  n: number;
  X: Cplx;
  maxP: number;
}

/** deconvRef prepares the spectrum of x for deconvolveWith (n = NextPow2(len(x)+len(y))). */
export function deconvRef(x: Signal, n: number): DeconvRef {
  const X = rfft(x, n);
  return { n, X, maxP: maxPower(X) };
}

/** deconvolveWith is deconvolve with the reference spectrum precomputed. ref.n must equal
 *  NextPow2(len(x)+len(y)). */
export function deconvolveWith(y: Signal, ref: DeconvRef, rate: number, f1: number, f2: number): Float64Array {
  const Y = rfft(y, ref.n);
  return deconvolveSpectra(Y, ref.X, ref.maxP, ref.n, rate, f1, f2);
}

/** Deconvolve computes h with y = x * h by regularized spectral division. The result has length
 *  n = NextPow2(len(x)+len(y)); index 0 is zero lag and negative lags wrap to the end. */
export function deconvolve(y: Signal, x: Signal, rate: number, f1: number, f2: number): Float64Array {
  const n = nextPow2(x.length + y.length);
  return deconvolveWith(y, deconvRef(x, n), rate, f1, f2);
}

/** PeakIndex finds the largest |h| in [from, to) and refines it with a parabola. Returns
 *  [fractional index, signed peak value]. */
export function peakIndex(h: Signal, from: number, to: number): [number, number] {
  if (from < 0) from = 0;
  if (to > h.length || to <= 0) to = h.length;
  let bi = from;
  let bv = Math.abs(h[bi]);
  for (let i = from; i < to; i++) {
    const a = Math.abs(h[i]);
    if (a > bv) {
      bi = i;
      bv = a;
    }
  }
  if (bi <= 0 || bi >= h.length - 1) return [bi, h[bi]];
  const a = Math.abs(h[bi - 1]), b = Math.abs(h[bi]), c = Math.abs(h[bi + 1]);
  const den = a - 2 * b + c;
  let off = 0;
  if (den !== 0) off = (0.5 * (a - c)) / den;
  return [bi + off, h[bi]];
}

/** Window cuts the impulse response around peak (pre s before, post s after) with half-Hann
 *  fades. Returns [cut, index of the peak inside it]. */
export function window(h: Signal, peakPos: number, rate: number, pre: number, post: number): [Float64Array, number] {
  const n = h.length;
  const np = Math.trunc(pre * rate);
  const nq = Math.trunc(post * rate);
  const p = goRound(peakPos);
  const out = new Float64Array(np + nq);
  for (let i = 0; i < out.length; i++) {
    const idx = (((p - np + i) % n) + n) % n;
    out[i] = h[idx];
  }
  const fi = Math.trunc(np / 10);
  for (let i = 0; i < fi; i++) out[i] *= 0.5 - 0.5 * Math.cos((Math.PI * i) / fi);
  const fo = Math.trunc(nq / 5);
  for (let i = 0; i < fo; i++) out[out.length - 1 - i] *= 0.5 - 0.5 * Math.cos((Math.PI * i) / fo);
  return [out, peakPos - (p - np)];
}

// ---------------------------------------------------------------------------------------------
// response.go

/** Curve is a frequency response on an arbitrary grid (JSON names as in Go). */
export interface Curve {
  hz: number[] | null;
  db: number[] | null;
  phase?: number[]; // degrees
  gd?: number[]; // group delay, ms
}

/** Spectrum of a windowed impulse response: complex values at k*rate/n. */
export class Spectrum {
  constructor(
    public rate: number,
    public n: number,
    public re: Float64Array,
    public im: Float64Array,
  ) {}

  /** DivideBy divides s by ref bin by bin (same n required); bins where ref is more than 60 dB
   *  below its peak are left alone. */
  divideBy(ref: Spectrum): void {
    if (ref.n !== this.n) return;
    let pk = 0;
    for (let k = 0; k < ref.re.length; k++) pk = Math.max(pk, hypot(ref.re[k], ref.im[k]));
    const o = [0, 0];
    for (let k = 0; k < this.re.length; k++) {
      if (hypot(ref.re[k], ref.im[k]) > pk * 1e-3) {
        cdiv(this.re[k], this.im[k], ref.re[k], ref.im[k], o);
        this.re[k] = o[0];
        this.im[k] = o[1];
      }
    }
  }

  /** At interpolates the spectrum at frequency f (linear in complex value). Returns [re, im]. */
  at(f: number): [number, number] {
    const o: [number, number] = [0, 0];
    this.atInto(f, o);
    return o;
  }

  private atInto(f: number, o: number[]): void {
    const pos = (f * this.n) / this.rate;
    const i = Math.trunc(pos);
    const last = this.re.length - 1;
    if (i >= last) {
      o[0] = this.re[last];
      o[1] = this.im[last];
      return;
    }
    const fr = pos - i;
    // X[i]*complex(1-fr,0) + X[i+1]*complex(fr,0), with Go's complex multiply.
    const g = 1 - fr;
    const ar = this.re[i] * g - this.im[i] * 0;
    const ai = this.re[i] * 0 + this.im[i] * g;
    const br = this.re[i + 1] * fr - this.im[i + 1] * 0;
    const bi = this.re[i + 1] * 0 + this.im[i + 1] * fr;
    o[0] = ar + br;
    o[1] = ai + bi;
  }

  /** CurveFrom samples the spectrum on grid with optional 1/smooth-octave power smoothing. */
  curveFrom(grid: ArrayLike<number>, smooth: number): Curve {
    const ng = grid.length;
    const hz = Array.from(grid);
    const dbv: number[] = new Array(ng);
    const phase: number[] = new Array(ng);
    const gd: number[] = new Array(ng);
    const df = this.rate / this.n;
    const nb = this.re.length;
    let pre: Float64Array | null = null;
    if (smooth > 0) {
      pre = new Float64Array(nb + 1);
      for (let k = 0; k < nb; k++) pre[k + 1] = pre[k] + this.re[k] * this.re[k] + this.im[k] * this.im[k];
    }
    const v = [0, 0];
    const v0 = [0, 0];
    const v1 = [0, 0];
    for (let i = 0; i < ng; i++) {
      const f = hz[i];
      this.atInto(f, v);
      if (pre) {
        const lo = f * Math.pow(2, -0.5 / smooth);
        const hi = f * Math.pow(2, 0.5 / smooth);
        const a = Math.ceil(lo / df);
        let b = Math.floor(hi / df);
        if (b >= nb) b = nb - 1;
        if (b - a >= 1) {
          dbv[i] = dbPow((pre[b + 1] - pre[a]) / (b - a + 1));
        } else {
          dbv[i] = db(hypot(v[0], v[1]));
        }
      } else {
        dbv[i] = db(hypot(v[0], v[1]));
      }
      phase[i] = (Math.atan2(v[1], v[0]) * 180) / Math.PI;
      const f0 = Math.max(f - df, 0);
      const f1 = f + df;
      this.atInto(f0, v0);
      this.atInto(f1, v1);
      const p0 = Math.atan2(v0[1], v0[0]);
      const p1 = Math.atan2(v1[1], v1[0]);
      let d = p1 - p0;
      while (d > Math.PI) d -= 2 * Math.PI;
      while (d < -Math.PI) d += 2 * Math.PI;
      gd[i] = (-d / (2 * Math.PI * (f1 - f0))) * 1000;
    }
    return { hz, db: dbv, phase, gd };
  }
}

/** ResponseOf returns the spectrum of ir zero-padded to at least minN, with the linear-phase
 *  delay of peakOffset removed. */
export function responseOf(ir: Signal, rate: number, peakOffset: number, minN: number): Spectrum {
  const n = nextPow2(Math.max(ir.length, minN));
  const X = rfft(ir, n);
  const re = X.re;
  const im = X.im;
  for (let k = 0; k < re.length; k++) {
    const w = (2 * Math.PI * k) / n;
    const a = w * peakOffset;
    const c = Math.cos(a);
    const s = Math.sin(a);
    const xr = re[k], xi = im[k];
    re[k] = xr * c - xi * s;
    im[k] = xr * s + xi * c;
  }
  return new Spectrum(rate, n, re, im);
}

/** LogGrid returns frequencies from f1 to f2 with ppo points per octave. */
export function logGrid(f1: number, f2: number, ppo: number): number[] {
  const n = Math.ceil(goLog2(f2 / f1) * ppo) + 1;
  const out: number[] = new Array(n);
  for (let i = 0; i < n; i++) out[i] = f1 * Math.pow(2, i / ppo);
  out[n - 1] = Math.min(out[n - 1], f2);
  return out;
}

/** sort.SearchFloat64s: smallest i with a[i] >= x. */
function searchFloat64s(a: ArrayLike<number>, x: number): number {
  let lo = 0;
  let hi = a.length;
  while (lo < hi) {
    const h = (lo + hi) >>> 1;
    if (!(a[h] >= x)) lo = h + 1;
    else hi = h;
  }
  return lo;
}

/** ValueAt interpolates a curve's dB value at f on a log axis. */
export function curveValueAt(c: Curve, f: number): number {
  const hz = c.hz ?? [];
  const d = c.db ?? [];
  const n = hz.length;
  if (n === 0) return 0;
  const i = searchFloat64s(hz, f);
  if (i <= 0) return d[0];
  if (i >= n) return d[n - 1];
  const a = Math.log(hz[i - 1]);
  const b = Math.log(hz[i]);
  const t = (Math.log(f) - a) / (b - a);
  return d[i - 1] * (1 - t) + d[i] * t;
}

/** Normalize shifts the curve so its value at ref Hz is 0 dB; returns the shift. */
export function curveNormalize(c: Curve, ref: number): number {
  const v = curveValueAt(c, ref);
  if (c.db) for (let i = 0; i < c.db.length; i++) c.db[i] -= v;
  return v;
}

/** Subtract returns c minus ref (interpolated onto c's grid). */
export function curveSubtract(c: Curve, ref: Curve): Curve {
  const hz = c.hz ?? [];
  const out: Curve = { hz: c.hz, db: hz.map((f, i) => (c.db as number[])[i] - curveValueAt(ref, f)) };
  if (c.phase && c.phase.length) out.phase = c.phase;
  if (c.gd && c.gd.length) out.gd = c.gd;
  return out;
}

/** Range returns [min, max] dB between f1 and f2. */
export function curveRange(c: Curve, f1: number, f2: number): [number, number] {
  let lo = Infinity;
  let hi = -Infinity;
  const hz = c.hz ?? [];
  const d = c.db ?? [];
  for (let i = 0; i < hz.length; i++) {
    const f = hz[i];
    if (f < f1 || f > f2) continue;
    lo = Math.min(lo, d[i]);
    hi = Math.max(hi, d[i]);
  }
  return [lo, hi];
}

/** AWeight returns the A-weighting gain in dB at f (IEC 61672, 0 dB at 1 kHz). */
export function aWeight(f: number): number {
  const f2 = f * f;
  const num = 12194.0 * 12194.0 * f2 * f2;
  const den = (f2 + 20.6 * 20.6) * Math.sqrt((f2 + 107.7 * 107.7) * (f2 + 737.9 * 737.9)) * (f2 + 12194.0 * 12194.0);
  return 20 * Math.log10(num / den) + 2.0;
}

// ---------------------------------------------------------------------------------------------
// tone.go

/** ToneResult is the analysis of a single sine (JSON names as in Go). */
export interface ToneResult {
  freq_hz: number;
  level_dbfs: number; // fundamental, dBFS (full-scale sine = 0 dBFS)
  thd_db: number; // harmonics 2..10 re fundamental, dB
  thd_pct: number;
  thdn_db: number; // everything but the fundamental in band, re fundamental, dB
  thdn_pct: number;
  thdn_a_db: number; // A-weighted THD+N re fundamental
  resid_dbfs: number; // level of the THD+N residual, dBFS
  resid_a_dbfs: number; // A-weighted residual, dBFS
  harmonics_db: number[] | null; // H2.. re fundamental, dB
}

/** Per-bin power of the windowed signal, scaled so a sine's main lobe sums to A^2/2. Uses the
 *  first n samples of x (n = len(w)). */
function spectrumPower(x: Signal, w: Float64Array): Float64Array {
  const n = w.length;
  const buf = new Float64Array(n);
  let sw2 = 0;
  for (let i = 0; i < n; i++) {
    buf[i] = x[i] * w[i];
    sw2 += w[i] * w[i];
  }
  const m = Math.floor(n / 2) + 1;
  const re = new Float64Array(m);
  const im = new Float64Array(m);
  rfftInto(buf, n, re, im);
  const p = new Float64Array(m);
  const nsw = n * sw2;
  for (let k = 0; k < m; k++) {
    const mm = re[k] * re[k] + im[k] * im[k];
    const s = k === 0 || k === m - 1 ? 1 : 2;
    p[k] = (s * mm) / nsw;
  }
  return p;
}

/** AnalyzeTone measures a sine near f0 in x. The THD+N band is [lo, hi] Hz. */
export function analyzeTone(x: Signal, rate: number, f0: number, lo: number, hi: number): ToneResult {
  if (x.length < 1024) {
    return {
      freq_hz: f0, level_dbfs: -999, thd_db: NaN, thd_pct: NaN, thdn_db: NaN, thdn_pct: NaN,
      thdn_a_db: NaN, resid_dbfs: NaN, resid_a_dbfs: NaN, harmonics_db: null,
    };
  }
  const n = goodLen(x.length);
  let mean = 0;
  for (let i = 0; i < n; i++) mean += x[i];
  mean /= n;
  const xx = new Float64Array(n);
  for (let i = 0; i < n; i++) xx[i] = x[i] - mean;
  const w = bh7(n);
  const p = spectrumPower(xx, w);
  const df = rate / n;
  const lobe = 8;
  const pl = p.length;
  let k0 = Math.trunc(f0 / df);
  const ka = Math.max(Math.trunc((f0 * 0.97) / df), 1);
  const kb = Math.min(Math.trunc((f0 * 1.03) / df) + 1, pl - 1);
  for (let k = ka; k <= kb; k++) {
    if (p[k] > p[k0]) k0 = k;
  }
  let sw = 0;
  let swk = 0;
  for (let k = Math.max(k0 - lobe, 1); k <= Math.min(k0 + lobe, pl - 1); k++) {
    sw += p[k];
    swk += p[k] * k;
  }
  const freq = (swk / sw) * df;
  const fund = sw;
  let harmonics: number[] | null = null;
  let hsum = 0;
  for (let h = 2; h <= 10; h++) {
    const fh = freq * h;
    if (fh > hi || fh > rate / 2 - lobe * df) break;
    let kc = goRound(fh / df);
    // As in Go, the loop bound follows kc while it moves.
    for (let k = kc - 2; k <= kc + 2; k++) {
      if (k > 0 && k < pl && p[k] > p[kc]) kc = k;
    }
    let s = 0;
    for (let k = Math.max(kc - lobe, 1); k <= Math.min(kc + lobe, pl - 1); k++) s += p[k];
    hsum += s;
    if (!harmonics) harmonics = [];
    harmonics.push(dbPow(s / fund));
  }
  let resid = 0;
  let residA = 0;
  const kl = Math.max(Math.ceil(lo / df), 1);
  const kh = Math.min(Math.trunc(hi / df), pl - 1);
  for (let k = kl; k <= kh; k++) {
    if (k >= k0 - lobe && k <= k0 + lobe) continue;
    resid += p[k];
    residA += p[k] * Math.pow(10, aWeight(k * df) / 10);
  }
  const nb = kh - kl + 1 - (2 * lobe + 1);
  if (nb > 0) {
    resid += (resid / nb) * (2 * lobe + 1);
    residA += (residA / nb) * (2 * lobe + 1);
  }
  return {
    freq_hz: freq,
    level_dbfs: dbPow(fund * 2),
    thd_db: dbPow(hsum / fund),
    thd_pct: 100 * Math.sqrt(hsum / fund),
    thdn_db: dbPow(resid / fund),
    thdn_pct: 100 * Math.sqrt(resid / fund),
    thdn_a_db: dbPow(residA / fund),
    resid_dbfs: dbPow(resid * 2),
    resid_a_dbfs: dbPow(residA * 2),
    harmonics_db: harmonics,
  };
}

/** LevelAt returns the level in dBFS of the component at f (sum over the BH7 lobe). */
export function levelAt(x: Signal, rate: number, f: number): number {
  if (x.length < 1024) return -999;
  const n = goodLen(x.length);
  const p = spectrumPower(x, bh7(n));
  const df = rate / n;
  const kc = goRound(f / df);
  let s = 0;
  for (let k = Math.max(kc - 8, 1); k <= Math.min(kc + 8, p.length - 1); k++) s += p[k];
  return dbPow(s * 2);
}

/** NoiseResult is a broadband noise measurement (JSON names as in Go). The PSD is attached as
 *  non-enumerable properties psdHz / psdDb, so it is available but, as in Go (json:"-"), not
 *  serialized. */
export interface NoiseResult {
  dbfs: number; // unweighted, lo..hi, RMS re full-scale sine
  dbfs_a: number; // A-weighted
  dbfs_wide: number; // unweighted, 10 Hz .. Nyquist
  peak_dbfs: number; // sample peak
  hum_dbfs: number; // largest of 50/60 Hz and harmonics up to 300 Hz
  hum_hz: number;
  readonly psdHz?: Float64Array;
  readonly psdDb?: Float64Array;
}

function noiseResult(dbfs: number, dbfsA: number, wide: number, pk: number, hum: number, humHz: number, psdHz?: Float64Array, psdDb?: Float64Array): NoiseResult {
  const r: NoiseResult = { dbfs, dbfs_a: dbfsA, dbfs_wide: wide, peak_dbfs: pk, hum_dbfs: hum, hum_hz: humHz };
  if (psdHz && psdDb) {
    Object.defineProperty(r, 'psdHz', { value: psdHz, enumerable: false });
    Object.defineProperty(r, 'psdDb', { value: psdDb, enumerable: false });
  }
  return r;
}

/** AnalyzeNoise measures noise in x with a Welch average of Hann-windowed blocks. */
export function analyzeNoise(x: Signal, rate: number, lo: number, hi: number): NoiseResult {
  let bl = nextPow2(Math.trunc(rate / 2));
  if (bl > x.length) bl = nextPow2(x.length) / 2;
  if (bl < 256) return noiseResult(-999, -999, -999, 0, 0, 0);
  const w = hann(bl);
  const acc = new Float64Array(bl / 2 + 1);
  let cnt = 0;
  const s = new Float64Array(bl);
  const step = Math.trunc(bl / 2);
  for (let off = 0; off + bl <= x.length; off += step) {
    let mean = 0;
    for (let i = 0; i < bl; i++) mean += x[off + i];
    mean /= bl;
    for (let i = 0; i < bl; i++) s[i] = x[off + i] - mean;
    const p = spectrumPower(s, w);
    for (let k = 0; k < acc.length; k++) acc[k] += p[k];
    cnt++;
  }
  const df = rate / bl;
  let sum = 0;
  let sumA = 0;
  let sumW = 0;
  const psdHz = new Float64Array(acc.length);
  const psdDb = new Float64Array(acc.length);
  for (let k = 0; k < acc.length; k++) {
    acc[k] /= cnt;
    const f = k * df;
    psdHz[k] = f;
    psdDb[k] = dbPow(acc[k] * 2);
    if (f >= 10) sumW += acc[k];
    if (f >= lo && f <= hi) {
      sum += acc[k];
      sumA += acc[k] * Math.pow(10, aWeight(f) / 10);
    }
  }
  let hum = -999;
  let humHz = 0;
  for (const base of [50, 60]) {
    for (let h = 1; h <= 6; h++) {
      const f = base * h;
      const k = goRound(f / df);
      let sh = 0;
      for (let j = k - 1; j <= k + 1; j++) {
        if (j > 0 && j < acc.length) sh += acc[j];
      }
      const d = dbPow(sh * 2);
      if (d > hum) {
        hum = d;
        humHz = f;
      }
    }
  }
  return noiseResult(dbPow(sum * 2), dbPow(sumA * 2), dbPow(sumW * 2), db(peak(x)), hum, humHz, psdHz, psdDb);
}

// ---------------------------------------------------------------------------------------------
// biquad.go

/** Biquad is a second-order IIR section (RBJ cookbook), normalized so a0 = 1. */
export class Biquad {
  constructor(
    public B0: number,
    public B1: number,
    public B2: number,
    public A1: number,
    public A2: number,
  ) {}

  /** Process filters x and returns a new signal. */
  process(x: Signal): Float64Array {
    const y = new Float64Array(x.length);
    let x1 = 0, x2 = 0, y1 = 0, y2 = 0;
    for (let i = 0; i < x.length; i++) {
      const v = x[i];
      const o = this.B0 * v + this.B1 * x1 + this.B2 * x2 - this.A1 * y1 - this.A2 * y2;
      x2 = x1;
      x1 = v;
      y2 = y1;
      y1 = o;
      y[i] = o;
    }
    return y;
  }

  /** Response returns the complex response at f as [re, im]. */
  response(rate: number, f: number): [number, number] {
    const a = (-2 * Math.PI * f) / rate;
    const zr = Math.cos(a), zi = Math.sin(a);
    const z2r = zr * zr - zi * zi, z2i = zr * zi + zi * zr;
    const nr = this.B0 + this.B1 * zr + this.B2 * z2r;
    const ni = this.B1 * zi + this.B2 * z2i;
    const dr = 1 + this.A1 * zr + this.A2 * z2r;
    const di = this.A1 * zi + this.A2 * z2i;
    const o = [0, 0];
    cdiv(nr, ni, dr, di, o);
    return [o[0], o[1]];
  }
}

export function peaking(rate: number, f0: number, gainDB: number, q: number): Biquad {
  const A = Math.pow(10, gainDB / 40);
  const w = (2 * Math.PI * f0) / rate;
  const al = Math.sin(w) / (2 * q);
  const a0 = 1 + al / A;
  return new Biquad((1 + al * A) / a0, (-2 * Math.cos(w)) / a0, (1 - al * A) / a0, (-2 * Math.cos(w)) / a0, (1 - al / A) / a0);
}

export function lowPass(rate: number, f0: number, q: number): Biquad {
  const w = (2 * Math.PI * f0) / rate;
  const al = Math.sin(w) / (2 * q);
  const c = Math.cos(w);
  const a0 = 1 + al;
  return new Biquad((1 - c) / 2 / a0, (1 - c) / a0, (1 - c) / 2 / a0, (-2 * c) / a0, (1 - al) / a0);
}

export function highPass(rate: number, f0: number, q: number): Biquad {
  const w = (2 * Math.PI * f0) / rate;
  const al = Math.sin(w) / (2 * q);
  const c = Math.cos(w);
  const a0 = 1 + al;
  return new Biquad((1 + c) / 2 / a0, -(1 + c) / a0, (1 + c) / 2 / a0, (-2 * c) / a0, (1 - al) / a0);
}

export function highShelf(rate: number, f0: number, gainDB: number): Biquad {
  const A = Math.pow(10, gainDB / 40);
  const w = (2 * Math.PI * f0) / rate;
  const c = Math.cos(w);
  const al = (Math.sin(w) / 2) * Math.sqrt(2);
  const sa = 2 * Math.sqrt(A) * al;
  const a0 = A + 1 - (A - 1) * c + sa;
  return new Biquad(
    (A * (A + 1 + (A - 1) * c + sa)) / a0,
    (-2 * A * (A - 1 + (A + 1) * c)) / a0,
    (A * (A + 1 + (A - 1) * c - sa)) / a0,
    (2 * (A - 1 - (A + 1) * c)) / a0,
    (A + 1 - (A - 1) * c - sa) / a0,
  );
}
