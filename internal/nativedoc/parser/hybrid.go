package parser

import (
	"strings"
	"unicode"
)

// hybridPageMaxChars is the text, in characters, under which a page that holds a big picture is read as a picture. A page
// of text has thousands; a scan with a stamp, a header or a few words typed over it has a few hundred at most, and its
// content is in the picture, which the text layer says nothing about.
const hybridPageMaxChars = 400

// isHybridPage says whether a page with a text layer is in fact a picture with a little text on it: it holds a big
// picture and its text is thin. Such a page is read by the OCR, whatever the length of its text layer.
func isHybridPage(textChars int, hasBigPicture bool) bool {
	return hasBigPicture && textChars < hybridPageMaxChars
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
