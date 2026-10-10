// Small WebGL2 helpers: shader compile, canvas sizing, and an instanced thick-line renderer that
// maps data to pixels in the vertex shader (log or linear x), so a whole chart redraws in one or
// two draw calls per series.

export function compile(gl: WebGL2RenderingContext, vs: string, fs: string): WebGLProgram {
  const mk = (type: number, src: string) => {
    const s = gl.createShader(type)!;
    gl.shaderSource(s, src);
    gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) || 'shader');
    return s;
  };
  const p = gl.createProgram()!;
  gl.attachShader(p, mk(gl.VERTEX_SHADER, vs));
  gl.attachShader(p, mk(gl.FRAGMENT_SHADER, fs));
  gl.linkProgram(p);
  if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p) || 'link');
  return p;
}

/** Match the canvas backing store to its CSS size. Returns [width, height, dpr]. */
export function fit(c: HTMLCanvasElement): [number, number, number] {
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  const w = Math.max(1, Math.round(c.clientWidth * dpr));
  const h = Math.max(1, Math.round(c.clientHeight * dpr));
  if (c.width !== w || c.height !== h) { c.width = w; c.height = h; }
  return [w, h, dpr];
}

export function rgba(hex: string, a = 1): [number, number, number, number] {
  const h = hex.replace('#', '').trim();
  const n = parseInt(h.length === 3 ? h.split('').map(x => x + x).join('') : h, 16);
  return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255, a];
}

export interface View { xmin: number; xmax: number; ymin: number; ymax: number; logx: boolean }
export interface Rect { left: number; top: number; width: number; height: number } // CSS px

const LINE_VS = `#version 300 es
precision highp float;
in vec2 a_p0;
in vec2 a_p1;
uniform vec4 u_view;      // xmin, xmax, ymin, ymax
uniform bool u_logx;
uniform vec4 u_rect;      // left, top, width, height in device px
uniform vec2 u_size;      // canvas size in device px
uniform float u_width;    // line width in device px
vec2 toPx(vec2 p) {
  float u = u_logx ? (log(max(p.x, 1e-9)) - log(u_view.x)) / (log(u_view.y) - log(u_view.x))
                   : (p.x - u_view.x) / (u_view.y - u_view.x);
  float v = (p.y - u_view.z) / (u_view.w - u_view.z);
  return vec2(u_rect.x + u * u_rect.z, u_rect.y + (1.0 - v) * u_rect.w);
}
void main() {
  vec2 a = toPx(a_p0), b = toPx(a_p1);
  vec2 d = b - a;
  float len = length(d);
  vec2 dir = len > 0.0 ? d / len : vec2(1.0, 0.0);
  vec2 nrm = vec2(-dir.y, dir.x);
  int id = gl_VertexID;
  vec2 base = (id == 0 || id == 1) ? a : b;
  float side = (id == 0 || id == 2) ? -1.0 : 1.0;
  float ext = (id == 0 || id == 1) ? -1.0 : 1.0;
  vec2 px = base + nrm * side * u_width * 0.5 + dir * ext * u_width * 0.5;
  vec2 clip = px / u_size * 2.0 - 1.0;
  gl_Position = vec4(clip.x, -clip.y, 0.0, 1.0);
}`;

const LINE_FS = `#version 300 es
precision mediump float;
uniform vec4 u_color;
out vec4 o;
void main() { o = u_color; }`;

/** Draws polylines as instanced quads. One buffer per series, updated in place. */
export class LineRenderer {
  prog: WebGLProgram;
  vao: WebGLVertexArrayObject;
  buf: WebGLBuffer;
  cap = 0;
  u: Record<string, WebGLUniformLocation | null> = {};
  constructor(private gl: WebGL2RenderingContext) {
    this.prog = compile(gl, LINE_VS, LINE_FS);
    for (const n of ['u_view', 'u_logx', 'u_rect', 'u_size', 'u_width', 'u_color']) this.u[n] = gl.getUniformLocation(this.prog, n);
    this.vao = gl.createVertexArray()!;
    this.buf = gl.createBuffer()!;
  }

  /** xy holds x0,y0,x1,y1,... in data units. */
  draw(xy: Float32Array, count: number, view: View, rect: Rect, dpr: number, color: [number, number, number, number], width: number) {
    const gl = this.gl;
    if (count < 2) return;
    gl.useProgram(this.prog);
    gl.bindVertexArray(this.vao);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
    const bytes = count * 8;
    if (bytes > this.cap) { gl.bufferData(gl.ARRAY_BUFFER, Math.max(bytes, this.cap * 2), gl.DYNAMIC_DRAW); this.cap = Math.max(bytes, this.cap * 2); }
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, xy, 0, count * 2);
    const l0 = gl.getAttribLocation(this.prog, 'a_p0');
    const l1 = gl.getAttribLocation(this.prog, 'a_p1');
    gl.enableVertexAttribArray(l0);
    gl.vertexAttribPointer(l0, 2, gl.FLOAT, false, 8, 0);
    gl.vertexAttribDivisor(l0, 1);
    gl.enableVertexAttribArray(l1);
    gl.vertexAttribPointer(l1, 2, gl.FLOAT, false, 8, 8);
    gl.vertexAttribDivisor(l1, 1);
    gl.uniform4f(this.u.u_view, view.xmin, view.xmax, view.ymin, view.ymax);
    gl.uniform1i(this.u.u_logx, view.logx ? 1 : 0);
    gl.uniform4f(this.u.u_rect, rect.left * dpr, rect.top * dpr, rect.width * dpr, rect.height * dpr);
    gl.uniform2f(this.u.u_size, gl.drawingBufferWidth, gl.drawingBufferHeight);
    gl.uniform1f(this.u.u_width, width * dpr);
    gl.uniform4f(this.u.u_color, color[0], color[1], color[2], color[3]);
    gl.drawArraysInstanced(gl.TRIANGLE_STRIP, 0, 4, count - 1);
  }
}

/** Grid, ticks and labels on a 2D canvas layered under the GL canvas. */
export function drawAxes(ctx: CanvasRenderingContext2D, w: number, h: number, rect: Rect, view: View,
  opts: { unit?: string; ystep?: number; theme: Theme; xfmt?: (x: number) => string; bands?: boolean }) {
  const t = opts.theme;
  ctx.clearRect(0, 0, w, h);
  ctx.fillStyle = t.plot;
  ctx.fillRect(rect.left, rect.top, rect.width, rect.height);
  const xm = (x: number) => view.logx
    ? rect.left + rect.width * Math.log(x / view.xmin) / Math.log(view.xmax / view.xmin)
    : rect.left + rect.width * (x - view.xmin) / (view.xmax - view.xmin);
  const ym = (y: number) => rect.top + rect.height * (view.ymax - y) / (view.ymax - view.ymin);
  ctx.font = '11px Inter, system-ui, sans-serif';
  ctx.lineWidth = 1;
  const ys = opts.ystep || niceStep((view.ymax - view.ymin) / 6);
  ctx.textAlign = 'right';
  ctx.fillStyle = t.ink3;
  for (let y = Math.ceil(view.ymin / ys) * ys; y <= view.ymax + 1e-9; y += ys) {
    ctx.strokeStyle = t.grid;
    ctx.beginPath(); ctx.moveTo(rect.left, Math.round(ym(y)) + 0.5); ctx.lineTo(rect.left + rect.width, Math.round(ym(y)) + 0.5); ctx.stroke();
    ctx.fillText(String(+y.toFixed(2)), rect.left - 6, ym(y) + 4);
  }
  ctx.textAlign = 'center';
  const fmt = opts.xfmt || (view.logx ? freqLabel : (x: number) => String(+x.toFixed(3)));
  if (view.logx) {
    for (let d = Math.pow(10, Math.floor(Math.log10(view.xmin))); d <= view.xmax; d *= 10) {
      for (let m = 1; m <= 9; m++) {
        const f = d * m;
        if (f < view.xmin * 0.999 || f > view.xmax * 1.001) continue;
        const major = m === 1 || m === 2 || m === 5;
        ctx.strokeStyle = major ? t.grid : t.gridMinor;
        const px = Math.round(xm(f)) + 0.5;
        ctx.beginPath(); ctx.moveTo(px, rect.top); ctx.lineTo(px, rect.top + rect.height); ctx.stroke();
        if (major) ctx.fillText(fmt(f), px, rect.top + rect.height + 15);
      }
    }
    if (opts.bands) {
      const bands: [number, number, string][] = [[20, 60, 'Sub bass'], [60, 250, 'Bass'], [250, 500, 'Low mids'], [500, 2000, 'Mids'], [2000, 4000, 'Upper mids'], [4000, 6000, 'Presence'], [6000, 20000, 'Brilliance']];
      ctx.font = '10px Inter, system-ui, sans-serif';
      bands.forEach(([a, b, n], i) => {
        const xa = xm(Math.max(a, view.xmin)), xb = xm(Math.min(b, view.xmax));
        if (xb <= xa) return;
        if (i % 2 === 0) { ctx.fillStyle = t.band; ctx.fillRect(xa, rect.top - 16, xb - xa, 14); }
        if (xb - xa > 44) { ctx.fillStyle = t.ink3; ctx.fillText(n, (xa + xb) / 2, rect.top - 5); }
      });
    }
  } else {
    const xs = niceStep((view.xmax - view.xmin) / 8);
    for (let x = Math.ceil(view.xmin / xs) * xs; x <= view.xmax + 1e-9; x += xs) {
      ctx.strokeStyle = t.grid;
      const px = Math.round(xm(x)) + 0.5;
      ctx.beginPath(); ctx.moveTo(px, rect.top); ctx.lineTo(px, rect.top + rect.height); ctx.stroke();
      ctx.fillText(fmt(x), px, rect.top + rect.height + 15);
    }
  }
  ctx.strokeStyle = t.frame;
  ctx.strokeRect(rect.left + 0.5, rect.top + 0.5, rect.width - 1, rect.height - 1);
  if (opts.unit) { ctx.textAlign = 'left'; ctx.fillStyle = t.ink3; ctx.fillText(opts.unit, 4, Math.max(11, rect.top - (opts.bands ? 20 : 4))); }
}

export function niceStep(raw: number) {
  const p = Math.pow(10, Math.floor(Math.log10(raw)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= raw) return m * p;
  return 10 * p;
}

export function freqLabel(f: number) { return f >= 1000 ? (+(f / 1000).toFixed(1)) + 'k' : String(Math.round(f)); }

export interface Theme {
  name: string; bg: string; surface: string; plot: string; ink: string; ink2: string; ink3: string;
  grid: string; gridMinor: string; band: string; frame: string; series: string[]; amber: string; ice: string;
}

export const DARK: Theme = {
  name: 'dark', bg: '#111216', surface: '#16181d', plot: '#13151a', ink: '#f0efec', ink2: '#c3c2b7', ink3: '#8f8e86',
  grid: '#2a2d34', gridMinor: '#1c1f25', band: '#1a1d23', frame: '#3a3e46',
  series: ['#8ec5ff', '#ffab2e', '#1fbf87', '#e87ba4', '#9085e9', '#e66767', '#c9d94a', '#f0efec'],
  amber: '#ffab2e', ice: '#8ec5ff',
};

export const LIGHT: Theme = {
  name: 'light', bg: '#f4f3f0', surface: '#fcfcfb', plot: '#ffffff', ink: '#0b0b0b', ink2: '#52514e', ink3: '#8a8984',
  grid: '#dcdbd6', gridMinor: '#efeeea', band: '#f3f2ee', frame: '#b9b8b2',
  series: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948'],
  amber: '#c77700', ice: '#2a78d6',
};
