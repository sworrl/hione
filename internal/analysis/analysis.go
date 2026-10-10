// Package analysis turns a capture of the test signal into measurements: it finds the sync
// markers, corrects for clock drift, and runs the sweep, tone, noise and crosstalk analyses.
package analysis

import (
	"fmt"
	"math"

	"github.com/sworrl/mikumeasure/internal/dsp"
	"github.com/sworrl/mikumeasure/internal/stim"
)

// Options for an analysis.
type Options struct {
	PlayRate float64
	CapRate  float64
	BandLo   float64 // THD+N and noise band, default 20 Hz
	BandHi   float64 // default 20 kHz
	// DBVAtFS converts capture dBFS to dBV: dBV = dBFS + DBVAtFS. NaN when not calibrated.
	DBVAtFS float64
	// Loopback is the capture chain's own response (from calibrate loopback); subtracted from the
	// frequency response when set.
	Loopback *ChainCal
}

// ChainCal is what a loopback calibration measured about the PC side.
type ChainCal struct {
	Rate      float64   `json:"rate"`
	FR        dsp.Curve `json:"fr"` // relative, 0 dB at 1 kHz
	NoiseDBFS float64   `json:"noise_dbfs"`
	NoiseA    float64   `json:"noise_a_dbfs"`
	THDN      float64   `json:"thdn_db"`
	Level     float64   `json:"level_dbfs"`
	Device    string    `json:"device"`
	When      string    `json:"when"`
}

// Channel holds one channel's results.
type Channel struct {
	Name        string         `json:"name"`
	FR          dsp.Curve      `json:"fr"`          // dB re 1 kHz
	FRComp      *dsp.Curve     `json:"fr_comp,omitempty"` // with the capture chain removed
	GainDB      float64        `json:"gain_db"`     // capture level minus played level at 1 kHz
	Polarity    string         `json:"polarity"`    // normal or inverted
	IR          []float64      `json:"ir"`          // averaged impulse, centered on its peak
	IRStart     float64        `json:"ir_start_ms"` // time of IR[0] re peak, ms
	SweepIR     []float64      `json:"sweep_ir"`
	SweepIRStart float64       `json:"sweep_ir_start_ms"`
	Tone        *dsp.ToneResult `json:"tone,omitempty"`  // 1 kHz at the test level
	Low         *dsp.ToneResult `json:"low,omitempty"`   // 1 kHz at -60 dB re test level
	Noise       *dsp.NoiseResult `json:"noise,omitempty"`
	NoiseDUTFS  float64        `json:"noise_re_dut_fs_db"`   // unweighted, re device full scale
	NoiseADUTFS float64        `json:"noise_a_re_dut_fs_db"` // A-weighted
	SNRA        float64        `json:"snr_a_db"`
	DRA         float64        `json:"dr_a_db"` // AES17 dynamic range, A-weighted
	XtalkDB     float64        `json:"xtalk_db"` // this channel's signal leaking into the other one
	FullScaleDBV float64       `json:"full_scale_dbv"` // device output at 0 dBFS, dBV (NaN = uncalibrated)
	THDvsF      []Point        `json:"thd_vs_freq,omitempty"`
	THDvsL      []Point        `json:"thd_vs_level,omitempty"`
	IMDSMPTE    float64        `json:"imd_smpte_db"`
	IMDCCIF     float64        `json:"imd_ccif_db"`
	Clipped     []string       `json:"clipped,omitempty"`
}

// Point is one step of a stepped-sine sweep.
type Point struct {
	X     float64 `json:"x"` // Hz or dBFS
	THDN  float64 `json:"thdn_db"`
	THD   float64 `json:"thd_db"`
	Level float64 `json:"level_dbfs"`
}

// Result of one run.
type Result struct {
	Found      bool       `json:"found"`
	Message    string     `json:"message,omitempty"`
	SyncConf   float64    `json:"sync_confidence"`
	StartSec   float64    `json:"start_s"`   // capture time of the start sync
	DriftPPM   float64    `json:"drift_ppm"` // capture clock re player clock
	CapRate    float64    `json:"cap_rate"`
	PlayRate   float64    `json:"play_rate"`
	PeakDBFS   float64    `json:"peak_dbfs"`
	Balance    float64    `json:"balance_db"` // L minus R at 1 kHz
	LRSwapped  bool       `json:"lr_swapped"`
	Ch         []*Channel `json:"channels"`
	IdleNoise  []*dsp.NoiseResult `json:"idle_noise,omitempty"` // when no signal was found
	Notes      []string   `json:"notes,omitempty"`
}

// Locate finds the start sync, per channel (a channel with inverted polarity must not cancel
// the other), and returns the start index and confidence of the clearer one.
func Locate(rec [][]float64, plan *stim.Plan, capRate float64) (float64, float64) {
	return locateRef(rec, stim.SyncSweep(plan.Level).Render(capRate, 1))
}

func locateRef(rec [][]float64, ref []float64) (float64, float64) {
	bp, bc := 0.0, 0.0
	for _, x := range rec {
		p, c := dsp.FindMarker(x, ref)
		if c > bc {
			bp, bc = p, c
		}
	}
	return bp, bc
}

func mono(rec [][]float64) []float64 {
	n := len(rec[0])
	m := make([]float64, n)
	for i := 0; i < n; i++ {
		m[i] = rec[0][i]
		if len(rec) > 1 && i < len(rec[1]) {
			m[i] += rec[1][i]
		}
	}
	return m
}

// MinConfidence is the matched-filter confidence above which a sync counts as found.
const MinConfidence = 8

// Analyze runs everything on a capture.
func Analyze(rec [][]float64, plan *stim.Plan, o Options) *Result {
	if o.BandLo == 0 {
		o.BandLo = 20
	}
	if o.BandHi == 0 {
		o.BandHi = math.Min(20000, o.CapRate*0.45)
	}
	r := &Result{CapRate: o.CapRate, PlayRate: o.PlayRate}
	for _, c := range rec {
		r.PeakDBFS = math.Max(r.PeakDBFS, dsp.DB(dsp.Peak(c)))
	}
	if len(rec[0]) == 0 {
		r.PeakDBFS = -999
	}
	start, conf := Locate(rec, plan, o.CapRate)
	r.SyncConf = conf
	if conf < MinConfidence {
		r.Found = false
		r.Message = fmt.Sprintf("The test signal was not found in the capture (sync confidence %.1f, needs %d). Only the idle noise was measured.", conf, MinConfidence)
		for _, c := range rec {
			nr := dsp.AnalyzeNoise(c, o.CapRate, o.BandLo, o.BandHi)
			r.IdleNoise = append(r.IdleNoise, &nr)
		}
		return r
	}
	r.Found = true
	// Anchor on the end sync (a downward sweep, found once): if playback restarted near the
	// beginning, the start sync shows up twice and only the one that fits the end counts.
	span := (plan.SyncTail - plan.SyncLead) * o.CapRate
	endRef := stim.EndSync(plan.Level).Render(o.CapRate, 1)
	startRef := stim.SyncSweep(plan.Level).Render(o.CapRate, 1)
	drift := 0.0
	endPos, endConf := locateRef(rec, endRef)
	anchored := false
	if endConf >= MinConfidence {
		exp := endPos - span
		win := 0.05 * o.CapRate // +-50 ms covers +-2000 ppm over 25 s
		a, b := int(exp-win), int(exp+win)+len(startRef)
		if a >= 0 && b <= len(rec[0]) {
			cut := make([][]float64, len(rec))
			for i := range rec {
				cut[i] = rec[i][a:b]
			}
			if p, c2 := locateRef(cut, startRef); c2 >= MinConfidence/2 {
				if math.Abs(float64(a)+p-start) > 0.01*o.CapRate {
					r.Notes = append(r.Notes, fmt.Sprintf("Playback restarted near the beginning (the start marker came twice, %.2f s apart). The second start was used.", math.Abs(float64(a)+p-start)/o.CapRate))
				}
				start = float64(a) + p
				drift = (endPos-start)/span - 1
				anchored = true
			}
		}
	}
	if !anchored {
		r.Notes = append(r.Notes, "The end marker was not found, so clock drift was not corrected and a restart of playback would go unnoticed.")
	}
	r.StartSec = start / o.CapRate
	r.DriftPPM = drift * 1e6
	// recIdx maps plan time to capture sample index.
	recIdx := func(t float64) float64 {
		return start + (t-plan.SyncLead)*(1+drift)*o.CapRate
	}
	seg := func(c int, s *stim.Segment, guardA, guardB float64) []float64 {
		return dsp.Slice(rec[c], int(recIdx(s.Start+guardA)), int(recIdx(s.Start+s.Dur-guardB)))
	}
	names := []string{"Left", "Right"}
	for c := 0; c < len(rec) && c < 2; c++ {
		ch := &Channel{Name: names[c], FullScaleDBV: math.NaN(), IMDSMPTE: math.NaN(), IMDCCIF: math.NaN()}
		r.Ch = append(r.Ch, ch)
		// Clipping per segment.
		for i := range plan.Segments {
			s := &plan.Segments[i]
			if s.Kind == stim.Silence {
				continue
			}
			if dsp.Peak(seg(c, s, 0, 0)) >= 0.999 {
				ch.Clipped = append(ch.Clipped, s.Name)
			}
		}
		// 1 kHz tone: level, THD, THD+N.
		if s := plan.Find("thd"); s != nil {
			tr := dsp.AnalyzeTone(seg(c, s, 0.25, 0.25), o.CapRate, s.Freqs[0]/(1+drift), o.BandLo, o.BandHi)
			ch.Tone = &tr
			ch.GainDB = tr.LevelDBFS - s.Level
			if !math.IsNaN(o.DBVAtFS) {
				ch.FullScaleDBV = ch.GainDB + o.DBVAtFS
			}
		}
		// Sweep: impulse and frequency response.
		if s := plan.Find("fr"); s != nil {
			pre := 0.2
			y := dsp.Slice(rec[c], int(recIdx(s.Start-pre)), int(recIdx(s.Start+s.Dur+0.9)))
			x := s.Sweep.Render(o.CapRate, 1+drift)
			h := dsp.Deconvolve(y, x, o.CapRate, s.Sweep.F1, s.Sweep.F2)
			pk, pv := dsp.PeakIndex(h, int(0.1*o.CapRate), int(0.5*o.CapRate))
			if pv < 0 {
				ch.Polarity = "inverted"
			} else {
				ch.Polarity = "normal"
			}
			w, off := dsp.Window(h, pk, o.CapRate, 0.005, 0.6)
			spec := dsp.ResponseOf(w, o.CapRate, off, int(o.CapRate))
			// The same deconvolution and window applied to the reference itself: dividing by it
			// removes what the regularization and the window do at the band edges.
			xr := make([]float64, len(y))
			copy(xr[int(math.Round(pre*o.CapRate)):], x)
			hr := dsp.Deconvolve(xr, x, o.CapRate, s.Sweep.F1, s.Sweep.F2)
			pkr, _ := dsp.PeakIndex(hr, int(0.1*o.CapRate), int(0.5*o.CapRate))
			wr, offr := dsp.Window(hr, pkr, o.CapRate, 0.005, 0.6)
			spec.DivideBy(dsp.ResponseOf(wr, o.CapRate, offr, int(o.CapRate)))
			grid := dsp.LogGrid(10, s.Sweep.F2, 48)
			ch.FR = spec.CurveFrom(grid, 0)
			ch.FR.Normalize(1000)
			if o.Loopback != nil {
				comp := ch.FR.Subtract(o.Loopback.FR)
				ch.FRComp = &comp
			}
			// Short IR view from the sweep: -1 ms .. +3 ms.
			ia := int(math.Round(pk)) - int(0.001*o.CapRate)
			ib := int(math.Round(pk)) + int(0.003*o.CapRate)
			sir := dsp.Slice(h, ia, ib)
			scale := 1 / math.Max(math.Abs(pv), 1e-12)
			ch.SweepIR = make([]float64, len(sir))
			for i, v := range sir {
				ch.SweepIR[i] = v * scale
			}
			ch.SweepIRStart = (float64(ia) - pk) / o.CapRate * 1000
		}
		// Impulses: average with fractional alignment.
		if s := plan.Find("ir"); s != nil {
			ch.IR, ch.IRStart = averageImpulses(rec[c], s, o, recIdx)
		}
		// Noise floor with digital silence playing.
		if s := plan.Find("noise"); s != nil {
			nr := dsp.AnalyzeNoise(seg(c, s, 0.3, 0.2), o.CapRate, o.BandLo, o.BandHi)
			ch.Noise = &nr
			ch.NoiseDUTFS = nr.DBFS - ch.GainDB
			ch.NoiseADUTFS = nr.DBFSA - ch.GainDB
			ch.SNRA = -ch.NoiseADUTFS
		}
		// Dynamic range (AES17): THD+N of a -60 dB tone, A-weighted, re full scale.
		if s := plan.Find("dr"); s != nil {
			tr := dsp.AnalyzeTone(seg(c, s, 0.25, 0.25), o.CapRate, s.Freqs[0]/(1+drift), o.BandLo, o.BandHi)
			ch.Low = &tr
			ch.DRA = -(tr.ResidADBFS - ch.GainDB)
		}
		// Stepped sines.
		for _, s := range plan.FindAll("thd-freq") {
			tr := dsp.AnalyzeTone(seg(c, s, 0.2, 0.1), o.CapRate, s.Freqs[0]/(1+drift), o.BandLo, o.BandHi)
			ch.THDvsF = append(ch.THDvsF, Point{X: s.Freqs[0], THDN: tr.THDN, THD: tr.THD, Level: tr.LevelDBFS})
		}
		for _, s := range plan.FindAll("thd-level") {
			tr := dsp.AnalyzeTone(seg(c, s, 0.2, 0.1), o.CapRate, s.Freqs[0]/(1+drift), o.BandLo, o.BandHi)
			ch.THDvsL = append(ch.THDvsL, Point{X: s.Level, THDN: tr.THDN, THD: tr.THD, Level: tr.LevelDBFS})
		}
		if s := plan.Find("imd-smpte"); s != nil {
			ch.IMDSMPTE = imdSMPTE(seg(c, s, 0.2, 0.2), o.CapRate, s.Freqs[0]/(1+drift), s.Freqs[1]/(1+drift))
		}
		if s := plan.Find("imd-ccif"); s != nil {
			ch.IMDCCIF = imdCCIF(seg(c, s, 0.2, 0.2), o.CapRate, s.Freqs[0]/(1+drift), s.Freqs[1]/(1+drift))
		}
	}
	// Crosstalk and L/R identification.
	if len(r.Ch) == 2 {
		if sl, sr := plan.Find("xtalk-l"), plan.Find("xtalk-r"); sl != nil && sr != nil {
			f := sl.Freqs[0] / (1 + drift)
			ll := dsp.LevelAt(seg(0, sl, 0.25, 0.25), o.CapRate, f)
			lr := dsp.LevelAt(seg(1, sl, 0.25, 0.25), o.CapRate, f)
			rr := dsp.LevelAt(seg(1, sr, 0.25, 0.25), o.CapRate, f)
			rl := dsp.LevelAt(seg(0, sr, 0.25, 0.25), o.CapRate, f)
			r.Ch[0].XtalkDB = lr - ll
			r.Ch[1].XtalkDB = rl - rr
			if lr > ll && rl > rr {
				r.LRSwapped = true
				r.Notes = append(r.Notes, "Left and right look swapped: the left-only tone came out on the right input.")
			}
		}
		if r.Ch[0].Tone != nil && r.Ch[1].Tone != nil {
			r.Balance = r.Ch[0].Tone.LevelDBFS - r.Ch[1].Tone.LevelDBFS
		}
	}
	return r
}

func averageImpulses(x []float64, s *stim.Segment, o Options, recIdx func(float64) float64) ([]float64, float64) {
	pre := int(0.001 * o.CapRate)
	post := int(0.003 * o.CapRate)
	every := int(math.Round(s.Every * o.PlayRate))
	a0 := math.Ceil(s.Start * o.PlayRate)
	acc := make([]float64, pre+post)
	n := 0
	for k := 0; k < s.Count; k++ {
		tp := (a0 + float64(k*every+every/2)) / o.PlayRate
		ci := recIdx(tp)
		base := int(math.Floor(ci)) - pre - 8
		w := dsp.Slice(x, base, base+pre+post+16)
		if len(w) < pre+post+16 {
			continue
		}
		// Shift so the expected impulse lands exactly on index pre+8.
		frac := ci - math.Floor(ci)
		sh := dsp.FracShift(w, -frac)
		for i := range acc {
			acc[i] += sh[i+8]
		}
		n++
	}
	if n == 0 {
		return nil, 0
	}
	for i := range acc {
		acc[i] /= float64(n)
	}
	// Center on the peak and normalize to the impulse's height.
	pk, pv := dsp.PeakIndex(acc, 0, len(acc))
	out := make([]float64, len(acc))
	g := 1 / math.Max(math.Abs(pv), 1e-12)
	for i := range acc {
		out[i] = acc[i] * g
	}
	return out, -pk / o.CapRate * 1000
}

func imdSMPTE(x []float64, rate, f1, f2 float64) float64 {
	a2 := dsp.FromDB(dsp.LevelAt(x, rate, f2))
	p := 0.0
	for n := 1; n <= 3; n++ {
		for _, sgn := range []float64{-1, 1} {
			v := dsp.FromDB(dsp.LevelAt(x, rate, f2+sgn*float64(n)*f1))
			p += v * v
		}
	}
	return dsp.DB(math.Sqrt(p) / a2)
}

func imdCCIF(x []float64, rate, f1, f2 float64) float64 {
	a1 := dsp.FromDB(dsp.LevelAt(x, rate, f1))
	a2 := dsp.FromDB(dsp.LevelAt(x, rate, f2))
	p := 0.0
	for _, f := range []float64{f2 - f1, 2*f1 - f2, 2*f2 - f1} {
		if f <= 0 || f >= rate/2 {
			continue
		}
		v := dsp.FromDB(dsp.LevelAt(x, rate, f))
		p += v * v
	}
	return dsp.DB(math.Sqrt(p) / (a1 + a2))
}
