package parser

import "testing"

func TestIsHybridPage(t *testing.T) {
	cases := []struct {
		name  string
		chars int
		big   bool
		want  bool
	}{
		{"scan with a stamp", 131, true, true},
		{"scan with nothing over it", 0, true, true},
		{"page of text with a small figure", 131, false, false},
		{"page of text with a big picture", 2500, true, false},
		{"just under the bound", hybridPageMaxChars - 1, true, true},
		{"at the bound", hybridPageMaxChars, true, false},
	}
	for _, tc := range cases {
		if got := isHybridPage(tc.chars, tc.big); got != tc.want {
			t.Errorf("%s: isHybridPage(%d, %v) = %v, want %v", tc.name, tc.chars, tc.big, got, tc.want)
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
