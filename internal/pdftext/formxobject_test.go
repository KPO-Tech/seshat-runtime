package pdftext

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// buildObjectsPDF is a PDF made of the objects given, the first being the catalog. Streams are written by the caller as objects.
func buildObjectsPDF(objects []string) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

func stream(dict, content string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%sendstream", dict, len(content), content)
}

// Pages made by HTML to PDF converters hold one call, "/Fm1 Do", and the text is in the form it draws. Reading only the page's own
// stream found no text at all, and such a file was taken for a scan.
func TestExtractReadsTextDrawnByAFormXObject(t *testing.T) {
	data := buildObjectsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Fm1 5 0 R >> >> >>",
		stream("", "q 1 0 0 1 0 0 cm /Fm1 Do Q\n"),
		// The form has its own resources: the font is not in the page's.
		stream("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Matrix [1 0 0 1 0 0] /Resources << /Font << /F1 6 0 R >> >>",
			"BT /F1 12 Tf 72 700 Td (Hello from a form) Tj ET\n"),
		helveticaFont(""),
	})
	res, err := Extract(data)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(res.Text, "Hello from a form") {
		t.Fatalf("the text of the form must be read, got %q", res.Text)
	}
}

func TestExtractReadsFormsInsideForms(t *testing.T) {
	data := buildObjectsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Outer 5 0 R >> >> >>",
		stream("", "/Outer Do\n"),
		stream("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /XObject << /Inner 6 0 R >> >>", "q 1 0 0 1 0 0 cm /Inner Do Q\n"),
		stream("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /Font << /F1 7 0 R >> >>", "BT /F1 12 Tf 72 700 Td (Nested form text) Tj ET\n"),
		helveticaFont(""),
	})
	res, err := Extract(data)
	if err != nil || !strings.Contains(res.Text, "Nested form text") {
		t.Fatalf("a form drawn by a form must be read, got %v, %+v", err, res)
	}
}

func TestExtractStopsAFormThatDrawsItself(t *testing.T) {
	data := buildObjectsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Loop 5 0 R >> >> >>",
		stream("", "/Loop Do\n"),
		stream("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /XObject << /Loop 5 0 R >> /Font << /F1 6 0 R >> >>",
			"BT /F1 12 Tf 72 700 Td (again) Tj ET /Loop Do\n"),
		helveticaFont(""),
	})
	if _, err := Extract(data); err != nil {
		t.Fatalf("a form that draws itself must end, not fail: %v", err)
	}
}

// A composite font (Type0, two bytes a glyph) has no /Widths: the advance of a glyph comes from /W and /DW of its descendant font. With
// a width of zero every glyph of a string was drawn at the same place.
func TestCompositeFontAdvancesByItsCIDWidths(t *testing.T) {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"1 begincodespacerange <0000> <FFFF> endcodespacerange\n" +
		"3 beginbfchar <0001> <0041> <0002> <0042> <0003> <0043> endbfchar\n" +
		"endcmap end end\n"
	data := buildObjectsPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		stream("", "BT /F1 10 Tf 72 700 Td <000100020003> Tj ET\n"),
		"<< /Type /Font /Subtype /Type0 /BaseFont /Test /Encoding /Identity-H /DescendantFonts [6 0 R] /ToUnicode 7 0 R >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Test /DW 500 /W [1 [600 700] 3 3 800] >>",
		stream("", cmap),
	})
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	runs := r.Page(1).Content().Text
	if len(runs) != 3 {
		t.Fatalf("three glyphs expected, got %d: %v", len(runs), runs)
	}
	if runs[0].S+runs[1].S+runs[2].S != "ABC" {
		t.Fatalf("the glyphs must be decoded two bytes at a time, got %v", runs)
	}
	// 10 pt text: the glyph of width 600 moves the next one 6 points, then 700 moves it 7.
	if got := runs[1].X - runs[0].X; got < 5.99 || got > 6.01 {
		t.Fatalf("the first glyph advances by its /W width (6 pt), got %v", got)
	}
	if got := runs[2].X - runs[1].X; got < 6.99 || got > 7.01 {
		t.Fatalf("the second glyph advances by its /W width (7 pt), got %v", got)
	}
	if runs[2].W < 7.99 || runs[2].W > 8.01 {
		t.Fatalf("a glyph in a /W range carries the width of the range (8 pt), got %v", runs[2].W)
	}
}
