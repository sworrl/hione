// Hione capture worklet: copies the input's two channels into blocks of `block` frames and posts
// them to the page. Runs on the audio thread, so it does nothing but copy. A mono input is sent
// as the same samples on both channels, and the page is told so it can say crosstalk and
// balance are not meaningful.
class HioneCapture extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.block = (options.processorOptions && options.processorOptions.block) || 4096;
    this.l = new Float32Array(this.block);
    this.r = new Float32Array(this.block);
    this.n = 0;
    this.mono = null;
  }

  process(inputs) {
    const input = inputs[0];
    if (!input || input.length === 0) return true;
    const L = input[0];
    const R = input.length > 1 ? input[1] : input[0];
    const mono = input.length < 2;
    if (mono !== this.mono) {
      this.mono = mono;
      this.port.postMessage({ type: 'channels', mono });
    }
    let i = 0;
    while (i < L.length) {
      const take = Math.min(L.length - i, this.block - this.n);
      this.l.set(L.subarray(i, i + take), this.n);
      this.r.set(R.subarray(i, i + take), this.n);
      this.n += take;
      i += take;
      if (this.n === this.block) {
        this.port.postMessage({ type: 'block', l: this.l, r: this.r }, [this.l.buffer, this.r.buffer]);
        this.l = new Float32Array(this.block);
        this.r = new Float32Array(this.block);
        this.n = 0;
      }
    }
    return true;
  }
}

registerProcessor('hione-capture', HioneCapture);
