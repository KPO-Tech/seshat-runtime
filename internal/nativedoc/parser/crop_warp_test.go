//go:build cgo && nativedoc

package parser

import (
	"math"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/nativedoc"
)

// A 200x100 page with a dark bar tilted by 3 degrees: cut as a tilted line, the bar fills the crop; cut with the bounding
// rectangle it would be mixed with the white around it.
func TestWarpBoxStraightensATiltedLine(t *testing.T) {
	const W, H = 200, 100
	img := &nativedoc.Image{W: W, H: H, Pix: make([]byte, W*H*3)}
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	slope := math.Tan(3 * math.Pi / 180)
	const x0, y0, bw, bh = 20.0, 30.0, 150.0, 12.0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			top := y0 + (float64(x)-x0)*slope
			if float64(x) >= x0 && float64(x) < x0+bw && float64(y) >= top && float64(y) < top+bh {
				copy(img.Pix[(y*W+x)*3:], []byte{0, 0, 0})
			}
		}
	}
	rise := bw * slope
	box := nativedoc.DetBox{Pts: [4][2]float32{
		{x0, y0}, {x0 + bw, float32(y0 + rise)},
		{x0 + bw, float32(y0 + rise + bh)}, {x0, y0 + bh},
	}}
	if !isTilted(box) {
		t.Fatal("a 3 degree box over 150 px must count as tilted")
	}
	crop := cropBox(img, box)
	dark := 0
	for i := 0; i < len(crop.Pix); i += 3 {
		if crop.Pix[i] < 128 {
			dark++
		}
	}
	if share := float64(dark) / float64(crop.W*crop.H); share < 0.85 {
		t.Fatalf("the straightened line should be mostly the bar, dark share = %.2f", share)
	}
}

func TestWarpBoxLeavesAnUprightBoxToTheRectangleCrop(t *testing.T) {
	box := nativedoc.DetBox{Pts: [4][2]float32{{10, 10}, {60, 10}, {60, 30}, {10, 30}}}
	if isTilted(box) {
		t.Fatal("an upright box is not tilted")
	}
}
