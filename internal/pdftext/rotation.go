package pdftext

import (
	"math"
	"sort"
	"unicode/utf8"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// A page with /Rotate 90, 180 or 270 is shown turned, and a landscape table in a portrait document is drawn that way: its
// text runs along the page's vertical axis, so read as it is drawn every glyph is a line of its own. Text and rulings are
// turned into the orientation the page is shown in before anything is laid out, so that lines, columns and tables are
// found where a reader sees them.

// pageRotation is the page's /Rotate (it may be inherited) as 0, 90, 180 or 270.
func pageRotation(page pdf.Page) int {
	return ((int(inherited(page.V, "Rotate").Int64())%360 + 360) % 360) / 90 * 90
}

// turned gives the place a point of the page (x right, y up) has when the page is shown rotated clockwise by rotation.
func turned(rotation int, x, y float64) (float64, float64) {
	switch rotation {
	case 90:
		return y, -x
	case 180:
		return -x, -y
	case 270:
		return -y, x
	}
	return x, y
}

// turnTexts returns the glyphs placed as the page is shown, with the size and the advance of each. The library takes both
// from the horizontal scale of the text matrix: text drawn through a rotated matrix has a size and an advance of zero (and
// of a negative number when it is turned upside down), which the layout would read as the default size and as words made
// of single letters.
func turnTexts(page pdf.Page, texts []pdf.Text, rotation int) []pdf.Text {
	if rotation == 0 {
		return texts
	}
	out := make([]pdf.Text, len(texts))
	for i, t := range texts {
		t.X, t.Y = turned(rotation, t.X, t.Y)
		out[i] = t
	}
	restoreMetrics(out, fontWidths(page))
	return out
}

// fontWidths gives, for each font of the page by its base name, the advance of its characters in ems, read from the font's
// own width table through its encoding.
func fontWidths(page pdf.Page) (widths map[string]map[rune]float64) {
	widths = map[string]map[rune]float64{}
	defer func() { _ = recover() }() // a font the library cannot read is a font with no widths
	for _, name := range page.Fonts() {
		font := page.Font(name)
		encoder := font.Encoder()
		table := map[rune]float64{}
		for code := 0; code < 256; code++ {
			width := font.Width(code)
			if width <= 0 {
				continue
			}
			r := rune(code)
			if encoder != nil {
				r, _ = utf8.DecodeRuneInString(encoder.Decode(string([]byte{byte(code)})))
				if r == utf8.RuneError {
					continue
				}
			}
			if _, taken := table[r]; !taken {
				table[r] = width / 1000
			}
		}
		widths[textFontName(font.BaseFont())] = table
	}
	return widths
}

// helvetica is the advance, in thousandths of an em, of the characters from space to tilde in Helvetica. It gives the
// proportions between letters of a font whose real widths are not known; the scale comes from the page itself.
var helvetica = [...]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // space to /
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // 0 to ?
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // @ to O
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // P to _
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // ` to o
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // p to ~
}

func helveticaEm(r rune) float64 {
	if r >= ' ' && r <= '~' {
		return float64(helvetica[r-' ']) / 1000
	}
	return 0.55
}

// restoreMetrics gives every glyph of the page whose size is unknown (zero) its advance and its size. The advance of a
// character comes from its font's width table, or from the proportions of Helvetica when the font has none; the size of a
// font is the median, over the pairs of neighbours on one line, of the distance between them divided by that advance (most
// pairs are two letters of a word; the ones that hold a space are longer).
func restoreMetrics(texts []pdf.Text, widths map[string]map[rune]float64) {
	const sameLine, longest = 0.5, 40.0
	advance := func(font string, r rune) float64 {
		if w := widths[font][r]; w > 0 {
			return w
		}
		return helveticaEm(r)
	}
	ratios := map[string][]float64{}
	for i := 0; i+1 < len(texts); i++ {
		a, b := texts[i], texts[i+1]
		r, _ := utf8.DecodeRuneInString(a.S)
		dx := b.X - a.X
		if a.FontSize != 0 || a.W != 0 || a.Font != b.Font || r == '\n' || r == utf8.RuneError || math.Abs(a.Y-b.Y) > sameLine || dx <= 0 || dx > longest {
			continue
		}
		ratios[a.Font] = append(ratios[a.Font], dx/advance(a.Font, r))
	}
	size := map[string]float64{}
	for font, list := range ratios {
		sort.Float64s(list)
		size[font] = list[len(list)/2]
	}
	for i := range texts {
		t := &texts[i]
		switch {
		case t.W < 0:
			t.W, t.FontSize = -t.W, math.Abs(t.FontSize) // turned upside down
		case t.FontSize == 0 && t.W == 0:
			if em, known := size[t.Font]; known {
				r, _ := utf8.DecodeRuneInString(t.S)
				t.FontSize, t.W = em, em*advance(t.Font, r)
			}
		}
	}
}

// turnRulings returns the rulings as the page is shown: a vertical line of the page is a horizontal one on the screen.
func turnRulings(r rulings, rotation int) rulings {
	if rotation == 0 {
		return r
	}
	var out rulings
	add := func(x0, y0, x1, y1 float64) {
		x0, y0 = turned(rotation, x0, y0)
		x1, y1 = turned(rotation, x1, y1)
		if x0 > x1 {
			x0, x1 = x1, x0
		}
		if y0 > y1 {
			y0, y1 = y1, y0
		}
		if y0 == y1 {
			out.h = append(out.h, hline{y: y0, x0: x0, x1: x1})
		} else {
			out.v = append(out.v, vline{x: x0, y0: y0, y1: y1})
		}
	}
	for _, h := range r.h {
		add(h.x0, h.y, h.x1, h.y)
	}
	for _, v := range r.v {
		add(v.x, v.y0, v.x, v.y1)
	}
	return out
}
