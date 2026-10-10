package analysis

import (
	"math"
	"testing"

	"github.com/sworrl/hione/internal/dsp"
	"github.com/sworrl/hione/internal/sim"
	"github.com/sworrl/hione/internal/stim"
)

// End to end through the simulator: every number the model sets must come back.
func TestSimulatedLoopback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		play, cap     float64
		drift         float64
	}{
		{"same rate, no drift", 48000, 48000, 0},
		{"same rate, 40 ppm drift", 48000, 48000, 40},
		{"44.1k into 96k", 44100, 96000, -15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := stim.Build(stim.Options{Name: "quick", Level: -12, PlayRate: tc.play})
			if err != nil {
				t.Fatal(err)
			}
			sig := plan.Render(tc.play, 0)
			m := sim.Model{
				GainDB:    -3,
				Filters:   []dsp.Biquad{dsp.Peaking(tc.cap, 3000, 4, 1.2)},
				H2:        0.002, // H2 at -12 dBFS input: 0.002*a/2 re a -> about -72 dB
				H3:        0,
				NoiseDBFS: -100,
				XtalkDB:   -70,
				Delay:     0.37,
				DriftPPM:  tc.drift,
				InvertR:   true,
				Seed:      3,
			}
			rec := m.Apply(sig, tc.play, tc.cap)
			r := Analyze(rec, plan, Options{PlayRate: tc.play, CapRate: tc.cap, DBVAtFS: math.NaN()})
			if !r.Found {
				t.Fatalf("not found: %s", r.Message)
			}
			if math.Abs(r.DriftPPM-tc.drift) > 3 {
				t.Errorf("drift %.1f ppm, want %.1f", r.DriftPPM, tc.drift)
			}
			L := r.Ch[0]
			// FR shape: the peaking filter re its 1 kHz value.
			ref := dsp.DB(cmplxAbs(m.Filters[0].Response(tc.cap, 1000)))
			if math.Abs(L.GainDB-(-3+ref)) > 0.1 {
				t.Errorf("gain %.2f dB, want %.2f", L.GainDB, -3+ref)
			}
			worst, worstF := 0.0, 0.0
			for i, f := range L.FR.Hz {
				if f < 30 || f > 0.9*plan.Find("fr").Sweep.F2 {
					continue
				}
				want := dsp.DB(cmplxAbs(m.Filters[0].Response(tc.cap, f))) - ref
				if tc.play != tc.cap && f > 0.85*tc.play/2 {
					continue // the resampler's own roll-off
				}
				if e := math.Abs(L.FR.DB[i] - want); e > worst {
					worst, worstF = e, f
				}
			}
			if worst > 0.1 {
				t.Errorf("FR error %.3f dB at %.0f Hz, want <= 0.1", worst, worstF)
			}
			// H2 of x + h2*x^2 for a sine of amplitude a is h2*a/2 re a.
			a := dsp.FromDB(-12)
			wantH2 := dsp.DB(0.002*a/2) + dsp.DB(cmplxAbs(m.Filters[0].Response(tc.cap, 2000))) - ref
			if got := L.Tone.Harmonics[0]; math.Abs(got-wantH2) > 0.5 {
				t.Errorf("H2 %.2f dB, want %.2f", got, wantH2)
			}
			if L.Polarity != "normal" || r.Ch[1].Polarity != "inverted" {
				t.Errorf("polarity L %s R %s", L.Polarity, r.Ch[1].Polarity)
			}
			if math.Abs(L.XtalkDB-(-70)) > 1 {
				t.Errorf("crosstalk %.1f dB, want -70", L.XtalkDB)
			}
			// Noise: white noise at -100 dBFS over the full band, measured 20 Hz - 20 kHz.
			wantN := -100 + 10*math.Log10((20000-20)/(tc.cap/2))
			if math.Abs(L.Noise.DBFS-wantN) > 0.5 {
				t.Errorf("noise %.2f dBFS, want %.2f", L.Noise.DBFS, wantN)
			}
			if L.IR == nil {
				t.Errorf("no impulse average")
			}
		})
	}
}

func TestNoSignal(t *testing.T) {
	plan, _ := stim.Build(stim.Options{Name: "quick", Level: -12, PlayRate: 48000})
	m := sim.Model{NoiseDBFS: -90, XtalkDB: -999, Seed: 1}
	rec := m.NoiseOnly(48000*5, 48000, newRand(), 0)
	r := Analyze(rec, plan, Options{PlayRate: 48000, CapRate: 48000})
	if r.Found {
		t.Fatalf("found a signal in noise (confidence %.1f)", r.SyncConf)
	}
	if len(r.IdleNoise) != 2 {
		t.Fatal("no idle noise")
	}
}
