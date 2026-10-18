package textsafe

import "testing"

func TestSanitizeTextRemovesEscapeSequences(t *testing.T) {
	cases := map[string]string{
		"plain text":                    "plain text",
		"\x1b[31mred\x1b[0m":            "red",
		"\x1b]52;c;ZXZpbA==\x07after":   "after", // clipboard write, BEL-terminated
		"\x1b]8;;http://evil\x1b\\link": "link",  // hyperlink, ST-terminated
		"a\x1bPq#0;2;0;0;0\x1b\\b":      "ab",    // DCS
		"a\u009b31mb":                   "ab",    // 8-bit CSI
		"title\x1b]0;pwned":             "title", // unterminated OSC
		"line1\r\nline2\rline3":         "line1\nline2\nline3",
		"a\tb":                          "a  b",
		"bell\x07 and nul\x00":          "bell and nul",
		"zero​width and bidi‮!":         "zerowidth and bidi!",
	}
	for in, want := range cases {
		if got := SanitizeText(in); got != want {
			t.Errorf("SanitizeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeOutputKeepsOnlyTheLastRedrawOfALine(t *testing.T) {
	in := "10%\r50%\r100%\ndone\r\nnext"
	want := "100%\ndone\nnext"
	if got := SanitizeOutput(in); got != want {
		t.Errorf("SanitizeOutput = %q, want %q", got, want)
	}
	if SanitizeOutput("") != "" {
		t.Error("empty stays empty")
	}
}

func TestRevealShowsWhatIsHidden(t *testing.T) {
	got := Reveal("ls‮ txt\x1b[2J")
	want := "ls«U+202E» txt«U+001B»[2J"
	if got != want {
		t.Errorf("Reveal = %q, want %q", got, want)
	}
	if Reveal("héllo\nworld\t!") != "héllo\nworld\t!" {
		t.Error("visible text, newlines and tabs must be unchanged")
	}
}

func TestDeceptiveRune(t *testing.T) {
	for _, r := range []rune{0x202e, 0x2066, 0x200f, 0x061c, 0xe0041, 0x00, 0x1b, 0x9b} {
		if got, ok := DeceptiveRune("echo " + string(r) + "x"); !ok || got != r {
			t.Errorf("U+%04X must be reported", r)
		}
	}
	for _, s := range []string{"echo héllo\n\t", "👨‍👩‍👧 emoji with joiner", "❤️", "a\r\nb", "日本語"} {
		if r, ok := DeceptiveRune(s); ok {
			t.Errorf("%q must be clean, got U+%04X", s, r)
		}
	}
}
