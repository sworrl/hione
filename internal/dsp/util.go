package dsp

import (
	"math"
	"math/cmplx"
)

// Correlate returns the cross-correlation of x with ref for lags 0..len(x)-1 (circular wrap
// removed by zero padding). c[k] = sum ref[i]*x[i+k].
func Correlate(x, ref []float64) []float64 {
	n := NextPow2(len(x) + len(ref))
	X := RFFT(x, n)
	R := RFFT(ref, n)
	for k := range X {
		X[k] *= cmplx.Conj(R[k])
	}
	c := IRFFT(X, n)
	return c[:len(x)]
}

// FindMarker locates ref inside x by matched filtering. It returns the fractional sample index
// where ref starts and a confidence: the peak over the 99th percentile of |c|. A clean capture
// gives confidence well above 10; noise alone gives about 1 to 3.
func FindMarker(x, ref []float64) (pos float64, confidence float64) {
	if len(x) < len(ref) {
		return 0, 0
	}
	c := Correlate(x, ref)
	// Use the envelope-free absolute value: polarity flips must still be found.
	p, _ := PeakIndex(c, 0, len(c)-len(ref)+1)
	pk := math.Abs(c[int(math.Round(p))])
	// Robust floor: mean |c| away from the peak.
	s, cnt := 0.0, 0
	excl := len(ref)
	for i := 0; i < len(c); i += 7 {
		if math.Abs(float64(i)-p) < float64(excl) {
			continue
		}
		s += math.Abs(c[i])
		cnt++
	}
	if cnt == 0 {
		return p, 0
	}
	floor := s / float64(cnt)
	if floor == 0 {
		return p, math.Inf(1)
	}
	return p, pk / floor
}

// FracShift delays x by d samples (any real d) using a linear phase ramp in the frequency
// domain. Exact for band-limited signals away from the edges.
func FracShift(x []float64, d float64) []float64 {
	n := NextPow2(len(x) * 2)
	X := RFFT(x, n)
	for k := range X {
		w := 2 * math.Pi * float64(k) / float64(n)
		X[k] *= cmplx.Exp(complex(0, -w*d))
	}
	return IRFFT(X, n)[:len(x)]
}

// Resample converts x from rate a to rate b with a windowed-sinc interpolator (Kaiser, beta 9,
// 32 zero crossings each side, table-driven). Used by the simulator and the music test, not on
// the sweep measurement path.
func Resample(x []float64, a, b float64) []float64 {
	n := int(float64(len(x)) * b / a)
	out := make([]float64, n)
	cut := math.Min(1, b/a) * 0.97
	const half = 32
	const per = 512
	// Kernel table over u in [0, half] input samples, linear interpolation between entries.
	span := float64(half) / cut
	tab := make([]float64, half*per+2)
	i0b := besselI0(9)
	for i := range tab {
		u := float64(i) / per / cut
		r := u / span
		if r >= 1 {
			continue
		}
		tab[i] = cut * sinc(cut*u) * besselI0(9*math.Sqrt(1-r*r)) / i0b
	}
	reach := int(math.Ceil(span))
	for j := range out {
		t := float64(j) * a / b
		c := int(math.Floor(t))
		s := 0.0
		for i := c - reach + 1; i <= c+reach; i++ {
			if i < 0 || i >= len(x) {
				continue
			}
			u := math.Abs(t-float64(i)) * cut * per
			k := int(u)
			if k >= len(tab)-1 {
				continue
			}
			fr := u - float64(k)
			s += x[i] * (tab[k]*(1-fr) + tab[k+1]*fr)
		}
		out[j] = s
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func besselI0(x float64) float64 {
	s, t := 1.0, 1.0
	for k := 1; k < 50; k++ {
		t *= (x / 2) / float64(k)
		s += t * t
		if t*t < 1e-17*s {
			break
		}
	}
	return s
}
