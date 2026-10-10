package report

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-pdf/fpdf"

	"github.com/sworrl/hione/internal/engine"
	"github.com/sworrl/hione/internal/plot"
)

// PDF builds the review report: cover, results table, every chart full width with a caption,
// and a methods section. Charts are vectors.
func PDF(r *engine.Run, th *plot.Theme) ([]byte, error) {
	p := fpdf.New("P", "pt", "Letter", "")
	tr := p.UnicodeTranslatorFromDescriptor("")
	p.SetMargins(48, 48, 48)
	p.SetAutoPageBreak(true, 48)
	pw, _ := p.GetPageSize()
	cw := pw - 96
	p.SetFooterFunc(func() {
		p.SetY(-34)
		p.SetFont("Helvetica", "", 8)
		p.SetTextColor(138, 137, 132)
		p.CellFormat(0, 10, tr(fmt.Sprintf("Hione audio measurement, run %s, page %d", r.ID, p.PageNo())), "", 0, "C", false, 0, "")
	})
	// Cover.
	p.AddPage()
	p.SetFont("Helvetica", "B", 22)
	p.SetTextColor(11, 11, 11)
	dev := r.Device["device"]
	if dev == "" {
		dev = "Device under test"
	}
	p.MultiCell(cw, 28, tr(dev), "", "L", false)
	p.SetFont("Helvetica", "", 13)
	p.SetTextColor(82, 81, 78)
	p.MultiCell(cw, 18, tr("Measurement report, "+r.Started.Format("January 2, 2006 15:04")), "", "L", false)
	p.Ln(12)
	kv := func(k, v string) {
		if v == "" {
			return
		}
		p.SetFont("Helvetica", "B", 10)
		p.SetTextColor(11, 11, 11)
		p.CellFormat(150, 14, tr(k), "", 0, "L", false, 0, "")
		p.SetFont("Helvetica", "", 10)
		p.SetTextColor(82, 81, 78)
		p.MultiCell(cw-150, 14, tr(v), "", "L", false)
	}
	kv("Device", r.Device["device"])
	kv("Firmware", r.Device["build"])
	kv("Output", strings.TrimSpace(r.Spec.Jack+" "+r.Device["output"]))
	kv("Volume", r.Device["volume"])
	if r.UserDAC != nil {
		kv("DAC settings before the run", r.UserDAC.String()+" (restored after the run)")
	}
	kv("Capture chain", fmt.Sprintf("%s, %s Hz, %s", r.Capture["uri"], r.Capture["rate"], r.Capture["format"]))
	if r.Level != nil {
		kv("Test level", fmt.Sprintf("%.0f dBFS digital", r.Level.TestLevel))
		kv("Level check", r.Level.Message)
	}
	kv("Run", fmt.Sprintf("%s, %s, %d modes", r.ID, r.Status, len(r.Steps)))
	p.Ln(16)
	// Results table.
	p.SetFont("Helvetica", "B", 13)
	p.SetTextColor(11, 11, 11)
	p.CellFormat(0, 18, "Results", "", 1, "L", false, 0, "")
	cols := []struct {
		h string
		w float64
	}{{"Mode", 190}, {"FR dev. dB", 54}, {"THD+N dB", 50}, {"Noise dBFS(A)", 62}, {"SNR dB(A)", 50}, {"Xtalk dB", 46}, {"Level dBFS", 52}}
	p.SetFont("Helvetica", "B", 8)
	p.SetFillColor(243, 242, 238)
	for i, c := range cols {
		al := "R"
		if i == 0 {
			al = "L"
		}
		p.CellFormat(c.w, 14, tr(c.h), "B", 0, al, true, 0, "")
	}
	p.Ln(-1)
	p.SetFont("Helvetica", "", 8)
	p.SetTextColor(11, 11, 11)
	for _, st := range r.Steps {
		cells := []string{st.Label, "-", "-", "-", "-", "-", "-"}
		if found(st) {
			L := st.Result.Ch[0]
			lo, hi := L.FR.Range(20, 20000)
			cells[1] = fmt.Sprintf("%+.2f/%+.2f", lo, hi)
			if L.Tone != nil {
				cells[2], cells[6] = f1(L.Tone.THDN), f1(L.Tone.LevelDBFS)
			}
			if L.Noise != nil {
				cells[3] = f1(L.Noise.DBFSA)
			}
			cells[4], cells[5] = f1(L.SNRA), f1(L.XtalkDB)
		} else {
			cells[1] = st.Status
		}
		for i, c := range cols {
			al := "R"
			if i == 0 {
				al = "L"
			}
			txt := cells[i]
			for p.GetStringWidth(tr(txt)) > c.w-4 && len(txt) > 4 {
				txt = txt[:len(txt)-2]
			}
			p.CellFormat(c.w, 12, tr(txt), "B", 0, al, false, 0, "")
		}
		p.Ln(-1)
	}
	p.Ln(6)
	p.SetFont("Helvetica", "", 8)
	p.SetTextColor(82, 81, 78)
	p.MultiCell(cw, 11, tr("FR dev. is the lowest and highest point of the left channel's response re 1 kHz between 20 Hz and 20 kHz. THD+N is at 1 kHz over 20 Hz to 20 kHz, re the fundamental. Noise is with digital silence playing. SNR is A-weighted and re the device's full scale. Crosstalk is the left-only tone leaking into the right input."), "", "L", false)
	// Charts, two per page.
	charts := Charts(r)
	o := Opts{Theme: th, Stamp: false, Mark: true}
	for i, cs := range charts {
		if i%2 == 0 {
			p.AddPage()
		}
		h := cs.H * cw / cs.W
		if p.GetY()+h+30 > 792-48 {
			p.AddPage()
		}
		y := p.GetY()
		cs.Draw(plot.NewPDFCanvas(p, 48, y, cs.W, cs.H, cw), o)
		p.SetY(y + h + 4)
		p.SetFont("Helvetica", "", 9)
		p.SetTextColor(82, 81, 78)
		p.MultiCell(cw, 11, tr(cs.Caption), "", "L", false)
		p.Ln(10)
	}
	// Methods.
	p.AddPage()
	p.SetFont("Helvetica", "B", 13)
	p.SetTextColor(11, 11, 11)
	p.CellFormat(0, 18, "Methods", "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "", 9.5)
	p.SetTextColor(40, 40, 38)
	for _, para := range methods(r) {
		p.MultiCell(cw, 13, tr(para), "", "L", false)
		p.Ln(6)
	}
	var b bytes.Buffer
	if err := p.Output(&b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func methods(r *engine.Run) []string {
	return []string{
		"Each mode plays one test file through the device: a start marker, a 1 kHz tone, an exponential sine sweep (Farina method), 32 single-sample impulses, digital silence, a 1 kHz tone 60 dB down, left-only and right-only tones, and an end marker. The device plays it from its own storage, so the digital path is the device's normal playback path.",
		"The capture is located by the start and end markers. The time between them gives the clock difference between the device and the capture card, and the analysis follows that difference. The frequency response comes from deconvolving the sweep, windowed 5 ms before to 600 ms after the peak, and corrected by the same processing applied to the stimulus itself. The impulse view averages 32 impulses after aligning them to the sub-sample.",
		"THD and THD+N use a 7-term Blackman-Harris window. THD sums harmonics 2 to 10. THD+N is everything in 20 Hz to 20 kHz except the fundamental. Noise is measured with digital silence playing, in 20 Hz to 20 kHz, unweighted and A-weighted. SNR and dynamic range (AES17, from the tone 60 dB down) are referred to the device's full scale by adding the test level's headroom, which assumes the device stays linear up to 0 dBFS.",
		"Capture limits: every result includes the capture card's own noise, distortion and anti-alias filter. When a device result is near the card's own floor, the result describes the card. A loopback calibration (the card's output into its input) measures that floor. Capture chain for this run: " + fmt.Sprintf("%s at %s Hz %s.", r.Capture["uri"], r.Capture["rate"], r.Capture["format"]),
		"Levels are in dBFS of the capture card unless a level calibration was made (a known voltage at the input), in which case output levels are also given in dBV.",
	}
}

// FileBase is the device part of export file names.
func FileBase(r *engine.Run) string {
	d := r.Device["device"]
	if i := strings.Index(d, "("); i > 0 {
		d = d[:i]
	}
	if d == "" {
		d = "device"
	}
	return slug(d)
}

// Zip bundles the PDF, every chart as SVG and PNG (2x), and the data files.
func Zip(r *engine.Run, th *plot.Theme, mark bool) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	add := func(name string, b []byte) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	base := FileBase(r)
	pdf, err := PDF(r, th)
	if err != nil {
		return nil, err
	}
	add(fmt.Sprintf("%s_report_%s.pdf", base, r.ID), pdf)
	o := Opts{Theme: th, Stamp: true, Mark: mark}
	for _, cs := range Charts(r) {
		name := fmt.Sprintf("%s_%s", base, cs.ID)
		add("charts/"+name+".svg", SVG(cs, o))
		if b, err := PNG(cs, o, 2); err == nil {
			add("charts/"+name+".png", b)
		}
	}
	// Data files written by the run.
	filepath.Walk(r.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(r.Dir, path)
		ext := strings.ToLower(filepath.Ext(rel))
		if ext == ".csv" || ext == ".json" || ext == ".md" || ext == ".html" {
			b, err := os.ReadFile(path)
			if err == nil {
				add("data/"+filepath.ToSlash(rel), b)
			}
		}
		return nil
	})
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FindChart returns the chart with id.
func FindChart(r *engine.Run, id string) (ChartSpec, bool) {
	for _, cs := range Charts(r) {
		if cs.ID == id {
			return cs, true
		}
	}
	return ChartSpec{}, false
}

var _ = json.Marshal
var _ = math.Abs
