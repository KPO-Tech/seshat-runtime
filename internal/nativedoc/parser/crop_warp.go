//go:build cgo && nativedoc

package parser

import (
	"math"

	"github.com/KPO-Tech/seshat-runtime/internal/nativedoc"
)

// tiltTolerance is how far, in pixels, the top edge of a detected box may leave the horizontal (and its left edge the vertical)
// before the box is cut as a tilted line. Below it the plain bounding rectangle is exact.
const tiltTolerance = 1.5

// warpBox cuts a detected quad out of the page as an upright rectangle: the line of a page scanned a little crooked is straightened
// instead of cut with its bounding rectangle, which holds the end of the line above and below it and, for a long line, a piece of
// its neighbours. The quad is read as a parallelogram (top-left, top-right and bottom-left corners), the way the recognizer expects a
// line, and sampled with bilinear interpolation. It reports false when the quad has no area.
func warpBox(img *nativedoc.Image, box nativedoc.DetBox) (*nativedoc.Image, bool) {
	p0, p1, p3 := box.Pts[0], box.Pts[1], box.Pts[3]
	w := int(math.Round(math.Hypot(float64(p1[0]-p0[0]), float64(p1[1]-p0[1]))))
	h := int(math.Round(math.Hypot(float64(p3[0]-p0[0]), float64(p3[1]-p0[1]))))
	if w <= 0 || h <= 0 {
		return nil, false
	}
	ux, uy := float64(p1[0]-p0[0])/float64(w), float64(p1[1]-p0[1])/float64(w) // one step along the line
	vx, vy := float64(p3[0]-p0[0])/float64(h), float64(p3[1]-p0[1])/float64(h) // one step down the line height
	pix := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := float64(p0[0]) + float64(x)*ux + float64(y)*vx
			sy := float64(p0[1]) + float64(x)*uy + float64(y)*vy
			sample(img, sx, sy, pix[(y*w+x)*3:(y*w+x)*3+3])
		}
	}
	return &nativedoc.Image{W: w, H: h, Pix: pix}, true
}

// isTilted reports whether a quad is meaningfully off the axes.
func isTilted(box nativedoc.DetBox) bool {
	return math.Abs(float64(box.Pts[1][1]-box.Pts[0][1])) > tiltTolerance ||
		math.Abs(float64(box.Pts[3][0]-box.Pts[0][0])) > tiltTolerance
}

// sample writes the bilinearly interpolated colour at (x, y), clamped to the image edge.
func sample(img *nativedoc.Image, x, y float64, out []byte) {
	x = math.Min(math.Max(x, 0), float64(img.W-1))
	y = math.Min(math.Max(y, 0), float64(img.H-1))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, img.W-1), min(y0+1, img.H-1)
	fx, fy := x-float64(x0), y-float64(y0)
	for c := 0; c < 3; c++ {
		a := float64(img.Pix[(y0*img.W+x0)*3+c])*(1-fx) + float64(img.Pix[(y0*img.W+x1)*3+c])*fx
		b := float64(img.Pix[(y1*img.W+x0)*3+c])*(1-fx) + float64(img.Pix[(y1*img.W+x1)*3+c])*fx
		out[c] = byte(a*(1-fy) + b*fy + 0.5)
	}
}
