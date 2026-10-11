/**
 * Capture from the visitor's own audio input, in the browser.
 *
 * The browser's voice processing (echo cancellation, noise suppression, automatic gain) is
 * switched off, because each of those changes the signal being measured. The AudioContext runs
 * at the rate the input track reports, so the browser does not resample on the way in. What the
 * operating system's mixer does before the browser sees the samples is outside our control; the
 * page reports the rate and channel count it actually got.
 */

export interface InputInfo {
  deviceId: string;
  label: string;
  rate: number;
  /** Channels the track says it has. A mono input cannot measure crosstalk or balance. */
  channels: number;
  /** Processing the browser left on despite being asked to turn it off (empty is good). */
  processing: string[];
}

export type BlockListener = (l: Float32Array, r: Float32Array) => void;

export class Capture {
  info: InputInfo | null = null;
  mono = false;
  private ctx: AudioContext | null = null;
  private stream: MediaStream | null = null;
  private node: AudioWorkletNode | null = null;
  private listeners = new Set<BlockListener>();
  onChannels: ((mono: boolean) => void) | null = null;
  /** Test hook (?debug): blocks fed by inject() replace the device's. */
  injecting = false;

  inject(l: Float32Array, r: Float32Array) { this.injecting = true; this.listeners.forEach(fn => fn(l, r)); }

  static supported(): boolean {
    return !!navigator.mediaDevices && typeof navigator.mediaDevices.getUserMedia === 'function' && typeof window.AudioContext === 'function' && typeof window.AudioWorkletNode === 'function';
  }

  /** Audio inputs. Labels are empty until the visitor has granted permission once. */
  static async inputs(): Promise<MediaDeviceInfo[]> {
    const all = await navigator.mediaDevices.enumerateDevices();
    return all.filter(d => d.kind === 'audioinput');
  }

  async open(deviceId?: string): Promise<InputInfo> {
    await this.close();
    const audio: MediaTrackConstraints = {
      echoCancellation: false,
      noiseSuppression: false,
      autoGainControl: false,
      channelCount: { ideal: 2 },
      sampleRate: { ideal: 192000 },
      sampleSize: { ideal: 32 },
    };
    if (deviceId) audio.deviceId = { exact: deviceId };
    // Chrome's legacy switches, honoured where the standard ones are not.
    Object.assign(audio, { googEchoCancellation: false, googNoiseSuppression: false, googAutoGainControl: false, googHighpassFilter: false });
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({ audio, video: false });
    } catch (e) {
      // A device id goes stale when the input is unplugged or the browser re-keys its ids; try
      // the system's default input before giving up.
      const name = (e as DOMException)?.name;
      if (deviceId && (name === 'NotFoundError' || name === 'OverconstrainedError')) {
        delete audio.deviceId;
        try {
          this.stream = await navigator.mediaDevices.getUserMedia({ audio, video: false });
        } catch (e2) {
          throw new Error(explain(e2));
        }
      } else {
        throw new Error(explain(e));
      }
    }
    const track = this.stream.getAudioTracks()[0];
    const s = track.getSettings();
    const rate = s.sampleRate || 48000;
    const processing = (['echoCancellation', 'noiseSuppression', 'autoGainControl'] as const).filter(k => s[k] === true);
    this.ctx = new AudioContext({ sampleRate: rate, latencyHint: 'playback' });
    await this.ctx.audioWorklet.addModule(new URL('worklet/capture.js', document.baseURI).href);
    const src = this.ctx.createMediaStreamSource(this.stream);
    this.node = new AudioWorkletNode(this.ctx, 'hione-capture', {
      numberOfInputs: 1, numberOfOutputs: 0, channelCount: 2, channelCountMode: 'explicit',
      channelInterpretation: 'discrete', processorOptions: { block: 4096 },
    });
    this.node.port.onmessage = (ev: MessageEvent) => {
      const m = ev.data;
      if (m.type === 'block' && !this.injecting) this.listeners.forEach(fn => fn(m.l, m.r));
      else if (m.type === 'channels') { this.mono = m.mono; this.onChannels?.(m.mono); }
    };
    src.connect(this.node);
    if (this.ctx.state === 'suspended') await this.ctx.resume();
    this.info = {
      deviceId: s.deviceId || deviceId || '',
      label: track.label || 'Audio input',
      rate: this.ctx.sampleRate,
      channels: s.channelCount || 2,
      processing,
    };
    this.mono = (s.channelCount || 2) < 2;
    return this.info;
  }

  on(fn: BlockListener): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }

  async close() {
    this.node?.disconnect();
    this.node = null;
    this.stream?.getTracks().forEach(t => t.stop());
    this.stream = null;
    if (this.ctx) await this.ctx.close().catch(() => {});
    this.ctx = null;
    this.info = null;
  }
}

/** The browser's own errors say little; say what to check. */
export function explain(e: unknown): string {
  const name = (e as DOMException)?.name || '';
  const msg = (e as Error)?.message || String(e);
  const os = navigator.userAgent;
  const where = /Windows/.test(os)
    ? 'In Windows, check Settings > Privacy & security > Microphone, and that the line input is enabled under Sound > Recording.'
    : /Mac OS X|Macintosh/.test(os)
      ? 'On a Mac, check System Settings > Privacy & Security > Microphone for this browser, and Audio MIDI Setup for the input.'
      : 'On Linux, check that the input shows up in "pactl list short sources" (a .monitor source does not count), and that a Flatpak or Snap browser has been given audio input access.';
  if (name === 'NotFoundError' || name === 'OverconstrainedError') {
    return `This browser can't see any audio input on this computer. Plug the cable into this computer's line input or USB audio interface, or open this page on the computer it is plugged into. ${where}`;
  }
  if (name === 'NotAllowedError' || name === 'SecurityError') {
    return `The browser was not allowed to use the audio input. Allow it from the icon in the address bar and try again. ${where}`;
  }
  if (name === 'NotReadableError' || name === 'AbortError') {
    return `The audio input is there but could not be opened. Another program may hold it, or the system blocked it. ${where}`;
  }
  return msg;
}
