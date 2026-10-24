package pdftext

import (
	"strings"
	"unicode/utf8"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// Two things happen to the ligatures of a PDF between the file and the text a reader searches in.
//
// A font may name its ligature glyphs by their parts ("f_i", "f_f_l", the convention of Linux Libertine and of many fonts made
// with FontForge). The library does not know such a name and gives the character code itself, a control character under 32,
// which was then dropped: "files" came out as "les". The names are read from the font's /Differences and the code is turned
// back into the letters it stands for.
//
// A font that names them "fi" or "fl" gives the single characters U+FB01 and U+FB02. They look like the two letters but are
// not them to a search or to a model that reads the text: "ﬁne-tuning" is not found by "fine-tuning". They are written as the
// letters.

// ligatureLetters writes the ligature characters of Unicode as the letters they stand for.
var ligatureLetters = strings.NewReplacer(
	"ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st",
)

// textFontName is the name the library gives a font in the text it returns: the base font without the tag of a subset
// ("GLEUEB+LinLibertineT" is "LinLibertineT").
func textFontName(base string) string {
	if i := strings.Index(base, "+"); i >= 0 {
		return base[i+1:]
	}
	return base
}

// ligatureNames finds, for each font of the page by its base name, the character codes that /Differences names by parts and
// the letters they stand for. A code that two fonts of the same name give different letters is left alone.
func ligatureNames(page pdf.Page) (codes map[string]map[rune]string) {
	codes = map[string]map[rune]string{}
	defer func() { _ = recover() }() // a font the library cannot read has no ligature names
	conflicts := map[string]map[rune]bool{}
	for _, name := range page.Fonts() {
		font := page.Font(name)
		differences := font.V.Key("Encoding").Key("Differences")
		if differences.Kind() != pdf.Array {
			continue
		}
		base := textFontName(font.BaseFont())
		code := 0
		for i := 0; i < differences.Len(); i++ {
			item := differences.Index(i)
			switch item.Kind() {
			case pdf.Integer:
				code = int(item.Int64())
			case pdf.Name:
				if letters, ok := lettersOfGlyphName(item.Name()); ok && code < 32 {
					if codes[base] == nil {
						codes[base], conflicts[base] = map[rune]string{}, map[rune]bool{}
					}
					if known, taken := codes[base][rune(code)]; taken && known != letters {
						conflicts[base][rune(code)] = true
					}
					codes[base][rune(code)] = letters
				}
				code++
			}
		}
	}
	for base, taken := range conflicts {
		for code := range taken {
			delete(codes[base], code)
		}
	}
	return codes
}

// lettersOfGlyphName reads a glyph name made of single letters joined by underscores ("f_f_i") as those letters.
func lettersOfGlyphName(name string) (string, bool) {
	parts := strings.Split(name, "_")
	if len(parts) < 2 {
		return "", false
	}
	for _, part := range parts {
		if len(part) != 1 || !(part[0] >= 'a' && part[0] <= 'z' || part[0] >= 'A' && part[0] <= 'Z') {
			return "", false
		}
	}
	return strings.Join(parts, ""), true
}

// restoreLigatures returns the glyphs with the control characters that stand for named ligatures written as letters.
func restoreLigatures(texts []pdf.Text, codes map[string]map[rune]string) []pdf.Text {
	if len(codes) == 0 {
		return texts
	}
	var out []pdf.Text
	for i, t := range texts {
		r, size := utf8.DecodeRuneInString(t.S)
		if size != len(t.S) || r >= 32 {
			continue
		}
		letters, ok := codes[t.Font][r]
		if !ok {
			continue
		}
		if out == nil {
			out = append([]pdf.Text(nil), texts...)
		}
		out[i].S = letters
	}
	if out == nil {
		return texts
	}
	return out
}

// withoutControls drops the control characters left in a text (other than the line break and the tab): a glyph of a symbol
// font (a bullet, a check mark) that has no Unicode value comes out as its code, a character nothing can display or index.
func withoutControls(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}
