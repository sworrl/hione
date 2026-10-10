# Measured curves for the M500 Hardware app

Hione writes `hardware-curves.json` into every run directory (and into the export zip under
`data/`). The M500 Hardware app (`com.m500.hardware`, DAC graph) can import it as measured
curves. This file is the contract between the two.

## File

```json
{
  "format": "hione.curves",
  "version": 1,
  "source": "hione",
  "measured": "2026-10-10T16:21:53-04:00",
  "device": "HiBy M500 / MikuOS",
  "capture": "hw:0,0 over ssh 192000 Hz S32",
  "curves": [
    {
      "id": "20261010-162153-step02",
      "label": "Fast, low latency, gain high, 44.1 kHz",
      "port": "3.5mm",
      "filter": "fast_rolloff_low_latency",
      "gain": "high",
      "highPower": false,
      "dre": true,
      "rateHz": 44100,
      "channel": "left",
      "ref": "1kHz",
      "hz": [10.0, 10.29, 10.59, "..."],
      "db": [0.08, 0.08, 0.09, "..."],
      "thdnDb": -80.8,
      "noiseDbfsA": -93.2
    }
  ]
}
```

## Fields

| Field | Type | Meaning |
|---|---|---|
| `format` | string | Always `hione.curves`. Files from before the rename say `mikumeasure.curves` and are otherwise identical. Accept both. |
| `version` | int | 1. A reader should refuse a higher major version. |
| `measured` | string | RFC 3339 time the run started. |
| `device` | string | Device profile name. |
| `capture` | string | Capture card, rate and format. Show it with the curve, it bounds what the curve can show. |
| `curves[].id` | string | Unique per curve. Use it as the storage key. |
| `curves[].label` | string | Human label, already in plain words. |
| `curves[].port` | string | `3.5mm` or `4.4mm`, same strings as `OutputProfiles.PORT_35` and `PORT_44`. |
| `curves[].filter` | string | Kernel filter name as in `miku_dac_state` (`fast_rolloff_low_latency`, `fast_rolloff_phase_compensated`, `slow_rolloff_low_latency`, `slow_rolloff_phase_compensated`, `nos`). |
| `curves[].gain` | string | `low`, `middle` or `high`. |
| `curves[].highPower`, `dre` | bool | Optional. Missing means not known. |
| `curves[].rateHz` | int | Sample rate of the played file. The digital filters act relative to it, so match it to the now-playing rate. |
| `curves[].channel` | string | `left` (the right channel is in the run's CSV). |
| `curves[].ref` | string | `1kHz`: `db` is 0 at 1 kHz. |
| `curves[].hz`, `db` | number[] | Same length, `hz` ascending, 24 points per octave from 10 Hz to the sweep's top (0.475 x rate, at most 40 kHz). |
| `curves[].thdnDb` | number | Optional. THD+N at 1 kHz, 20 Hz to 20 kHz, re the fundamental. |
| `curves[].noiseDbfsA` | number | Optional. A-weighted idle noise at the capture input, dBFS. |

## Mapping to the Hardware app

`ResponseStore` keeps one curve per key and kind as `{label, at, hz[], db[]}`. To store an
imported curve there:

- key: `port-<port>` (or a profile id if the user picks one), kind `measured`
- label: `curves[].label`
- at: `measured` parsed to epoch milliseconds
- hz, db: copied as they are

To pick the curve that matches what is playing, match `filter`, `gain`, `port` and
`rateHz` against `miku_dac_state`, the active output and `miku_now_playing_format`.

## Caveats to show next to the curve

- The curve includes the capture card's own response. Hione can subtract a loopback
  calibration, but the default export is uncorrected. The `capture` field says which card.
- Above about 20 kHz the curve stops at the sweep's top frequency. Do not extrapolate.
