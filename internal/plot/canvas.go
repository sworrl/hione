// Package plot draws review-style charts (log frequency axis with labelled gridlines and band
// labels, dB axis, legend, several traces) to SVG and PNG from one drawing routine.
package plot

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Canvas is what a chart draws on. Coordinates are in pixels, origin top left.
type Canvas interface {
	Rect(x, y, w, h float64, fill string)
	Polyline(xs, ys []float64, stroke string, width float64, dash bool)
	Text(x, y float64, s string, size float64, anchor string, fill string, bold bool)
	Size() (float64, float64)
}

// ---- SVG ----

// SVG accumulates an SVG document.
type SVG struct {
	W, H float64
	b    strings.Builder
	clip string
}

// NewSVG starts a document.
func NewSVG(w, h float64) *SVG {
	s := &SVG{W: w, H: h}
	fmt.Fprintf(&s.b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %g %g" width="%g" height="%g" font-family="Go, Helvetica, Arial, sans-serif">`, w, h, w, h)
	return s
}

func (s *SVG) Size() (float64, float64) { return s.W, s.H }

func (s *SVG) Rect(x, y, w, h float64, fill string) {
	fmt.Fprintf(&s.b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, x, y, w, h, fill)
}

func (s *SVG) Polyline(xs, ys []float64, stroke string, width float64, dash bool) {
	if len(xs) < 2 {
		return
	}
	var pts strings.Builder
	for i := range xs {
		if math.IsNaN(ys[i]) || math.IsInf(ys[i], 0) {
			continue
		}
		fmt.Fprintf(&pts, "%.1f,%.1f ", xs[i], ys[i])
	}
	d := ""
	if dash {
		d = ` stroke-dasharray="5,4"`
	}
	fmt.Fprintf(&s.b, `<polyline fill="none" stroke="%s" stroke-width="%g" stroke-linejoin="round" stroke-linecap="round"%s points="%s"/>`, stroke, width, d, strings.TrimSpace(pts.String()))
}

func (s *SVG) Text(x, y float64, t string, size float64, anchor, fill string, bold bool) {
	w := ""
	if bold {
		w = ` font-weight="600"`
	}
	a := map[string]string{"l": "start", "c": "middle", "r": "end"}[anchor]
	if a == "" {
		a = "start"
	}
	fmt.Fprintf(&s.b, `<text x="%.1f" y="%.1f" font-size="%g" text-anchor="%s" fill="%s"%s>%s</text>`, x, y, size, a, fill, w, esc(t))
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// Bytes closes and returns the document.
func (s *SVG) Bytes() []byte { return []byte(s.b.String() + "</svg>") }

// ---- PNG ----

// Raster draws into an RGBA image at a scale factor (2 for crisp PNGs).
type Raster struct {
	W, H  float64
	scale float64
	img   *image.RGBA
}

// NewRaster makes a white canvas.
func NewRaster(w, h, scale float64) *Raster {
	img := image.NewRGBA(image.Rect(0, 0, int(w*scale), int(h*scale)))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	return &Raster{W: w, H: h, scale: scale, img: img}
}

func (r *Raster) Size() (float64, float64) { return r.W, r.H }

func parseHex(s string) color.RGBA {
	var c color.RGBA
	c.A = 255
	s = strings.TrimPrefix(s, "#")
	if len(s) == 6 {
		fmt.Sscanf(s, "%02x%02x%02x", &c.R, &c.G, &c.B)
	}
	return c
}

func (r *Raster) fill(z *vector.Rasterizer, col string) {
	src := image.NewUniform(parseHex(col))
	z.Draw(r.img, r.img.Bounds(), src, image.Point{})
}

func (r *Raster) Rect(x, y, w, h float64, fill string) {
	k := r.scale
	z := vector.NewRasterizer(r.img.Bounds().Dx(), r.img.Bounds().Dy())
	z.MoveTo(float32(x*k), float32(y*k))
	z.LineTo(float32((x+w)*k), float32(y*k))
	z.LineTo(float32((x+w)*k), float32((y+h)*k))
	z.LineTo(float32(x*k), float32((y+h)*k))
	z.ClosePath()
	r.fill(z, fill)
}

func (r *Raster) Polyline(xs, ys []float64, stroke string, width float64, dash bool) {
	k := r.scale
	z := vector.NewRasterizer(r.img.Bounds().Dx(), r.img.Bounds().Dy())
	hw := width * k / 2
	acc := 0.0
	for i := 1; i < len(xs); i++ {
		x0, y0, x1, y1 := xs[i-1]*k, ys[i-1]*k, xs[i]*k, ys[i]*k
		if bad(y0) || bad(y1) {
			continue
		}
		dx, dy := x1-x0, y1-y0
		l := math.Hypot(dx, dy)
		if l == 0 {
			continue
		}
		if dash {
			acc += l
			if int(acc/(9*k))%2 == 1 {
				continue
			}
		}
		nx, ny := -dy/l*hw, dx/l*hw
		// Extend each segment by half the width so joins have no gaps.
		ex, ey := dx/l*hw, dy/l*hw
		z.MoveTo(float32(x0+nx-ex), float32(y0+ny-ey))
		z.LineTo(float32(x1+nx+ex), float32(y1+ny+ey))
		z.LineTo(float32(x1-nx+ex), float32(y1-ny+ey))
		z.LineTo(float32(x0-nx-ex), float32(y0-ny-ey))
		z.ClosePath()
	}
	r.fill(z, stroke)
}

func bad(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

var (
	fontOnce sync.Once
	regFont  *opentype.Font
	boldFont *opentype.Font
	faces    = map[[2]float64]font.Face{}
	faceMu   sync.Mutex
)

func face(size float64, bold bool) font.Face {
	fontOnce.Do(func() {
		regFont, _ = opentype.Parse(goregular.TTF)
		boldFont, _ = opentype.Parse(gobold.TTF)
	})
	b := 0.0
	if bold {
		b = 1
	}
	key := [2]float64{size, b}
	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faces[key]; ok {
		return f
	}
	src := regFont
	if bold {
		src = boldFont
	}
	f, _ := opentype.NewFace(src, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	faces[key] = f
	return f
}

func (r *Raster) Text(x, y float64, s string, size float64, anchor, fill string, bold bool) {
	f := face(size*r.scale, bold)
	d := &font.Drawer{Dst: r.img, Src: image.NewUniform(parseHex(fill)), Face: f}
	w := float64(d.MeasureString(s)) / 64
	px := x * r.scale
	switch anchor {
	case "c":
		px -= w / 2
	case "r":
		px -= w
	}
	d.Dot = fixed.Point26_6{X: fixed.Int26_6(px * 64), Y: fixed.Int26_6(y * r.scale * 64)}
	d.DrawString(s)
}

// PNG encodes the image.
func (r *Raster) PNG() ([]byte, error) {
	var b bytes.Buffer
	err := png.Encode(&b, r.img)
	return b.Bytes(), err
}

// WriteBoth renders draw to path.svg and path.png.
func WriteBoth(path string, w, h float64, drawFn func(Canvas)) error {
	s := NewSVG(w, h)
	drawFn(s)
	if err := os.WriteFile(path+".svg", s.Bytes(), 0o644); err != nil {
		return err
	}
	r := NewRaster(w, h, 2)
	drawFn(r)
	b, err := r.PNG()
	if err != nil {
		return err
	}
	return os.WriteFile(path+".png", b, 0o644)
}

// SVGString renders draw to an SVG string (for inline HTML).
func SVGString(w, h float64, drawFn func(Canvas)) string {
	s := NewSVG(w, h)
	drawFn(s)
	return string(s.Bytes())
}
