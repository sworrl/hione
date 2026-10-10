import { useEffect, useRef } from 'react';
import { LineRenderer, drawAxes, fit, rgba, type Rect, type Theme, type View } from './gl';

export interface Series { key: string; x: ArrayLike<number>; y: ArrayLike<number>; color: string; width?: number }

interface Props {
  view: View;
  theme: Theme;
  unit?: string;
  ystep?: number;
  bands?: boolean;
  height: number;
  /** Static series. */
  series?: Series[];
  /** Live source: called every animation frame; return null when nothing changed. */
  live?: () => Series[] | null;
  xfmt?: (x: number) => string;
  onHover?: (x: number | null) => void;
}

const PAD = { left: 48, right: 12, top: 12, bottom: 26 };

/**
 * A line chart drawn with WebGL2: a 2D canvas underneath holds the grid and labels (redrawn only
 * when the size or view changes), the GL canvas on top holds the traces (redrawn when the data
 * changes, at the display's frame rate for live data).
 */
export function LineChart(p: Props) {
  const wrap = useRef<HTMLDivElement>(null);
  const axes = useRef<HTMLCanvasElement>(null);
  const glc = useRef<HTMLCanvasElement>(null);
  const props = useRef(p);
  props.current = p;
  const dirty = useRef(true);
  useEffect(() => { dirty.current = true; }, [p.view, p.theme, p.series, p.unit, p.bands]);

  useEffect(() => {
    const c = glc.current!, a = axes.current!;
    const gl = c.getContext('webgl2', { antialias: true, premultipliedAlpha: false, alpha: true });
    if (!gl) return;
    const lines = new LineRenderer(gl);
    const ctx = a.getContext('2d')!;
    let raf = 0, lastKey = '';
    let buf = new Float32Array(4096);
    let liveSeries: Series[] | null = null;
    const frame = () => {
      raf = requestAnimationFrame(frame);
      const P = props.current;
      const [w, h, dpr] = fit(c);
      fit(a);
      const cw = w / dpr, ch = h / dpr;
      const top = PAD.top + (P.bands ? 18 : 0);
      const rect: Rect = { left: PAD.left, top, width: cw - PAD.left - PAD.right, height: ch - top - PAD.bottom };
      const key = `${w}x${h}|${P.view.xmin},${P.view.xmax},${P.view.ymin},${P.view.ymax}|${P.theme.name}|${P.unit}|${P.bands}`;
      if (key !== lastKey) {
        lastKey = key;
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
        drawAxes(ctx, cw, ch, rect, P.view, { unit: P.unit, ystep: P.ystep, theme: P.theme, xfmt: P.xfmt, bands: P.bands });
        dirty.current = true;
      }
      if (P.live) {
        const s = P.live();
        if (s) { liveSeries = s; dirty.current = true; }
      }
      if (!dirty.current) return;
      dirty.current = false;
      gl.viewport(0, 0, w, h);
      gl.clearColor(0, 0, 0, 0);
      gl.clear(gl.COLOR_BUFFER_BIT);
      gl.enable(gl.BLEND);
      gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
      gl.enable(gl.SCISSOR_TEST);
      gl.scissor(Math.round(rect.left * dpr), Math.round((ch - rect.top - rect.height) * dpr), Math.round(rect.width * dpr), Math.round(rect.height * dpr));
      const all = [...(P.series || []), ...(P.live ? liveSeries || [] : [])];
      for (const s of all) {
        const n = Math.min(s.x.length, s.y.length);
        if (buf.length < n * 2) buf = new Float32Array(n * 4);
        let k = 0;
        for (let i = 0; i < n; i++) {
          const y = s.y[i];
          if (!isFinite(y) || y < -900) continue;
          buf[k++] = s.x[i]; buf[k++] = Math.max(P.view.ymin - 50, Math.min(P.view.ymax + 50, y));
        }
        lines.draw(buf, k / 2, P.view, rect, dpr, rgba(s.color), s.width || 1.8);
      }
      gl.disable(gl.SCISSOR_TEST);
    };
    raf = requestAnimationFrame(frame);
    const onMove = (e: MouseEvent) => {
      const P = props.current;
      if (!P.onHover) return;
      const r = c.getBoundingClientRect();
      const x = e.clientX - r.left - PAD.left;
      const wpx = r.width - PAD.left - PAD.right;
      if (x < 0 || x > wpx) { P.onHover(null); return; }
      const u = x / wpx;
      const v = P.view;
      P.onHover(v.logx ? v.xmin * Math.pow(v.xmax / v.xmin, u) : v.xmin + u * (v.xmax - v.xmin));
    };
    const onLeave = () => props.current.onHover?.(null);
    c.addEventListener('mousemove', onMove);
    c.addEventListener('mouseleave', onLeave);
    return () => { cancelAnimationFrame(raf); c.removeEventListener('mousemove', onMove); c.removeEventListener('mouseleave', onLeave); };
  }, []);

  return (
    <div ref={wrap} className="chart" style={{ height: p.height }}>
      <canvas ref={axes} className="layer" />
      <canvas ref={glc} className="layer" />
    </div>
  );
}
