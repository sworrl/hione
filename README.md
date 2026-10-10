# Hione

Audio measurement for portable players, DACs and anything else with a line or headphone out.
It plays a test signal through the device, records it on a capture card, and reports
frequency response, impulse response, THD and THD+N, noise, dynamic range, crosstalk,
channel balance and polarity. For the HiBy M500 on MikuOS it can also set every DAC mode over
adb and measure them all in one run.

One static Go binary, no cgo. The web UI and the reports are built in.

Press kit, screenshots, charts and the first real measurement (the HiBy M500's 3.5 mm output):
[docs/PRESS_KIT.md](docs/PRESS_KIT.md).

## Build

```
go build -o hione ./cmd/hione
go test ./...
```

The web UI is a Vite + React + TypeScript app in `web/` that draws its charts with WebGL2. Its
build goes into `internal/server/ui/` and is committed, so `go build` alone gives a complete
binary. After changing the UI:

```
cd web && npm install && npm run build
```

Go 1.22 or newer. Linux is the main target. The capture backends call `arecord`,
`pw-record` or `ssh`, so the binary itself builds for macOS and Windows too, but capture there
needs a backend that is not written yet.

## Setup

- The device's output goes into the capture card's line input with a shielded cable.
- Measure with the device on battery. A USB cable from the device to the same PC makes a
  ground loop. On the M500 into a desktop ALC1220 it put a 4.75 kHz tone and a 226 Hz series
  about 60 dB under the test level, which swamps noise and THD+N. Use adb over Wi-Fi instead.
- The capture card can be on another machine: `--capture ssh://host/hw:0,0` runs `arecord`
  there over ssh and streams the samples back.
- Put the capture you use in `HIONE_CAPTURE` (for example `export HIONE_CAPTURE=ssh://host/hw:0,0`)
  so it is the default. Without it Hione uses `auto`: PipeWire if it is there, else the ALSA
  default device. With `auto` or the ALSA default it leaves the mixer alone, so name the card
  (`alsa:hw:1,0`) to get the mixer setup below.

Hione sets the capture card's mixer for a clean line capture (input source Line, no boost,
capture gain 0 dB, analog loopback off). It saves the card's mixer with `alsactl store`
first and puts it back on exit, also on Ctrl-C.

## Commands

```
hione devices --ssh host1,host2       list capture and playback devices, local and on ssh hosts ($HIONE_SSH_HOSTS), and the adb device
hione serve                           live web UI and JSON API on http://127.0.0.1:8484/
hione level                           level check only
hione measure                         one measurement of the device as it is set now
hione matrix                          every DAC mode on the M500
hione calibrate loopback              measure the capture chain itself (card output into card input)
hione calibrate level --vrms 0.5      level calibration from a known voltage, for results in dBV
hione report <run dir>                rebuild a run's report
hione generate                        write the stimulus WAVs only
```

Common flags:

| Flag | Default | |
|---|---|---|
| `--capture` | `$HIONE_CAPTURE`, else `auto` | `alsa:hw:1,0`, `pw:<node name>`, `ssh://host/hw:C,D`, `auto`, or `sim` |
| `--rate` | `192000` | Capture rate |
| `--format` | `S32` | `S16`, `S32` or `F32` |
| `--device` | `m500` | `m500` (adb), `manual` (you press play), or `sim` |
| `--adb` | first MikuOS device | adb serial such as `<device ip>:5555`, or `t:<transport id>` |
| `--level` | `-12` | Test level in dBFS. Anything louder than -6 is refused |
| `--out` | `hione-out` | Where runs, stimuli and calibration go |

Matrix flags: `--rates 44100,96000`, `--gains low,high`, `--filters` (default all five),
`--power-dre` (adds high power on/off times DRE on/off at your own filter), `--plan quick|full`
(full adds stepped sines vs frequency and level, SMPTE and CCIF IMD, L/R identification).

Try it without hardware: `hione serve --device sim` runs against a simulated device.

## What a run does

1. Reads the DAC settings (`Settings.Global miku_dac_state`) so it can put them back.
2. Level check: sets the loudest mode the run will use, plays a 1 kHz tone at the test level and
   reads it at the input. The peak must be under -2 dBFS and the tone over -50 dBFS. If it is
   too loud it turns the M500 volume down a step at a time. It never turns it up.
3. For each mode: sets the DAC through Miku Music, checks `miku_dac_state`, pushes the test file to
   `/sdcard/Music/hione/` (with a `.nomedia`), plays it, records until the end marker,
   checks the DAC did not change during the recording, and analyzes.
4. Puts your DAC settings back, also when the run fails or is stopped.

The test file: 1.5 s lead-in, a start marker (short upward sweep), 1 kHz tone, exponential sweep
from 10 Hz to 0.475 x the playback rate, 32 single-sample impulses, 3 s of digital silence,
1 kHz 60 dB down, left-only and right-only tones, an end marker (downward sweep). The two markers
give the clock difference between the device and the card (about 48 ppm for the M500 into the
ALC1220), and the analysis follows it.

### The M500 side

Miku Music answers four adb broadcasts. They go through `MikuPrefsShellReceiver`, which is
guarded by `android.permission.DUMP`, so only `adb shell` can send them:

```
adb shell am broadcast -p com.miku.player -a com.miku.player.action.PLAY_FILE --es path /sdcard/Music/hione/x.wav
adb shell am broadcast -p com.miku.player -a com.miku.player.action.MEASURE_STOP
adb shell am broadcast -p com.miku.player -a com.miku.player.action.MEASURE_STATUS
adb shell am broadcast -p com.miku.player -a com.miku.player.action.DAC_SET --es digital_filter nos --es gain high
```

PLAY_FILE turns the live EQ off and keeps your queue, MEASURE_STOP puts both back. DAC_SET
forwards to `com.miku.sysbridge` the same way the Hardware app does.

Other devices: `--device manual`. Hione tells you when to press play and finds the signal by
its start marker.

## Results

Each run gets a directory `hione-out/runs/<id>/`:

- `report.html`, `report.md`, `report.pdf` (cover, results table, every chart, methods)
- `charts/*.svg` and `charts/*.png`
- `summary.csv` (one row per mode), `stepNN/fr.csv`, `stepNN/ir.csv`, `stepNN/result.json`
- `run.json` (everything)
- `hardware-curves.json` for the M500 Hardware app, see `docs/hardware-app-format.md`

Charts come in a light review theme and the dark Hione theme. Exported charts carry the
device, DAC settings, capture chain and date, and a small Hione mark you can turn off.
Reports and charts leave out the ssh host of the capture card and the device's adb address,
since they get published. `run.json` keeps both.

## Web UI

`hione serve` prints the address, http://127.0.0.1:8484/ by default. The page shows the input
meters, live spectrum and spectrogram (from a binary WebSocket, about 30 frames a second, drawn
with WebGL), the run controls and progress, the frequency response of every mode with toggles,
the impulse response, a results table, exports and the log. The older single-file page is at
`/classic/`.

The same frontend can be hosted on a website. Visitors without hardware get a demo that plays a
recorded session. With hardware, they run `hione serve --allow-origin https://their.site` and
pair with the token it prints. See `docs/live-protocol.md` for the stream format and the pairing
rules.

## API

Everything the UI does goes through this API.

```
curl -s localhost:8484/api/state                      # config, device status, runs, recent log
curl -s localhost:8484/api/live                       # meters and spectrum right now
curl -s localhost:8484/api/devices
curl -s -X POST localhost:8484/api/level-check
curl -s -X POST localhost:8484/api/runs -d '{"kind":"matrix","rates":[44100,96000],"gains":["low","high"],"power_dre":true,"jack":"3.5mm"}'
curl -s -X POST localhost:8484/api/runs -d '{"kind":"measure","rates":[44100],"label":"after the cable swap"}'
curl -s localhost:8484/api/runs                       # list
curl -s localhost:8484/api/runs/<id>                  # status and every result
curl -s -X POST localhost:8484/api/stop
curl -s localhost:8484/api/report/<id>                # HTML report
curl -s localhost:8484/api/runs/<id>/charts           # chart ids
curl -s -o fr.svg 'localhost:8484/api/runs/<id>/charts/<chart>.svg?theme=light'
curl -s -o fr.png 'localhost:8484/api/runs/<id>/charts/<chart>.png?scale=4&theme=dark&mark=0&bg=transparent'
curl -s -o report.pdf 'localhost:8484/api/runs/<id>/export?format=pdf'
curl -s -o all.zip 'localhost:8484/api/runs/<id>/export?format=zip&theme=light'
curl -N localhost:8484/api/events                     # server-sent events: live, log, progress, step, run
```

Chart options: `theme=light|dark`, `scale=1|2|4` (PNG), `stamp=0` drops the provenance lines,
`mark=0` drops the Hione mark, `bg=transparent`.

## Limits

Every number goes through the capture card. Its noise, distortion and anti-alias filter are
part of every result, and a device that is better than the card measures as the card. The
reports say so. `hione calibrate loopback` measures the card on its own, and the report then
puts the card's limits next to the device's numbers.

Noise, SNR and dynamic range are referred to the device's full scale by adding the headroom
of the test level (12 dB by default), so they assume the device stays linear up to 0 dBFS.
Levels are dBFS at the card unless `calibrate level` was run.

## License

GPL-3.0-or-later, the same as MikuOS. It is a tool people run and modify, and copyleft keeps
the modified versions open. Dependencies: gonum (BSD-3-Clause), golang.org/x/image
(BSD-3-Clause, including the Go fonts), go-pdf/fpdf (MIT), coder/websocket (ISC). The web UI
uses React (MIT) and the Orbitron font (SIL OFL 1.1). The Hione character art ships under the
same license for now, see the press kit.
