package plot

import (
	"fmt"
	"math"
)

// Palette is the categorical series order (fixed, never cycled past eight).
var Palette = []string{"#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#6250d6", "#e34948"}

// Theme is a chart color set.
type Theme struct {
	Name                                string
	Surface, Plot                       string // page and plot area; "" = transparent
	Ink, Ink2, Muted                    string
	GridMajor, GridMinor, Band, Frame   string
	Palette                             []string
}

// Light is the review theme: white plot, dark ink.
var Light = &Theme{Name: "light", Surface: "#fcfcfb", Plot: "#ffffff", Ink: "#0b0b0b", Ink2: "#52514e", Muted: "#8a8984",
	GridMajor: "#d9d8d4", GridMinor: "#eeede9", Band: "#f3f2ee", Frame: "#b9b8b2", Palette: Palette}

// Dark is the Hione theme: night background, ice blue and amber first.
var Dark = &Theme{Name: "dark", Surface: "#111216", Plot: "#16181d", Ink: "#f0efec", Ink2: "#c3c2b7", Muted: "#8f8e86",
	GridMajor: "#2e3138", GridMinor: "#1f2228", Band: "#1b1d23", Frame: "#3a3e46",
	Palette: []string{"#8ec5ff", "#ffab2e", "#1fbf87", "#e87ba4", "#9085e9", "#e66767", "#c9d94a", "#f0efec"}}

// ThemeByName returns Light or Dark.
func ThemeByName(n string) *Theme {
	if n == "dark" {
		return Dark
	}
	return Light
}

// Stamp is the provenance line baked into exported charts.
type Stamp struct {
	Lines []string // e.g. device, DAC settings, capture chain, date
	Mark  bool     // draw the Hione mark
}

// Series is one trace.
type Series struct {
	Name  string
	X, Y  []float64
	Color string
	Dash  bool
	Width float64
}

// Chart describes a 2D line chart.
type Chart struct {
	Title, Subtitle string
	XLabel, YLabel  string
	LogX            bool
	XMin, XMax      float64
	YMin, YMax      float64
	YStep           float64 // 0 = automatic
	Bands           bool    // audio band labels along the top (log axis, 20 Hz - 20 kHz)
	Series          []Series
	Note            string // small print under the chart
	XFormat         func(float64) string
	Theme           *Theme
	Stamp           *Stamp
	Transparent     bool
}

func (ch *Chart) th() *Theme {
	if ch.Theme == nil {
		return Light
	}
	return ch.Theme
}

// FreqLabel formats a frequency for an axis.
func FreqLabel(f float64) string {
	if f >= 1000 {
		v := f / 1000
		if v == math.Trunc(v) {
			return fmt.Sprintf("%.0fk", v)
		}
		return fmt.Sprintf("%.1fk", v)
	}
	return fmt.Sprintf("%.0f", f)
}

// Draw renders the chart on c.
func (ch *Chart) Draw(c Canvas) {
	W, H := c.Size()
	t := ch.th()
	if !ch.Transparent {
		c.Rect(0, 0, W, H, t.Surface)
	}
	left, right, top := 62.0, 20.0, 56.0
	if ch.Bands {
		top += 18
	}
	// Legend rows under the plot.
	legendRows := int(math.Ceil(float64(len(ch.Series)) / 3))
	if len(ch.Series) <= 1 {
		legendRows = 0
	}
	bottom := 48.0 + float64(legendRows)*18
	if ch.Note != "" {
		bottom += 16
	}
	if ch.Stamp != nil {
		bottom += 14 * float64(len(ch.Stamp.Lines))
		if ch.Stamp.Mark && len(ch.Stamp.Lines) == 0 {
			bottom += 14
		}
	}
	pw, ph := W-left-right, H-top-bottom
	c.Text(left, 24, ch.Title, 16, "l", t.Ink, true)
	if ch.Subtitle != "" {
		c.Text(left, 42, ch.Subtitle, 11.5, "l", t.Ink2, false)
	}
	xmap := func(x float64) float64 {
		if ch.LogX {
			return left + pw*math.Log(x/ch.XMin)/math.Log(ch.XMax/ch.XMin)
		}
		return left + pw*(x-ch.XMin)/(ch.XMax-ch.XMin)
	}
	ymap := func(y float64) float64 {
		y = math.Max(math.Min(y, ch.YMax+(ch.YMax-ch.YMin)*0.02), ch.YMin-(ch.YMax-ch.YMin)*0.02)
		return top + ph*(ch.YMax-y)/(ch.YMax-ch.YMin)
	}
	if !ch.Transparent {
		c.Rect(left, top, pw, ph, t.Plot)
	}
	// Y grid.
	step := ch.YStep
	if step == 0 {
		step = niceStep((ch.YMax - ch.YMin) / 8)
	}
	for y := math.Ceil(ch.YMin/step) * step; y <= ch.YMax+1e-9; y += step {
		py := ymap(y)
		c.Polyline([]float64{left, left + pw}, []float64{py, py}, t.GridMajor, 1, false)
		c.Text(left-6, py+4, trimNum(y), 11, "r", t.Ink2, false)
	}
	// X grid.
	xf := ch.XFormat
	if xf == nil {
		if ch.LogX {
			xf = FreqLabel
		} else {
			xf = trimNum
		}
	}
	if ch.LogX {
		for dec := math.Pow(10, math.Floor(math.Log10(ch.XMin))); dec <= ch.XMax; dec *= 10 {
			for m := 1; m <= 9; m++ {
				f := dec * float64(m)
				if f < ch.XMin*0.999 || f > ch.XMax*1.001 {
					continue
				}
				px := xmap(f)
				major := m == 1 || m == 2 || m == 5
				col := t.GridMinor
				if major {
					col = t.GridMajor
				}
				c.Polyline([]float64{px, px}, []float64{top, top + ph}, col, 1, false)
				if major {
					c.Text(px, top+ph+16, xf(f), 11, "c", t.Ink2, false)
				}
			}
		}
		// Label the right end unless a major tick label sits close to it.
		near := false
		for dec := math.Pow(10, math.Floor(math.Log10(ch.XMin))); dec <= ch.XMax*10; dec *= 10 {
			for _, m := range []float64{1, 2, 5} {
				f := dec * m
				if f >= ch.XMin && f <= ch.XMax && xmap(ch.XMax)-xmap(f) < 44 {
					near = true
				}
			}
		}
		if !near {
			c.Text(xmap(ch.XMax), top+ph+16, xf(ch.XMax), 11, "c", t.Ink2, false)
		}
	} else {
		xs := niceStep((ch.XMax - ch.XMin) / 8)
		for x := math.Ceil(ch.XMin/xs) * xs; x <= ch.XMax+1e-9; x += xs {
			px := xmap(x)
			c.Polyline([]float64{px, px}, []float64{top, top + ph}, t.GridMajor, 1, false)
			c.Text(px, top+ph+16, xf(x), 11, "c", t.Ink2, false)
		}
	}
	if ch.Bands && ch.LogX {
		bands := []struct {
			a, b float64
			n    string
		}{{20, 60, "Sub bass"}, {60, 250, "Bass"}, {250, 500, "Low mids"}, {500, 2000, "Mids"},
			{2000, 4000, "Upper mids"}, {4000, 6000, "Presence"}, {6000, 20000, "Brilliance"}}
		for i, b := range bands {
			a, e := math.Max(b.a, ch.XMin), math.Min(b.b, ch.XMax)
			if e <= a {
				continue
			}
			xa, xe := xmap(a), xmap(e)
			if i%2 == 0 {
				c.Rect(xa, top-18, xe-xa, 16, t.Band)
			}
			if xe-xa > 40 {
				c.Text((xa+xe)/2, top-6, b.n, 10, "c", t.Ink2, false)
			}
		}
	}
	// Axis labels.
	c.Text(left+pw/2, top+ph+34, ch.XLabel, 11.5, "c", t.Ink2, false)
	c.Text(12, top-8, ch.YLabel, 11.5, "l", t.Ink2, false)
	// Series.
	for i, s := range ch.Series {
		col := s.Color
		if col == "" {
			col = t.Palette[i%len(t.Palette)]
		}
		w := s.Width
		if w == 0 {
			w = 2
		}
		var xs, ys []float64
		for j := range s.X {
			if s.X[j] < ch.XMin || s.X[j] > ch.XMax || math.IsNaN(s.Y[j]) {
				continue
			}
			xs = append(xs, xmap(s.X[j]))
			ys = append(ys, ymap(s.Y[j]))
		}
		c.Polyline(xs, ys, col, w, s.Dash)
	}
	// Frame.
	c.Polyline([]float64{left, left + pw, left + pw, left, left}, []float64{top, top, top + ph, top + ph, top}, t.Frame, 1, false)
	// Legend.
	if legendRows > 0 {
		colW := pw / 3
		for i, s := range ch.Series {
			col := s.Color
			if col == "" {
				col = t.Palette[i%len(t.Palette)]
			}
			lx := left + float64(i%3)*colW
			ly := top + ph + 52 + float64(i/3)*18
			c.Polyline([]float64{lx, lx + 18}, []float64{ly - 4, ly - 4}, col, 3, s.Dash)
			c.Text(lx+24, ly, s.Name, 11, "l", t.Ink, false)
		}
	}
	y := H - 8
	if ch.Stamp != nil {
		for i := len(ch.Stamp.Lines) - 1; i >= 0; i-- {
			c.Text(left, y, ch.Stamp.Lines[i], 10, "l", t.Ink2, false)
			y -= 14
		}
		if ch.Stamp.Mark {
			drawMark(c, W-right, H-8, t)
		}
	}
	if ch.Note != "" {
		c.Text(left, y, ch.Note, 10, "l", t.Muted, false)
	}
}

// drawMark draws the small "Hione" mark, right-aligned at (x, y).
func drawMark(c Canvas, x, y float64, t *Theme) {
	c.Text(x, y, "Hione", 12, "r", "#ffab2e", true)
	c.Rect(x-46, y-14, 6, 6, "#8ec5ff")
}

func niceStep(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*p >= raw {
			return m * p
		}
	}
	return 10 * p
}

func trimNum(v float64) string {
	if math.Abs(v-math.Round(v)) < 1e-9 {
		return fmt.Sprintf("%.0f", v)
	}
	if math.Abs(v*10-math.Round(v*10)) < 1e-9 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// Bars draws a simple horizontal bar chart of values (for THD+N per mode). Lower is better is
// shown by the axis title, not by color.
type Bars struct {
	Title, Subtitle, Unit string
	Labels                []string
	Values                []float64
	Min, Max              float64
	Color                 string
	Theme                 *Theme
	Stamp                 *Stamp
	Transparent           bool
}

// Draw renders the bars.
func (b *Bars) Draw(c Canvas) {
	W, H := c.Size()
	t := b.Theme
	if t == nil {
		t = Light
	}
	if !b.Transparent {
		c.Rect(0, 0, W, H, t.Surface)
	}
	foot := 0.0
	if b.Stamp != nil {
		foot = 14 * float64(len(b.Stamp.Lines))
		y := H - 8
		for i := len(b.Stamp.Lines) - 1; i >= 0; i-- {
			c.Text(16, y, b.Stamp.Lines[i], 10, "l", t.Ink2, false)
			y -= 14
		}
		if b.Stamp.Mark {
			drawMark(c, W-16, H-8, t)
		}
	}
	c.Text(16, 24, b.Title, 16, "l", t.Ink, true)
	if b.Subtitle != "" {
		c.Text(16, 42, b.Subtitle, 11.5, "l", t.Ink2, false)
	}
	left, right, top := 230.0, 70.0, 58.0
	// Room for the longest label at 11 px (about 6.2 px a character), so long mode names
	// are not cut off at the left edge.
	for _, l := range b.Labels {
		left = math.Max(left, 24+6.2*float64(len([]rune(l))))
	}
	n := len(b.Values)
	if n == 0 {
		return
	}
	rowH := (H - top - 30 - foot) / float64(n)
	pw := W - left - right
	xmap := func(v float64) float64 {
		v = math.Max(b.Min, math.Min(b.Max, v))
		return left + pw*(v-b.Min)/(b.Max-b.Min)
	}
	step := niceStep((b.Max - b.Min) / 6)
	for v := math.Ceil(b.Min/step) * step; v <= b.Max+1e-9; v += step {
		px := xmap(v)
		c.Polyline([]float64{px, px}, []float64{top, H - 30 - foot}, t.GridMajor, 1, false)
		c.Text(px, H-14-foot, trimNum(v), 10.5, "c", t.Ink2, false)
	}
	col := b.Color
	if col == "" {
		col = t.Palette[0]
	}
	for i, v := range b.Values {
		y := top + float64(i)*rowH
		c.Text(left-8, y+rowH/2+4, b.Labels[i], 11, "r", t.Ink, false)
		if math.IsNaN(v) {
			c.Text(left+4, y+rowH/2+4, "n/a", 11, "l", t.Muted, false)
			continue
		}
		x0 := xmap(b.Min)
		c.Rect(x0, y+rowH*0.18, math.Max(xmap(v)-x0, 1), rowH*0.64, col)
		c.Text(xmap(v)+6, y+rowH/2+4, fmt.Sprintf("%.1f %s", v, b.Unit), 11, "l", t.Ink2, false)
	}
}
