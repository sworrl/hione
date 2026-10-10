package plot

import (
	"github.com/go-pdf/fpdf"
)

// PDFCanvas draws a chart as vectors into a region of a PDF page. Chart pixels map to points
// with Scale; (X0, Y0) is the region's top left in points.
type PDFCanvas struct {
	P          *fpdf.Fpdf
	X0, Y0     float64
	W, H       float64 // chart size in chart pixels
	Scale      float64
	tr         func(string) string
}

// NewPDFCanvas places a w x h chart at (x0, y0) with width widthPt.
func NewPDFCanvas(p *fpdf.Fpdf, x0, y0, w, h, widthPt float64) *PDFCanvas {
	return &PDFCanvas{P: p, X0: x0, Y0: y0, W: w, H: h, Scale: widthPt / w, tr: p.UnicodeTranslatorFromDescriptor("")}
}

func (c *PDFCanvas) Size() (float64, float64) { return c.W, c.H }

func rgb(s string) (int, int, int) {
	x := parseHex(s)
	return int(x.R), int(x.G), int(x.B)
}

func (c *PDFCanvas) Rect(x, y, w, h float64, fill string) {
	r, g, b := rgb(fill)
	c.P.SetFillColor(r, g, b)
	c.P.Rect(c.X0+x*c.Scale, c.Y0+y*c.Scale, w*c.Scale, h*c.Scale, "F")
}

func (c *PDFCanvas) Polyline(xs, ys []float64, stroke string, width float64, dash bool) {
	if len(xs) < 2 {
		return
	}
	r, g, b := rgb(stroke)
	c.P.SetDrawColor(r, g, b)
	c.P.SetLineWidth(width * c.Scale * 0.8)
	c.P.SetLineCapStyle("round")
	c.P.SetLineJoinStyle("round")
	if dash {
		c.P.SetDashPattern([]float64{4 * c.Scale, 3 * c.Scale}, 0)
	}
	for i := 1; i < len(xs); i++ {
		if bad(ys[i-1]) || bad(ys[i]) {
			continue
		}
		c.P.Line(c.X0+xs[i-1]*c.Scale, c.Y0+ys[i-1]*c.Scale, c.X0+xs[i]*c.Scale, c.Y0+ys[i]*c.Scale)
	}
	if dash {
		c.P.SetDashPattern(nil, 0)
	}
}

func (c *PDFCanvas) Text(x, y float64, s string, size float64, anchor, fill string, bold bool) {
	st := ""
	if bold {
		st = "B"
	}
	c.P.SetFont("Helvetica", st, size*c.Scale) // the document unit is pt
	r, g, b := rgb(fill)
	c.P.SetTextColor(r, g, b)
	s = c.tr(s)
	w := c.P.GetStringWidth(s)
	px := c.X0 + x*c.Scale
	switch anchor {
	case "c":
		px -= w / 2
	case "r":
		px -= w
	}
	c.P.Text(px, c.Y0+y*c.Scale, s)
}
