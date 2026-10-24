package pdftext

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// rotatedPDF is a one page PDF, shown rotated by `rotate`, whose lines are drawn with the text matrix that makes them upright
// on the screen: rotate 90 shows the page turned clockwise, so the text is drawn turned counter-clockwise, and so on. The font
// has a width table, as the fonts of most files do: without one the library does not move from one glyph to the next.
func rotatedPDF(rotate int, lines ...string) []byte {
	var content strings.Builder
	for i, text := range lines {
		var tm string
		switch rotate {
		case 90:
			tm = fmt.Sprintf("0 1 -1 0 %d 100", 100+20*i)
		case 180:
			tm = fmt.Sprintf("-1 0 0 -1 500 %d", 100+20*i)
		case 270:
			tm = fmt.Sprintf("0 -1 1 0 %d 700", 400-20*i)
		default:
			tm = fmt.Sprintf("1 0 0 1 100 %d", 700-20*i)
		}
		fmt.Fprintf(&content, "BT /F1 12 Tf %s Tm (%s) Tj ET\n", tm, text)
	}
	return buildPDF(rotate, content.String(), helveticaFont(""))
}

func firstPage(t *testing.T, data []byte) pdf.Page {
	t.Helper()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return reader.Page(1)
}

func TestAPageShownRotatedIsReadInTheOrientationItIsShown(t *testing.T) {
	t.Parallel()
	lines := []string{"For release at 2:00 p.m., EDT, March 20, 2024", "Projections of the Board members and the Bank presidents", "The longer run is not collected."}
	for _, rotate := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprintf("rotate %d", rotate), func(t *testing.T) {
			md, err := PageMarkdown(firstPage(t, rotatedPDF(rotate, lines...)))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(strings.Fields(md), " ")
			if want := strings.Join(lines, " "); got != want {
				t.Errorf("rotate %d:\n got %q\nwant %q", rotate, got, want)
			}
			if strings.Contains(md, "\n\nF\n\n") || strings.Count(md, "\n\n") > len(lines) {
				t.Errorf("rotate %d: glyphs were laid out one per line:\n%s", rotate, md)
			}
		})
	}
}

func TestTurnedRulingsKeepTheirPlaceOnTheScreen(t *testing.T) {
	t.Parallel()
	// A horizontal rule from x 10 to 50 at y 20.
	r := rulings{h: []hline{{y: 20, x0: 10, x1: 50}}}
	cases := []struct {
		rotation int
		wantV    vline // a rotation by a quarter turn makes it vertical
		wantH    hline // a half turn keeps it horizontal
	}{
		{90, vline{x: 20, y0: -50, y1: -10}, hline{}},
		{270, vline{x: -20, y0: 10, y1: 50}, hline{}},
		{180, vline{}, hline{y: -20, x0: -50, x1: -10}},
	}
	for _, tc := range cases {
		got := turnRulings(r, tc.rotation)
		if tc.rotation == 180 {
			if len(got.h) != 1 || got.h[0] != tc.wantH || len(got.v) != 0 {
				t.Errorf("rotation %d: %+v", tc.rotation, got)
			}
			continue
		}
		if len(got.v) != 1 || got.v[0] != tc.wantV || len(got.h) != 0 {
			t.Errorf("rotation %d: %+v", tc.rotation, got)
		}
	}
	if got := turnRulings(r, 0); len(got.h) != 1 || got.h[0] != r.h[0] {
		t.Error("an upright page is left as it is")
	}
}

func TestPageRotationIsNormalised(t *testing.T) {
	t.Parallel()
	for rotate, want := range map[int]int{0: 0, 90: 90, 180: 180, 270: 270, 360: 0, -90: 270, 450: 90} {
		data := rotatedPDF(rotate, "x")
		if got := pageRotation(firstPage(t, data)); got != want {
			t.Errorf("/Rotate %d: got %d, want %d", rotate, got, want)
		}
	}
}
