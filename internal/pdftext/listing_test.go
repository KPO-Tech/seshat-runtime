package pdftext

import (
	"strings"
	"testing"

	pdf "github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// A line of words in a monospace font, each word in its own size (as an OCR text layer sets them), then a line set in one size.
func monoWord(word string, x, y, size float64) []pdf.Text {
	var out []pdf.Text
	for i, r := range word {
		out = append(out, pdf.Text{S: string(r), X: x + float64(i)*size*0.6, Y: y, W: size * 0.6, FontSize: size, Font: "Courier"})
	}
	return out
}

func TestOCRTextInMonospaceWithWordSizesIsNotAListing(t *testing.T) {
	var texts []pdf.Text
	texts = append(texts, monoWord("Payer", 36, 533, 7.3)...)
	texts = append(texts, monoWord("Name", 68, 533, 9.6)...)
	texts = append(texts, monoWord("and", 95, 533, 7.8)...)
	texts = append(texts, monoWord("Address", 112, 533, 7.8)...)
	md := layoutMarkdown(texts)
	if strings.Contains(md, "```") {
		t.Errorf("a line whose words each have their own size is not a listing:\n%s", md)
	}
	for _, word := range []string{"Payer", "Name", "and", "Address"} {
		if !strings.Contains(md, word) {
			t.Errorf("%q was lost:\n%s", word, md)
		}
	}
}

func TestMonospaceInOneSizeIsStillAListing(t *testing.T) {
	var texts []pdf.Text
	texts = append(texts, monoWord("func main() {", 36, 533, 10)...)
	texts = append(texts, monoWord("return", 56, 521, 10)...)
	texts = append(texts, monoWord("}", 36, 509, 10)...)
	if md := layoutMarkdown(texts); !strings.HasPrefix(md, "```") {
		t.Errorf("a monospace block in one size must stay a fenced listing:\n%s", md)
	}
}

func TestAPageOfOCRTextInMonospaceIsNotChoppedIntoListings(t *testing.T) {
	var texts []pdf.Text
	sizes := []float64{7.3, 9.6, 7.8, 9.7, 10.2, 8.4}
	words := []string{"Payer", "Name", "and", "SUNTRUST", "BANK", "TAX", "SERVICES", "Account", "Number", "Recipient"}
	for row := 0; row < 4; row++ {
		x := 36.0
		for i, w := range words {
			size := sizes[(i+row)%len(sizes)]
			texts = append(texts, monoWord(w, x, 533-float64(row)*12, size)...)
			x += float64(len(w))*size*0.6 + 6
		}
	}
	md := layoutMarkdown(texts)
	if strings.Contains(md, "```") {
		t.Errorf("no line of a page whose words come in many sizes is a listing:\n%s", md)
	}
	if !strings.Contains(md, "SUNTRUST") || !strings.Contains(md, "Recipient") {
		t.Errorf("words were lost:\n%s", md)
	}
}
