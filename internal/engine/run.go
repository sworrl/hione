package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/sworrl/hione/internal/adb"
	"github.com/sworrl/hione/internal/analysis"
	"github.com/sworrl/hione/internal/dsp"
	"github.com/sworrl/hione/internal/stim"
)

// LevelResult is the outcome of the level check.
type LevelResult struct {
	Pass       bool       `json:"pass"`
	ToneDBFS   [2]float64 `json:"tone_dbfs"`
	PeakDBFS   [2]float64 `json:"peak_dbfs"`
	TestLevel  float64    `json:"test_level_dbfs"`
	StepsDown  int        `json:"volume_steps_down"`
	Message    string     `json:"message"`
	DAC        *adb.DAC   `json:"dac,omitempty"`
}

// Level limits: the capture of the test level must peak below MaxPeak and the tone must be
// above MinTone, or the run does not start.
const (
	MaxPeak = -2.0
	MinTone = -50.0
)

// LevelCheck plays a 1 kHz tone at the test level and reads it on the capture. With adb it
// lowers the M500 volume a step at a time until the peak is under MaxPeak. It never raises it.
func (e *Engine) LevelCheck(ctx context.Context, rate int) (*LevelResult, error) {
	plan, _ := stim.Build(stim.Options{Name: "level", Level: e.Cfg.Level, PlayRate: float64(rate)})
	path, err := e.StimFile(plan, rate)
	if err != nil {
		return nil, err
	}
	h, err := e.player.Prepare(path)
	if err != nil {
		return nil, err
	}
	id, c := e.tap()
	defer e.untap(id)
	if err := e.player.Play(h, 0); err != nil {
		return nil, err
	}
	defer e.player.Stop()
	capRate := float64(e.Cfg.CapRate)
	res := &LevelResult{TestLevel: e.Cfg.Level}
	read := func(d time.Duration) ([2][]float64, error) {
		var b [2][]float64
		n := int(d.Seconds() * capRate)
		for len(b[0]) < n {
			select {
			case <-ctx.Done():
				return b, ctx.Err()
			case blk, ok := <-c:
				if !ok {
					return b, errors.New("capture stopped")
				}
				b[0] = append(b[0], blk[0]...)
				b[1] = append(b[1], blk[1]...)
			}
		}
		return b, nil
	}
	e.Logf("Level check: playing 1 kHz at %.0f dBFS.", e.Cfg.Level)
	// Wait for the tone (playback start latency), up to 15 s.
	found := false
	for i := 0; i < 30 && !found; i++ {
		b, err := read(500 * time.Millisecond)
		if err != nil {
			return nil, err
		}
		for ch := 0; ch < 2; ch++ {
			if lv := dsp.LevelAt(b[ch], capRate, 1000); lv > -90 && lv > dsp.DB(dsp.RMS(b[ch])*math.Sqrt2)-3 {
				found = true
			}
		}
	}
	if !found {
		res.Message = "No 1 kHz tone arrived at the capture input. Check the cable, the jack and that the device is playing."
		return res, nil
	}
	for step := 0; ; step++ {
		if _, err := read(700 * time.Millisecond); err != nil { // settle
			return nil, err
		}
		b, err := read(1500 * time.Millisecond)
		if err != nil {
			return nil, err
		}
		for ch := 0; ch < 2; ch++ {
			res.ToneDBFS[ch] = dsp.LevelAt(b[ch], capRate, 1000)
			res.PeakDBFS[ch] = dsp.DB(dsp.Peak(b[ch]))
		}
		pk := math.Max(res.PeakDBFS[0], res.PeakDBFS[1])
		tone := math.Max(res.ToneDBFS[0], res.ToneDBFS[1])
		e.Logf("Level check: tone L %.1f / R %.1f dBFS, peak %.1f dBFS.", res.ToneDBFS[0], res.ToneDBFS[1], pk)
		e.emit(Event{Type: "level", Msg: fmt.Sprintf("%.1f,%.1f", res.ToneDBFS[0], res.ToneDBFS[1])})
		if pk <= MaxPeak {
			if tone < MinTone {
				res.Message = fmt.Sprintf("Too quiet: the tone reads %.1f dBFS at the input. Raise the device volume (the tool never raises it on its own).", tone)
				return res, nil
			}
			res.Pass = true
			res.Message = fmt.Sprintf("Level OK: the test level reads %.1f dBFS at the input, peak %.1f dBFS.", tone, pk)
			if tone < -30 {
				res.Message += " That is on the quiet side, so noise results will be limited by the input. A higher device volume helps."
			}
			return res, nil
		}
		if step >= 20 || !e.player.LowerVolume() {
			res.Message = fmt.Sprintf("Too loud: peak %.1f dBFS at the input, needs %.0f or lower. Turn the device volume down.", pk, MaxPeak)
			return res, nil
		}
		res.StepsDown++
		e.Logf("Too loud for the input, volume down one step.")
	}
}

type stepPlan struct {
	label string
	group string
	dac   adb.DAC
	rate  int
}

func (e *Engine) execute(ctx context.Context, r *Run) error {
	spec := r.Spec
	if spec.Plan == "" {
		spec.Plan = "quick"
	}
	if len(spec.Rates) == 0 {
		spec.Rates = []int{44100}
		if spec.Kind == "matrix" {
			spec.Rates = []int{44100, 96000}
		}
	}
	r.mu.Lock()
	r.Device = e.player.Info()
	r.Spec = spec
	r.mu.Unlock()
	var user adb.DAC
	if e.player.CanSetDAC() {
		d, err := e.player.DAC()
		if err != nil {
			return fmt.Errorf("reading the DAC state: %w", err)
		}
		user = d
		r.mu.Lock()
		r.UserDAC = &user
		r.mu.Unlock()
		e.Logf("Your DAC settings: %s. They are put back at the end.", user)
		defer func() {
			if err := e.player.SetDAC(user); err != nil {
				e.Logf("Could not restore the DAC settings: %v", err)
			} else {
				e.Logf("DAC settings restored: %s.", user)
			}
		}()
	}
	// Steps.
	var steps []stepPlan
	switch spec.Kind {
	case "matrix":
		filters := spec.Filters
		if len(filters) == 0 {
			filters = adb.Filters
		}
		gains := spec.Gains
		if len(gains) == 0 {
			gains = []string{"low", "high"}
		}
		for _, rate := range spec.Rates {
			for _, f := range filters {
				for _, g := range gains {
					d := adb.DAC{Filter: f, Gain: g, DRE: user.DRE, HP: user.HP}
					steps = append(steps, stepPlan{label: fmt.Sprintf("%s, gain %s, %.1f kHz", adb.FilterLabel(f), g, float64(rate)/1000), group: "filters", dac: d, rate: rate})
				}
			}
		}
		if spec.PowerDRE {
			g := user.Gain
			if g == "" {
				g = "high"
			}
			for _, hp := range []string{"hpower_disable", "hpower_enable"} {
				for _, dre := range []string{"dremode_disable", "dremode_enable"} {
					d := adb.DAC{Filter: user.Filter, Gain: g, DRE: dre, HP: hp}
					steps = append(steps, stepPlan{label: fmt.Sprintf("HP %s, DRE %s", onoff(hp == "hpower_enable"), onoff(dre == "dremode_enable")), group: "power", dac: d, rate: spec.Rates[0]})
				}
			}
		}
	case "measure":
		steps = append(steps, stepPlan{label: firstNonEmpty(spec.Label, "measurement"), group: "single", dac: adb.DAC{}, rate: spec.Rates[0]})
	case "level":
	default:
		return fmt.Errorf("unknown run kind %q", spec.Kind)
	}
	r.mu.Lock()
	for i, s := range steps {
		r.Steps = append(r.Steps, &Step{RunID: r.ID, Index: i, Label: s.label, Group: s.group, DAC: s.dac, PlayRate: s.rate, Status: "queued"})
	}
	r.mu.Unlock()
	e.emit(Event{Type: "run", Run: r.summary()})

	// Level check in the loudest setting the run will use.
	if !spec.SkipLevel || spec.Kind == "level" {
		loud := adb.DAC{}
		if e.player.CanSetDAC() {
			loud = user
			for _, s := range steps {
				if s.dac.Gain == "high" {
					loud.Gain = "high"
				}
				if s.dac.HP == "hpower_enable" {
					loud.HP = "hpower_enable"
				}
			}
			if loud != user {
				if err := e.player.SetDAC(loud); err != nil {
					return err
				}
			}
		}
		rate := 44100
		if len(spec.Rates) > 0 {
			rate = spec.Rates[0]
		}
		lr, err := e.LevelCheck(ctx, rate)
		if err != nil {
			return fmt.Errorf("level check: %w", err)
		}
		if e.player.CanSetDAC() {
			lr.DAC = &loud
		}
		r.mu.Lock()
		r.Level = lr
		r.mu.Unlock()
		e.Logf("%s", lr.Message)
		if !lr.Pass {
			return errors.New("level check did not pass: " + lr.Message)
		}
		if lr.StepsDown > 0 {
			r.mu.Lock()
			r.Device["volume"] = e.player.Info()["volume"]
			r.mu.Unlock()
		}
	}

	for i, sp := range steps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st := r.Steps[i]
		r.mu.Lock()
		st.Status = "running"
		st.Started = time.Now()
		r.mu.Unlock()
		e.emit(Event{Type: "step", Step: st})
		e.Logf("Step %d of %d: %s", i+1, len(steps), sp.label)
		err := e.runStep(ctx, r, st, sp)
		r.mu.Lock()
		if err != nil {
			st.Status = "failed"
			st.Error = err.Error()
		} else {
			st.Status = "done"
		}
		r.mu.Unlock()
		if err != nil {
			e.Logf("Step failed: %v", err)
			if errors.Is(err, context.Canceled) {
				return err
			}
		}
		e.emit(Event{Type: "step", Step: st})
		e.emit(Event{Type: "run", Run: r.summary()})
		e.saveStepFiles(r, st)
	}
	return nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (e *Engine) runStep(ctx context.Context, r *Run, st *Step, sp stepPlan) error {
	if e.player.CanSetDAC() && sp.dac != (adb.DAC{}) {
		if err := e.player.SetDAC(sp.dac); err != nil {
			return err
		}
		time.Sleep(400 * time.Millisecond)
	} else if sp.dac != (adb.DAC{}) {
		if err := e.player.SetDAC(sp.dac); err != nil {
			return err
		}
	}
	if d, err := e.player.DAC(); err == nil && d != (adb.DAC{}) {
		r.mu.Lock()
		st.DAC = d
		r.mu.Unlock()
	}
	plan, err := stim.Build(stim.Options{Name: r.Spec.Plan, Level: e.Cfg.Level, PlayRate: float64(sp.rate)})
	if err != nil {
		return err
	}
	path, err := e.StimFile(plan, sp.rate)
	if err != nil {
		return err
	}
	h, err := e.player.Prepare(path)
	if err != nil {
		return err
	}
	timeout := time.Duration(plan.Total*float64(time.Second)) + 25*time.Second
	if _, ok := e.player.(*manualPlayer); ok {
		timeout += 5 * time.Minute
	}
	lastEmit := time.Time{}
	rec, err := e.recordPlan(ctx, plan, timeout, func() error { return e.player.Play(h, 0) }, func(phase string, f float64) {
		if time.Since(lastEmit) > 250*time.Millisecond {
			lastEmit = time.Now()
			e.emit(Event{Type: "progress", Progress: &Progress{RunID: r.ID, Label: sp.label, Phase: phase, Fraction: f, Segment: segmentAt(plan, f)}})
		}
	})
	e.player.Stop()
	if err != nil && !errors.Is(err, ErrNoSignal) {
		return err
	}
	// The DAC must still be what was asked: Miku Music re-applies the user's policy on some
	// starts, and that would make the step measure the wrong mode.
	if e.player.CanSetDAC() && sp.dac != (adb.DAC{}) {
		if d, derr := e.player.DAC(); derr == nil && !adb.Matches(d, sp.dac) {
			return fmt.Errorf("the DAC changed during the recording (now %s)", d)
		}
	}
	e.emit(Event{Type: "progress", Progress: &Progress{RunID: r.ID, Label: sp.label, Phase: "analyzing", Fraction: 1}})
	res := analysis.Analyze(rec, plan, analysis.Options{PlayRate: float64(sp.rate), CapRate: float64(e.Cfg.CapRate), DBVAtFS: e.Cfg.DBVAtFS, Loopback: e.Cfg.Loopback})
	Sanitize(res)
	r.mu.Lock()
	st.Result = res
	r.mu.Unlock()
	if errors.Is(err, ErrNoSignal) || !res.Found {
		msg := "the test signal never arrived at the capture input"
		if len(res.IdleNoise) > 0 {
			msg += fmt.Sprintf(" (idle noise L %.1f / R %.1f dBFS)", res.IdleNoise[0].DBFS, res.IdleNoise[min(1, len(res.IdleNoise)-1)].DBFS)
		}
		return errors.New(msg)
	}
	L := res.Ch[0]
	e.Logf("  level %.1f dBFS, THD+N %.1f dB, noise %.1f dBFS(A), drift %.1f ppm", L.Tone.LevelDBFS, L.Tone.THDN, L.Noise.DBFSA, res.DriftPPM)
	return nil
}

func segmentAt(p *stim.Plan, f float64) string {
	t := p.SyncLead + f*(p.Total-p.SyncLead)
	for _, s := range p.Segments {
		if t >= s.Start && t < s.Start+s.Dur && s.Kind != stim.Silence {
			return s.Name
		}
		if t >= s.Start && t < s.Start+s.Dur && s.Tag == "noise" {
			return s.Name
		}
	}
	return ""
}

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// saveStepFiles writes the step's JSON and CSVs.
func (e *Engine) saveStepFiles(r *Run, st *Step) {
	if st.Result == nil {
		return
	}
	dir := filepath.Join(r.Dir, fmt.Sprintf("step%02d", st.Index+1))
	os.MkdirAll(dir, 0o755)
	b, _ := json.MarshalIndent(st, "", " ")
	os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644)
	if StepCSVHook != nil {
		StepCSVHook(dir, st)
	}
}

// StepCSVHook writes CSV exports for a step (set by the report package user).
var StepCSVHook func(dir string, st *Step)

// Calibrate measures the capture chain alone (PC output into PC input) with the quick plan,
// played through playFn, and returns the chain calibration.
func (e *Engine) Calibrate(ctx context.Context, playFn func(path string) error) (*analysis.ChainCal, *analysis.Result, error) {
	plan, err := stim.Build(stim.Options{Name: "quick", Level: e.Cfg.Level, PlayRate: float64(e.Cfg.CapRate)})
	if err != nil {
		return nil, nil, err
	}
	path, err := e.StimFile(plan, e.Cfg.CapRate)
	if err != nil {
		return nil, nil, err
	}
	errc := make(chan error, 1)
	rec, err := e.recordPlan(ctx, plan, time.Duration(plan.Total*float64(time.Second))+20*time.Second, func() error {
		go func() { errc <- playFn(path) }()
		return nil
	}, nil)
	if err != nil {
		select {
		case perr := <-errc:
			if perr != nil {
				return nil, nil, perr
			}
		default:
		}
		return nil, nil, err
	}
	res := analysis.Analyze(rec, plan, analysis.Options{PlayRate: float64(e.Cfg.CapRate), CapRate: float64(e.Cfg.CapRate), DBVAtFS: math.NaN()})
	if !res.Found {
		return nil, res, errors.New(res.Message)
	}
	L := res.Ch[0]
	cal := &analysis.ChainCal{Rate: float64(e.Cfg.CapRate), FR: L.FR, NoiseDBFS: L.Noise.DBFS, NoiseA: L.Noise.DBFSA,
		THDN: L.Tone.THDN, Level: L.Tone.LevelDBFS, Device: e.Cfg.Capture, When: time.Now().Format(time.RFC3339)}
	return cal, res, nil
}
