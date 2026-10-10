import { useEffect, useRef } from 'react';
import { compile, fit, rgba, type Theme } from './gl';
import type { LiveStore } from '../api';

const VS = `#version 300 es
in vec2 a; uniform vec4 u_r; uniform vec2 u_s;
void main() { vec2 p = u_r.xy + a * u_r.zw; vec2 c = p / u_s * 2.0 - 1.0; gl_Position = vec4(c.x, -c.y, 0.0, 1.0); }`;
const FS = `#version 300 es
precision mediump float; uniform vec4 u_c; out vec4 o; void main() { o = u_c; }`;

const MIN_DB = -120;

/** Peak and RMS meters for both channels, drawn with WebGL at the display rate. */
export function Meters({ store, theme }: { store: LiveStore; theme: Theme }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const valL = useRef<HTMLSpanElement>(null);
  const valR = useRef<HTMLSpanElement>(null);
  const clipL = useRef<HTMLSpanElement>(null);
  const clipR = useRef<HTMLSpanElement>(null);
  const th = useRef(theme);
  th.current = theme;
  useEffect(() => {
    const c = ref.current!;
    const gl = c.getContext('webgl2')!;
    if (!gl) return;
    const prog = compile(gl, VS, FS);
    const vao = gl.createVertexArray()!;
    gl.bindVertexArray(vao);
    const b = gl.createBuffer()!;
    gl.bindBuffer(gl.ARRAY_BUFFER, b);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([0, 0, 1, 0, 0, 1, 1, 1]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.bindAttribLocation(prog, 0, 'a');
    gl.vertexAttribPointer(gl.getAttribLocation(prog, 'a'), 2, gl.FLOAT, false, 0, 0);
    const uR = gl.getUniformLocation(prog, 'u_r'), uS = gl.getUniformLocation(prog, 'u_s'), uC = gl.getUniformLocation(prog, 'u_c');
    // Smoothed display values so the bars glide between 30 Hz frames at 60-144 fps.
    const disp = { rms: [MIN_DB, MIN_DB], peak: [MIN_DB, MIN_DB] };
    let raf = 0, lastText = 0;
    const frac = (db: number) => Math.max(0, Math.min(1, (db - MIN_DB) / -MIN_DB));
    const frame = (t: number) => {
      raf = requestAnimationFrame(frame);
      const f = store.frame;
      const [w, h] = fit(c);
      gl.viewport(0, 0, w, h);
      const T = th.current;
      const bg = rgba(T.surface);
      gl.clearColor(bg[0], bg[1], bg[2], 1);
      gl.clear(gl.COLOR_BUFFER_BIT);
      gl.useProgram(prog);
      gl.bindVertexArray(vao);
      gl.uniform2f(uS, w, h);
      const rect = (x: number, y: number, rw: number, rh: number, col: [number, number, number, number]) => {
        gl.uniform4f(uR, x, y, rw, rh); gl.uniform4f(uC, ...col); gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
      };
      gl.enable(gl.BLEND); gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
      const barH = h * 0.36, gap = h * 0.12;
      for (let ch = 0; ch < 2; ch++) {
        if (f) {
          const k = 0.35;
          disp.rms[ch] += (f.rms[ch] - disp.rms[ch]) * k;
          disp.peak[ch] = f.peak[ch] > disp.peak[ch] ? f.peak[ch] : disp.peak[ch] + (f.peak[ch] - disp.peak[ch]) * 0.08;
        }
        const y = gap / 2 + ch * (barH + gap);
        rect(0, y, w, barH, rgba(T.grid, 0.55));
        rect(w * frac(-6), y, w * (1 - frac(-6)), barH, rgba('#e66767', 0.22));
        const g = rgba(T.ice, 0.85);
        rect(0, y, w * frac(disp.rms[ch]), barH, g);
        rect(Math.max(0, w * frac(disp.peak[ch]) - 3), y, 3, barH, rgba(T.amber));
      }
      if (f && t - lastText > 120) {
        lastText = t;
        if (valL.current) valL.current.textContent = `${f.peak[0].toFixed(1)} pk  ${f.rms[0].toFixed(1)} rms`;
        if (valR.current) valR.current.textContent = `${f.peak[1].toFixed(1)} pk  ${f.rms[1].toFixed(1)} rms`;
        clipL.current?.classList.toggle('on', f.clip[0]);
        clipR.current?.classList.toggle('on', f.clip[1]);
      }
    };
    raf = requestAnimationFrame(frame);
    return () => cancelAnimationFrame(raf);
  }, [store]);
  return (
    <div className="meters">
      <div className="meter-labels"><span>L</span><span>R</span></div>
      <div className="meter-canvas"><canvas ref={ref} /></div>
      <div className="meter-vals"><span ref={valL}>-</span><span ref={valR}>-</span></div>
      <div className="meter-scale"><span>-120</span><span>-90</span><span>-60</span><span>-30</span><span>0 dBFS</span></div>
      <div className="clips"><span ref={clipL} className="clip">CLIP L</span><span ref={clipR} className="clip">CLIP R</span></div>
    </div>
  );
}
