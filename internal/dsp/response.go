package dsp

import (
	"math"
	"math/cmplx"
	"sort"
)

// Curve is a frequency response on an arbitrary frequency grid.
type Curve struct {
	Hz    []float64 `json:"hz"`
	DB    []float64 `json:"db"`
	Phase []float64 `json:"phase,omitempty"` // degrees
	GD    []float64 `json:"gd,omitempty"`    // group delay, ms
}

// Spectrum of a windowed impulse response: complex values at k*rate/n.
type Spectrum struct {
	Rate float64
	N    int
	X    []complex128
}

// ResponseOf returns the spectrum of ir (zero-padded to at least minN). peakOffset is the index
// of the main peak inside ir; its linear-phase delay is removed so phase and group delay show
// only what is left once the bulk delay is gone.
func ResponseOf(ir []float64, rate float64, peakOffset float64, minN int) Spectrum {
	n := NextPow2(max(len(ir), minN))
	X := RFFT(ir, n)
	for k := range X {
		w := 2 * math.Pi * float64(k) / float64(n)
		X[k] *= cmplx.Exp(complex(0, w*peakOffset))
	}
	return Spectrum{Rate: rate, N: n, X: X}
}

// DivideBy divides s by ref bin by bin (same N required). Bins where ref is more than 60 dB
// below its peak are left alone.
func (s *Spectrum) DivideBy(ref Spectrum) {
	if ref.N != s.N {
		return
	}
	pk := 0.0
	for _, v := range ref.X {
		pk = math.Max(pk, cmplx.Abs(v))
	}
	for k := range s.X {
		if cmplx.Abs(ref.X[k]) > pk*1e-3 {
			s.X[k] /= ref.X[k]
		}
	}
}

// At interpolates the spectrum at frequency f (linear in complex value).
func (s Spectrum) At(f float64) complex128 {
	pos := f * float64(s.N) / s.Rate
	i := int(pos)
	if i >= len(s.X)-1 {
		return s.X[len(s.X)-1]
	}
	fr := pos - float64(i)
	return s.X[i]*complex(1-fr, 0) + s.X[i+1]*complex(fr, 0)
}

// LogGrid returns frequencies from f1 to f2 with ppo points per octave.
func LogGrid(f1, f2 float64, ppo int) []float64 {
	n := int(math.Ceil(math.Log2(f2/f1)*float64(ppo))) + 1
	out := make([]float64, n)
	for i := range out {
		out[i] = f1 * math.Pow(2, float64(i)/float64(ppo))
	}
	out[n-1] = math.Min(out[n-1], f2)
	return out
}

// CurveFrom samples the spectrum on grid, with optional fractional-octave smoothing (0 = none,
// else 3, 6, 12, 24 for 1/N octave). Magnitude is power-averaged over the smoothing band; phase is
// taken unsmoothed from the interpolated point. Group delay is -dphi/dw from neighbouring bins.
func (s Spectrum) CurveFrom(grid []float64, smooth int) Curve {
	c := Curve{Hz: grid, DB: make([]float64, len(grid)), Phase: make([]float64, len(grid)), GD: make([]float64, len(grid))}
	df := s.Rate / float64(s.N)
	// Prefix sums of power for fast band averages.
	pre := make([]float64, len(s.X)+1)
	for k, v := range s.X {
		pre[k+1] = pre[k] + real(v)*real(v) + imag(v)*imag(v)
	}
	for i, f := range grid {
		v := s.At(f)
		if smooth > 0 {
			lo := f * math.Pow(2, -0.5/float64(smooth))
			hi := f * math.Pow(2, 0.5/float64(smooth))
			a := int(math.Ceil(lo / df))
			b := int(math.Floor(hi / df))
			if b >= len(s.X) {
				b = len(s.X) - 1
			}
			if b-a >= 1 {
				p := (pre[b+1] - pre[a]) / float64(b-a+1)
				c.DB[i] = DBPow(p)
			} else {
				c.DB[i] = DB(cmplx.Abs(v))
			}
		} else {
			c.DB[i] = DB(cmplx.Abs(v))
		}
		c.Phase[i] = cmplx.Phase(v) * 180 / math.Pi
		// Group delay from two points 1 bin either side.
		f0 := math.Max(f-df, 0)
		f1 := f + df
		p0 := cmplx.Phase(s.At(f0))
		p1 := cmplx.Phase(s.At(f1))
		d := p1 - p0
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		c.GD[i] = -d / (2 * math.Pi * (f1 - f0)) * 1000
	}
	return c
}

// Normalize shifts the curve so its value at ref Hz is 0 dB and returns the shift applied.
func (c *Curve) Normalize(ref float64) float64 {
	v := c.ValueAt(ref)
	for i := range c.DB {
		c.DB[i] -= v
	}
	return v
}

// ValueAt interpolates the dB value at f on a log axis.
func (c Curve) ValueAt(f float64) float64 {
	n := len(c.Hz)
	if n == 0 {
		return 0
	}
	i := sort.SearchFloat64s(c.Hz, f)
	if i <= 0 {
		return c.DB[0]
	}
	if i >= n {
		return c.DB[n-1]
	}
	a, b := math.Log(c.Hz[i-1]), math.Log(c.Hz[i])
	t := (math.Log(f) - a) / (b - a)
	return c.DB[i-1]*(1-t) + c.DB[i]*t
}

// Subtract returns c minus ref (interpolated onto c's grid).
func (c Curve) Subtract(ref Curve) Curve {
	out := Curve{Hz: c.Hz, DB: make([]float64, len(c.DB)), Phase: c.Phase, GD: c.GD}
	for i, f := range c.Hz {
		out.DB[i] = c.DB[i] - ref.ValueAt(f)
	}
	return out
}

// Range returns min and max dB between f1 and f2.
func (c Curve) Range(f1, f2 float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for i, f := range c.Hz {
		if f < f1 || f > f2 {
			continue
		}
		lo = math.Min(lo, c.DB[i])
		hi = math.Max(hi, c.DB[i])
	}
	return
}

// AWeight returns the A-weighting gain in dB at f (IEC 61672, 0 dB at 1 kHz).
func AWeight(f float64) float64 {
	f2 := f * f
	num := 12194.0 * 12194.0 * f2 * f2
	den := (f2 + 20.6*20.6) * math.Sqrt((f2+107.7*107.7)*(f2+737.9*737.9)) * (f2 + 12194.0*12194.0)
	return 20*math.Log10(num/den) + 2.0
}
