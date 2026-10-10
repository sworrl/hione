// Package dsp holds the signal processing: FFT helpers, windows, the exponential sweep and its
// deconvolution, tone and noise analysis, weighting curves, smoothing and biquads.
package dsp

import (
	"math"
	"math/cmplx"
	"sync"

	"gonum.org/v1/gonum/dsp/fourier"
)

// NextPow2 returns the smallest power of two >= n.
func NextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

var (
	fftMu    sync.Mutex
	fftCache = map[int]*fourier.FFT{}
)

func plan(n int) *fourier.FFT {
	fftMu.Lock()
	defer fftMu.Unlock()
	f, ok := fftCache[n]
	if !ok {
		f = fourier.NewFFT(n)
		fftCache[n] = f
	}
	return f
}

// RFFT returns the n/2+1 coefficients of x zero-padded (or truncated) to n samples.
func RFFT(x []float64, n int) []complex128 {
	buf := make([]float64, n)
	copy(buf, x)
	return plan(n).Coefficients(nil, buf)
}

// IRFFT inverts RFFT, scaled so IRFFT(RFFT(x)) == x.
func IRFFT(X []complex128, n int) []float64 {
	out := plan(n).Sequence(nil, X)
	inv := 1 / float64(n)
	for i := range out {
		out[i] *= inv
	}
	return out
}

// Mag returns |X| for each coefficient.
func Mag(X []complex128) []float64 {
	m := make([]float64, len(X))
	for i, v := range X {
		m[i] = cmplx.Abs(v)
	}
	return m
}

// DB converts an amplitude ratio to decibels with a floor.
func DB(v float64) float64 {
	if v < 1e-15 {
		v = 1e-15
	}
	return 20 * math.Log10(v)
}

// DBPow converts a power ratio to decibels with a floor.
func DBPow(p float64) float64 {
	if p < 1e-30 {
		p = 1e-30
	}
	return 10 * math.Log10(p)
}

// FromDB converts decibels to an amplitude ratio.
func FromDB(db float64) float64 { return math.Pow(10, db/20) }

// RMS of a slice.
func RMS(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

// Peak returns max |x|.
func Peak(x []float64) float64 {
	p := 0.0
	for _, v := range x {
		if a := math.Abs(v); a > p {
			p = a
		}
	}
	return p
}

// Hann window of length n (periodic=false).
func Hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	return w
}

// BlackmanHarris7 is a 7-term Blackman-Harris window (sidelobes near -180 dB), used for
// distortion analysis where leakage from the fundamental must stay below the residual.
func BlackmanHarris7(n int) []float64 {
	a := []float64{0.27105140069342, 0.43329793923448, 0.21812299954311, 0.06592544638803,
		0.01081174209837, 0.00077658482522, 0.00001388721735}
	w := make([]float64, n)
	for i := range w {
		x := 2 * math.Pi * float64(i) / float64(n)
		v := 0.0
		s := 1.0
		for k, ak := range a {
			v += s * ak * math.Cos(float64(k)*x)
			s = -s
		}
		w[i] = v
	}
	return w
}

// Slice returns x[a:b] clamped to the slice bounds.
func Slice(x []float64, a, b int) []float64 {
	if a < 0 {
		a = 0
	}
	if b > len(x) {
		b = len(x)
	}
	if a >= b {
		return nil
	}
	return x[a:b]
}

// GoodLen returns the largest n <= max whose only prime factors are 2, 3 and 5, so the FFT of a
// segment of that length stays fast. Odd results are avoided.
func GoodLen(maxN int) int {
	best := 2
	for p2 := 2; p2 <= maxN; p2 *= 2 {
		for p3 := p2; p3 <= maxN; p3 *= 3 {
			for p5 := p3; p5 <= maxN; p5 *= 5 {
				if p5 > best {
					best = p5
				}
			}
		}
	}
	return best
}
