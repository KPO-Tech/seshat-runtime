package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// toUnicodeCodespacePDF is a one page PDF whose font has a ToUnicode CMap with a codespace of <51><7a> (the lower case
// letters) that also maps <42> to an X and <6f> to a Y: the first is outside the declared codespace, the second inside.
func toUnicodeCodespacePDF(content string) []byte {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"/CMapName /Adobe-Identity-UCS def /CMapType 2 def\n" +
		"1 begincodespacerange <51> <7a> endcodespacerange\n" +
		"2 beginbfrange <42> <42> <0058> <6f> <6f> <0059> endbfrange\n" +
		"endcmap CMapName currentdict /CMap defineresource pop end end"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content),
		"<< /Type /Font /Subtype /TrueType /BaseFont /AAAAAA+Arial /ToUnicode 6 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cmap)+1, cmap),
	}
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

func TestACodeMappedOutsideTheDeclaredCodespaceIsStillDecoded(t *testing.T) {
	data := toUnicodeCodespacePDF("BT /F1 12 Tf 10 700 Td (Bo) Tj ET")
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, text := range r.Page(1).Content().Text {
		sb.WriteString(text.S)
	}
	if got := sb.String(); got != "XY" {
		t.Errorf("decoded %q, want \"XY\" (B is outside the codespace but mapped; o is inside)", got)
	}
}
