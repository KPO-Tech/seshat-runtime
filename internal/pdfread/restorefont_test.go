package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// twoFontPDF is a one page PDF with the fonts F1, which shows the code 65 as a B, and F2, which shows it as the A it is.
func twoFontPDF(content string) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Type /Encoding /BaseEncoding /WinAnsiEncoding /Differences [65 /B] >> >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
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

func TestTheFontComesBackWithQ(t *testing.T) {
	// F1 is chosen, then F2 inside q ... Q: after the Q the font is F1 again, and the text must be decoded by F1.
	data := twoFontPDF("BT /F1 12 Tf 10 700 Td (A) Tj ET\nq BT /F2 12 Tf 10 680 Td (A) Tj ET Q\nBT 10 660 Td (A) Tj ET")
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, text := range r.Page(1).Content().Text {
		got = append(got, text.S)
	}
	if joined := strings.Join(got, ","); joined != "B,A,B" {
		t.Errorf("decoded %q, want \"B,A,B\" (F1, F2, then F1 again after the Q)", joined)
	}
}
