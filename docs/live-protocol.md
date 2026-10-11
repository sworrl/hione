# Live stream and pairing

## WebSocket

`GET /api/ws?fps=30` (5 to 60 frames a second). From a hosted page, add `&token=<pairing token>`.

Text messages are JSON:

- `{"type":"hello","version":1,"rate":192000,"freqs":[...]}` comes first, once. `freqs` holds the
  centre frequencies of the spectrum bins (log-spaced, from 10 Hz to half the capture rate).
- Engine events are the same objects `/api/events` sends: `log`, `run`, `step`, `progress`, `level`.

Binary messages are live frames, little endian:

| Offset | Type | |
|---|---|---|
| 0 | u8 | type, 1 = live |
| 1 | u8 | flags: bit 0 clip L, bit 1 clip R, bit 2 capturing |
| 2 | u16 | number of bins N |
| 4 | f32 | capture rate, Hz |
| 8 | f64 | time, ms since the Unix epoch |
| 16 | f32 x 4 | peak L, peak R, RMS L, RMS R, in dBFS (RMS is re a full-scale sine) |
| 32 | f32 x N | spectrum L, dBFS per bin (the largest FFT bin in each log band) |
| 32 + 4N | f32 x N | spectrum R |

With 240 bins a frame is 1952 bytes, about 59 kB/s at 30 frames a second.

## Demo sessions

`GET /api/demo/record?seconds=15` records the live stream. The file is one JSON header line
(`{"format":"hione.live","version":1,"freqs":[...],"rate":...,"fps":20}`), then frames as
`[u32 length][frame]`. The web UI's demo mode plays `demo/live.bin` (a file like this) and shows
`demo/run.json` (a run as `GET /api/runs/<id>` returns it). A hosted copy works that way for
visitors who have no hardware.

## Pairing a hosted frontend

The hosted build at https://hione.falcontechnix.com does not pair: it measures in the browser
(see the README). Pairing is still there for other frontends.

The frontend in `web/` builds to static files. Served by `hione serve`, it needs nothing else.
Hosted on a website, it talks to a Hione backend on the visitor's own machine:

1. Start the backend with the site listed: `hione serve --allow-origin https://example.com`.
   It prints a pairing token. The token is also in `<out>/pairing-token` and on the local page
   under Pairing.
2. On the hosted page, open Connection and enter `http://127.0.0.1:8484` and the token.

Rules the backend enforces:

- A request from another origin needs that origin on `--allow-origin` and the token, sent as
  `Authorization: Bearer <token>` (or `?token=` for the WebSocket and downloads). Otherwise
  the answer is 403 or 401, with no CORS headers.
- Preflights from allowed origins get `Access-Control-Allow-Private-Network: true`. Chrome
  needs it before a public page may call localhost.
- While the server listens on loopback, the Host header must be a loopback name. That blocks
  DNS rebinding.
- Only the page the backend serves itself can read or reset the token (`GET /api/pairing`,
  `POST /api/pairing/reset`).
- Listening on a non-loopback address makes the token required for every API call.
