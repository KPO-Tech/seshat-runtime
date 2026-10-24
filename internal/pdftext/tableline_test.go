package pdftext

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

func shown(s string, table int) glyph { return glyph{Text: pdf.Text{S: s}, table: table} }

func TestTableOfLine(t *testing.T) {
	stand := shown("", 2)
	space := shown(" ", 0)
	letter := shown("a", 0)
	cases := []struct {
		name   string
		glyphs []glyph
		want   int
	}{
		{"the stand-in alone", []glyph{stand}, 2},
		// buildLines adds the spaces drawn after the stand-in to its line: it is still the line of the table.
		{"the stand-in and spaces after it", []glyph{stand, space, space}, 2},
		{"spaces before the stand-in", []glyph{space, stand}, 2},
		{"text next to the stand-in", []glyph{stand, letter}, 0},
		{"text only", []glyph{letter, space}, 0},
		{"two stand-ins are not one table line", []glyph{stand, shown("", 3)}, 0},
	}
	for _, c := range cases {
		if got := tableOfLine(&line{glyphs: c.glyphs}); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
