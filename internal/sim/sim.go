// Package sim is a synthetic device under test: it takes the stimulus as the player would send
// it and returns what a capture card would record, with a known gain, frequency response,
// distortion, noise, crosstalk, delay and clock drift. The tests and the "sim" capture use it to
// check the whole chain against numbers that are known in advance.
package sim

import (
	"math"
	"math/rand"

	"github.com/sworrl/mikumeasure/internal/dsp"
)

// Model of the device and capture chain.
type Model struct {
	GainDB    float64      // overall gain
	Filters   []dsp.Biquad // applied at the capture rate, both channels
	Filters2  []dsp.Biquad // extra filters on the right channel only (channel mismatch)
	H2, H3    float64      // harmonic coefficients: y = x + H2*x^2 + H3*x^3 (before gain)
	NoiseDBFS float64      // white noise RMS re full-scale sine, per channel (-999 = none)
	XtalkDB   float64      // left into right and right into left (-999 = none)
	Delay     float64      // seconds before the signal starts
	DriftPPM  float64      // capture clock vs player clock
	ZOH       bool         // reconstruct with zero-order hold (NOS-like images) instead of sinc
	InvertR   bool         // right channel polarity inverted
	Hum       float64      // amplitude of a 4750 Hz interference tone (0 = none)
	Seed      int64
}

// Default is a plausible portable player into a line input.
func Default(rate float64) Model {
	return Model{
		GainDB:    -1.5,
		Filters:   []dsp.Biquad{dsp.HighPass(rate, 8, 0.707), dsp.LowPass(rate, math.Min(30000, rate*0.45), 0.6)},
		H2:        0.0008,
		H3:        0.0003,
		NoiseDBFS: -105,
		XtalkDB:   -85,
		Delay:     0.25,
		DriftPPM:  23,
		Seed:      7,
	}
}

// Apply runs the model on a stereo signal at playRate and returns the capture at capRate.
func (m Model) Apply(in [][]float64, playRate, capRate float64) [][]float64 {
	out := make([][]float64, 2)
	g := dsp.FromDB(m.GainDB)
	for c := 0; c < 2; c++ {
		x := make([]float64, len(in[c]))
		for i, v := range in[c] {
			x[i] = v + m.H2*v*v + m.H3*v*v*v
		}
		eff := capRate * (1 + m.DriftPPM*1e-6)
		var y []float64
		switch {
		case m.ZOH && eff != playRate:
			n := int(float64(len(x)) * eff / playRate)
			y = make([]float64, n)
			for j := range y {
				k := int(float64(j) * playRate / eff)
				if k < len(x) {
					y[j] = x[k]
				}
			}
		case eff != playRate:
			y = dsp.Resample(x, playRate, eff)
		default:
			y = x
		}
		for _, f := range m.Filters {
			y = f.Process(y)
		}
		if c == 1 {
			for _, f := range m.Filters2 {
				y = f.Process(y)
			}
		}
		for i := range y {
			y[i] *= g
		}
		if c == 1 && m.InvertR {
			for i := range y {
				y[i] = -y[i]
			}
		}
		out[c] = y
	}
	if m.XtalkDB > -900 {
		k := dsp.FromDB(m.XtalkDB)
		l, r := out[0], out[1]
		n := min(len(l), len(r))
		nl, nr := make([]float64, n), make([]float64, n)
		for i := 0; i < n; i++ {
			nl[i] = l[i] + k*r[i]
			nr[i] = r[i] + k*l[i]
		}
		out[0], out[1] = nl, nr
	}
	d := int(m.Delay * capRate)
	rng := rand.New(rand.NewSource(m.Seed))
	sigma := 0.0
	if m.NoiseDBFS > -900 {
		sigma = dsp.FromDB(m.NoiseDBFS) / math.Sqrt2
	}
	for c := range out {
		y := make([]float64, d+len(out[c])+int(0.5*capRate))
		copy(y[d:], out[c])
		for i := range y {
			if sigma > 0 {
				y[i] += sigma * rng.NormFloat64()
			}
			if m.Hum > 0 {
				y[i] += m.Hum * math.Sin(2*math.Pi*4750*float64(i)/capRate)
			}
		}
		out[c] = y
	}
	return out
}

// NoiseOnly returns n frames of the model's idle output (noise and interference only).
func (m Model) NoiseOnly(n int, capRate float64, rng *rand.Rand, phase int) [][]float64 {
	sigma := 0.0
	if m.NoiseDBFS > -900 {
		sigma = dsp.FromDB(m.NoiseDBFS) / math.Sqrt2
	}
	out := [][]float64{make([]float64, n), make([]float64, n)}
	for c := range out {
		for i := range out[c] {
			out[c][i] = sigma * rng.NormFloat64()
			if m.Hum > 0 {
				out[c][i] += m.Hum * math.Sin(2*math.Pi*4750*float64(i+phase)/capRate)
			}
		}
	}
	return out
}
