package pdf

import (
	"bytes"
	"fmt"
	"testing"
)

// pdfWithSize builds a one-page PDF whose objects are numbered 5 to 7 (the catalog is object 5), with the /Size the caller
// gives in its trailer. The xref table lists the objects 0 to 7, as the file really holds them.
func pdfWithSize(size int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	write := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	write(5, "<< /Type /Catalog /Pages 6 0 R >>")
	write(6, "<< /Type /Pages /Kids [7 0 R] /Count 1 >>")
	write(7, "<< /Type /Page /Parent 6 0 R /MediaBox [0 0 100 100] >>")
	xrefAt := buf.Len()
	buf.WriteString("xref\n0 8\n0000000000 65535 f \n")
	for num := 1; num <= 7; num++ {
		if off, ok := offsets[num]; ok {
			fmt.Fprintf(&buf, "%010d 00000 n \n", off)
		} else {
			buf.WriteString("0000000000 00000 f \n")
		}
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xrefAt)
	return buf.Bytes()
}

func TestAPDFWithASizeSmallerThanItsTableStillHasItsPages(t *testing.T) {
	for _, size := range []int{8, 3} {
		data := pdfWithSize(size)
		r, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if got := r.NumPage(); got != 1 {
			t.Errorf("/Size %d: NumPage = %d, want 1", size, got)
		}
	}
}
