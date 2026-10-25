package pdftext

import (
	"strings"
	"testing"

	pdf "github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// A word drawn by an invisible OCR text layer: one x that drifts to the left by 0.02 point at each letter, and no width.
func driftingWord(word string, x float64) []glyph {
	var gs []glyph
	for i, r := range word {
		gs = append(gs, newGlyph(pdf.Text{S: string(r), X: x - 0.02*float64(i), Y: 690, FontSize: 6.8, Font: "Helvetica"}))
	}
	return gs
}

func lineText(gs []glyph) string {
	var sb strings.Builder
	for _, g := range gs {
		sb.WriteString(g.S)
	}
	return sb.String()
}

func TestTheLettersOfADriftingWordKeepTheirOrder(t *testing.T) {
	line := driftingWord("Revenue", 49.52)
	orderByString(line)
	if got := lineText(line); got != "Revenue" {
		t.Errorf("got %q, want Revenue", got)
	}
}

func TestDriftingStringsAreStillOrderedAgainstEachOther(t *testing.T) {
	// Drawn right to left on purpose: the line has to come out in reading order, each word whole.
	line := append(driftingWord("rental", 79.11), driftingWord("Net", 24.52)...)
	orderByString(line)
	if got := lineText(line); got != "Netrental" {
		t.Errorf("got %q, want Netrental", got)
	}
}

func TestAStepBeyondTheDriftIsARealPosition(t *testing.T) {
	// The second glyph drawn is a whole point to the left of the first (the drift is 0.34 at most for this size): its own
	// string, placed by its own x, so it comes first.
	line := []glyph{
		newGlyph(pdf.Text{S: "b", X: 11, Y: 690, FontSize: 6.8, Font: "Helvetica"}),
		newGlyph(pdf.Text{S: "a", X: 10, Y: 690, FontSize: 6.8, Font: "Helvetica"}),
	}
	orderByString(line)
	if got := lineText(line); got != "ab" {
		t.Errorf("got %q, want ab", got)
	}
}
