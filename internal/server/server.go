// Package server is the local web UI and its JSON API. Everything the page does goes through the
// API, so a script can drive and watch a run the same way.
package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sworrl/hione/internal/adb"
	"github.com/sworrl/hione/internal/capture"
	"github.com/sworrl/hione/internal/engine"
	"github.com/sworrl/hione/internal/plot"
	"github.com/sworrl/hione/internal/report"
)

//go:embed static ui
var static embed.FS

// Server wraps an engine.
type Server struct {
	E    *engine.Engine
	Auth *Auth
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(v)
}

func errJSON(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	classic, _ := fs.Sub(static, "static")
	ui, _ := fs.Sub(static, "ui")
	mux.Handle("GET /classic/", http.StripPrefix("/classic/", http.FileServer(http.FS(classic))))
	mux.Handle("GET /", http.FileServer(http.FS(ui)))
	mux.HandleFunc("GET /api/ws", s.ws)
	mux.HandleFunc("GET /api/demo/record", s.demoRecord)
	mux.HandleFunc("GET /api/pairing", func(w http.ResponseWriter, r *http.Request) {
		if !s.Auth.SameOrigin(r) {
			errJSON(w, 403, fmt.Errorf("only the local page can read the pairing token"))
			return
		}
		writeJSON(w, 200, map[string]any{"token": s.Auth.Token, "allow_origins": s.Auth.AllowOrigins, "loopback": s.Auth.Loopback})
	})
	mux.HandleFunc("POST /api/pairing/reset", func(w http.ResponseWriter, r *http.Request) {
		if !s.Auth.SameOrigin(r) {
			errJSON(w, 403, fmt.Errorf("only the local page can reset pairing"))
			return
		}
		if err := s.Auth.Reset(); err != nil {
			errJSON(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]string{"token": s.Auth.Token})
	})

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		st := map[string]any{
			"capture":       s.E.Cfg.Capture,
			"cap_rate":      s.E.Cfg.CapRate,
			"cap_format":    s.E.Cfg.CapFormat,
			"device":        s.E.Cfg.Device,
			"level_dbfs":    s.E.Cfg.Level,
			"busy":          s.E.Busy(),
			"capture_error": s.E.CaptureError(),
			"runs":          s.E.Runs(),
			"logs":          s.E.Logs(),
			"filters":       adb.Filters,
		}
		if ds := s.E.DevStatus(); ds != nil {
			st["device_status"] = ds
		}
		writeJSON(w, 200, st)
	})
	mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.E.Live().Snapshot())
	})
	mux.HandleFunc("GET /api/spectrogram", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("rows"))
		if n <= 0 {
			n = 200
		}
		writeJSON(w, 200, map[string]any{"freqs": s.E.Live().Snapshot().Freqs, "rows": s.E.Live().Spectrogram(n)})
	})
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		var hosts []string
		if strings.HasPrefix(s.E.Cfg.Capture, "ssh://") {
			if p, err := capture.Parse(s.E.Cfg.Capture); err == nil {
				hosts = append(hosts, p.Host)
			}
		}
		ds, notes := capture.List(hosts)
		writeJSON(w, 200, map[string]any{"devices": ds, "notes": notes})
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.E.Runs())
	})
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		var spec engine.RunSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			errJSON(w, 400, fmt.Errorf("bad JSON: %w", err))
			return
		}
		if spec.Kind == "" {
			spec.Kind = "measure"
		}
		run, err := s.E.StartRun(spec)
		if err != nil {
			errJSON(w, 409, err)
			return
		}
		writeJSON(w, 202, map[string]string{"id": run.ID, "status": "running"})
	})
	mux.HandleFunc("POST /api/level-check", func(w http.ResponseWriter, r *http.Request) {
		spec := engine.RunSpec{Kind: "level"}
		if r.ContentLength > 0 {
			json.NewDecoder(r.Body).Decode(&spec)
			spec.Kind = "level"
		}
		run, err := s.E.StartRun(spec)
		if err != nil {
			errJSON(w, 409, err)
			return
		}
		writeJSON(w, 202, map[string]string{"id": run.ID, "status": "running"})
	})
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"stopped": s.E.StopRun()})
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		run := s.E.GetRun(r.PathValue("id"))
		if run == nil {
			run = loadRun(s.E.Cfg.OutDir, r.PathValue("id"))
		}
		if run == nil {
			errJSON(w, 404, fmt.Errorf("no run %s", r.PathValue("id")))
			return
		}
		b, err := engine.MarshalRun(run)
		if err != nil {
			errJSON(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	runFile := func(w http.ResponseWriter, r *http.Request, name string) {
		id := r.PathValue("id")
		if strings.ContainsAny(id, "/\\") || strings.Contains(name, "..") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.E.Cfg.OutDir, "runs", id, filepath.FromSlash(name)))
	}
	mux.HandleFunc("GET /api/report/{id}", func(w http.ResponseWriter, r *http.Request) { runFile(w, r, "report.html") })
	mux.HandleFunc("GET /api/runs/{id}/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		runFile(w, r, r.PathValue("path"))
	})
	mux.HandleFunc("GET /api/runs/{id}/charts", func(w http.ResponseWriter, r *http.Request) {
		run := s.run(r.PathValue("id"))
		if run == nil {
			errJSON(w, 404, fmt.Errorf("no run %s", r.PathValue("id")))
			return
		}
		writeJSON(w, 200, report.Charts(run))
	})
	mux.HandleFunc("GET /api/runs/{id}/charts/{file}", func(w http.ResponseWriter, r *http.Request) {
		run := s.run(r.PathValue("id"))
		if run == nil {
			errJSON(w, 404, fmt.Errorf("no run %s", r.PathValue("id")))
			return
		}
		file := r.PathValue("file")
		ext := filepath.Ext(file)
		cs, ok := report.FindChart(run, strings.TrimSuffix(file, ext))
		if !ok {
			errJSON(w, 404, fmt.Errorf("no chart %s", file))
			return
		}
		q := r.URL.Query()
		o := report.Opts{Theme: plot.ThemeByName(q.Get("theme")), Stamp: q.Get("stamp") != "0", Mark: q.Get("mark") != "0", Transparent: q.Get("bg") == "transparent"}
		switch ext {
		case ".svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write(report.SVG(cs, o))
		case ".png":
			scale, _ := strconv.ParseFloat(q.Get("scale"), 64)
			if scale != 1 && scale != 2 && scale != 4 {
				scale = 2
			}
			b, err := report.PNG(cs, o, scale)
			if err != nil {
				errJSON(w, 500, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(b)
		default:
			errJSON(w, 400, fmt.Errorf("use .svg or .png"))
		}
	})
	mux.HandleFunc("GET /api/runs/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		run := s.run(r.PathValue("id"))
		if run == nil {
			errJSON(w, 404, fmt.Errorf("no run %s", r.PathValue("id")))
			return
		}
		q := r.URL.Query()
		th := plot.ThemeByName(q.Get("theme"))
		switch q.Get("format") {
		case "pdf", "":
			b, err := report.PDF(run, th)
			if err != nil {
				errJSON(w, 500, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s_report_%s.pdf"`, report.FileBase(run), run.ID))
			w.Write(b)
		case "zip":
			b, err := report.Zip(run, th, q.Get("mark") != "0")
			if err != nil {
				errJSON(w, 500, err)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s_%s.zip"`, report.FileBase(run), run.ID))
			w.Write(b)
		default:
			errJSON(w, 400, fmt.Errorf("format is pdf or zip"))
		}
	})
	mux.HandleFunc("GET /api/events", s.events)
	return s.Auth.Wrap(mux)
}

func (s *Server) run(id string) *engine.Run {
	if r := s.E.GetRun(id); r != nil {
		return r
	}
	return loadRun(s.E.Cfg.OutDir, id)
}

func loadRun(out, id string) *engine.Run {
	if strings.ContainsAny(id, "/\\.") {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(out, "runs", id, "run.json"))
	if err != nil {
		return nil
	}
	var r engine.Run
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	r.Dir = filepath.Join(out, "runs", id)
	return &r
}

// events streams engine events and a live snapshot about 8 times a second (Server-Sent Events).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := s.E.Subscribe()
	defer cancel()
	tick := time.NewTicker(125 * time.Millisecond)
	defer tick.Stop()
	send := func(kind string, v any) bool {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !send(ev.Type, ev) {
				return
			}
		case <-tick.C:
			if !send("live", s.E.Live().Snapshot()) {
				return
			}
		}
	}
}
