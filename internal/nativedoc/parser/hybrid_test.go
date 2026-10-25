package parser

import "testing"

func TestIsScannedPage(t *testing.T) {
	cases := []struct {
		name     string
		chars    int
		big      bool
		coverage float64
		want     bool
	}{
		{"scan with a stamp", 131, true, 0, true},
		{"scan with nothing over it", 0, true, 0, true},
		{"page of text with a small figure", 131, false, 0.02, false},
		{"page of text with a big picture", 2500, true, 0.2, false},
		{"just under the bound", hybridPageMaxChars - 1, true, 0, true},
		{"at the bound", hybridPageMaxChars, true, 0, false},
		{"scan with an OCR text layer of thousands of characters", 3400, true, 1, true},
		{"image over most of the page", 3400, false, 0.95, true},
		{"image over most of the page, just under", 3400, true, 0.89, false},
		{"image over exactly the bound", 3400, false, scanCoverageMin, true},
	}
	for _, tc := range cases {
		if got := isScannedPage(tc.chars, tc.big, tc.coverage); got != tc.want {
			t.Errorf("%s: isScannedPage(%d, %v, %.2f) = %v, want %v", tc.name, tc.chars, tc.big, tc.coverage, got, tc.want)
		}
	}
}

func TestWithNativeLines(t *testing.T) {
	ocr := "OPERATOR  Produced Water Transfer LLC\nCounty Atascosa\n3400 USDW"
	native := "1050/2500/3250\n3400 USDW\n\n[Image 1 on page 1]\nCounty: Atascosa"

	got := withNativeLines(ocr, native)
	want := ocr + "\n1050/2500/3250"
	if got != want {
		t.Errorf("withNativeLines =\n%q\nwant\n%q", got, want)
	}
}

func TestWithNativeLinesKeepsOCRWhenNothingIsMissing(t *testing.T) {
	ocr := "Total 12.50\nPaid"
	if got := withNativeLines(ocr, "TOTAL 12.50\n[Image 1 on page 2]"); got != ocr {
		t.Errorf("got %q, want the OCR text unchanged", got)
	}
}

func TestWithNativeLinesWhenOCRFoundNothing(t *testing.T) {
	if got := withNativeLines("", "Stamp A\nStamp B"); got != "Stamp A\nStamp B" {
		t.Errorf("got %q, want the native lines", got)
	}
}
