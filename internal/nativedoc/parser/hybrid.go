package parser

import (
	"strings"
	"unicode"
)

// hybridPageMaxChars is the text, in characters, under which a page that holds a big picture is read as a picture. A page
// of text has thousands; a scan with a stamp, a header or a few words typed over it has a few hundred at most, and its
// content is in the picture, which the text layer says nothing about.
const hybridPageMaxChars = 400

// scanCoverageMin is the share of a page, from 0 to 1, that its images must cover for the page to be a scan. A scanned page is
// one image over the whole page; a page of text with a figure or a photo is a small share. Measured on the 252 short documents of
// the ExtractBench split, no page of text has an image over more than a quarter of it, and every scan that carries a text layer
// has a coverage of 1.
const scanCoverageMin = 0.9

// isScannedPage says whether a page with a text layer is in fact a picture of a page: an image covers it (the text is then what a
// scanner's OCR wrote, which is often poor: on 70 such documents the OCR of this package never read less than that layer and
// read 10 to 20 points more of the expected values), or it holds a big picture and its text is thin (a stamp or a header over a
// scan whose coverage could not be worked out). Such a page is read by the OCR, whatever the length of its text layer.
func isScannedPage(textChars int, hasBigPicture bool, coverage float64) bool {
	return coverage >= scanCoverageMin || (hasBigPicture && textChars < hybridPageMaxChars)
}

// withNativeLines is the text the OCR read from a page, plus the lines of the page's own text layer that the OCR did not
// give (a stamp it missed, a word in a font it could not read): the text layer is exact where it exists, so nothing it
// says is lost. A line counts as given when its letters and digits are found in the OCR text.
func withNativeLines(ocr, native string) string {
	given := squashText(ocr)
	var missing []string
	for _, line := range strings.Split(native, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isImageMarker(line) {
			continue
		}
		if squashed := squashText(line); squashed == "" || strings.Contains(given, squashed) {
			continue
		}
		missing = append(missing, line)
	}
	if len(missing) == 0 {
		return ocr
	}
	if strings.TrimSpace(ocr) == "" {
		return strings.Join(missing, "\n")
	}
	return strings.TrimRight(ocr, "\n") + "\n" + strings.Join(missing, "\n")
}

func isImageMarker(line string) bool {
	return strings.HasPrefix(line, "[Image ") && strings.HasSuffix(line, "]")
}

func squashText(text string) string {
	var sb strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(unicode.ToLower(r))
		}
	}
	return sb.String()
}
