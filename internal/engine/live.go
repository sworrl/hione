package engine

import (
	"math"
	"sync"
	"time"

	"github.com/sworrl/hione/internal/dsp"
)

// Live is the rolling view of the capture for the meters, spectrum and spectrogram.
type Live struct {
	mu        sync.Mutex
	rate      float64
	hist      [2][]float64 // last fftN samples per channel
	fftN      int
	peak      [2]float64 // held peak, linear
	rms       [2]float64
	clipUntil [2]time.Time
	lastSpec  time.Time
	snap      LiveSnapshot
	spectro   [][]float32 // rows of log bins (mono)
	bins      []float64   // bin centre frequencies
}

// LiveSnapshot is what /api/live returns and the UI draws.
type LiveSnapshot struct {
	Time      time.Time   `json:"time"`
	Rate      float64     `json:"rate"`
	PeakDBFS  [2]float64  `json:"peak_dbfs"`
	RMSDBFS   [2]float64  `json:"rms_dbfs"` // RMS re full-scale sine (RMS + 3 dB)
	Clip      [2]bool     `json:"clip"`
	Freqs     []float64   `json:"freqs"`
	SpecDB    [2][]float64 `json:"spec_db"` // dBFS per bin, log-spaced
	Capturing bool        `json:"capturing"`
	Error     string      `json:"error,omitempty"`
}

func newLive(rate float64) *Live {
	l := &Live{rate: rate, fftN: 16384}
	if rate > 100000 {
		l.fftN = 32768
	}
	// 240 log bins from 10 Hz to Nyquist.
	nb := 240
	hi := rate / 2
	for i := 0; i < nb; i++ {
		l.bins = append(l.bins, 10*math.Pow(hi/10, float64(i)/float64(nb-1)))
	}
	return l
}

func (l *Live) feed(blk [][]float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(blk[0])
	for c := 0; c < 2 && c < len(blk); c++ {
		pk, ss := 0.0, 0.0
		for _, v := range blk[c] {
			a := math.Abs(v)
			if a > pk {
				pk = a
			}
			ss += v * v
		}
		// Peak hold with ~20 dB/s fall, RMS with a ~300 ms time constant.
		dt := float64(n) / l.rate
		l.peak[c] = math.Max(pk, l.peak[c]*math.Pow(10, -20*dt/20))
		r := math.Sqrt(ss / float64(n))
		k := math.Exp(-dt / 0.3)
		l.rms[c] = math.Sqrt(k*l.rms[c]*l.rms[c] + (1-k)*r*r)
		if pk >= 0.995 {
			l.clipUntil[c] = time.Now().Add(2 * time.Second)
		}
		l.hist[c] = append(l.hist[c], blk[c]...)
		if len(l.hist[c]) > l.fftN {
			l.hist[c] = l.hist[c][len(l.hist[c])-l.fftN:]
		}
	}
	if time.Since(l.lastSpec) > 150*time.Millisecond && len(l.hist[0]) == l.fftN {
		l.lastSpec = time.Now()
		l.computeSpectrum()
	}
}

func (l *Live) computeSpectrum() {
	w := dsp.Hann(l.fftN)
	sw := 0.0
	for _, v := range w {
		sw += v
	}
	df := l.rate / float64(l.fftN)
	row := make([]float32, len(l.bins))
	for c := 0; c < 2; c++ {
		x := make([]float64, l.fftN)
		for i := range x {
			x[i] = l.hist[c][i] * w[i]
		}
		X := dsp.RFFT(x, l.fftN)
		out := make([]float64, len(l.bins))
		for i, f := range l.bins {
			// Max over the bins this log band covers (peaks stay visible).
			lo := f * math.Pow(2, -1.0/48)
			hi := f * math.Pow(2, 1.0/48)
			a := int(lo / df)
			b := int(hi/df) + 1
			if b >= len(X) {
				b = len(X) - 1
			}
			m := 0.0
			for k := max(a, 1); k <= b; k++ {
				v := real(X[k])*real(X[k]) + imag(X[k])*imag(X[k])
				if v > m {
					m = v
				}
			}
			// Sine amplitude scaling: |X| = A * sum(w) / 2.
			out[i] = dsp.DB(2 * math.Sqrt(m) / sw)
			if c == 0 {
				row[i] = float32(out[i])
			} else if float32(out[i]) > row[i] {
				row[i] = float32(out[i])
			}
		}
		l.snap.SpecDB[c] = out
	}
	l.spectro = append(l.spectro, row)
	if len(l.spectro) > 400 {
		l.spectro = l.spectro[len(l.spectro)-400:]
	}
}

// Snapshot returns the current meters and spectrum.
func (l *Live) Snapshot() LiveSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.snap
	s.Time = time.Now()
	s.Rate = l.rate
	s.Freqs = l.bins
	for c := 0; c < 2; c++ {
		s.PeakDBFS[c] = math.Max(dsp.DB(l.peak[c]), -150)
		s.RMSDBFS[c] = math.Max(dsp.DB(l.rms[c]*math.Sqrt2), -150)
		s.Clip[c] = time.Now().Before(l.clipUntil[c])
	}
	return s
}

// Spectrogram returns the most recent rows (oldest first).
func (l *Live) Spectrogram(maxRows int) [][]float32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := l.spectro
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	out := make([][]float32, len(rows))
	copy(out, rows)
	return out
}
