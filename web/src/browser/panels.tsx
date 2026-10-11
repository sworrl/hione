import { useEffect, useState } from 'react';
import { FILTER_LABEL, type Progress, type Run } from '../types';
import { Capture } from './capture';
import { BrowserEngine, TEST_LEVEL } from './engine';
import type { PlanName } from './worker';

const RATES = [44100, 48000, 96000, 192000];

/** Choosing the audio input, and the plain statement of what happens where. */
export function InputPanel({ engine, demo, onDemo, onOpened }: { engine: BrowserEngine; demo: boolean; onDemo: (d: boolean) => void; onOpened: () => void }) {
  const [inputs, setInputs] = useState<MediaDeviceInfo[]>([]);
  const [sel, setSel] = useState(engine.capture.info?.deviceId || '');
  const [err, setErr] = useState('');
  const [opening, setOpening] = useState(false);
  const info = engine.capture.info;
  const open = async (id?: string) => {
    setErr(''); setOpening(true);
    try {
      const i = await engine.open(id || undefined);
      setSel(i.deviceId);
      setInputs(await Capture.inputs());
      onDemo(false);
      onOpened();
    } catch (e) {
      const m = (e as Error).message || String(e);
      setErr(/denied|NotAllowed/i.test(m) ? 'The browser was not allowed to use the audio input. Allow it in the address bar and try again.' : m);
    } finally { setOpening(false); }
  };
  useEffect(() => { if (info) Capture.inputs().then(setInputs).catch(() => {}); }, [info]);
  if (!Capture.supported()) {
    return <section className="connpanel"><div><h2>Measure in your browser</h2><p className="fail">This browser cannot record audio for measurement. Use a current Chrome, Edge, Firefox or Safari on a computer.</p></div></section>;
  }
  return (
    <section className="connpanel">
      <div>
        <h2>Measure in your browser</h2>
        <p className="muted">Everything runs on this computer. The recording, the analysis and the results stay in this browser, and nothing is uploaded. What you need: the player, a cable from its headphone output to a line input or USB audio interface on this computer (stereo, for crosstalk and balance), and that input chosen below. The page does not control the player: you set its mode and press play, and the page listens for the test file.</p>
        <div className="row small">
          {!info && <button className="primary" disabled={opening} onClick={() => open()}>{opening ? 'Opening the input' : 'Use my audio input'}</button>}
          {info && (
            <select value={sel} onChange={e => open(e.target.value)} disabled={opening || engine.busy}>
              {inputs.map(d => <option key={d.deviceId} value={d.deviceId}>{d.label || 'Audio input'}</option>)}
            </select>
          )}
          <label><input type="checkbox" checked={demo} onChange={e => onDemo(e.target.checked)} /> Watch the demo instead (a recorded HiBy M500 session)</label>
        </div>
        {info && <p className="muted small">Recording {info.channels < 2 ? 'mono' : 'stereo'} at {info.rate / 1000} kHz, 32-bit float, with the browser's echo cancellation, noise suppression and automatic gain {info.processing.length ? `asked off, but ${info.processing.join(', ')} stayed on` : 'off'}. Set the input's level in your system's sound settings, not here.</p>}
        {err && <p className="fail">{err}</p>}
      </div>
    </section>
  );
}

function useStimulus(engine: BrowserEngine, plan: PlanName, rate: number) {
  const [url, setUrl] = useState('');
  useEffect(() => {
    let live = true;
    setUrl('');
    engine.stimulusURL(plan, rate).then(u => { if (live) setUrl(u); }).catch(() => {});
    return () => { live = false; };
  }, [engine, plan, rate]);
  return url;
}

/** The two steps: level check, then one recording per player setting. */
export function BrowserRunPanel({ engine, busy, disabled, progress }: { engine: BrowserEngine; busy: boolean; disabled: boolean; progress: Progress | null }) {
  const [rate, setRate] = useState(44100);
  const [full, setFull] = useState(false);
  const [m500, setM500] = useState(true);
  const [filter, setFilter] = useState('fast_rolloff_low_latency');
  const [gain, setGain] = useState('high');
  const [label, setLabel] = useState('');
  const [err, setErr] = useState('');
  const plan = full ? 'full' : 'quick';
  const levelUrl = useStimulus(engine, 'level', rate);
  const testUrl = useStimulus(engine, plan, rate);
  const stepLabel = m500 ? `${FILTER_LABEL[filter]}, gain ${gain}, ${rate / 1000} kHz` : (label.trim() || `measurement, ${rate / 1000} kHz`);
  const go = async (fn: () => Promise<unknown>) => { setErr(''); try { await fn(); } catch (e) { setErr((e as Error).message); } };
  return (
    <>
      <h2>Run</h2>
      <div className="opts">
        <span>Test file rate</span>
        {RATES.map(r => <label key={r}><input type="radio" name="rate" checked={rate === r} onChange={() => setRate(r)} /> {r / 1000} kHz</label>)}
        <label><input type="checkbox" checked={full} onChange={e => setFull(e.target.checked)} /> full plan (stepped sines, IMD)</label>
      </div>
      <p className="muted small"><b>1.</b> Copy both files to the player and play the level file. The level check listens until the 1 kHz tone reads between {-50} and {-2} dBFS peak at the input; adjust the player's volume while it listens.</p>
      <div className="row">
        <a className="btn" href={levelUrl || undefined} download={BrowserEngine.stimulusName('level', rate)} aria-disabled={!levelUrl}>Level file</a>
        <a className="btn" href={testUrl || undefined} download={BrowserEngine.stimulusName(plan, rate)} aria-disabled={!testUrl}>Test file ({plan})</a>
        <button disabled={disabled || busy} onClick={() => go(() => engine.levelCheck())}>Level check</button>
      </div>
      <p className="muted small"><b>2.</b> Set the player, press Record, then play the test file from the start. Repeat for each setting you want to compare; they collect in one session.</p>
      <div className="opts">
        <label><input type="checkbox" checked={m500} onChange={e => setM500(e.target.checked)} /> HiBy M500</label>
        {m500 ? <>
          <select value={filter} onChange={e => setFilter(e.target.value)}>{Object.entries(FILTER_LABEL).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select>
          <select value={gain} onChange={e => setGain(e.target.value)}>{['low', 'middle', 'high'].map(g => <option key={g} value={g}>gain {g}</option>)}</select>
        </> : <input type="text" value={label} onChange={e => setLabel(e.target.value)} placeholder="what you are measuring, e.g. player, filter, gain" />}
      </div>
      <div className="row">
        <button className="primary" disabled={disabled || busy} onClick={() => go(() => engine.measure({ label: stepLabel, plan, playRate: rate, filter: m500 ? filter : undefined, gain: m500 ? gain : undefined }))}>Record</button>
        <button disabled={!busy} onClick={() => engine.stop()}>Stop</button>
        <button className="ghost" disabled={busy} onClick={() => engine.newSession()}>New session</button>
      </div>
      <div className="progress"><div style={{ width: `${((progress?.fraction ?? 0) * 100).toFixed(1)}%` }} /></div>
      <div className="muted">{progress ? `${progress.label}: ${progress.phase}` : busy ? 'Working.' : disabled ? 'Choose an audio input above to start.' : `Nothing running. Test level ${TEST_LEVEL} dBFS.`}</div>
      {err && <div className="fail">{err}</div>}
    </>
  );
}

/** Results files, made in the browser. */
export function BrowserExport({ engine, run, onOpened }: { engine: BrowserEngine; run: Run | null; onOpened: (r: Run) => void }) {
  const [err, setErr] = useState('');
  const save = (blob: Blob, name: string) => {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 10_000);
  };
  return (
    <>
      <h2>Results files</h2>
      <p className="muted small">Saved to this computer. A results file opens here again later, or in the Hione app.</p>
      <div className="row">
        <button disabled={!run?.steps.length} onClick={() => run && save(engine.exportJSON(run), `hione-${run.id}.json`)}>Save results (JSON)</button>
        <button disabled={!run?.steps.length} onClick={() => run && save(engine.exportCSV(run), `hione-${run.id}.csv`)}>Table (CSV)</button>
        <label className="btn ghost">Open results file
          <input type="file" accept="application/json,.json" hidden onChange={e => { const f = e.target.files?.[0]; if (f) engine.importJSON(f).then(onOpened).catch(x => setErr((x as Error).message)); e.target.value = ''; }} />
        </label>
      </div>
      {err && <p className="fail">{err}</p>}
    </>
  );
}
