// WAV writer: a port of the PCM integer path of internal/wav's Encode. Canonical 44-byte RIFF
// header, format tag 1, interleaved little-endian samples. Quantization matches Go's clampInt:
// round(v * 2^(bits-1)) with half away from zero, then clamp to [-2^(bits-1), 2^(bits-1)-1].
// No dither is added here (the stimulus generator adds it where it matters).

import { goRound } from './dsp';

function clampInt(v: number, max: number): number {
  let r = goRound(v * max);
  if (r > max - 1) r = max - 1;
  if (r < -max) r = -max;
  return r;
}

/** encodeWav returns a complete WAV file for data[channel][frame] at rate, 16 or 24-bit PCM.
 *  All channels are written; frames are taken from the first channel's length. */
export function encodeWav(data: Float64Array[] | number[][], rate: number, bits: 16 | 24): Uint8Array {
  const ch = data.length;
  if (ch === 0) throw new Error('wav: no channels');
  if (bits !== 16 && bits !== 24) throw new Error('wav: only 16 and 24-bit PCM are supported');
  const n = data[0].length;
  const bps = bits / 8;
  const bpf = ch * bps;
  const dataLen = n * bpf;
  const buf = new Uint8Array(44 + dataLen);
  const dv = new DataView(buf.buffer);
  const ascii = (off: number, s: string): void => {
    for (let i = 0; i < s.length; i++) buf[off + i] = s.charCodeAt(i);
  };
  ascii(0, 'RIFF');
  dv.setUint32(4, (36 + dataLen) >>> 0, true);
  ascii(8, 'WAVEfmt ');
  dv.setUint32(16, 16, true);
  dv.setUint16(20, 1, true);
  dv.setUint16(22, ch, true);
  dv.setUint32(24, rate >>> 0, true);
  dv.setUint32(28, (rate * bpf) >>> 0, true);
  dv.setUint16(32, bpf, true);
  dv.setUint16(34, bits, true);
  ascii(36, 'data');
  dv.setUint32(40, dataLen >>> 0, true);
  let o = 44;
  if (bits === 16) {
    for (let i = 0; i < n; i++) {
      for (let c = 0; c < ch; c++) {
        const x = clampInt(data[c][i], 32768) & 0xffff;
        buf[o] = x & 0xff;
        buf[o + 1] = x >>> 8;
        o += 2;
      }
    }
  } else {
    for (let i = 0; i < n; i++) {
      for (let c = 0; c < ch; c++) {
        const x = clampInt(data[c][i], 8388608) & 0xffffff;
        buf[o] = x & 0xff;
        buf[o + 1] = (x >>> 8) & 0xff;
        buf[o + 2] = x >>> 16;
        o += 3;
      }
    }
  }
  return buf;
}
