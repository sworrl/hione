// Package report writes a run's results: PNG and SVG charts, an HTML page, a Markdown page for
// reviews, CSV per measurement, and the JSON the M500 Hardware app imports.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sworrl/hione/internal/adb"
	"github.com/sworrl/hione/internal/analysis"
	"github.com/sworrl/hione/internal/engine"
	"github.com/sworrl/hione/internal/plot"
)

type figure struct {
	File    string // base name without extension
	Caption string
	SVG     string
}

type group struct {
	Title string
	Steps []*engine.Step
}

func found(st *engine.Step) bool {
	return st.Result != nil && st.Result.Found && len(st.Result.Ch) > 0
}

// groups splits the run into chart groups: filters per rate (at the highest gain measured),
// filters at other gains, high power and DRE, single measurements.
func groups(r *engine.Run) []group {
	byKey := map[string]*group{}
	var order []string
	add := func(key, title string, st *engine.Step) {
		g, ok := byKey[key]
		if !ok {
			g = &group{Title: title}
			byKey[key] = g
			order = append(order, key)
		}
		g.Steps = append(g.Steps, st)
	}
	for _, st := range r.Steps {
		if !found(st) {
			continue
		}
		switch st.Group {
		case "filters":
			add(fmt.Sprintf("f-%d-%s", st.PlayRate, st.DAC.Gain),
				fmt.Sprintf("Digital filters at %.1f kHz, gain %s", float64(st.PlayRate)/1000, st.DAC.Gain), st)
		case "power":
			add("power", "High power and DRE", st)
		default:
			add("single", "Measurement", st)
		}
	}
	var out []group
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func shortLabel(st *engine.Step, g group) string {
	if st.Group == "filters" {
		return adb.FilterLabel(st.DAC.Filter)
	}
	return st.Label
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// Opts are the export choices for a chart.
type Opts struct {
	Theme       *plot.Theme
	Stamp       bool // bake device, settings, capture chain and date into the chart
	Mark        bool // the small Hione mark
	Transparent bool
}

// ChartSpec is one chart of a run, drawable in any theme.
type ChartSpec struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Caption string `json:"caption"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	draw    func(c plot.Canvas, o Opts)
}

// Draw renders the chart with o.
func (cs ChartSpec) Draw(c plot.Canvas, o Opts) { cs.draw(c, o) }

func stampLines(r *engine.Run) []string {
	dev := r.Device["device"]
	if b := r.Device["build"]; b != "" {
		dev += ", " + b
	}
	if r.Spec.Jack != "" {
		dev += ", " + r.Spec.Jack + " out"
	}
	if v := r.Device["volume"]; v != "" {
		dev += ", volume " + v
	}
	cap := fmt.Sprintf("Capture %s, %s Hz %s", r.Capture["uri"], r.Capture["rate"], r.Capture["format"])
	if r.Level != nil {
		cap += fmt.Sprintf(", test level %.0f dBFS", r.Level.TestLevel)
	}
	return []string{dev, cap + ". " + r.Started.Format("2006-01-02 15:04")}
}

func (o Opts) stamp(r *engine.Run, extra string) *plot.Stamp {
	if !o.Stamp && !o.Mark {
		return nil
	}
	st := &plot.Stamp{Mark: o.Mark}
	if o.Stamp {
		if extra != "" {
			st.Lines = append(st.Lines, extra)
		}
		st.Lines = append(st.Lines, stampLines(r)...)
	}
	return st
}

func commonDAC(steps []*engine.Step) string {
	if len(steps) == 0 {
		return ""
	}
	d := steps[0].DAC
	var parts []string
	same := func(f func(adb.DAC) string) bool {
		for _, s := range steps {
			if f(s.DAC) != f(d) {
				return false
			}
		}
		return f(d) != ""
	}
	if same(func(x adb.DAC) string { return x.Filter }) {
		parts = append(parts, "filter "+adb.FilterLabel(d.Filter))
	}
	if same(func(x adb.DAC) string { return x.Gain }) {
		parts = append(parts, "gain "+d.Gain)
	}
	if same(func(x adb.DAC) string { return x.DRE }) {
		parts = append(parts, "DRE "+onoff(d.DRE == "dremode_enable"))
	}
	if same(func(x adb.DAC) string { return x.HP }) {
		parts = append(parts, "high power "+onoff(d.HP == "hpower_enable"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "DAC: " + strings.Join(parts, ", ") + ". Other settings as in the legend."
}

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Charts lists the charts of a run.
func Charts(r *engine.Run) []ChartSpec {
	var out []ChartSpec
	const W, H = 900.0, 520.0
	capNote := "Left channel."
	for _, g := range groups(r) {
		g := g
		base := slug(g.Title)
		var fr, ir []plot.Series
		f2 := 20000.0
		for _, st := range g.Steps {
			L := st.Result.Ch[0]
			name := shortLabel(st, g)
			fr = append(fr, plot.Series{Name: name, X: L.FR.Hz, Y: L.FR.DB})
			f2 = math.Max(f2, L.FR.Hz[len(L.FR.Hz)-1])
			if L.IR != nil {
				xs := make([]float64, len(L.IR))
				for i := range xs {
					xs[i] = L.IRStart + float64(i)/st.Result.CapRate*1000
				}
				ir = append(ir, plot.Series{Name: name, X: xs, Y: L.IR})
			}
		}
		dac := commonDAC(g.Steps)
		out = append(out, ChartSpec{ID: base + "-fr", Title: "Frequency response: " + g.Title, W: W, H: H,
			Caption: "Frequency response re 1 kHz, 20 Hz to 20 kHz, no smoothing. " + dac,
			draw: func(c plot.Canvas, o Opts) {
				ch := &plot.Chart{Title: "Frequency response: " + g.Title, Subtitle: "dB re 1 kHz, no smoothing", XLabel: "Frequency (Hz)",
					YLabel: "dB", LogX: true, XMin: 20, XMax: 20000, YMin: -3, YMax: 1, YStep: 0.5, Bands: true, Series: fr, Note: capNote,
					Theme: o.Theme, Stamp: o.stamp(r, dac), Transparent: o.Transparent}
				ch.Draw(c)
			}})
		out = append(out, ChartSpec{ID: base + "-top", Title: "Top octave: " + g.Title, W: W, H: H,
			Caption: "The top octave, where the digital filters differ. " + dac,
			draw: func(c plot.Canvas, o Opts) {
				ch := &plot.Chart{Title: "Top octave: " + g.Title, Subtitle: "dB re 1 kHz", XLabel: "Frequency (Hz)",
					YLabel: "dB", LogX: true, XMin: 10000, XMax: f2, YMin: -6, YMax: 1, YStep: 1, Series: fr, Note: capNote,
					Theme: o.Theme, Stamp: o.stamp(r, dac), Transparent: o.Transparent}
				ch.Draw(c)
			}})
		if len(ir) > 0 {
			out = append(out, ChartSpec{ID: base + "-ir", Title: "Impulse response: " + g.Title, W: W, H: H,
				Caption: "Average of 32 single-sample impulses, normalized to the peak. Pre-ringing shows up left of 0 ms. " + dac,
				draw: func(c plot.Canvas, o Opts) {
					ch := &plot.Chart{Title: "Impulse response: " + g.Title, Subtitle: "Average of 32 single-sample impulses, normalized to the peak",
						XLabel: "Time re peak (ms)", XMin: -1, XMax: 3, YMin: -0.4, YMax: 1.1, YStep: 0.2, Series: ir, Note: capNote,
						Theme: o.Theme, Stamp: o.stamp(r, dac), Transparent: o.Transparent}
					ch.Draw(c)
				}})
		}
	}
	var labels []string
	var vals []float64
	for _, st := range r.Steps {
		if !found(st) || st.Result.Ch[0].Tone == nil || st.Result.Ch[0].Tone.THDN < -900 {
			continue
		}
		labels = append(labels, st.Label)
		vals = append(vals, st.Result.Ch[0].Tone.THDN)
	}
	if len(vals) > 0 {
		lo := math.Floor(minF(vals)/10)*10 - 10
		hi := math.Max(-20, maxF(vals)+5)
		h := 70 + float64(len(vals))*22 + 40
		out = append(out, ChartSpec{ID: "thdn", Title: "THD+N at 1 kHz", W: W, H: h + 44,
			Caption: fmt.Sprintf("THD+N at 1 kHz, left channel, 20 Hz to 20 kHz, re the fundamental, test level %s.", levelText(r)),
			draw: func(c plot.Canvas, o Opts) {
				b := &plot.Bars{Title: "THD+N at 1 kHz, left", Subtitle: fmt.Sprintf("dB re the fundamental, 20 Hz to 20 kHz, test level %s. Lower is better.", levelText(r)), Unit: "dB",
					Labels: labels, Values: vals, Min: lo, Max: hi, Theme: o.Theme, Stamp: o.stamp(r, ""), Transparent: o.Transparent}
				b.Draw(c)
			}})
	}
	return out
}

// SVG renders one chart.
func SVG(cs ChartSpec, o Opts) []byte {
	s := plot.NewSVG(cs.W, cs.H)
	cs.Draw(s, o)
	return s.Bytes()
}

// PNG renders one chart at scale (1, 2, 4).
func PNG(cs ChartSpec, o Opts, scale float64) ([]byte, error) {
	var r *plot.Raster
	if o.Transparent {
		r = plot.NewRasterTransparent(cs.W, cs.H, scale)
	} else {
		r = plot.NewRaster(cs.W, cs.H, scale)
	}
	cs.Draw(r, o)
	return r.PNG()
}

// Write renders everything for r into r.Dir.
func Write(r *engine.Run) error {
	figDir := filepath.Join(r.Dir, "charts")
	os.MkdirAll(figDir, 0o755)
	var figs []figure
	o := Opts{Theme: plot.Light, Stamp: true, Mark: true}
	for _, cs := range Charts(r) {
		svg := SVG(cs, o)
		os.WriteFile(filepath.Join(figDir, cs.ID+".svg"), svg, 0o644)
		if b, err := PNG(cs, o, 2); err == nil {
			os.WriteFile(filepath.Join(figDir, cs.ID+".png"), b, 0o644)
		}
		figs = append(figs, figure{File: "charts/" + cs.ID, Caption: cs.Caption, SVG: string(SVG(cs, Opts{Theme: plot.Light}))})
	}
	if err := writeHTML(r, groups(r), figs); err != nil {
		return err
	}
	if err := writeMarkdown(r, figs); err != nil {
		return err
	}
	if err := writeSummaryCSV(r); err != nil {
		return err
	}
	if b, err := PDF(r, plot.Light); err == nil {
		os.WriteFile(filepath.Join(r.Dir, "report.pdf"), b, 0o644)
	}
	return WriteHardwareJSON(r, filepath.Join(r.Dir, "hardware-curves.json"))
}

func levelText(r *engine.Run) string {
	if r.Level != nil {
		return fmt.Sprintf("%.0f dBFS", r.Level.TestLevel)
	}
	return "see run.json"
}

func minF(v []float64) float64 {
	m := math.Inf(1)
	for _, x := range v {
		if !math.IsNaN(x) {
			m = math.Min(m, x)
		}
	}
	return m
}

func maxF(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		if !math.IsNaN(x) {
			m = math.Max(m, x)
		}
	}
	return m
}


func f1(v float64) string {
	if math.IsNaN(v) || v < -900 {
		return "-"
	}
	return fmt.Sprintf("%.1f", v)
}

type row struct {
	Mode, Level, THD, THDN, NoiseA, SNR, DR, Xtalk, Bal, Pol, Drift string
}

func rows(r *engine.Run) []row {
	var out []row
	for _, st := range r.Steps {
		if !found(st) {
			out = append(out, row{Mode: st.Label, Level: st.Status + ": " + st.Error})
			continue
		}
		res := st.Result
		L := res.Ch[0]
		rw := row{Mode: st.Label, Drift: fmt.Sprintf("%.1f", res.DriftPPM), Bal: fmt.Sprintf("%.2f", res.Balance)}
		if L.Tone != nil {
			rw.Level, rw.THD, rw.THDN = f1(L.Tone.LevelDBFS), f1(L.Tone.THD), f1(L.Tone.THDN)
		}
		if L.Noise != nil {
			rw.NoiseA = f1(L.Noise.DBFSA)
		}
		rw.SNR, rw.DR, rw.Xtalk = f1(L.SNRA), f1(L.DRA), f1(L.XtalkDB)
		pol := L.Polarity
		if len(res.Ch) > 1 {
			pol += "/" + res.Ch[1].Polarity
		}
		rw.Pol = pol
		out = append(out, rw)
	}
	return out
}

func chainNote(r *engine.Run) string {
	return "Every number here went through the capture card's input, so its own noise, distortion and filter are part of each result. " +
		"Results close to the card's own limits measure the card, not the device. Run \"hione calibrate loopback\" to measure those limits, " +
		"and the report then lists them next to the device's numbers. Noise and SNR are in capture dBFS and re the device's full scale, " +
		"extrapolated from the test level, because the device was never driven to 0 dBFS."
}

func writeHTML(r *engine.Run, gs []group, figs []figure) error {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Hione report</title><style>
:root{color-scheme:light;--bg:#fcfcfb;--ink:#0b0b0b;--ink2:#52514e;--line:#e3e2de}
body{margin:0 auto;max-width:960px;padding:16px;background:var(--bg);color:var(--ink);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
h1{font-size:22px}h2{font-size:17px;margin-top:28px}figure{margin:16px 0}figure svg{width:100%;height:auto;border:1px solid var(--line);border-radius:6px}
figcaption{font-size:13px;color:var(--ink2)}table{border-collapse:collapse;width:100%;font-size:13px;font-variant-numeric:tabular-nums}
th,td{padding:4px 6px;border-bottom:1px solid var(--line);text-align:right}th:first-child,td:first-child{text-align:left}th{color:var(--ink2)}
.note{color:var(--ink2);font-size:13.5px}.wrap{overflow-x:auto}
</style></head><body>`)
	fmt.Fprintf(&b, "<h1>Measurement report, run %s</h1>", html.EscapeString(r.ID))
	fmt.Fprintf(&b, `<p class="note">%s. Status: %s.`, r.Started.Format("2006-01-02 15:04"), html.EscapeString(r.Status))
	if r.Error != "" {
		fmt.Fprintf(&b, " %s", html.EscapeString(r.Error))
	}
	b.WriteString("</p><table>")
	kv := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "<tr><td>%s</td><td style=\"text-align:left\">%s</td></tr>", html.EscapeString(k), html.EscapeString(v))
		}
	}
	for _, k := range sortedKeys(r.Device) {
		kv("Device "+k, r.Device[k])
	}
	kv("Capture", fmt.Sprintf("%s, %s Hz, %s", r.Capture["uri"], r.Capture["rate"], r.Capture["format"]))
	kv("Output jack", r.Spec.Jack)
	if r.UserDAC != nil {
		kv("Your DAC settings (restored after the run)", r.UserDAC.String())
	}
	if r.Level != nil {
		kv("Level check", r.Level.Message)
	}
	b.WriteString("</table>")
	fmt.Fprintf(&b, `<p class="note">%s</p>`, html.EscapeString(chainNote(r)))
	b.WriteString("<h2>Results</h2><div class=\"wrap\"><table><tr><th>Mode</th><th>Level dBFS</th><th>THD dB</th><th>THD+N dB</th><th>Noise dBFS(A)</th><th>SNR dB(A)</th><th>DR dB(A)</th><th>Crosstalk dB</th><th>Balance dB</th><th>Polarity L/R</th><th>Drift ppm</th></tr>")
	for _, rw := range rows(r) {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>",
			html.EscapeString(rw.Mode), html.EscapeString(rw.Level), rw.THD, rw.THDN, rw.NoiseA, rw.SNR, rw.DR, rw.Xtalk, rw.Bal, rw.Pol, rw.Drift)
	}
	b.WriteString("</table></div>")
	b.WriteString(`<p class="note">Level is the 1 kHz test tone at the input. THD and THD+N are re the fundamental over 20 Hz to 20 kHz. SNR and DR (AES17, -60 dB tone) are A-weighted and re the device's full scale. Crosstalk is the left-only tone leaking into the right input.</p>`)
	for _, f := range figs {
		fmt.Fprintf(&b, "<figure>%s<figcaption>%s. Files: <a href=\"%s.png\">PNG</a>, <a href=\"%s.svg\">SVG</a>.</figcaption></figure>", f.SVG, html.EscapeString(f.Caption), f.File, f.File)
	}
	b.WriteString(`<h2>Files</h2><p class="note">summary.csv (one row per mode), stepNN/fr.csv, stepNN/ir.csv, stepNN/result.json, run.json, hardware-curves.json (import into the M500 Hardware app).</p>`)
	b.WriteString("</body></html>")
	return os.WriteFile(filepath.Join(r.Dir, "report.html"), []byte(b.String()), 0o644)
}

func sortedKeys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func writeMarkdown(r *engine.Run, figs []figure) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Measurement report, run %s\n\n", r.ID)
	fmt.Fprintf(&b, "%s. Status: %s.\n\n", r.Started.Format("2006-01-02 15:04"), r.Status)
	for _, k := range sortedKeys(r.Device) {
		if r.Device[k] != "" {
			fmt.Fprintf(&b, "- Device %s: %s\n", k, r.Device[k])
		}
	}
	fmt.Fprintf(&b, "- Capture: %s, %s Hz, %s\n", r.Capture["uri"], r.Capture["rate"], r.Capture["format"])
	if r.UserDAC != nil {
		fmt.Fprintf(&b, "- DAC settings before the run (restored after): %s\n", r.UserDAC)
	}
	if r.Level != nil {
		fmt.Fprintf(&b, "- Level check: %s\n", r.Level.Message)
	}
	fmt.Fprintf(&b, "\n%s\n\n", chainNote(r))
	b.WriteString("| Mode | Level dBFS | THD dB | THD+N dB | Noise dBFS(A) | SNR dB(A) | DR dB(A) | Crosstalk dB | Balance dB |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, rw := range rows(r) {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", rw.Mode, rw.Level, rw.THD, rw.THDN, rw.NoiseA, rw.SNR, rw.DR, rw.Xtalk, rw.Bal)
	}
	b.WriteString("\n")
	for _, f := range figs {
		fmt.Fprintf(&b, "![%s](%s.png)\n\n", f.Caption, f.File)
	}
	return os.WriteFile(filepath.Join(r.Dir, "report.md"), []byte(b.String()), 0o644)
}

func writeSummaryCSV(r *engine.Run) error {
	f, err := os.Create(filepath.Join(r.Dir, "summary.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"mode", "filter", "gain", "dre", "high_power", "play_rate", "level_dbfs", "thd_db", "thdn_db", "noise_dbfs", "noise_a_dbfs", "snr_a_db", "dr_a_db", "xtalk_db", "balance_db", "drift_ppm"})
	for _, st := range r.Steps {
		if !found(st) {
			continue
		}
		L := st.Result.Ch[0]
		rec := []string{st.Label, st.DAC.Filter, st.DAC.Gain, st.DAC.DRE, st.DAC.HP, fmt.Sprint(st.PlayRate)}
		g := func(v float64) string { return fmt.Sprintf("%.2f", v) }
		if L.Tone != nil {
			rec = append(rec, g(L.Tone.LevelDBFS), g(L.Tone.THD), g(L.Tone.THDN))
		} else {
			rec = append(rec, "", "", "")
		}
		if L.Noise != nil {
			rec = append(rec, g(L.Noise.DBFS), g(L.Noise.DBFSA))
		} else {
			rec = append(rec, "", "")
		}
		rec = append(rec, g(L.SNRA), g(L.DRA), g(L.XtalkDB), g(st.Result.Balance), g(st.Result.DriftPPM))
		w.Write(rec)
	}
	w.Flush()
	return w.Error()
}

// StepCSV writes fr.csv (both channels) and ir.csv for one step.
func StepCSV(dir string, st *engine.Step) {
	if !found(st) {
		return
	}
	res := st.Result
	if f, err := os.Create(filepath.Join(dir, "fr.csv")); err == nil {
		w := csv.NewWriter(f)
		hdr := []string{"hz"}
		for _, c := range res.Ch {
			n := strings.ToLower(c.Name)
			hdr = append(hdr, n+"_db", n+"_phase_deg", n+"_gd_ms")
		}
		w.Write(hdr)
		for i, hz := range res.Ch[0].FR.Hz {
			rec := []string{fmt.Sprintf("%.3f", hz)}
			for _, c := range res.Ch {
				rec = append(rec, fmt.Sprintf("%.4f", c.FR.DB[i]), fmt.Sprintf("%.2f", c.FR.Phase[i]), fmt.Sprintf("%.5f", c.FR.GD[i]))
			}
			w.Write(rec)
		}
		w.Flush()
		f.Close()
	}
	if res.Ch[0].IR != nil {
		if f, err := os.Create(filepath.Join(dir, "ir.csv")); err == nil {
			w := csv.NewWriter(f)
			w.Write([]string{"ms", "left", "right"})
			for i := range res.Ch[0].IR {
				rec := []string{fmt.Sprintf("%.5f", res.Ch[0].IRStart+float64(i)/res.CapRate*1000), fmt.Sprintf("%.6f", res.Ch[0].IR[i])}
				if len(res.Ch) > 1 && i < len(res.Ch[1].IR) {
					rec = append(rec, fmt.Sprintf("%.6f", res.Ch[1].IR[i]))
				}
				w.Write(rec)
			}
			w.Flush()
			f.Close()
		}
	}
}

// ---- Hardware app export ----

// HWCurve is one measured curve in the Hardware app's import format (docs/hardware-app-format.md).
type HWCurve struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Port      string    `json:"port"`
	Filter    string    `json:"filter,omitempty"`
	Gain      string    `json:"gain,omitempty"`
	HighPower *bool     `json:"highPower,omitempty"`
	DRE       *bool     `json:"dre,omitempty"`
	RateHz    int       `json:"rateHz"`
	Channel   string    `json:"channel"`
	Ref       string    `json:"ref"`
	Hz        []float64 `json:"hz"`
	DB        []float64 `json:"db"`
	THDNDB    *float64  `json:"thdnDb,omitempty"`
	NoiseDBFSA *float64 `json:"noiseDbfsA,omitempty"`
}

// HWFile is the import file.
type HWFile struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	Source    string    `json:"source"`
	Measured  string    `json:"measured"`
	Device    string    `json:"device"`
	Capture   string    `json:"capture"`
	Curves    []HWCurve `json:"curves"`
}

// WriteHardwareJSON writes the curves of r, decimated to 1/24 octave from 10 Hz up.
func WriteHardwareJSON(r *engine.Run, path string) error {
	port := r.Spec.Jack
	if port == "" {
		port = "3.5mm"
	}
	f := HWFile{Format: "hione.curves", Version: 1, Source: "hione", Measured: r.Started.Format(time.RFC3339),
		Device: r.Device["device"], Capture: fmt.Sprintf("%s %s Hz %s", r.Capture["uri"], r.Capture["rate"], r.Capture["format"])}
	for _, st := range r.Steps {
		if !found(st) {
			continue
		}
		L := st.Result.Ch[0]
		var hz, db []float64
		for i, x := range L.FR.Hz {
			if i%2 == 0 { // 48 -> 24 points per octave
				hz = append(hz, math.Round(x*100)/100)
				db = append(db, math.Round(L.FR.DB[i]*1000)/1000)
			}
		}
		c := HWCurve{ID: fmt.Sprintf("%s-step%02d", r.ID, st.Index+1), Label: st.Label, Port: port, Filter: st.DAC.Filter, Gain: st.DAC.Gain,
			RateHz: st.PlayRate, Channel: "left", Ref: "1kHz", Hz: hz, DB: db}
		if st.DAC.HP != "" {
			v := st.DAC.HP == "hpower_enable"
			c.HighPower = &v
		}
		if st.DAC.DRE != "" {
			v := st.DAC.DRE == "dremode_enable"
			c.DRE = &v
		}
		if L.Tone != nil && !math.IsNaN(L.Tone.THDN) {
			v := math.Round(L.Tone.THDN*10) / 10
			c.THDNDB = &v
		}
		if L.Noise != nil {
			v := math.Round(L.Noise.DBFSA*10) / 10
			c.NoiseDBFSA = &v
		}
		f.Curves = append(f.Curves, c)
	}
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

var _ = analysis.MinConfidence
