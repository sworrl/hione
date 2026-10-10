import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Api, LiveStore, decodeFrame, isLocal, loadConn, playDemo, saveConn, type Conn } from './api';
import { DARK, LIGHT, freqLabel, type Theme, type View } from './gl/gl';
import { LineChart, type Series } from './gl/LineChart';
import { Meters } from './gl/Meters';
import { Spectrogram } from './gl/Spectrogram';
import { FILTER_LABEL, type ChartInfo, type LogEvent, type Progress, type Run, type State, type Step } from './types';

const fmt = (v: number | undefined | null, d = 1) => (v === undefined || v === null || !isFinite(v) || v < -900 ? '-' : v.toFixed(d));

function useTheme(): [Theme, () => void] {
  const [name, setName] = useState<string>(() => { try { return localStorage.getItem('hione-theme') || 'dark'; } catch { return 'dark'; } });
  useEffect(() => {
    document.documentElement.dataset.theme = name;
    try { localStorage.setItem('hione-theme', name); } catch { /* ignore */ }
  }, [name]);
  return [name === 'light' ? LIGHT : DARK, () => setName(n => (n === 'light' ? 'dark' : 'light'))];
}

export function App() {
  const [theme, toggleTheme] = useTheme();
  const [conn, setConn] = useState<Conn>(loadConn);
  const api = useMemo(() => new Api(conn), [conn]);
  const store = useMemo(() => new LiveStore(), []);
  const [state, setState] = useState<State | null>(null);
  const [link, setLink] = useState<'connecting' | 'up' | 'down' | 'demo'>('connecting');
  const [linkErr, setLinkErr] = useState('');
  const [logs, setLogs] = useState<LogEvent[]>([]);
  const [progress, setProgress] = useState<Progress | null>(null);
  const [runId, setRunId] = useState<string | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [showConn, setShowConn] = useState(!isLocal() && !conn.demo);
  const [freqTick, setFreqTick] = useState(0);
  const addLog = useCallback((l: LogEvent) => setLogs(ls => [...ls.slice(-400), l]), []);

  // Demo mode: a recorded session and a finished run, bundled with the page.
  useEffect(() => {
    if (!conn.demo) return;
    setLink('demo');
    const ac = new AbortController();
    playDemo(store, './demo/live.bin', ac.signal).then(() => setFreqTick(t => t + 1)).catch(e => setLinkErr(String(e)));
    setTimeout(() => setFreqTick(t => t + 1), 500);
    fetch('./demo/run.json').then(r => r.json()).then((r: Run) => { setRun(r); setRunId(r.id); }).catch(() => {});
    setState(null);
    setLogs([{ type: 'log', time: new Date().toISOString(), msg: 'Demo mode: a recorded session from a HiBy M500 into an ALC1220 line input. Nothing here touches hardware.' }]);
    return () => ac.abort();
  }, [conn.demo, store]);

  // Live link.
  useEffect(() => {
    if (conn.demo) return;
    let ws: WebSocket | null = null;
    let closed = false;
    let retry = 0;
    const open = () => {
      setLink('connecting');
      ws = new WebSocket(api.wsURL());
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => { setLink('up'); setLinkErr(''); retry = 0; };
      ws.onmessage = ev => {
        if (typeof ev.data !== 'string') {
          const f = decodeFrame(ev.data as ArrayBuffer);
          if (f) store.push(f);
          return;
        }
        const m = JSON.parse(ev.data);
        if (m.type === 'hello') { store.freqs = new Float32Array(m.freqs); store.rate = m.rate; setFreqTick(t => t + 1); return; }
        if (m.type === 'log') addLog(m);
        if (m.type === 'progress') setProgress(m.progress);
        if (m.type === 'step' && (m.step.status === 'done' || m.step.status === 'failed')) setRunId(id => { refreshRun(m.step.run_id); return id || m.step.run_id; });
        if (m.type === 'run') { setRunId(m.run.id); refreshRun(m.run.id); refreshState(); if (m.run.status !== 'running') setProgress(null); }
      };
      ws.onclose = () => {
        setLink('down');
        if (!closed) setTimeout(open, Math.min(5000, 500 * 2 ** retry++));
      };
      ws.onerror = () => setLinkErr('No connection to the Hione backend at ' + (conn.base || location.origin) + '.');
    };
    open();
    return () => { closed = true; ws?.close(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, conn.demo]);

  const refreshState = useCallback(async () => {
    if (conn.demo) return;
    try {
      const s = await api.state();
      setState(s);
      setRunId(id => id || (s.runs[0]?.id ?? null));
    } catch (e) { setLinkErr(String((e as Error).message)); }
  }, [api, conn.demo]);

  const refreshRun = useCallback(async (id: string) => {
    if (conn.demo) return;
    try { setRun(await api.run(id)); } catch { /* not there yet */ }
  }, [api, conn.demo]);

  useEffect(() => {
    if (conn.demo) return;
    refreshState();
    api.state().then(s => setLogs(s.logs || [])).catch(() => {});
    const t = setInterval(refreshState, 4000);
    return () => clearInterval(t);
  }, [refreshState, conn.demo, api]);
  useEffect(() => { if (runId) refreshRun(runId); }, [runId, refreshRun]);

  const busy = !!state?.busy;
  const ds = state?.device_status;

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <img src="./art/avatar.webp" alt="" className="avatar" />
          <div>
            <div className="wordmark">Hione</div>
            <div className="sub">audio measurement</div>
          </div>
        </div>
        <div className="chips">
          <Chip label="link" value={link === 'up' ? 'live' : link} tone={link === 'up' || link === 'demo' ? 'ok' : link === 'down' ? 'bad' : undefined} title={linkErr} />
          {state && <Chip label="capture" value={`${state.capture} · ${state.cap_rate / 1000} kHz ${state.cap_format}`} />}
          {ds ? <>
            <Chip label="device" value={ds.ok ? 'HiBy M500 / MikuOS' : 'adb lost'} tone={ds.ok ? undefined : 'bad'} title={ds.ok ? ds.target : ds.error} />
            <Chip label="DAC" value={ds.dac_label || '-'} title={ds.sources?.dac} />
            <Chip label="jack" value={ds.wired_output === 'none' ? 'nothing plugged' : ds.wired_output || '-'} tone={ds.wired_output === 'none' ? 'bad' : undefined} title={ds.sources?.wired} />
            <Chip label="volume" value={ds.volume || '-'} title={ds.sources?.volume} />
          </> : state && <Chip label="device" value={state.device} />}
          {state && <Chip label="test level" value={`${state.level_dbfs} dBFS`} />}
          {busy && <Chip label="" value="measuring" tone="warn" />}
        </div>
        <div className="actions">
          <button className="ghost" onClick={() => setShowConn(s => !s)}>{conn.demo ? 'Demo' : isLocal() ? 'Pairing' : 'Connection'}</button>
          <button className="ghost" onClick={toggleTheme}>{theme.name === 'dark' ? 'Light' : 'Dark'}</button>
        </div>
      </header>

      {showConn && <ConnPanel conn={conn} api={api} onChange={c => { saveConn(c); setConn(c); }} />}

      <main className="grid">
        <section className="card span5">
          <h2>Input level</h2>
          <Meters store={store} theme={theme} />
          {run?.level && <p className={run.level.pass ? 'pass' : 'fail'}>{run.level.pass ? 'Level check passed. ' : 'Level check did not pass. '}<span className="muted">{run.level.message.replace(/^Level OK: /, '')}</span></p>}
        </section>
        <section className="card span7">
          <RunPanel api={api} busy={busy} disabled={conn.demo || link !== 'up'} progress={progress} onStarted={id => { setRunId(id); refreshState(); }} />
        </section>

        <section className="card span6">
          <h2>Live spectrum</h2>
          <LiveSpectrum store={store} theme={theme} tick={freqTick} />
        </section>
        <section className="card span6 art-bg">
          <h2>Spectrogram</h2>
          <Spectrogram store={store} height={260} />
        </section>

        <Results run={run} theme={theme} api={api} demo={conn.demo} />

        <section className="card span7">
          <ResultTable run={run} theme={theme} />
        </section>
        <section className="card span5">
          <Export api={api} run={run} demo={conn.demo} />
          <h2 style={{ marginTop: 18 }}>Log</h2>
          <LogView logs={logs} />
        </section>
      </main>
      <footer className="foot">
        <span>Hione {conn.demo ? '· demo session' : ''}</span>
        <span className="muted">Every number goes through the capture card. Its own noise, distortion and filter are part of each result.</span>
      </footer>
    </div>
  );
}

function Chip({ label, value, tone, title }: { label: string; value: string; tone?: 'ok' | 'bad' | 'warn'; title?: string }) {
  return <span className={'chip ' + (tone || '')} title={title}>{label && <i>{label}</i>}<b>{value}</b></span>;
}

function LiveSpectrum({ store, theme, tick }: { store: LiveStore; theme: Theme; tick: number }) {
  const seen = useRef(-1);
  const rate = store.rate || 192000;
  const view = useMemo<View>(() => ({ xmin: 10, xmax: rate / 2, ymin: -160, ymax: 0, logx: true }), [rate, tick]);
  const live = useCallback((): Series[] | null => {
    if (!store.frame || store.seq === seen.current || store.freqs.length === 0) return null;
    seen.current = store.seq;
    const f = store.frame;
    return [
      { key: 'L', x: store.freqs, y: f.specL, color: theme.series[0], width: 1.4 },
      { key: 'R', x: store.freqs, y: f.specR, color: theme.series[1], width: 1.4 },
    ];
  }, [store, theme]);
  return <LineChart view={view} theme={theme} unit="dBFS" ystep={20} height={260} live={live} />;
}

function RunPanel({ api, busy, disabled, progress, onStarted }: { api: Api; busy: boolean; disabled: boolean; progress: Progress | null; onStarted: (id: string) => void }) {
  const [rates, setRates] = useState<number[]>([44100, 96000]);
  const [gains, setGains] = useState<string[]>(['low', 'high']);
  const [powerDre, setPowerDre] = useState(true);
  const [full, setFull] = useState(false);
  const [err, setErr] = useState('');
  const toggle = <T,>(arr: T[], v: T) => (arr.includes(v) ? arr.filter(x => x !== v) : [...arr, v]);
  const go = async (fn: () => Promise<{ id: string }>) => {
    setErr('');
    try { const r = await fn(); onStarted(r.id); } catch (e) { setErr((e as Error).message); }
  };
  const plan = full ? 'full' : 'quick';
  return (
    <>
      <h2>Run</h2>
      <div className="row">
        <button disabled={disabled || busy} onClick={() => go(() => api.levelCheck())}>Level check</button>
        <button className="primary" disabled={disabled || busy} onClick={() => go(() => api.start({ kind: 'matrix', plan, rates, gains, power_dre: powerDre, jack: '3.5mm' }))}>Run DAC matrix</button>
        <button disabled={disabled || busy} onClick={() => go(() => api.start({ kind: 'measure', plan, rates: rates.slice(0, 1) }))}>Measure current setting</button>
        <button disabled={disabled || !busy} onClick={() => api.stop()}>Stop</button>
      </div>
      <div className="opts">
        <span>Rates</span>
        {[44100, 48000, 96000, 192000].map(r => <label key={r}><input type="checkbox" checked={rates.includes(r)} onChange={() => setRates(toggle(rates, r).sort((a, b) => a - b))} /> {r / 1000} kHz</label>)}
        <span>Gain</span>
        {['low', 'middle', 'high'].map(g => <label key={g}><input type="checkbox" checked={gains.includes(g)} onChange={() => setGains(toggle(gains, g))} /> {g}</label>)}
        <label><input type="checkbox" checked={powerDre} onChange={e => setPowerDre(e.target.checked)} /> high power and DRE runs</label>
        <label><input type="checkbox" checked={full} onChange={e => setFull(e.target.checked)} /> full plan (stepped sines, IMD)</label>
      </div>
      <div className="progress"><div style={{ width: `${((progress?.fraction ?? 0) * 100).toFixed(1)}%` }} /></div>
      <div className="muted">{progress ? `${progress.label}: ${progress.phase}${progress.segment ? ` (${progress.segment})` : ''}` : busy ? 'Working.' : 'Nothing running.'}</div>
      {err && <div className="fail">{err}</div>}
    </>
  );
}

function stepKey(s: Step) { return s.label; }

function Results({ run, theme, api, demo }: { run: Run | null; theme: Theme; api: Api; demo: boolean }) {
  const [zoom, setZoom] = useState<'full' | 'top' | 'wide'>('full');
  const [hidden, setHidden] = useState<Record<string, boolean>>({});
  const [group, setGroup] = useState<string>('all');
  const [hover, setHover] = useState<number | null>(null);
  const steps = (run?.steps || []).filter(s => s.result?.found);
  const groups = useMemo(() => {
    const g = new Map<string, string>();
    for (const s of steps) {
      const k = s.group === 'filters' ? `${s.play_rate}-${s.dac.gain}` : s.group;
      g.set(k, s.group === 'filters' ? `Filters, ${s.play_rate / 1000} kHz, gain ${s.dac.gain}` : s.group === 'power' ? 'High power and DRE' : 'Single');
    }
    return [...g.entries()];
  }, [run]);
  // Start on one comparable group (the filters at the first rate, gain high) so the colors never repeat.
  useEffect(() => {
    if (!groups.length) return;
    const pick = groups.find(([k]) => k.endsWith('-high')) || groups[0];
    setGroup(g => (g === 'all' || !groups.some(([k]) => k === g) ? pick[0] : g));
  }, [run?.id, groups.length]);
  const inGroup = (s: Step) => group === 'all' || (s.group === 'filters' ? `${s.play_rate}-${s.dac.gain}` : s.group) === group;
  const shown = steps.filter(inGroup);
  const color = (i: number) => theme.series[i % theme.series.length];
  const maxF = Math.max(20000, ...shown.map(s => { const hz = s.result!.channels[0].fr.hz; return hz[hz.length - 1]; }));
  const view: View = zoom === 'top' ? { xmin: 10000, xmax: maxF, ymin: -6, ymax: 1, logx: true }
    : zoom === 'wide' ? { xmin: 10, xmax: maxF, ymin: -12, ymax: 2, logx: true }
      : { xmin: 20, xmax: 20000, ymin: -3, ymax: 1, logx: true };
  const frSeries: Series[] = shown.map((s, i) => ({ key: stepKey(s), x: s.result!.channels[0].fr.hz, y: s.result!.channels[0].fr.db, color: color(i) })).filter(s => !hidden[s.key]);
  const irSeries: Series[] = shown.map((s, i) => {
    const ch = s.result!.channels[0];
    const ir = ch.ir || [];
    return { key: stepKey(s), x: ir.map((_, k) => ch.ir_start_ms + k / s.result!.cap_rate * 1000), y: ir, color: color(i) };
  }).filter(s => !hidden[s.key] && s.x.length);
  const label = (s: Step) => (s.group === 'filters' && group !== 'all' ? FILTER_LABEL[s.dac.filter] || s.label : s.label);
  const hoverVals = hover !== null ? shown.filter(s => !hidden[stepKey(s)]).map((s, i) => {
    const fr = s.result!.channels[0].fr;
    let k = 0; while (k < fr.hz.length - 1 && fr.hz[k] < hover) k++;
    return { name: label(s), v: fr.db[k], c: color(shown.indexOf(s)), i };
  }) : [];
  return (
    <>
      <section className="card span8">
        <div className="h2row">
          <h2>Frequency response</h2>
          <div className="seg">
            {(['full', 'top', 'wide'] as const).map(z => <button key={z} className={zoom === z ? 'on' : ''} onClick={() => setZoom(z)}>{z === 'full' ? '20 Hz to 20 kHz' : z === 'top' ? 'Top octave' : 'To the sweep end'}</button>)}
          </div>
        </div>
        <div className="row small">
          <select value={group} onChange={e => setGroup(e.target.value)}>
            <option value="all">All modes</option>
            {groups.map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
          {run && <span className="muted">Run {run.id} · {run.status}{demo ? ' · recorded' : ''}</span>}
        </div>
        {shown.length === 0 ? <Empty /> : <LineChart view={view} theme={theme} unit="dB re 1 kHz" bands={zoom === 'full'} height={340} series={frSeries} onHover={setHover} />}
        <div className="hover">{hover !== null && <><b>{freqLabel(hover)} Hz</b>{hoverVals.map(h => <span key={h.name}><i style={{ background: h.c }} />{h.name} {fmt(h.v, 2)} dB</span>)}</>}</div>
        <div className="toggles">
          {shown.map((s, i) => (
            <label key={stepKey(s)}><input type="checkbox" checked={!hidden[stepKey(s)]} onChange={e => setHidden({ ...hidden, [stepKey(s)]: !e.target.checked })} /><i style={{ background: color(i) }} />{label(s)}</label>
          ))}
        </div>
      </section>
      <section className="card span4">
        <h2>Impulse response</h2>
        {irSeries.length === 0 ? <Empty /> : <LineChart view={{ xmin: -1, xmax: 3, ymin: -0.4, ymax: 1.1, logx: false }} theme={theme} unit="normalized" height={340} series={irSeries} xfmt={x => x + ' ms'} />}
        <p className="muted small">Average of 32 single-sample impulses. Ringing left of 0 ms is the filter's pre-ringing.</p>
        {api && null}
      </section>
    </>
  );
}

function Empty() {
  return <div className="empty"><img src="./art/pose_headphones.webp" alt="" /><p>No results yet. Run the level check, then the matrix.</p></div>;
}

function ResultTable({ run, theme }: { run: Run | null; theme: Theme }) {
  const steps = run?.steps || [];
  const vals = steps.map(s => s.result?.channels?.[0]?.tone?.thdn_db).filter((v): v is number => v !== undefined && v > -900);
  const lo = Math.min(-120, ...vals), hi = Math.max(-40, ...vals);
  return (
    <>
      <h2>Results per mode</h2>
      <div className="tablewrap">
        <table>
          <thead><tr><th>Mode</th><th>Level dBFS</th><th>THD+N dB</th><th></th><th>Noise dBFS(A)</th><th>SNR dB(A)</th><th>DR dB(A)</th><th>Xtalk dB</th><th>Bal dB</th></tr></thead>
          <tbody>
            {steps.map(s => {
              const r = s.result;
              if (!r || !r.found) return <tr key={s.index}><td>{s.label}</td><td colSpan={8} className="muted left">{s.status}{s.error ? `: ${s.error}` : ''}</td></tr>;
              const L = r.channels[0];
              const t = L.tone;
              const w = t ? Math.max(2, (t.thdn_db - lo) / (hi - lo) * 90) : 0;
              return (
                <tr key={s.index}>
                  <td>{s.label}</td><td>{fmt(t?.level_dbfs)}</td><td>{fmt(t?.thdn_db)}</td>
                  <td className="left"><span className="bar" style={{ width: w, background: theme.ice }} /></td>
                  <td>{fmt(L.noise?.dbfs_a)}</td><td>{fmt(L.snr_a_db)}</td><td>{fmt(L.dr_a_db)}</td><td>{fmt(L.xtalk_db)}</td><td>{fmt(r.balance_db, 2)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

function Export({ api, run, demo }: { api: Api; run: Run | null; demo: boolean }) {
  const [charts, setCharts] = useState<ChartInfo[]>([]);
  const [chart, setChart] = useState('');
  const [fmtSel, setFmt] = useState('png2');
  const [th, setTh] = useState('light');
  const [mark, setMark] = useState(true);
  const [stamp, setStamp] = useState(true);
  const [clear, setClear] = useState(false);
  useEffect(() => {
    if (!run || demo) return;
    api.charts(run.id).then(cs => { setCharts(cs); setChart(c => c || cs[0]?.id || ''); }).catch(() => setCharts([]));
  }, [run?.id, run?.status, api, demo]);
  if (demo) return <><h2>Export</h2><p className="muted">Exports need a Hione backend. The demo shows a recorded run.</p></>;
  if (!run) return <><h2>Export</h2><p className="muted">Nothing to export yet.</p></>;
  const q = `theme=${th}&mark=${mark ? 1 : 0}&stamp=${stamp ? 1 : 0}${clear ? '&bg=transparent' : ''}`;
  const chartURL = fmtSel === 'svg' ? `/api/runs/${run.id}/charts/${chart}.svg?${q}` : `/api/runs/${run.id}/charts/${chart}.png?scale=${fmtSel.slice(3)}&${q}`;
  return (
    <>
      <h2>Export</h2>
      <div className="row small">
        <select value={chart} onChange={e => setChart(e.target.value)}>{charts.map(c => <option key={c.id} value={c.id}>{c.title}</option>)}</select>
        <select value={fmtSel} onChange={e => setFmt(e.target.value)}><option value="svg">SVG</option><option value="png1">PNG 1x</option><option value="png2">PNG 2x</option><option value="png4">PNG 4x</option></select>
        <select value={th} onChange={e => setTh(e.target.value)}><option value="light">Light</option><option value="dark">Dark</option></select>
      </div>
      <div className="row small">
        <label><input type="checkbox" checked={mark} onChange={e => setMark(e.target.checked)} /> Hione mark</label>
        <label><input type="checkbox" checked={stamp} onChange={e => setStamp(e.target.checked)} /> settings line</label>
        <label><input type="checkbox" checked={clear} onChange={e => setClear(e.target.checked)} /> transparent</label>
      </div>
      <div className="row">
        <a className="btn" href={api.link(chartURL)} download>Save chart</a>
        <a className="btn" href={api.link(`/api/runs/${run.id}/export?format=pdf&theme=${th}`)} download>Report PDF</a>
        <a className="btn" href={api.link(`/api/runs/${run.id}/export?format=zip&theme=${th}&mark=${mark ? 1 : 0}`)} download>Everything (zip)</a>
        <a className="btn ghost" href={api.link(`/api/report/${run.id}`)} target="_blank" rel="noreferrer">HTML report</a>
      </div>
    </>
  );
}

function LogView({ logs }: { logs: LogEvent[] }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { const el = ref.current; if (el) el.scrollTop = el.scrollHeight; }, [logs]);
  return <div className="log" ref={ref}>{logs.map((l, i) => <div key={i}><span>{new Date(l.time).toTimeString().slice(0, 8)}</span> {l.msg}</div>)}</div>;
}

function ConnPanel({ conn, api, onChange }: { conn: Conn; api: Api; onChange: (c: Conn) => void }) {
  const [base, setBase] = useState(conn.base);
  const [token, setToken] = useState(conn.token);
  const [pair, setPair] = useState<{ token: string; allow_origins: string[] } | null>(null);
  const local = isLocal();
  useEffect(() => { if (local && !conn.demo) api.pairing().then(setPair).catch(() => {}); }, [api, local, conn.demo]);
  return (
    <section className="connpanel">
      {local ? (
        <div>
          <h2>Pairing a hosted page</h2>
          <p className="muted">A copy of this page hosted on a website can drive this backend once you start it with <code>--allow-origin https://your.site</code> and paste this token into that page. This page itself needs no token.</p>
          {pair && <p>Token <code className="token">{pair.token}</code> <button className="ghost" onClick={() => api.resetPairing().then(r => setPair(p => p && { ...p, token: r.token }))}>New token</button></p>}
          {pair && <p className="muted">Allowed sites: {pair.allow_origins.length ? pair.allow_origins.join(', ') : 'none'}</p>}
          <label className="row small"><input type="checkbox" checked={conn.demo} onChange={e => onChange({ ...conn, demo: e.target.checked })} /> Demo mode (play the recorded session instead)</label>
        </div>
      ) : (
        <div>
          <h2>Connect to your Hione backend</h2>
          <p className="muted">Run <code>hione serve --allow-origin {location.origin}</code> on your machine, then paste the pairing token it prints. Without a backend, the demo plays a recorded session.</p>
          <div className="row small">
            <input value={base} onChange={e => setBase(e.target.value)} placeholder="http://127.0.0.1:8484" />
            <input value={token} onChange={e => setToken(e.target.value)} placeholder="pairing token" />
            <button className="primary" onClick={() => onChange({ base, token, demo: false })}>Connect</button>
            <button onClick={() => onChange({ ...conn, demo: true })}>Demo</button>
          </div>
        </div>
      )}
    </section>
  );
}
