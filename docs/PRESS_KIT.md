# Hione press kit

**For:** anyone writing about Hione, putting it on a project page, or using its charts in a
review. Everything here is either in this repo or was measured with it, and where something is
unproven it says so.

**Status: work in progress.** One person, one capture card, one device measured so far. Issues
and pull requests are welcome once the repo is public (it is private for now).

---

## The one-liner

> Hione is an open-source audio measurement tool for gear reviews. It plays test signals
> through a player or DAC, records them on a capture card, and reports frequency response,
> distortion, noise and crosstalk, with a live web UI, a JSON API and charts ready to publish.

## The repo

| Repo | What it is |
|---|---|
| https://github.com/sworrl/hione | The tool: Go backend, React + WebGL UI, report and chart export |

GPL-3.0-or-later. Private for now.

---

## What it is

One static Go binary with the web UI built in. You point it at a capture card and a device, it
plays a test file through the device, records the output, and analyzes it. A run over every DAC
mode of a HiBy M500 takes about 15 minutes and needs no hands.

What it measures:

* Frequency response from a log sweep, with phase, group delay and octave smoothing
* Impulse response (an average of 32 single-sample impulses, so filter pre-ringing shows)
* THD and THD+N at 1 kHz
* Noise, SNR and dynamic range, A-weighted
* Crosstalk, channel balance and polarity
* The clock difference between the device and the card, which the analysis then follows
* With the full plan: stepped sines against frequency and level, SMPTE and CCIF IMD, and a
  left/right identification check

How it gets the signal:

* **Local capture** through ALSA (`arecord`) or PipeWire (`pw-record`)
* **Remote capture over ssh**: the capture card can sit in another machine. Hione runs
  `arecord` there and streams the samples back
* It sets the card's mixer for a clean line capture and puts the old mixer state back on exit

How it drives the device:

* **HiBy M500 on MikuOS, over adb.** Hione sets each DAC mode (five digital filters, gain,
  high power, DRE), pushes the test file, plays it, checks the DAC did not change during the
  recording, and puts your own settings back at the end, also when a run fails
* **Any other device in manual mode.** Hione tells you when to press play and finds the
  signal by its start marker
* **A simulated device**, so you can try the whole thing with no hardware

What you get out:

* A **live web UI**: input meters, live spectrum and spectrogram drawn with WebGL2 from a
  binary WebSocket (about 30 frames a second), run controls, the frequency and impulse
  response of every mode with toggles, and a results table
* A **JSON API** for everything the UI does, plus server-sent events for progress and logs
* **Reports** in HTML, Markdown and PDF, charts in **SVG and PNG** (light review theme or the
  dark Hione theme), a zip of the lot, CSV per measurement, and `run.json` with everything
* Exported charts carry the device, DAC settings, capture chain and date. They leave out the
  capture host and the device's network address, since they get published
* **A demo mode**: the UI plays a recorded M500 session and a finished run, so a visitor with
  no hardware can see what it does
* **Pairing for a hosted UI**: the same frontend can live on a website and talk to Hione on the
  visitor's own machine, after they pair it with a token. Origins are allowlisted and DNS
  rebinding is blocked. Details in `docs/live-protocol.md`

## Who it's for

* People who review DAPs, DACs and headphone amps and want numbers they can rerun
* People building audio firmware (MikuOS is the first) who want to check a change did what
  it claims
* Anyone with a decent capture card who is tired of measuring one setting at a time by hand

---

## The first real result: HiBy M500, 3.5 mm out (2026-10-10)

Measured with Hione on MikuOS 0.3.1. The M500 played the test file over its 3.5 mm jack into a
desktop's onboard Realtek ALC1220 line input, captured at 192 kHz / 32-bit over ssh. The M500
ran on battery with the USB cable unplugged and adb over Wi-Fi (with USB in, a ground loop put
tones at 4.75 kHz and 226 Hz into the capture). Test level -12 dBFS, volume 100/100. 24 modes:
five filters, two gains and two source rates, plus high power and DRE on and off.

Raw numbers: `docs/press/m500-3.5mm-2026-10-10-summary.csv` (one row per mode). The same run is
written up in the MikuOS press kit and in the MikuOS repo under
`docs/measurements/2026-10-10-m500-3.5mm/`.

| What | Result | How much to trust it |
|---|---|---|
| Gain high vs low | 12.0 dB apart (-9.2 vs -21.2 dBFS at the card) | solid |
| THD at 1 kHz, -12 dBFS | -92 to -100 dB | solid |
| THD+N | about -81 dB at gain high, -69 dB at gain low | limited by the capture card's noise |
| Noise | -93.2 dBFS(A) at the card in every mode | that is the ALC1220's own floor. The M500 is quieter than this card can show |
| SNR, dynamic range | about 96 dB(A) at gain high, 84 dB(A) at gain low | same card limit |
| Crosstalk | -89 to -93 dB | solid |
| Channel balance | -0.08 dB | solid |
| Clock vs the capture card | +47 ppm | solid |
| High power, DRE | no measurable change into a line input | as expected at line level |

**The finding: the five digital filters measure identical.** Fast or slow roll-off, low
latency or phase compensated, or NOS: the frequency response matches within 0.01 dB up to
20 kHz at both 44.1 and 96 kHz sources, and the impulse response is the same symmetric
linear-phase filter in every mode. Android isn't resampling (the track plays on a DIRECT output
at the file's own rate). The audio hardware below Android upsamples to 192 kHz before the DAC,
so the CS43198's own filter acts at 192 kHz, far above anything audible. NOS only adds the small
droop of a 192 kHz zero-order hold (-0.15 dB at 20 kHz). On this device today, the filter choice
makes no audible difference.

That is the kind of result Hione is for. The settings screen offers five filters, and a
measurement over all of them in one run shows what they actually do on this hardware.

Charts from this run, light and dark, SVG and PNG, in `docs/press/charts/`:

| File | What it shows |
|---|---|
| `digital-filters-at-44-1-khz-gain-high-top-{light,dark}` | The top octave, all five filters on top of each other |
| `digital-filters-at-44-1-khz-gain-high-ir-{light,dark}` | Impulse response, the same in every mode |
| `thdn-{light,dark}` | THD+N for all 24 modes. Gain low is worse only because the same card noise sits closer to a quieter signal |

---

## Capture limits (say this wherever the numbers appear)

Every number goes through the capture card. Its noise, distortion and anti-alias filter are
part of every result, and a device better than the card measures as the card. The reports say
so on every page.

For the M500 run that matters for noise: the onboard ALC1220 sets the floor, so the noise,
SNR, dynamic range and THD+N figures above describe the card more than the player. THD,
crosstalk, balance, gain and frequency response are well above that floor.

`hione calibrate loopback` measures the card on its own, and the report then puts the card's
limits next to the device's numbers. It has not been run on this card yet.

The M500's 4.4 mm balanced output is not measured. It needs a balanced-input ADC, and the
ALC1220 is single-ended.

---

## What is verified, what is not

**Verified on hardware:** a full 24-mode matrix run on the M500 over adb with remote capture
over ssh, start to finish with no hands, including putting the user's DAC settings back. The
level check. Clock tracking (+47 ppm measured). The device status reconnecting after Wi-Fi adb
drops. Chart and report export in both themes. The live UI and demo mode in Chrome.

**Implemented but not proven on hardware:** the full plan (stepped sines, SMPTE and CCIF IMD,
L/R identification) has only run against the simulated device. Loopback and level calibration
have not been run on a real card. The automatic volume step-down (the M500 run was already at
a safe level, so it never stepped). Pairing from a real hosted page, since the UI is not hosted
anywhere yet. Measuring a second device in manual mode.

**Not built yet:** music null tests (play a real track, subtract the source, listen to and
measure what is left). Capture backends for macOS and Windows: the binary builds there, but
capture needs `arecord`, `pw-record` or ssh to a Linux box.

---

## Screenshots

In `docs/press/screenshots/`, captured from the running tool at 1600 px wide. The capture
host is blurred in the two live shots. The demo shots show no host or address to begin with.

| File | What it shows | Best used for |
|---|---|---|
| `demo-full-dark.png` | The whole UI in demo mode: meters, spectrum, spectrogram, the five filters' response, impulse, and the results table for all 24 modes | The hero image |
| `demo-full-light.png` | The same in the light theme | Light pages and print |
| `demo.png` | The top of the demo page, 1600x1000 | Thumbnails, social cards |
| `demo-top-octave.png` | The top-octave view, where the filters would differ if they did | The filter finding |
| `live-ui.png` | Connected to the real M500 over adb: device, DAC mode, jack and volume read live in the header | Showing it is a working tool, not a mockup |
| `classic-ui.png` | The older single-file page at `/classic/`, still served | Completeness only |

---

## The mascot: Oki Hione

The tool is named after its mascot. **Hione** (氷音, "ice tone"), full name **Oki Hione**
(隠岐 氷音, family name first). Reading: HEE-oh-neh, three syllables, stress on the first.

The short version of her story: she spent six years undercover as a touring singer, and the
cover was the part she was best at. She walked away from the work, kept the voice, and now she
runs your music. She is dry, precise and competitive, and she hates clipping, bad masters and
sloppy measurements, which makes her a fair fit for a measurement tool. Her motif is the coda
mark, a circle crossed by a plus sign, which also reads as a scope reticle.

Fictional. No real person, unit or organization is named or implied.

### Palette

| Role | Name | Hex | Use |
|---|---|---|---|
| Primary | Tally Amber | `#FFAB2E` | Wordmark, accents, active states |
| Primary deep | Deep Amber | `#8F5300` | Amber text on light backgrounds (plain amber fails contrast there) |
| Accent | Ice Blue | `#8EC5FF` | Second accent, links, waveforms |
| Dark | Gunmetal Black | `#111216` | Backgrounds |
| Secondary | Gunmetal | `#2B2F38` | Cards, panels |
| Light | Stage Ivory | `#F4EFE6` | Light surfaces, text on dark |

The wordmark is "Hione" in Orbitron Bold (SIL OFL 1.1), amber on dark.

### Art

In `docs/press/art/`. Use these and only these.

| File | Size | Use |
|---|---|---|
| `avatar-512.png` | 512x512, round, transparent corners | Icons, author boxes, small placements |
| `wordmark-amber.png` | 742x274, transparent | The name on dark backgrounds |
| `wordmark-deep-amber.png` | 742x274, transparent | The name on light backgrounds |
| `wordmark-on-dark.png` | 742x274, on `#111216` | Where transparency is not supported |
| `banner-stage.jpg` | 1600x893 | Page headers, wide banners |
| `pose-headphones.jpg` | 896x1200 | Holding an M500, in-ear monitor in. The best fit for audio pieces |
| `pose-vocal.jpg` | 896x1200 | At a stage microphone |
| `audiophile.jpg` | 1200x1200 | Listening in an armchair with a player. Good for "how it sounds" pieces |

Rules: keep her fully clothed and in the art as given (no crops that single out a body part, no
edits to her outfit or face), no weapons, nothing suggestive. Do not use her to imply HiBy,
Crypton or anyone else endorses the tool.

---

## Build and run

```
go build -o hione ./cmd/hione
go test ./...
./hione serve --device sim          # try it with no hardware, then open http://127.0.0.1:8484/
```

Go 1.22 or newer, Linux. The web UI is built into the binary: its Vite build is committed under
`internal/server/ui/`, so `go build` alone gives a complete tool. After changing the UI,
`cd web && npm install && npm run build` rebuilds it into the same place.

With hardware: put the capture in `HIONE_CAPTURE` (for example `ssh://host/hw:0,0` or
`alsa:hw:1,0`), plug the device's output into the card's line input, run `hione serve`, then
Level check and Run DAC matrix in the UI. The README has every command and API call.

---

## License

GPL-3.0-or-later. Go dependencies: gonum (BSD-3-Clause), golang.org/x/image (BSD-3-Clause,
including the Go fonts), go-pdf/fpdf (MIT), coder/websocket (ISC). The UI uses React (MIT) and
Orbitron (SIL OFL 1.1).

The character art ships in the repo under the same GPL-3.0-or-later for now. A separate license
for the art has not been picked. Using the art, screenshots and charts to write about or review
Hione is fine.

The HiBy M500 is named because it is what was measured. Hione is not affiliated with HiBy.
Realtek and Cirrus Logic are named as the parts in the chain.

---

## Tone

Match the README. Plain, short sentences. US English. No brochure language and no
"revolutionary". Lead with what was measured, and say "not proven" where something is not
proven. The tool exists to stop audio claims from outrunning the measurements, so a page about
it that overclaims would undercut it.

Do not write "AI-generated" or credit any tooling. Attribute the author: **sworrl**.

---

## Where help is wanted

* **A balanced-input ADC run** of the M500's 4.4 mm output
* **A loopback calibration** of other capture cards, so their limits can sit next to results
* **Capture backends for macOS and Windows**
* **Device profiles** beyond the M500 (anything that can be told to play a file and set its
  modes from a shell)
* **The music null test**
