package pdfsmart

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

// pageWithAnImage is a one page PDF, 612 x 792 points, with a line of text and an image drawn as the matrix says.
func pageWithAnImage(matrix string) []byte {
	content := "q " + matrix + " cm /Im1 Do Q\nBT /F1 12 Tf 72 700 Td (Some text on the page, long enough to be read as the text of a page) Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> /XObject << /Im1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content),
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n\x80\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
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

func TestAPageReportsHowMuchItsImagesCover(t *testing.T) {
	cases := []struct {
		name   string
		matrix string
		min    float64
		max    float64
	}{
		{"a scan over the whole page", "612 0 0 792 0 0", 0.99, 1},
		{"a figure", "100 0 0 100 200 300", 0.01, 0.05},
	}
	for _, tc := range cases {
		result, _, err := ReadPages(context.Background(), pageWithAnImage(tc.matrix), nil, Options{}, nil, VisionFallback{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(result.Pages) != 1 {
			t.Fatalf("%s: %d pages", tc.name, len(result.Pages))
		}
		if got := result.Pages[0].ImageCoverage; got < tc.min || got > tc.max {
			t.Errorf("%s: ImageCoverage = %.3f, want between %.2f and %.2f", tc.name, got, tc.min, tc.max)
		}
	}
}
