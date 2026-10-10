package dsp

import (
	"math"
)

// ToneResult is the analysis of a single sine.
type ToneResult struct {
	Freq       float64   `json:"freq_hz"`
	LevelDBFS  float64   `json:"level_dbfs"`   // fundamental, dBFS (full-scale sine = 0 dBFS)
	THD        float64   `json:"thd_db"`       // harmonics 2..10 re fundamental, dB
	THDPct     float64   `json:"thd_pct"`      // same in percent
	THDN       float64   `json:"thdn_db"`      // everything but the fundamental in band, re fundamental, dB
	THDNPct    float64   `json:"thdn_pct"`     // same in percent
	THDNA      float64   `json:"thdn_a_db"`    // A-weighted THD+N re fundamental
	ResidDBFS  float64   `json:"resid_dbfs"`   // level of the THD+N residual, dBFS
	ResidADBFS float64   `json:"resid_a_dbfs"` // A-weighted residual, dBFS
	Harmonics  []float64 `json:"harmonics_db"` // H2.. re fundamental, dB
}

// spectrumPower returns per-bin power of the windowed signal, scaled so that the sum over the
// bins of a sine's main lobe equals the sine's mean-square value (A^2/2).
func spectrumPower(x []float64, w []float64) (p []float64, n int) {
	n = len(x)
	buf := make([]float64, n)
	sw2 := 0.0
	for i := range x {
		buf[i] = x[i] * w[i]
		sw2 += w[i] * w[i]
	}
	X := plan(n).Coefficients(nil, buf)
	p = make([]float64, len(X))
	for k, v := range X {
		m := real(v)*real(v) + imag(v)*imag(v)
		s := 2.0
		if k == 0 || k == len(X)-1 {
			s = 1
		}
		p[k] = s * m / (float64(n) * sw2)
	}
	return p, n
}

// AnalyzeTone measures a sine near f0 in x. The band for THD+N is [lo, hi] Hz. Use an x of a
// few hundred milliseconds or more; the length is cut to an even count.
func AnalyzeTone(x []float64, rate, f0, lo, hi float64) ToneResult {
	if len(x) < 1024 {
		nan := math.NaN()
		return ToneResult{Freq: f0, LevelDBFS: -999, THD: nan, THDPct: nan, THDN: nan, THDNPct: nan, THDNA: nan, ResidDBFS: nan, ResidADBFS: nan}
	}
	n := GoodLen(len(x))
	x = x[:n]
	// Remove DC first so it cannot leak into the low bins.
	mean := 0.0
	for _, v := range x {
		mean += v
	}
	mean /= float64(n)
	xx := make([]float64, n)
	for i, v := range x {
		xx[i] = v - mean
	}
	w := BlackmanHarris7(n)
	p, _ := spectrumPower(xx, w)
	df := rate / float64(n)
	const lobe = 8 // BH7 main lobe half-width in bins, with margin
	// Find the fundamental within +-3% of f0.
	k0 := int(f0 / df)
	ka := max(int(f0*0.97/df), 1)
	kb := min(int(f0*1.03/df)+1, len(p)-1)
	for k := ka; k <= kb; k++ {
		if p[k] > p[k0] {
			k0 = k
		}
	}
	// Refine the frequency with a power-weighted centroid over the lobe.
	sw, swk := 0.0, 0.0
	for k := max(k0-lobe, 1); k <= min(k0+lobe, len(p)-1); k++ {
		sw += p[k]
		swk += p[k] * float64(k)
	}
	freq := swk / sw * df
	fund := sw
	r := ToneResult{Freq: freq}
	r.LevelDBFS = DBPow(fund * 2) // A^2/2 -> A^2, so a full-scale sine reads 0 dBFS
	// Harmonics.
	hsum := 0.0
	for h := 2; h <= 10; h++ {
		fh := freq * float64(h)
		if fh > hi || fh > rate/2-lobe*df {
			break
		}
		kc := int(math.Round(fh / df))
		// Peak search +-2 bins for slight drift, then sum the lobe.
		for k := kc - 2; k <= kc+2; k++ {
			if k > 0 && k < len(p) && p[k] > p[kc] {
				kc = k
			}
		}
		s := 0.0
		for k := max(kc-lobe, 1); k <= min(kc+lobe, len(p)-1); k++ {
			s += p[k]
		}
		hsum += s
		r.Harmonics = append(r.Harmonics, DBPow(s/fund))
	}
	r.THD = DBPow(hsum / fund)
	r.THDPct = 100 * math.Sqrt(hsum/fund)
	// Residual: everything in band except the fundamental lobe.
	resid, residA := 0.0, 0.0
	kl := max(int(math.Ceil(lo/df)), 1)
	kh := min(int(hi/df), len(p)-1)
	for k := kl; k <= kh; k++ {
		if k >= k0-lobe && k <= k0+lobe {
			continue
		}
		resid += p[k]
		residA += p[k] * math.Pow(10, AWeight(float64(k)*df)/10)
	}
	// Fill the hole under the fundamental with the mean residual density so broadband noise
	// is not under-counted.
	nb := float64(kh - kl + 1 - (2*lobe + 1))
	if nb > 0 {
		resid += resid / nb * float64(2*lobe+1)
		residA += residA / nb * float64(2*lobe+1)
	}
	r.THDN = DBPow(resid / fund)
	r.THDNPct = 100 * math.Sqrt(resid/fund)
	r.THDNA = DBPow(residA / fund)
	r.ResidDBFS = DBPow(resid * 2)
	r.ResidADBFS = DBPow(residA * 2)
	return r
}

// LevelAt returns the level in dBFS of the component at f (sum over the BH7 lobe). Narrowband,
// so it reads a crosstalk tone well below the broadband noise.
func LevelAt(x []float64, rate, f float64) float64 {
	if len(x) < 1024 {
		return -999
	}
	n := GoodLen(len(x))
	p, _ := spectrumPower(x[:n], BlackmanHarris7(n))
	df := rate / float64(n)
	kc := int(math.Round(f / df))
	s := 0.0
	for k := max(kc-8, 1); k <= min(kc+8, len(p)-1); k++ {
		s += p[k]
	}
	return DBPow(s * 2)
}

// NoiseResult is a broadband noise measurement.
type NoiseResult struct {
	DBFS      float64 `json:"dbfs"`        // unweighted, lo..hi, RMS re full-scale sine (RMS + 3.01 dB)
	DBFSA     float64 `json:"dbfs_a"`      // A-weighted
	DBFSWide  float64 `json:"dbfs_wide"`   // unweighted, everything above 10 Hz up to Nyquist
	PeakDBFS  float64 `json:"peak_dbfs"`   // sample peak
	HumDBFS   float64 `json:"hum_dbfs"`    // largest of 50/60 Hz and harmonics up to 300 Hz
	HumFreq   float64 `json:"hum_hz"`
	PSDHz     []float64 `json:"-"`
	PSDDB     []float64 `json:"-"` // dBFS per bin (for plotting)
}

// AnalyzeNoise measures noise in x with a Welch average of Hann-windowed blocks.
func AnalyzeNoise(x []float64, rate, lo, hi float64) NoiseResult {
	bl := NextPow2(int(rate / 2)) // ~0.5 s blocks, < 2 Hz bins
	if bl > len(x) {
		bl = NextPow2(len(x)) / 2
	}
	if bl < 256 {
		return NoiseResult{DBFS: -999, DBFSA: -999, DBFSWide: -999}
	}
	w := Hann(bl)
	acc := make([]float64, bl/2+1)
	cnt := 0
	for off := 0; off+bl <= len(x); off += bl / 2 {
		seg := x[off : off+bl]
		mean := 0.0
		for _, v := range seg {
			mean += v
		}
		mean /= float64(bl)
		s := make([]float64, bl)
		for i := range s {
			s[i] = seg[i] - mean
		}
		p, _ := spectrumPower(s, w)
		for k := range acc {
			acc[k] += p[k]
		}
		cnt++
	}
	df := rate / float64(bl)
	var r NoiseResult
	sum, sumA, sumW := 0.0, 0.0, 0.0
	r.PSDHz = make([]float64, len(acc))
	r.PSDDB = make([]float64, len(acc))
	for k := range acc {
		acc[k] /= float64(cnt)
		f := float64(k) * df
		r.PSDHz[k] = f
		r.PSDDB[k] = DBPow(acc[k] * 2)
		if f >= 10 {
			sumW += acc[k]
		}
		if f >= lo && f <= hi {
			sum += acc[k]
			sumA += acc[k] * math.Pow(10, AWeight(f)/10)
		}
	}
	r.DBFS = DBPow(sum * 2)
	r.DBFSA = DBPow(sumA * 2)
	r.DBFSWide = DBPow(sumW * 2)
	r.PeakDBFS = DB(Peak(x))
	r.HumDBFS = -999
	for _, base := range []float64{50, 60} {
		for h := 1; h <= 6; h++ {
			f := base * float64(h)
			k := int(math.Round(f / df))
			s := 0.0
			for j := k - 1; j <= k+1; j++ {
				if j > 0 && j < len(acc) {
					s += acc[j]
				}
			}
			if d := DBPow(s * 2); d > r.HumDBFS {
				r.HumDBFS, r.HumFreq = d, f
			}
		}
	}
	return r
}
