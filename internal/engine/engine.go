// Package engine runs measurements: it keeps the capture running (for the live meters and
// spectrum), drives the device under test (M500 over adb, a person pressing play, or the
// simulator), records each test signal by its sync marker, analyzes it and writes the results.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sworrl/hione/internal/adb"
	"github.com/sworrl/hione/internal/analysis"
	"github.com/sworrl/hione/internal/capture"
	"github.com/sworrl/hione/internal/dsp"
	"github.com/sworrl/hione/internal/sim"
	"github.com/sworrl/hione/internal/stim"
	"github.com/sworrl/hione/internal/wav"
)

// Config for an engine.
type Config struct {
	Capture   string // capture URI (ssh://host/hw:0,0, alsa:hw:1,0, pw:node, sim)
	CapRate   int
	CapFormat wav.Format
	OutDir    string
	Device    string // "m500" (adb), "manual" or "sim"
	ADBSerial string
	Level     float64 // test level, dBFS
	NoMixer   bool    // leave the capture card's mixer alone
	DBVAtFS   float64 // capture calibration: dBV = dBFS + DBVAtFS (NaN = none)
	Loopback  *analysis.ChainCal
	Console   bool // print the log to stdout
	Prompt    func(msg string) error // manual mode: show msg and wait for the person
}

// Event goes to the UI.
type Event struct {
	Type string `json:"type"` // log, run, step, progress
	Time time.Time `json:"time"`
	Msg  string `json:"msg,omitempty"`
	Run  *RunSummary `json:"run,omitempty"`
	Step *Step `json:"step,omitempty"`
	Progress *Progress `json:"progress,omitempty"`
}

// Progress of the current recording.
type Progress struct {
	RunID   string  `json:"run_id"`
	Label   string  `json:"label"`
	Phase   string  `json:"phase"` // waiting, recording, analyzing
	Fraction float64 `json:"fraction"`
	Segment string  `json:"segment,omitempty"`
}

// Engine owns the capture and the device.
type Engine struct {
	Cfg    Config
	parsed capture.Parsed
	mixer  *capture.Mixer
	MixerInfo string
	stream *capture.Stream
	live   *Live

	tapMu sync.Mutex
	taps  map[int]chan [][]float64
	tapN  int

	evMu  sync.Mutex
	subs  map[int]chan Event
	subN  int
	logs  []Event

	Dev    *adb.Device
	player Player
	simDev *simDevice

	poll devPoller

	runMu   sync.Mutex
	runs    map[string]*Run
	order   []string
	current *Run
	capErr  string
}

// New prepares an engine; Start begins capturing.
func New(cfg Config) (*Engine, error) {
	if cfg.CapRate == 0 {
		cfg.CapRate = 192000
	}
	if cfg.CapFormat == "" {
		cfg.CapFormat = wav.S32
	}
	if cfg.Level == 0 {
		cfg.Level = -12
	}
	if cfg.Level > -6 {
		return nil, fmt.Errorf("test level %.1f dBFS is above the -6 dBFS cap", cfg.Level)
	}
	p, err := capture.Parse(cfg.Capture)
	if err != nil {
		return nil, err
	}
	e := &Engine{Cfg: cfg, parsed: p, taps: map[int]chan [][]float64{}, subs: map[int]chan Event{},
		runs: map[string]*Run{}, live: newLive(float64(cfg.CapRate))}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, err
	}
	switch cfg.Device {
	case "m500", "adb":
		d, err := adb.Find(cfg.ADBSerial)
		if err != nil {
			return nil, err
		}
		e.Dev = d
		e.player = &adbPlayer{d: d}
	case "sim":
		e.simDev = newSimDevice(float64(cfg.CapRate))
		e.player = e.simDev
	default:
		e.player = &manualPlayer{e: e}
	}
	if p.Kind == capture.KindSim && e.simDev == nil {
		return nil, errors.New("sim capture needs --device sim")
	}
	return e, nil
}

// Live returns the live view.
func (e *Engine) Live() *Live { return e.live }

// Start sets up the mixer and starts the capture.
func (e *Engine) Start(ctx context.Context) error {
	if !e.Cfg.NoMixer {
		m, info, err := capture.SetupLineIn(e.parsed)
		if err != nil {
			return fmt.Errorf("mixer setup: %w", err)
		}
		e.mixer = m
		e.MixerInfo = info
		if info != "" {
			e.Logf("Capture mixer set for line input (saved state is restored on exit).")
		}
	}
	block := e.Cfg.CapRate / 20
	var st *capture.Stream
	if e.parsed.Kind == capture.KindSim {
		st = capture.Synthetic(ctx, e.Cfg.CapRate, block, true, e.simDev.gen)
	} else {
		var err error
		st, err = capture.Start(ctx, capture.Spec{URI: e.Cfg.Capture, Rate: e.Cfg.CapRate, Channels: 2, Format: e.Cfg.CapFormat}, block)
		if err != nil {
			e.mixer.Restore()
			return err
		}
	}
	e.stream = st
	go e.pump()
	if e.Dev != nil {
		go e.pollDevice()
	}
	// Fail fast if the tool dies at once (wrong device, busy card).
	select {
	case <-st.Done():
		e.mixer.Restore()
		if err := st.Err(); err != nil {
			return err
		}
		return errors.New("capture ended at once")
	case <-time.After(800 * time.Millisecond):
	}
	e.Logf("Capturing %s at %d Hz %s.", e.Cfg.Capture, e.Cfg.CapRate, e.Cfg.CapFormat)
	return nil
}

func (e *Engine) pump() {
	for blk := range e.stream.C {
		e.live.feed(blk)
		e.tapMu.Lock()
		for _, t := range e.taps {
			select {
			case t <- blk:
			case <-time.After(2 * time.Second):
			}
		}
		e.tapMu.Unlock()
	}
	if err := e.stream.Err(); err != nil {
		e.capErr = err.Error()
		e.Logf("%v", err)
	}
	e.tapMu.Lock()
	for id, t := range e.taps {
		close(t)
		delete(e.taps, id)
	}
	e.tapMu.Unlock()
}

// Close stops the capture and restores the mixer.
func (e *Engine) Close() {
	if e.stream != nil {
		e.stream.Stop()
	}
	if e.mixer != nil {
		if err := e.mixer.Restore(); err != nil {
			e.Logf("Mixer restore failed: %v", err)
		} else {
			e.Logf("Capture mixer restored.")
		}
	}
}

func (e *Engine) tap() (int, chan [][]float64) {
	e.tapMu.Lock()
	defer e.tapMu.Unlock()
	e.tapN++
	c := make(chan [][]float64, 256)
	e.taps[e.tapN] = c
	return e.tapN, c
}

func (e *Engine) untap(id int) {
	e.tapMu.Lock()
	defer e.tapMu.Unlock()
	if c, ok := e.taps[id]; ok {
		delete(e.taps, id)
		// Drain so the pump never blocks on it.
		go func() {
			for range c {
			}
		}()
		close(c)
	}
}

// Logf records a log line and sends it to the UI (and stdout in console mode).
func (e *Engine) Logf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if e.Cfg.Console {
		fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), msg)
	}
	e.emit(Event{Type: "log", Msg: msg})
}

func (e *Engine) emit(ev Event) {
	ev.Time = time.Now()
	e.evMu.Lock()
	defer e.evMu.Unlock()
	if ev.Type == "log" {
		e.logs = append(e.logs, ev)
		if len(e.logs) > 500 {
			e.logs = e.logs[len(e.logs)-500:]
		}
	}
	for _, c := range e.subs {
		select {
		case c <- ev:
		default:
		}
	}
}

// Subscribe returns a channel of events and a cancel function.
func (e *Engine) Subscribe() (<-chan Event, func()) {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	e.subN++
	id := e.subN
	c := make(chan Event, 256)
	e.subs[id] = c
	return c, func() {
		e.evMu.Lock()
		defer e.evMu.Unlock()
		delete(e.subs, id)
	}
}

// Logs returns recent log lines.
func (e *Engine) Logs() []Event {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	return append([]Event(nil), e.logs...)
}

// CaptureError is the capture's failure, if it stopped.
func (e *Engine) CaptureError() string { return e.capErr }

// ---- stimulus files ----

// StimFile renders (once) the plan at rate and returns the local path.
func (e *Engine) StimFile(plan *stim.Plan, rate int) (string, error) {
	dir := filepath.Join(e.Cfg.OutDir, "stimuli")
	os.MkdirAll(dir, 0o755)
	name := fmt.Sprintf("hione-v2-%s-%.0fdB-%d.wav", plan.Name, -plan.Level, rate)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	data := plan.Render(float64(rate), 24)
	if err := wav.Write(path+".tmp", &wav.Audio{Rate: rate, Format: wav.S24, Data: data}); err != nil {
		return "", err
	}
	return path, os.Rename(path+".tmp", path)
}

// ---- recording by sync marker ----

// ErrNoSignal means the sync marker never showed up.
var ErrNoSignal = errors.New("the test signal never arrived")

// recordPlan starts playback with play() and records until the whole plan has been captured,
// located by the start sync. progress gets the fraction recorded.
func (e *Engine) recordPlan(ctx context.Context, plan *stim.Plan, timeout time.Duration, play func() error, progress func(phase string, f float64)) ([][]float64, error) {
	rate := float64(e.Cfg.CapRate)
	id, c := e.tap()
	defer e.untap(id)
	if err := play(); err != nil {
		return nil, err
	}
	ref := stim.SyncSweep(plan.Level).Render(rate, 1)
	keep := int(4 * rate)
	var buf [2][]float64
	offset := 0 // absolute index of buf[0][0]
	lastCheck := 0
	syncAbs := -1.0
	need := 0
	deadline := time.After(timeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			if syncAbs < 0 {
				return [][]float64{buf[0], buf[1]}, ErrNoSignal
			}
			return [][]float64{buf[0], buf[1]}, errors.New("recording timed out")
		case blk, ok := <-c:
			if !ok {
				return nil, fmt.Errorf("capture stopped: %s", e.capErr)
			}
			buf[0] = append(buf[0], blk[0]...)
			buf[1] = append(buf[1], blk[1]...)
			total := offset + len(buf[0])
			if syncAbs < 0 {
				if total-lastCheck >= int(0.25*rate) && len(buf[0]) > len(ref)+int(0.3*rate) {
					lastCheck = total
					win := min(len(buf[0]), int(2.5*rate))
					a := len(buf[0]) - win
					best, conf := 0.0, 0.0
					for ch := 0; ch < 2; ch++ {
						p, cf := dsp.FindMarker(buf[ch][a:], ref)
						if cf > conf {
							best, conf = p, cf
						}
					}
					// Accept only when the whole marker is inside the window.
					if conf >= analysis.MinConfidence && best+float64(len(ref)) < float64(win)-0.05*rate {
						syncAbs = float64(offset+a) + best
						start := int(syncAbs - (plan.SyncLead+0.3)*rate)
						if start > offset {
							cut := start - offset
							buf[0] = append([]float64(nil), buf[0][cut:]...)
							buf[1] = append([]float64(nil), buf[1][cut:]...)
							offset = start
						}
						// 3 s spare: playback may restart once near the start.
						need = int(syncAbs+(plan.Total-plan.SyncLead+3)*rate*1.001) - offset
						e.Logf("Test signal found (sync confidence %.0f).", conf)
					}
				}
				if syncAbs < 0 && len(buf[0]) > keep {
					cut := len(buf[0]) - keep
					buf[0] = append([]float64(nil), buf[0][cut:]...)
					buf[1] = append([]float64(nil), buf[1][cut:]...)
					offset += cut
				}
				if progress != nil {
					progress("waiting", 0)
				}
			} else {
				if progress != nil {
					progress("recording", math.Min(1, float64(len(buf[0]))/float64(need)))
				}
				if len(buf[0]) >= need {
					return [][]float64{buf[0], buf[1]}, nil
				}
			}
		}
	}
}

// ---- runs ----

// RunSpec says what to run.
type RunSpec struct {
	Kind      string   `json:"kind"` // measure, matrix, level
	Plan      string   `json:"plan"` // quick or full
	Rates     []int    `json:"rates"`
	Filters   []string `json:"filters"`
	Gains     []string `json:"gains"`
	PowerDRE  bool     `json:"power_dre"` // add the high power x DRE runs at the user's filter
	Jack      string   `json:"jack"`
	Label     string   `json:"label"`
	SkipLevel bool     `json:"skip_level"`
}

// Step is one measured configuration.
type Step struct {
	RunID    string           `json:"run_id"`
	Index    int              `json:"index"`
	Label    string           `json:"label"`
	Group    string           `json:"group"` // filters, power
	DAC      adb.DAC          `json:"dac"`
	PlayRate int              `json:"play_rate"`
	Status   string           `json:"status"`
	Error    string           `json:"error,omitempty"`
	Result   *analysis.Result `json:"result,omitempty"`
	Started  time.Time        `json:"started"`
}

// Run is a matrix or single measurement.
type Run struct {
	ID       string    `json:"id"`
	Spec     RunSpec   `json:"spec"`
	Status   string    `json:"status"` // running, done, failed, stopped
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
	Dir      string    `json:"dir"`
	Steps    []*Step   `json:"steps"`
	UserDAC  *adb.DAC  `json:"user_dac,omitempty"`
	Level    *LevelResult `json:"level,omitempty"`
	Device   map[string]string `json:"device,omitempty"`
	Capture  map[string]string `json:"capture"`
	cancel   context.CancelFunc
	mu       sync.Mutex
}

// RunSummary is the light version for lists and events.
type RunSummary struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Steps   int       `json:"steps"`
	Done    int       `json:"done"`
	Started time.Time `json:"started"`
}

func (r *Run) summary() *RunSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := 0
	for _, s := range r.Steps {
		if s.Status == "done" || s.Status == "failed" {
			d++
		}
	}
	return &RunSummary{ID: r.ID, Kind: r.Spec.Kind, Status: r.Status, Error: r.Error, Steps: len(r.Steps), Done: d, Started: r.Started}
}

// Runs lists runs, newest first.
func (e *Engine) Runs() []*RunSummary {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	var out []*RunSummary
	for i := len(e.order) - 1; i >= 0; i-- {
		out = append(out, e.runs[e.order[i]].summary())
	}
	return out
}

// GetRun returns a run (JSON-safe copy under its lock).
func (e *Engine) GetRun(id string) *Run {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.runs[id]
}

// MarshalRun encodes a run safely.
func MarshalRun(r *Run) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	Sanitize(r)
	return json.MarshalIndent(r, "", " ")
}

// Busy reports whether a run is in progress.
func (e *Engine) Busy() bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.current != nil
}

// StopRun cancels the current run.
func (e *Engine) StopRun() bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.current != nil && e.current.cancel != nil {
		e.current.cancel()
		return true
	}
	return false
}

// StartRun launches a run in the background and returns it.
func (e *Engine) StartRun(spec RunSpec) (*Run, error) {
	e.runMu.Lock()
	if e.current != nil {
		e.runMu.Unlock()
		return nil, errors.New("a run is already in progress")
	}
	id := time.Now().Format("20060102-150405")
	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{ID: id, Spec: spec, Status: "running", Started: time.Now(), cancel: cancel,
		Dir: filepath.Join(e.Cfg.OutDir, "runs", id),
		Capture: map[string]string{"uri": e.Cfg.Capture, "rate": fmt.Sprint(e.Cfg.CapRate), "format": string(e.Cfg.CapFormat)}}
	os.MkdirAll(r.Dir, 0o755)
	e.runs[id] = r
	e.order = append(e.order, id)
	e.current = r
	e.runMu.Unlock()
	go func() {
		err := e.execute(ctx, r)
		r.mu.Lock()
		r.Finished = time.Now()
		switch {
		case errors.Is(err, context.Canceled):
			r.Status = "stopped"
		case err != nil:
			r.Status = "failed"
			r.Error = err.Error()
		default:
			r.Status = "done"
		}
		r.mu.Unlock()
		if err != nil {
			e.Logf("Run %s: %v", r.ID, err)
		} else {
			e.Logf("Run %s finished.", r.ID)
		}
		e.saveRun(r)
		e.emit(Event{Type: "run", Run: r.summary()})
		e.runMu.Lock()
		e.current = nil
		e.runMu.Unlock()
	}()
	e.emit(Event{Type: "run", Run: r.summary()})
	return r, nil
}

// RunSync runs and waits (CLI).
func (e *Engine) RunSync(spec RunSpec) (*Run, error) {
	r, err := e.StartRun(spec)
	if err != nil {
		return nil, err
	}
	for {
		time.Sleep(200 * time.Millisecond)
		if !e.Busy() {
			break
		}
	}
	if r.Status == "failed" {
		return r, errors.New(r.Error)
	}
	return r, nil
}

func (e *Engine) saveRun(r *Run) {
	b, err := MarshalRun(r)
	if err == nil {
		os.WriteFile(filepath.Join(r.Dir, "run.json"), b, 0o644)
	}
	if ReportHook != nil {
		if err := ReportHook(r); err != nil {
			e.Logf("Report: %v", err)
		} else {
			e.Logf("Report written to %s", r.Dir)
		}
	}
}

// ReportHook writes the report files for a finished run (set by the report package user).
var ReportHook func(*Run) error

// ---- sim device ----

type simDevice struct {
	mu      sync.Mutex
	capRate float64
	playing [][]float64
	pos     int
	dac     adb.DAC
	rng     *rand.Rand
	phase   int
	file    map[string]*wav.Audio
}

func newSimDevice(capRate float64) *simDevice {
	return &simDevice{capRate: capRate, rng: rand.New(rand.NewSource(11)), file: map[string]*wav.Audio{},
		dac: adb.DAC{Filter: "fast_rolloff_low_latency", Gain: "high", DRE: "dremode_enable", HP: "hpower_disable"}}
}

func (s *simDevice) model() sim.Model {
	m := sim.Default(s.capRate)
	m.Delay = 0.3
	m.Hum = 0
	switch s.dac.Filter {
	case "slow_rolloff_low_latency", "slow_rolloff_phase_compensated":
		m.Filters = append(m.Filters, dsp.LowPass(s.capRate, 17000, 0.55))
	case "nos":
		m.ZOH = true
	}
	if s.dac.Gain == "low" {
		m.GainDB -= 12
	}
	if s.dac.HP == "hpower_enable" {
		m.GainDB += 1.5
		m.NoiseDBFS += 3
	}
	if s.dac.DRE != "dremode_enable" {
		m.NoiseDBFS += 4
	}
	return m
}

func (s *simDevice) gen(blk [][]float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(blk[0])
	idle := s.model().NoiseOnly(n, s.capRate, s.rng, s.phase)
	s.phase += n
	for c := 0; c < 2; c++ {
		for i := 0; i < n; i++ {
			v := idle[c][i]
			if s.playing != nil && s.pos+i < len(s.playing[c]) {
				v = s.playing[c][s.pos+i]
			}
			blk[c][i] = v
		}
	}
	if s.playing != nil {
		s.pos += n
		if s.pos >= len(s.playing[0]) {
			s.playing = nil
		}
	}
}

func (s *simDevice) Name() string { return "simulated device" }
func (s *simDevice) Prepare(local string) (string, error) {
	a, err := wav.Read(local)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.file[local] = a
	s.mu.Unlock()
	return local, nil
}
func (s *simDevice) Play(h string, startMs int64) error {
	s.mu.Lock()
	a := s.file[h]
	m := s.model()
	s.mu.Unlock()
	if a == nil {
		return errors.New("not prepared")
	}
	out := m.Apply(a.Data, float64(a.Rate), s.capRate)
	s.mu.Lock()
	s.playing, s.pos = out, 0
	s.mu.Unlock()
	return nil
}
func (s *simDevice) Stop() error {
	s.mu.Lock()
	s.playing = nil
	s.mu.Unlock()
	return nil
}
func (s *simDevice) SetDAC(d adb.DAC) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Filter != "" {
		s.dac.Filter = d.Filter
	}
	if d.Gain != "" {
		s.dac.Gain = d.Gain
	}
	if d.DRE != "" {
		s.dac.DRE = d.DRE
	}
	if d.HP != "" {
		s.dac.HP = d.HP
	}
	return nil
}
func (s *simDevice) DAC() (adb.DAC, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dac, nil
}
func (s *simDevice) CanSetDAC() bool       { return true }
func (s *simDevice) LowerVolume() bool      { return false }
func (s *simDevice) Info() map[string]string { return map[string]string{"device": "simulated"} }
