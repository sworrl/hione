import type { LiveFrame, Run, State, ChartInfo } from './types';

/**
 * Where the backend is and how to reach it. Served by the Hione binary itself, the page talks to
 * its own origin and needs no token. Hosted on a website, it talks to a Hione backend on the
 * visitor's machine (http://127.0.0.1:8484 by default) and needs the pairing token that backend
 * shows, plus that backend has to list the site in --allow-origin.
 */
export interface Conn { base: string; token: string; demo: boolean }

const KEY = 'hione-conn';

function servedByBackend(): boolean {
  const h = location.hostname;
  return location.protocol.startsWith('http') && (h === '127.0.0.1' || h === 'localhost' || h === '[::1]');
}

export function loadConn(): Conn {
  let saved: Partial<Conn> = {};
  try { saved = JSON.parse(localStorage.getItem(KEY) || '{}'); } catch { /* private mode */ }
  const local = servedByBackend();
  return {
    base: local ? '' : (saved.base || 'http://127.0.0.1:8484'),
    token: local ? '' : (saved.token || ''),
    demo: saved.demo ?? !local,
  };
}

export function saveConn(c: Conn) {
  try { localStorage.setItem(KEY, JSON.stringify({ base: c.base, token: c.token, demo: c.demo })); } catch { /* ignore */ }
}

export function isLocal() { return servedByBackend(); }

export class Api {
  constructor(public conn: Conn) {}

  url(path: string): string { return this.conn.base + path; }

  /** URL for links and downloads: the token rides along as a query parameter when needed. */
  link(path: string): string {
    if (!this.conn.token) return this.url(path);
    return this.url(path) + (path.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(this.conn.token);
  }

  async req<T>(path: string, init?: RequestInit): Promise<T> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (this.conn.token) headers.Authorization = 'Bearer ' + this.conn.token;
    const r = await fetch(this.url(path), { ...init, headers: { ...headers, ...(init?.headers as Record<string, string> || {}) } });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error((j as { error?: string }).error || r.statusText);
    return j as T;
  }

  state() { return this.req<State>('/api/state'); }
  run(id: string) { return this.req<Run>('/api/runs/' + id); }
  charts(id: string) { return this.req<ChartInfo[]>(`/api/runs/${id}/charts`); }
  start(spec: object) { return this.req<{ id: string }>('/api/runs', { method: 'POST', body: JSON.stringify(spec) }); }
  levelCheck() { return this.req<{ id: string }>('/api/level-check', { method: 'POST' }); }
  stop() { return this.req<{ stopped: boolean }>('/api/stop', { method: 'POST' }); }
  pairing() { return this.req<{ token: string; allow_origins: string[]; loopback: boolean }>('/api/pairing'); }
  resetPairing() { return this.req<{ token: string }>('/api/pairing/reset', { method: 'POST' }); }

  wsURL(): string {
    let b = this.conn.base || location.origin;
    b = b.replace(/^http/, 'ws');
    return b + '/api/ws?fps=30' + (this.conn.token ? '&token=' + encodeURIComponent(this.conn.token) : '');
  }
}

/** Decode one binary live frame (see docs/live-protocol.md). */
export function decodeFrame(buf: ArrayBuffer): LiveFrame | null {
  const v = new DataView(buf);
  if (v.byteLength < 32 || v.getUint8(0) !== 1) return null;
  const flags = v.getUint8(1);
  const n = v.getUint16(2, true);
  const f: LiveFrame = {
    rate: v.getFloat32(4, true),
    time: v.getFloat64(8, true),
    clip: [(flags & 1) !== 0, (flags & 2) !== 0],
    peak: [v.getFloat32(16, true), v.getFloat32(20, true)],
    rms: [v.getFloat32(24, true), v.getFloat32(28, true)],
    specL: new Float32Array(n),
    specR: new Float32Array(n),
  };
  for (let i = 0; i < n; i++) {
    f.specL[i] = v.getFloat32(32 + 4 * i, true);
    f.specR[i] = v.getFloat32(32 + 4 * (n + i), true);
  }
  return f;
}

/** Live data store outside React: the GL views read it every animation frame. */
export class LiveStore {
  freqs = new Float32Array(0);
  rate = 0;
  frame: LiveFrame | null = null;
  seq = 0;
  private subs = new Set<(f: LiveFrame) => void>();
  push(f: LiveFrame) { this.frame = f; this.seq++; this.subs.forEach(s => s(f)); }
  on(fn: (f: LiveFrame) => void) { this.subs.add(fn); return () => { this.subs.delete(fn); }; }
}

/** Plays a recorded live session (demo mode) into a store, looping. */
export async function playDemo(store: LiveStore, url: string, signal: AbortSignal) {
  const r = await fetch(url);
  const buf = await r.arrayBuffer();
  const bytes = new Uint8Array(buf);
  const nl = bytes.indexOf(10);
  const hdr = JSON.parse(new TextDecoder().decode(bytes.slice(0, nl)));
  store.freqs = new Float32Array(hdr.freqs);
  store.rate = hdr.rate;
  const frames: ArrayBuffer[] = [];
  let off = nl + 1;
  const dv = new DataView(buf);
  while (off + 4 <= buf.byteLength) {
    const len = dv.getUint32(off, true);
    frames.push(buf.slice(off + 4, off + 4 + len));
    off += 4 + len;
  }
  const dt = 1000 / (hdr.fps || 20);
  let i = 0;
  while (!signal.aborted && frames.length) {
    const f = decodeFrame(frames[i % frames.length]);
    if (f) { f.time = Date.now(); store.push(f); }
    i++;
    await new Promise(res => setTimeout(res, dt));
  }
}
