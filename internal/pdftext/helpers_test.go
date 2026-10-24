package pdftext

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("failed to read testdata/%s: %v", name, err)
	}
	return data
}

// drawn builds one drawn character 10pt wide at x on baseline y, in a 10pt font.
func drawn(s string, x, y float64) pdf.Text {
	return pdf.Text{FontSize: 10, X: x, Y: y, W: 5, S: s}
}

// word lays out the letters of w from x, each 5pt wide with no gap.
func word(w string, x, y float64) []pdf.Text {
	var out []pdf.Text
	for _, r := range w {
		out = append(out, drawn(string(r), x, y))
		x += 5
	}
	return out
}

func join(parts ...[]pdf.Text) []pdf.Text {
	var all []pdf.Text
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}
