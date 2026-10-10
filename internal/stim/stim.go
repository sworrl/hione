// Package stim defines the test signal: a plan of timed segments (sync sweeps, the measurement
// sweep, tones, impulses, silence) described in continuous time, so it can be rendered at the
// playback rate for the device and re-rendered at the capture rate as an analysis reference.
package stim

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/sworrl/hione/internal/dsp"
)

// Kind of segment.
type Kind string

const (
	Silence   Kind = "silence"
	Sync      Kind = "sync"
	SweepK    Kind = "sweep"
	Tone      Kind = "tone"
	Impulses  Kind = "impulses"
	Multitone Kind = "multitone"
)

// Segment is one timed part of the plan.
type Segment struct {
	Kind  Kind      `json:"kind"`
	Name  string    `json:"name"`
	Start float64   `json:"start_s"`
	Dur   float64   `json:"dur_s"`
	Chan  string    `json:"chan"`            // both, L, R
	Level float64   `json:"level_dbfs"`      // peak level of the whole segment
	Freqs []float64 `json:"freqs,omitempty"` // tone or multitone frequencies
	Ratio []float64 `json:"ratio,omitempty"` // multitone amplitude ratios (sum normalized to Level)
	Sweep *dsp.Sweep `json:"sweep,omitempty"`
	Count int       `json:"count,omitempty"` // impulses
	Every float64   `json:"every_s,omitempty"`
	Tag   string    `json:"tag,omitempty"` // what the analysis does with it: thd, dr, xtalk-l, ...
}

// Plan is the whole test signal.
type Plan struct {
	Name     string    `json:"name"`
	Level    float64   `json:"level_dbfs"`
	Segments []Segment `json:"segments"`
	Total    float64   `json:"total_s"`
	SyncLead float64   `json:"sync_lead_s"` // start of the first sync
	SyncTail float64   `json:"sync_tail_s"` // start of the end sync
}

// Options for building a plan.
type Options struct {
	Name     string  // quick, full, level
	Level    float64 // dBFS, peak level of the loudest segment
	PlayRate float64 // device playback rate (bounds the sweep's top frequency)
	SweepLen float64 // seconds
}

// SyncSweep is the short marker sweep at start and end.
func SyncSweep(level float64) dsp.Sweep {
	return dsp.Sweep{F1: 300, F2: 6000, T: 0.5, Amp: dsp.FromDB(level), Fade: 0.02}
}

// EndSync is the marker at the end: a downward sweep, so a capture that missed the start can
// never mistake it for the start marker.
func EndSync(level float64) dsp.Sweep {
	return dsp.Sweep{F1: 6000, F2: 300, T: 0.5, Amp: dsp.FromDB(level), Fade: 0.02}
}

// Build makes a plan.
func Build(o Options) (*Plan, error) {
	if o.Level > 0 {
		return nil, fmt.Errorf("level %.1f dBFS is above full scale", o.Level)
	}
	if o.SweepLen == 0 {
		o.SweepLen = 6
	}
	p := &Plan{Name: o.Name, Level: o.Level}
	t := 0.0
	add := func(s Segment) {
		s.Start = t
		if s.Chan == "" {
			s.Chan = "both"
		}
		p.Segments = append(p.Segments, s)
		t += s.Dur
	}
	gap := func(d float64) { add(Segment{Kind: Silence, Name: "gap", Dur: d, Level: -999}) }
	sync := func(name string) {
		sw := SyncSweep(o.Level)
		if name == "end sync" {
			sw = EndSync(o.Level)
		}
		add(Segment{Kind: Sync, Name: name, Dur: sw.T, Level: o.Level, Sweep: &sw})
	}
	f2 := math.Min(0.475*o.PlayRate, 40000)
	switch o.Name {
	case "level":
		gap(0.3)
		add(Segment{Kind: Tone, Name: "level check 1 kHz", Dur: 60, Level: o.Level, Freqs: []float64{1000}, Tag: "level"})
		p.Total = t
		return p, nil
	case "quick", "full":
	default:
		return nil, fmt.Errorf("unknown plan %q (quick, full, level)", o.Name)
	}
	// A long lead-in: some players reopen the output just after starting, which cuts the
	// first half second or so.
	gap(1.5)
	p.SyncLead = t
	sync("start sync")
	gap(0.5)
	add(Segment{Kind: Tone, Name: "1 kHz", Dur: 2.5, Level: o.Level, Freqs: []float64{1000}, Tag: "thd"})
	gap(0.3)
	sw := dsp.Sweep{F1: 10, F2: f2, T: o.SweepLen, Amp: dsp.FromDB(o.Level), Fade: 0.05}
	add(Segment{Kind: SweepK, Name: "sweep", Dur: sw.T, Level: o.Level, Sweep: &sw, Tag: "fr"})
	gap(1.0)
	add(Segment{Kind: Impulses, Name: "impulses", Dur: 32 * 0.05, Level: o.Level, Count: 32, Every: 0.05, Tag: "ir"})
	gap(0.3)
	add(Segment{Kind: Silence, Name: "digital silence", Dur: 3, Level: -999, Tag: "noise"})
	add(Segment{Kind: Tone, Name: "1 kHz at -60 dB", Dur: 2.5, Level: o.Level - 60, Freqs: []float64{1000}, Tag: "dr"})
	gap(0.3)
	add(Segment{Kind: Tone, Name: "left only", Dur: 1.5, Chan: "L", Level: o.Level, Freqs: []float64{1000}, Tag: "xtalk-l"})
	add(Segment{Kind: Tone, Name: "right only", Dur: 1.5, Chan: "R", Level: o.Level, Freqs: []float64{1000}, Tag: "xtalk-r"})
	gap(0.3)
	if o.Name == "full" {
		for _, f := range []float64{20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 15000, 20000} {
			if f > f2 {
				break
			}
			add(Segment{Kind: Tone, Name: fmt.Sprintf("%g Hz", f), Dur: 1.0, Level: o.Level, Freqs: []float64{f}, Tag: "thd-freq"})
		}
		gap(0.3)
		for d := 0.0; d >= -80; d -= 10 {
			add(Segment{Kind: Tone, Name: fmt.Sprintf("1 kHz %+.0f dB", d), Dur: 1.0, Level: o.Level + d, Freqs: []float64{1000}, Tag: "thd-level"})
		}
		gap(0.3)
		add(Segment{Kind: Multitone, Name: "SMPTE 60 Hz + 7 kHz 4:1", Dur: 2, Level: o.Level, Freqs: []float64{60, 7000}, Ratio: []float64{4, 1}, Tag: "imd-smpte"})
		if f2 > 20500 {
			add(Segment{Kind: Multitone, Name: "CCIF 19 + 20 kHz 1:1", Dur: 2, Level: o.Level, Freqs: []float64{19000, 20000}, Ratio: []float64{1, 1}, Tag: "imd-ccif"})
		}
		add(Segment{Kind: Tone, Name: "polarity and L/R id, left 440 Hz", Dur: 0.8, Chan: "L", Level: o.Level, Freqs: []float64{440}, Tag: "id-l"})
		add(Segment{Kind: Tone, Name: "polarity and L/R id, right 880 Hz", Dur: 0.8, Chan: "R", Level: o.Level, Freqs: []float64{880}, Tag: "id-r"})
		gap(0.3)
	}
	p.SyncTail = t
	sync("end sync")
	gap(0.7)
	p.Total = t
	return p, nil
}

// Find returns the first segment with tag.
func (p *Plan) Find(tag string) *Segment {
	for i := range p.Segments {
		if p.Segments[i].Tag == tag {
			return &p.Segments[i]
		}
	}
	return nil
}

// FindAll returns all segments with tag.
func (p *Plan) FindAll(tag string) []*Segment {
	var out []*Segment
	for i := range p.Segments {
		if p.Segments[i].Tag == tag {
			out = append(out, &p.Segments[i])
		}
	}
	return out
}

const toneFade = 0.02

// valueAt returns the segment's value at local time t (0..Dur) for the given rate (impulses need
// the rate to land on single samples).
func (s *Segment) valueAt(t float64, rate float64, sampleIdx int, segStartIdx int) float64 {
	a := dsp.FromDB(s.Level)
	switch s.Kind {
	case Sync, SweepK:
		return s.Sweep.At(t)
	case Tone:
		env := fade(t, s.Dur)
		return a * env * math.Sin(2*math.Pi*s.Freqs[0]*t)
	case Multitone:
		sum := 0.0
		for _, r := range s.Ratio {
			sum += r
		}
		env := fade(t, s.Dur)
		v := 0.0
		for i, f := range s.Freqs {
			v += s.Ratio[i] / sum * math.Sin(2*math.Pi*f*t)
		}
		return a * env * v
	case Impulses:
		every := int(math.Round(s.Every * rate))
		k := sampleIdx - segStartIdx
		if k >= 0 && every > 0 && k%every == every/2 && k/every < s.Count {
			return a
		}
	}
	return 0
}

func fade(t, dur float64) float64 {
	if t < toneFade {
		return 0.5 - 0.5*math.Cos(math.Pi*t/toneFade)
	}
	if t > dur-toneFade {
		return 0.5 - 0.5*math.Cos(math.Pi*math.Max(0, dur-t)/toneFade)
	}
	return 1
}

// Render produces the stereo signal at rate. ditherBits > 0 adds TPDF dither at that word length.
func (p *Plan) Render(rate float64, ditherBits int) [][]float64 {
	n := int(math.Ceil(p.Total * rate))
	out := [][]float64{make([]float64, n), make([]float64, n)}
	for i := range p.Segments {
		s := &p.Segments[i]
		a := int(math.Ceil(s.Start * rate))
		b := min(int(math.Ceil((s.Start+s.Dur)*rate)), n)
		for j := a; j < b; j++ {
			v := s.valueAt(float64(j)/rate-s.Start, rate, j, a)
			if s.Chan != "R" {
				out[0][j] = v
			}
			if s.Chan != "L" {
				out[1][j] = v
			}
		}
	}
	if ditherBits > 0 {
		r := rand.New(rand.NewSource(1))
		lsb := math.Pow(2, -float64(ditherBits-1))
		for c := range out {
			for j := range out[c] {
				out[c][j] += (r.Float64() - r.Float64()) * lsb
			}
		}
	}
	return out
}

// DryLevelDB is the peak of the plan in dBFS (checks the loudness cap).
func (p *Plan) DryLevelDB() float64 {
	m := -999.0
	for _, s := range p.Segments {
		if s.Level > m {
			m = s.Level
		}
	}
	return m
}
