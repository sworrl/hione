import { useEffect, useRef } from 'react';
import { compile, fit } from './gl';
import type { LiveStore } from '../api';

const VS = `#version 300 es
in vec2 a; out vec2 uv;
void main() { uv = a * 0.5 + 0.5; gl_Position = vec4(a, 0.0, 1.0); }`;

// Time runs left to right (newest at the right edge), frequency bottom to top on a log axis,
// because the bins already are log-spaced. Colors: night, ice blue, amber, white.
const FS = `#version 300 es
precision highp float;
in vec2 uv; out vec4 o;
uniform sampler2D u_tex;
uniform float u_head;   // column written last, 0..1
uniform float u_cols;
vec3 ramp(float t) {
  vec3 c0 = vec3(0.067, 0.071, 0.086);
  vec3 c1 = vec3(0.10, 0.20, 0.38);
  vec3 c2 = vec3(0.557, 0.773, 1.0);
  vec3 c3 = vec3(1.0, 0.671, 0.180);
  vec3 c4 = vec3(1.0, 0.97, 0.90);
  if (t < 0.25) return mix(c0, c1, t / 0.25);
  if (t < 0.55) return mix(c1, c2, (t - 0.25) / 0.30);
  if (t < 0.85) return mix(c2, c3, (t - 0.55) / 0.30);
  return mix(c3, c4, (t - 0.85) / 0.15);
}
void main() {
  float x = fract(u_head + 1.0 / u_cols + uv.x * (1.0 - 1.0 / u_cols));
  float v = texture(u_tex, vec2(x, uv.y)).r;
  o = vec4(ramp(v), 1.0);
}`;

/** Scrolling spectrogram: one texture column per live frame, drawn by a single quad. */
export function Spectrogram({ store, height, floor = -150, top = -10 }: { store: LiveStore; height: number; floor?: number; top?: number }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const lab = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const c = ref.current!;
    const gl = c.getContext('webgl2')!;
    if (!gl) return;
    const prog = compile(gl, VS, FS);
    const vao = gl.createVertexArray()!;
    gl.bindVertexArray(vao);
    const vb = gl.createBuffer()!;
    gl.bindBuffer(gl.ARRAY_BUFFER, vb);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
    const la = gl.getAttribLocation(prog, 'a');
    gl.enableVertexAttribArray(la);
    gl.vertexAttribPointer(la, 2, gl.FLOAT, false, 0, 0);
    const cols = 900;
    let bins = 0;
    const tex = gl.createTexture()!;
    let head = 0;
    let col = new Uint8Array(0);
    let pending = 0;
    const off = store.on(f => {
      const n = f.specL.length;
      if (n !== bins) {
        bins = n;
        gl.bindTexture(gl.TEXTURE_2D, tex);
        gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.R8, cols, bins, 0, gl.RED, gl.UNSIGNED_BYTE, new Uint8Array(cols * bins));
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.REPEAT);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        col = new Uint8Array(bins);
      }
      for (let i = 0; i < bins; i++) {
        const v = Math.max(f.specL[i], f.specR[i]);
        col[i] = Math.max(0, Math.min(255, Math.round((v - floor) / (top - floor) * 255)));
      }
      head = (head + 1) % cols;
      gl.bindTexture(gl.TEXTURE_2D, tex);
      gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
      gl.texSubImage2D(gl.TEXTURE_2D, 0, head, 0, 1, bins, gl.RED, gl.UNSIGNED_BYTE, col);
      pending++;
    });
    let raf = 0;
    let drawn = -1;
    const frame = () => {
      raf = requestAnimationFrame(frame);
      const [w, h] = fit(c);
      if (pending === drawn && c.dataset.w === String(w)) return;
      drawn = pending; c.dataset.w = String(w);
      gl.viewport(0, 0, w, h);
      if (!bins) { gl.clearColor(0.067, 0.071, 0.086, 1); gl.clear(gl.COLOR_BUFFER_BIT); return; }
      gl.useProgram(prog);
      gl.bindVertexArray(vao);
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, tex);
      gl.uniform1i(gl.getUniformLocation(prog, 'u_tex'), 0);
      gl.uniform1f(gl.getUniformLocation(prog, 'u_head'), (head + 0.5) / cols);
      gl.uniform1f(gl.getUniformLocation(prog, 'u_cols'), cols);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
      // Frequency labels.
      const lc = lab.current!;
      const [lw, lh, dpr] = fit(lc);
      const ctx = lc.getContext('2d')!;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, lw, lh);
      const fr = store.freqs;
      if (fr.length > 1) {
        ctx.font = '11px Inter, system-ui, sans-serif';
        ctx.fillStyle = 'rgba(240,239,236,.8)';
        const H = lh / dpr;
        for (const f of [100, 1000, 10000, 20000, 50000]) {
          if (f > fr[fr.length - 1]) continue;
          const u = Math.log(f / fr[0]) / Math.log(fr[fr.length - 1] / fr[0]);
          ctx.fillText(f >= 1000 ? f / 1000 + 'k' : String(f), 6, H - u * H + 4);
        }
      }
    };
    raf = requestAnimationFrame(frame);
    return () => { cancelAnimationFrame(raf); off(); };
  }, [store, floor, top]);
  return (
    <div className="chart" style={{ height }}>
      <canvas ref={ref} className="layer" />
      <canvas ref={lab} className="layer" />
    </div>
  );
}
