package dsp

import (
	"math"
	"math/cmplx"
)

// Sweep is an exponential (log) sine sweep, Farina's method. It is defined in continuous time so
// the same sweep can be rendered at the playback rate for the device and at the capture rate as
// the reference for deconvolution.
type Sweep struct {
	F1, F2 float64 // start and end frequency, Hz
	T      float64 // length, s
	Amp    float64 // peak amplitude, 1 = full scale
	Fade   float64 // fade-in/out length, s
}

// L is the sweep rate constant T / ln(F2/F1).
func (s Sweep) L() float64 { return s.T / math.Log(s.F2/s.F1) }

// At returns the sweep value at time t seconds (0 outside [0, T]).
func (s Sweep) At(t float64) float64 {
	if t < 0 || t > s.T {
		return 0
	}
	L := s.L()
	v := s.Amp * math.Sin(2*math.Pi*s.F1*L*(math.Exp(t/L)-1))
	if s.Fade > 0 {
		if t < s.Fade {
			v *= 0.5 - 0.5*math.Cos(math.Pi*t/s.Fade)
		} else if t > s.T-s.Fade {
			v *= 0.5 - 0.5*math.Cos(math.Pi*(s.T-t)/s.Fade)
		}
	}
	return v
}

// Render samples the sweep at rate. scale stretches time (1 + clock drift) so the reference
// can follow a capture clock that runs at a slightly different speed from the player.
func (s Sweep) Render(rate float64, scale float64) []float64 {
	if scale == 0 {
		scale = 1
	}
	n := int(math.Ceil(s.T * rate * scale))
	out := make([]float64, n)
	for i := range out {
		out[i] = s.At(float64(i) / (rate * scale))
	}
	return out
}

// HarmonicDelay is how far before the linear impulse the k-th harmonic's impulse lands.
func (s Sweep) HarmonicDelay(k int) float64 { return s.L() * math.Log(float64(k)) }

// Deconvolve computes the impulse response h with y = x * h by regularized spectral division.
// Inside [f1, f2] the regularization is negligible; outside it rises so the empty bands do not
// blow up. The result has length n (a power of two >= len(x)+len(y)); index 0 is zero lag and
// negative lags (the sweep's harmonic responses) wrap to the end.
func Deconvolve(y, x []float64, rate, f1, f2 float64) []float64 {
	n := NextPow2(len(x) + len(y))
	X := RFFT(x, n)
	Y := RFFT(y, n)
	maxP := 0.0
	for _, v := range X {
		if p := real(v)*real(v) + imag(v)*imag(v); p > maxP {
			maxP = p
		}
	}
	H := make([]complex128, len(X))
	for k := range X {
		f := float64(k) * rate / float64(n)
		eps := 1e-10 * maxP
		lo := f1 / 4 // the sweep (with its fade-in) still has usable energy under f1
		if f < lo || f > f2 {
			// Gentle rise over two octaves outside the band: a steep edge would ring for longer than
			// the analysis window and ripple the low end.
			var d float64
			if f < lo {
				d = math.Log2(lo / math.Max(f, 1e-3))
			} else {
				d = math.Log2(f / f2)
			}
			eps = maxP * math.Min(1, 1e-10*math.Pow(10, 5*d))
		}
		den := real(X[k])*real(X[k]) + imag(X[k])*imag(X[k]) + eps
		H[k] = Y[k] * cmplx.Conj(X[k]) / complex(den, 0)
	}
	return IRFFT(H, n)
}

// PeakIndex finds the largest |h| and refines it with a parabola through the neighbours.
// Returns the fractional index and the signed peak value.
func PeakIndex(h []float64, from, to int) (float64, float64) {
	if from < 0 {
		from = 0
	}
	if to > len(h) || to <= 0 {
		to = len(h)
	}
	bi := from
	for i := from; i < to; i++ {
		if math.Abs(h[i]) > math.Abs(h[bi]) {
			bi = i
		}
	}
	if bi <= 0 || bi >= len(h)-1 {
		return float64(bi), h[bi]
	}
	a, b, c := math.Abs(h[bi-1]), math.Abs(h[bi]), math.Abs(h[bi+1])
	den := a - 2*b + c
	off := 0.0
	if den != 0 {
		off = 0.5 * (a - c) / den
	}
	return float64(bi) + off, h[bi]
}

// Window cuts the impulse response around peak: pre seconds before, post seconds after, with a
// half-Hann fade over the first tenth of pre and the last fifth of post. The returned slice starts
// at the cut point, startOffset is the peak's index inside it.
func Window(h []float64, peak float64, rate, pre, post float64) (out []float64, startOffset float64) {
	n := len(h)
	np := int(pre * rate)
	nq := int(post * rate)
	p := int(math.Round(peak))
	out = make([]float64, np+nq)
	for i := range out {
		idx := ((p-np+i)%n + n) % n
		out[i] = h[idx]
	}
	fi := np / 10
	for i := 0; i < fi; i++ {
		out[i] *= 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(fi))
	}
	fo := nq / 5
	for i := 0; i < fo; i++ {
		out[len(out)-1-i] *= 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(fo))
	}
	return out, peak - float64(p-np)
}
