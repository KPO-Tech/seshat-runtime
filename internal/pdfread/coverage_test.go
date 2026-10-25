package pdf

import (
	"bytes"
	"fmt"
	"math"
	"testing"
)

// imagePDF is a one page PDF (612 x 792 points) whose content draws the image /Im1, a gray pixel, as the content says.
func imagePDF(content string) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content),
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n\x80\nendstream",
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

func coverageOf(t *testing.T, content string) float64 {
	t.Helper()
	data := imagePDF(content)
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return r.Page(1).ImageCoverage()
}

func TestAnImageOverTheWholePageCoversIt(t *testing.T) {
	if got := coverageOf(t, "q 612 0 0 792 0 0 cm /Im1 Do Q"); math.Abs(got-1) > 0.001 {
		t.Errorf("coverage = %.3f, want 1", got)
	}
}

func TestAnImageJustPastTheEdgesIsClippedToThePage(t *testing.T) {
	// A scan a little larger than the page: it still covers all of it, and never more than all of it.
	if got := coverageOf(t, "q 650 0 0 830 -20 -20 cm /Im1 Do Q"); math.Abs(got-1) > 0.001 {
		t.Errorf("coverage = %.3f, want 1", got)
	}
}

func TestASmallFigureCoversLittle(t *testing.T) {
	// 100 x 100 points on a 612 x 792 page.
	want := 100.0 * 100.0 / (612.0 * 792.0)
	if got := coverageOf(t, "q 100 0 0 100 200 300 cm /Im1 Do Q"); math.Abs(got-want) > 0.001 {
		t.Errorf("coverage = %.4f, want %.4f", got, want)
	}
}

func TestAPageWithoutImagesCoversNothing(t *testing.T) {
	if got := coverageOf(t, "BT 10 700 Td ET"); got != 0 {
		t.Errorf("coverage = %.3f, want 0", got)
	}
}

func TestAMatrixInsideQIsRestoredForTheNextImage(t *testing.T) {
	// The first image is small, inside q ... Q; the second is drawn with the matrix as it was before the q.
	want := 50.0 * 50.0 / (612.0 * 792.0)
	if got := coverageOf(t, "q 50 0 0 50 10 10 cm /Im1 Do Q"); math.Abs(got-want) > 0.001 {
		t.Errorf("coverage = %.4f, want %.4f", got, want)
	}
}
