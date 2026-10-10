package dsp

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

func sine(n int, rate, f, amp, phase float64) []float64 {
	x := make([]float64, n)
	for i := range x {
		x[i] = amp * math.Sin(2*math.Pi*f*float64(i)/rate+phase)
	}
	return x
}

// A known filter applied to a sweep must come back within 0.1 dB.
func TestSweepRecoversFilter(t *testing.T) {
	rate := 48000.0
	sw := Sweep{F1: 10, F2: 22000, T: 4, Amp: 0.25, Fade: 0.05}
	x := sw.Render(rate, 1)
	filters := []Biquad{Peaking(rate, 1000, 6, 1), HighShelf(rate, 8000, -4), HighPass(rate, 15, 0.707)}
	y := make([]float64, int(0.1*rate))
	y = append(y, x...)
	y = append(y, make([]float64, int(rate))...)
	for _, f := range filters {
		y = f.Process(y)
	}
	h := Deconvolve(y, x, rate, sw.F1, sw.F2)
	pk, _ := PeakIndex(h, 0, len(h)/2)
	w, off := Window(h, pk, rate, 0.005, 0.6)
	spec := ResponseOf(w, rate, off, int(rate))
	// Reference: the same processing on the bare sweep, as the analysis does.
	xr := append(make([]float64, int(0.1*rate)), x...)
	xr = append(xr, make([]float64, int(rate))...)
	hr := Deconvolve(xr, x, rate, sw.F1, sw.F2)
	pr, _ := PeakIndex(hr, 0, len(hr)/2)
	wr, offr := Window(hr, pr, rate, 0.005, 0.6)
	spec.DivideBy(ResponseOf(wr, rate, offr, int(rate)))
	worst, at := 0.0, 0.0
	for _, f := range LogGrid(20, 20000, 24) {
		want := complex(1, 0)
		for _, b := range filters {
			want *= b.Response(rate, f)
		}
		got := DB(cmplx.Abs(spec.At(f)))
		if e := math.Abs(got - DB(cmplx.Abs(want))); e > worst {
			worst, at = e, f
		}
	}
	if worst > 0.1 {
		t.Fatalf("worst error %.3f dB at %.0f Hz", worst, at)
	}
	t.Logf("worst error %.4f dB at %.0f Hz", worst, at)
}

// THD of a synthetic signal with known harmonics must match.
func TestTHDKnownHarmonics(t *testing.T) {
	rate := 96000.0
	n := int(rate)
	x := sine(n, rate, 1000, 0.5, 0)
	h2 := sine(n, rate, 2000, 0.5*1e-3, 0.3)  // -60 dB
	h3 := sine(n, rate, 3000, 0.5*3e-4, 1.1)  // -70.5 dB
	h5 := sine(n, rate, 5000, 0.5*1e-4, -0.4) // -80 dB
	for i := range x {
		x[i] += h2[i] + h3[i] + h5[i]
	}
	r := AnalyzeTone(x, rate, 1000, 20, 20000)
	want := 20 * math.Log10(math.Sqrt(1e-6+9e-8+1e-8))
	if math.Abs(r.THD-want) > 0.05 {
		t.Errorf("THD %.3f dB, want %.3f", r.THD, want)
	}
	if math.Abs(r.Harmonics[0]-(-60)) > 0.05 || math.Abs(r.Harmonics[1]-20*math.Log10(3e-4)) > 0.05 || math.Abs(r.Harmonics[3]-(-80)) > 0.1 {
		t.Errorf("harmonics %v", r.Harmonics[:4])
	}
	// No noise: THD+N equals THD.
	if math.Abs(r.THDN-want) > 0.1 {
		t.Errorf("THD+N %.3f dB, want %.3f", r.THDN, want)
	}
	if math.Abs(r.LevelDBFS-DB(0.5)) > 0.01 {
		t.Errorf("level %.3f dBFS, want %.3f", r.LevelDBFS, DB(0.5))
	}
	if math.Abs(r.Freq-1000) > 0.05 {
		t.Errorf("freq %.3f", r.Freq)
	}
}

// THD+N with noise: a clean sine plus white noise of known level.
func TestTHDNWithNoise(t *testing.T) {
	rate := 48000.0
	n := 2 * int(rate)
	x := sine(n, rate, 997, 0.25, 0)
	rng := rand.New(rand.NewSource(1))
	sigma := 1e-4 // RMS
	for i := range x {
		x[i] += sigma * rng.NormFloat64()
	}
	r := AnalyzeTone(x, rate, 997, 20, 20000)
	// Noise in 20 Hz-20 kHz is sigma^2 * (19980 / 24000); fundamental power is 0.25^2/2.
	want := 10 * math.Log10(sigma*sigma*(19980.0/24000)/(0.25*0.25/2))
	if math.Abs(r.THDN-want) > 0.3 {
		t.Errorf("THD+N %.2f dB, want %.2f", r.THDN, want)
	}
}

// Noise floor of known-level noise must match.
func TestNoiseFloor(t *testing.T) {
	for _, rate := range []float64{48000, 192000} {
		rng := rand.New(rand.NewSource(2))
		x := make([]float64, int(3*rate))
		level := -100.0 // dBFS, RMS re full-scale sine
		sigma := FromDB(level) / math.Sqrt2
		for i := range x {
			x[i] = sigma * rng.NormFloat64()
		}
		r := AnalyzeNoise(x, rate, 20, 20000)
		want := level + 10*math.Log10(19980/(rate/2))
		if math.Abs(r.DBFS-want) > 0.3 {
			t.Errorf("%.0f Hz: noise %.2f dBFS, want %.2f", rate, r.DBFS, want)
		}
		wantWide := level + 10*math.Log10((rate/2-10)/(rate/2))
		if math.Abs(r.DBFSWide-wantWide) > 0.3 {
			t.Errorf("%.0f Hz: wideband %.2f dBFS, want %.2f", rate, r.DBFSWide, wantWide)
		}
		// A-weighting of white noise over 20 Hz-20 kHz: integrate the curve.
		s := 0.0
		for f := 20.0; f <= 20000; f += 1 {
			s += math.Pow(10, AWeight(f)/10)
		}
		wantA := level + 10*math.Log10(s/(rate/2))
		if math.Abs(r.DBFSA-wantA) > 0.3 {
			t.Errorf("%.0f Hz: A-weighted %.2f dBFS, want %.2f", rate, r.DBFSA, wantA)
		}
	}
}

func TestAWeighting(t *testing.T) {
	for _, c := range []struct{ f, db float64 }{{1000, 0}, {100, -19.1}, {10000, -2.5}, {31.5, -39.4}, {20000, -9.3}} {
		if g := AWeight(c.f); math.Abs(g-c.db) > 0.15 {
			t.Errorf("A(%g) = %.2f, want %.1f", c.f, g, c.db)
		}
	}
}

func TestSmoothingKeepsFlat(t *testing.T) {
	ir := make([]float64, 4096)
	ir[100] = 0.5
	s := ResponseOf(ir, 48000, 100, 0)
	for _, sm := range []int{0, 3, 6, 12} {
		c := s.CurveFrom(LogGrid(20, 20000, 12), sm)
		lo, hi := c.Range(20, 20000)
		if math.Abs(lo-DB(0.5)) > 0.01 || math.Abs(hi-DB(0.5)) > 0.01 {
			t.Errorf("1/%d smoothing: %.3f..%.3f", sm, lo, hi)
		}
		for _, g := range c.GD {
			if math.Abs(g) > 1e-3 {
				t.Fatalf("group delay %.4f ms after removing the bulk delay", g)
			}
		}
	}
}

func TestMarkerAndFracShift(t *testing.T) {
	rate := 48000.0
	sw := Sweep{F1: 300, F2: 6000, T: 0.5, Amp: 0.25, Fade: 0.02}
	ref := sw.Render(rate, 1)
	x := make([]float64, int(2*rate))
	copy(x[30000:], ref)
	x = FracShift(x, 0.4)
	rng := rand.New(rand.NewSource(3))
	for i := range x {
		x[i] += 1e-3 * rng.NormFloat64()
	}
	p, c := FindMarker(x, ref)
	if math.Abs(p-30000.4) > 0.15 {
		t.Errorf("marker at %.2f, want 30000.4", p)
	}
	if c < 20 {
		t.Errorf("confidence %.1f", c)
	}
}

func TestResampleTone(t *testing.T) {
	x := sine(44100, 44100, 1000, 0.5, 0)
	y := Resample(x, 44100, 96000)
	r := AnalyzeTone(y[2000:len(y)-2000], 96000, 1000, 20, 20000)
	if math.Abs(r.LevelDBFS-DB(0.5)) > 0.02 || r.THDN > -110 {
		t.Errorf("resampled tone: level %.3f, THD+N %.1f", r.LevelDBFS, r.THDN)
	}
}
