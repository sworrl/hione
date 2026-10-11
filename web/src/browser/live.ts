import type { LiveFrame } from '../types';

/**
 * Meters and spectrum for the live view, computed in the browser from the capture blocks.
 * The same maths as internal/engine/live.go: peak hold falling about 20 dB/s, RMS with a 300 ms
 * time constant (shown re a full-scale sine), clip hold for 2 s, and 240 log bins from 10 Hz to
 * Nyquist, each the largest FFT bin it covers, scaled so a sine reads its own peak level.
 */
export class LiveMeter {
  readonly freqs: Float32Array;
  private fftN: number;
  private hist: [Float32Array, Float32Array];
  private filled = 0;
  private pos = 0;
  private peak = [0, 0];
  private rms = [0, 0];
  private clipUntil = [0, 0];
  private lastSpec = 0;
  private spec: [Float32Array, Float32Array];
  private win: Float64Array;
  private winSum = 0;
  private re: Float64Array;
  private im: Float64Array;

  constructor(readonly rate: number) {
    this.fftN = rate > 100000 ? 32768 : 16384;
    this.hist = [new Float32Array(this.fftN), new Float32Array(this.fftN)];
    const nb = 240;
    const hi = rate / 2;
    this.freqs = new Float32Array(nb);
    for (let i = 0; i < nb; i++) this.freqs[i] = 10 * Math.pow(hi / 10, i / (nb - 1));
    this.spec = [new Float32Array(nb).fill(-150), new Float32Array(nb).fill(-150)];
    this.win = new Float64Array(this.fftN);
    for (let i = 0; i < this.fftN; i++) {
      this.win[i] = 0.5 - 0.5 * Math.cos((2 * Math.PI * i) / this.fftN);
      this.winSum += this.win[i];
    }
    this.re = new Float64Array(this.fftN);
    this.im = new Float64Array(this.fftN);
  }

  /** Feed one block; returns a frame when the spectrum was refreshed (about 30 a second). */
  feed(l: Float32Array, r: Float32Array): LiveFrame | null {
    const n = l.length;
    const dt = n / this.rate;
    const now = performance.now();
    [l, r].forEach((x, c) => {
      let pk = 0, ss = 0;
      for (let i = 0; i < n; i++) { const a = Math.abs(x[i]); if (a > pk) pk = a; ss += x[i] * x[i]; }
      this.peak[c] = Math.max(pk, this.peak[c] * Math.pow(10, (-20 * dt) / 20));
      const k = Math.exp(-dt / 0.3);
      this.rms[c] = Math.sqrt(k * this.rms[c] * this.rms[c] + (1 - k) * (ss / n));
      if (pk >= 0.995) this.clipUntil[c] = now + 2000;
    });
    for (let i = 0; i < n; i++) {
      this.hist[0][this.pos] = l[i];
      this.hist[1][this.pos] = r[i];
      this.pos = (this.pos + 1) % this.fftN;
    }
    this.filled = Math.min(this.fftN, this.filled + n);
    if (this.filled < this.fftN || now - this.lastSpec < 30) return null;
    this.lastSpec = now;
    for (let c = 0; c < 2; c++) this.spectrum(c);
    const db = (v: number) => Math.max(20 * Math.log10(Math.max(v, 1e-12)), -150);
    return {
      rate: this.rate,
      time: Date.now(),
      clip: [now < this.clipUntil[0], now < this.clipUntil[1]],
      peak: [db(this.peak[0]), db(this.peak[1])],
      rms: [db(this.rms[0] * Math.SQRT2), db(this.rms[1] * Math.SQRT2)],
      specL: this.spec[0].slice(),
      specR: this.spec[1].slice(),
    };
  }

  private spectrum(c: number) {
    const N = this.fftN, h = this.hist[c];
    for (let i = 0; i < N; i++) { this.re[i] = h[(this.pos + i) % N] * this.win[i]; this.im[i] = 0; }
    fft(this.re, this.im);
    const df = this.rate / N;
    const out = this.spec[c];
    for (let b = 0; b < this.freqs.length; b++) {
      const f = this.freqs[b];
      const a = Math.max(1, Math.floor((f * Math.pow(2, -1 / 48)) / df));
      const e = Math.min(N / 2, Math.floor((f * Math.pow(2, 1 / 48)) / df) + 1);
      let m = 0;
      for (let k = a; k <= e; k++) { const v = this.re[k] * this.re[k] + this.im[k] * this.im[k]; if (v > m) m = v; }
      out[b] = Math.max(20 * Math.log10(Math.max((2 * Math.sqrt(m)) / this.winSum, 1e-12)), -150);
    }
  }
}

/** In-place radix-2 complex FFT; n must be a power of two. */
function fft(re: Float64Array, im: Float64Array) {
  const n = re.length;
  for (let i = 1, j = 0; i < n; i++) {
    let bit = n >> 1;
    for (; j & bit; bit >>= 1) j ^= bit;
    j ^= bit;
    if (i < j) { let t = re[i]; re[i] = re[j]; re[j] = t; t = im[i]; im[i] = im[j]; im[j] = t; }
  }
  for (let len = 2; len <= n; len <<= 1) {
    const ang = (-2 * Math.PI) / len;
    const wr = Math.cos(ang), wi = Math.sin(ang);
    for (let i = 0; i < n; i += len) {
      let cr = 1, ci = 0;
      for (let k = 0; k < len / 2; k++) {
        const a = i + k, b = a + len / 2;
        const xr = re[b] * cr - im[b] * ci, xi = re[b] * ci + im[b] * cr;
        re[b] = re[a] - xr; im[b] = im[a] - xi;
        re[a] += xr; im[a] += xi;
        const t = cr * wr - ci * wi; ci = cr * wi + ci * wr; cr = t;
      }
    }
  }
}
