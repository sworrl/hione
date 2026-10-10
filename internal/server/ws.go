package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/sworrl/hione/internal/engine"
)

// Live frame, binary, little endian (docs/live-protocol.md):
//
//	0  u8   type (1 = live)
//	1  u8   flags: bit 0 clip L, bit 1 clip R, bit 2 capturing
//	2  u16  number of bins N
//	4  f32  capture rate
//	8  f64  time, ms since the Unix epoch
//	16 f32  peak L, peak R, RMS L, RMS R (dBFS)
//	32 f32  spectrum L [N], spectrum R [N] (dBFS, log-spaced bins from the hello message)
func encodeLive(s engine.LiveSnapshot) []byte {
	n := len(s.Freqs)
	if len(s.SpecDB[0]) != n || len(s.SpecDB[1]) != n {
		n = 0
	}
	b := make([]byte, 32+8*n)
	le := binary.LittleEndian
	b[0] = 1
	var fl byte
	if s.Clip[0] {
		fl |= 1
	}
	if s.Clip[1] {
		fl |= 2
	}
	fl |= 4
	b[1] = fl
	le.PutUint16(b[2:], uint16(n))
	le.PutUint32(b[4:], math.Float32bits(float32(s.Rate)))
	le.PutUint64(b[8:], math.Float64bits(float64(s.Time.UnixNano())/1e6))
	for i, v := range []float64{s.PeakDBFS[0], s.PeakDBFS[1], s.RMSDBFS[0], s.RMSDBFS[1]} {
		le.PutUint32(b[16+4*i:], math.Float32bits(float32(v)))
	}
	for c := 0; c < 2 && n > 0; c++ {
		for i, v := range s.SpecDB[c] {
			le.PutUint32(b[32+4*(c*n+i):], math.Float32bits(float32(v)))
		}
	}
	return b
}

func (s *Server) originPatterns() []string {
	var p []string
	for _, o := range s.Auth.AllowOrigins {
		if u, err := url.Parse(o); err == nil {
			p = append(p, u.Host)
		}
	}
	return p
}

// ws streams live frames (binary, about 30 a second) and engine events (text JSON).
func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns(), CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // reader: only to notice the close
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	snap := s.E.Live().Snapshot()
	hello, _ := json.Marshal(map[string]any{"type": "hello", "freqs": snap.Freqs, "rate": snap.Rate, "version": 1})
	if c.Write(ctx, websocket.MessageText, hello) != nil {
		return
	}
	evs, unsub := s.E.Subscribe()
	defer unsub()
	fps := 30
	if v, _ := strconv.Atoi(r.URL.Query().Get("fps")); v >= 5 && v <= 60 {
		fps = v
	}
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-evs:
			b, _ := json.Marshal(ev)
			wctx, wc := context.WithTimeout(ctx, 5*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			wc()
			if err != nil {
				return
			}
		case <-tick.C:
			wctx, wc := context.WithTimeout(ctx, 5*time.Second)
			err := c.Write(wctx, websocket.MessageBinary, encodeLive(s.E.Live().Snapshot()))
			wc()
			if err != nil {
				return
			}
		}
	}
}

// demoRecord captures the live stream for a while and returns it as a session file the UI's demo
// mode can play back: a JSON header line, then frames as [u32 length][frame].
func (s *Server) demoRecord(w http.ResponseWriter, r *http.Request) {
	secs, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
	if secs <= 0 || secs > 120 {
		secs = 15
	}
	fps := 20
	snap := s.E.Live().Snapshot()
	hdr, _ := json.Marshal(map[string]any{"format": "hione.live", "version": 1, "freqs": snap.Freqs, "rate": snap.Rate, "fps": fps})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="hione-live.bin"`)
	w.Write(append(hdr, '\n'))
	t := time.NewTicker(time.Second / time.Duration(fps))
	defer t.Stop()
	end := time.After(time.Duration(secs) * time.Second)
	var lb [4]byte
	for {
		select {
		case <-r.Context().Done():
			return
		case <-end:
			return
		case <-t.C:
			f := encodeLive(s.E.Live().Snapshot())
			binary.LittleEndian.PutUint32(lb[:], uint32(len(f)))
			w.Write(lb[:])
			w.Write(f)
		}
	}
}
