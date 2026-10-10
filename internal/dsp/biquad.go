package dsp

import (
	"math"
	"math/cmplx"
)

// Biquad is a second-order IIR section (RBJ cookbook), normalized so a0 = 1.
type Biquad struct {
	B0, B1, B2, A1, A2 float64
}

// Peaking returns a peaking EQ at f0 with gain dB and quality q.
func Peaking(rate, f0, gainDB, q float64) Biquad {
	A := math.Pow(10, gainDB/40)
	w := 2 * math.Pi * f0 / rate
	al := math.Sin(w) / (2 * q)
	a0 := 1 + al/A
	return Biquad{(1 + al*A) / a0, -2 * math.Cos(w) / a0, (1 - al*A) / a0, -2 * math.Cos(w) / a0, (1 - al/A) / a0}
}

// LowPass returns a 2nd-order low-pass.
func LowPass(rate, f0, q float64) Biquad {
	w := 2 * math.Pi * f0 / rate
	al := math.Sin(w) / (2 * q)
	c := math.Cos(w)
	a0 := 1 + al
	return Biquad{(1 - c) / 2 / a0, (1 - c) / a0, (1 - c) / 2 / a0, -2 * c / a0, (1 - al) / a0}
}

// HighPass returns a 2nd-order high-pass.
func HighPass(rate, f0, q float64) Biquad {
	w := 2 * math.Pi * f0 / rate
	al := math.Sin(w) / (2 * q)
	c := math.Cos(w)
	a0 := 1 + al
	return Biquad{(1 + c) / 2 / a0, -(1 + c) / a0, (1 + c) / 2 / a0, -2 * c / a0, (1 - al) / a0}
}

// HighShelf returns a high shelf at f0 with gain dB (shelf slope 1).
func HighShelf(rate, f0, gainDB float64) Biquad {
	A := math.Pow(10, gainDB/40)
	w := 2 * math.Pi * f0 / rate
	c := math.Cos(w)
	al := math.Sin(w) / 2 * math.Sqrt(2)
	sa := 2 * math.Sqrt(A) * al
	a0 := (A + 1) - (A-1)*c + sa
	return Biquad{
		A * ((A + 1) + (A-1)*c + sa) / a0,
		-2 * A * ((A - 1) + (A+1)*c) / a0,
		A * ((A + 1) + (A-1)*c - sa) / a0,
		2 * ((A - 1) - (A+1)*c) / a0,
		((A + 1) - (A-1)*c - sa) / a0,
	}
}

// Process filters x and returns a new slice.
func (b Biquad) Process(x []float64) []float64 {
	y := make([]float64, len(x))
	var x1, x2, y1, y2 float64
	for i, v := range x {
		o := b.B0*v + b.B1*x1 + b.B2*x2 - b.A1*y1 - b.A2*y2
		x2, x1 = x1, v
		y2, y1 = y1, o
		y[i] = o
	}
	return y
}

// Response returns the complex response at f.
func (b Biquad) Response(rate, f float64) complex128 {
	z := cmplx.Exp(complex(0, -2*math.Pi*f/rate))
	z2 := z * z
	return (complex(b.B0, 0) + complex(b.B1, 0)*z + complex(b.B2, 0)*z2) /
		(1 + complex(b.A1, 0)*z + complex(b.A2, 0)*z2)
}
