package wav

import (
	"bytes"
	"math"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, f := range []Format{S16, S24, S32, F32} {
		a := &Audio{Rate: 96000, Format: f, Data: [][]float64{make([]float64, 1000), make([]float64, 1000)}}
		for i := range a.Data[0] {
			a.Data[0][i] = 0.9 * math.Sin(float64(i)/7)
			a.Data[1][i] = -0.5 * math.Cos(float64(i)/3)
		}
		var b bytes.Buffer
		if err := Encode(&b, a); err != nil {
			t.Fatal(err)
		}
		got, err := Decode(&b)
		if err != nil {
			t.Fatal(err)
		}
		tol := map[Format]float64{S16: 1.0 / 32768, S24: 1.0 / 8388608, S32: 1e-9, F32: 1e-7}[f]
		if got.Rate != 96000 || got.Format != f || got.Frames() != 1000 {
			t.Fatalf("%s: header %d %s %d", f, got.Rate, got.Format, got.Frames())
		}
		for c := 0; c < 2; c++ {
			for i := range a.Data[c] {
				if math.Abs(got.Data[c][i]-a.Data[c][i]) > tol {
					t.Fatalf("%s ch%d[%d]: %g vs %g", f, c, i, got.Data[c][i], a.Data[c][i])
				}
			}
		}
	}
}

// arecord and pw-record write a 0 or 0xFFFFFFFF data length when streaming to a pipe.
func TestStreamingHeader(t *testing.T) {
	a := &Audio{Rate: 48000, Format: S32, Data: [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}}
	var b bytes.Buffer
	Encode(&b, a)
	raw := b.Bytes()
	copy(raw[40:44], []byte{0xff, 0xff, 0xff, 0xff})
	got, err := Decode(bytes.NewReader(raw))
	if err != nil || got.Frames() != 3 {
		t.Fatalf("frames %d err %v", got.Frames(), err)
	}
}
