// Package textsafe cleans and reveals text that someone else wrote and that a person or a terminal is about to read:
// a command the model wants to run, the output of a process, a page that was fetched.
//
// A terminal interprets bytes, not strings. Text that reaches it with an escape sequence intact can write the user's
// clipboard (OSC 52), draw a link whose visible text is not its target (OSC 8), move the cursor and repaint other
// parts of the screen, or retitle the window. Text that reaches a person with invisible or direction-changing
// characters can read differently from what it does: a command whose visible text is `ls` and whose real text is
// something else (the "Trojan Source" family). Both matter most where a person approves an action by reading it.
//
// The sanitizing functions follow the approach of Freebuff (Codebuff, Apache-2.0) in
// common/src/util/terminal-safe-text.ts.
package textsafe

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	csi           = regexp.MustCompile(`(?:\x1b\[|\x{9b})[0-?]*[ -/]*[@-~]`)
	osc           = regexp.MustCompile(`(?s)(?:\x1b\]|\x{9d}).*?(?:\x07|\x1b\\|\x{9c}|$)`)
	stringCommand = regexp.MustCompile(`(?s)(?:\x1b[PX^_]|[\x{90}\x{98}\x{9e}\x{9f}]).*?(?:\x1b\\|\x07|\x{9c}|$)`)
	shortEscape   = regexp.MustCompile(`\x1b[ -/]*[0-~]?`)
)

// isControl reports a C0 control other than newline and tab, DEL, or a C1 control.
func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	}
	return false
}

// ignorable lists the invisible characters that the Unicode format category (Cf) does not hold: they take no space
// on screen, so they can split a word or change what it means without changing what a reader sees.
var ignorable = &unicode.RangeTable{
	R16: []unicode.Range16{
		{0x034f, 0x034f, 1}, // combining grapheme joiner
		{0x115f, 0x1160, 1}, // Hangul fillers
		{0x17b4, 0x17b5, 1}, // Khmer inherent vowels
		{0x180b, 0x180f, 1}, // Mongolian free variation selectors
		{0x3164, 0x3164, 1}, // Hangul filler
		{0xfe00, 0xfe0f, 1}, // variation selectors
		{0xffa0, 0xffa0, 1}, // halfwidth Hangul filler
		{0xfff0, 0xfff8, 1}, // reserved ignorable
	},
}

// isInvisible reports a character a reader cannot see: format characters (zero-width, bidi controls, soft hyphen,
// tags), variation selectors and the fillers above.
func isInvisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) || unicode.Is(ignorable, r) || unicode.Is(unicode.Variation_Selector, r)
}

// isDeceptive reports the invisible characters with no honest use in a command: direction controls, which make text
// read in another order than it executes, tag characters, and control characters. Zero-width joiners and variation
// selectors are NOT included: emoji need them, and a commit message may hold an emoji.
func isDeceptive(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e, // embedding and override
		r >= 0x2066 && r <= 0x2069,            // isolates
		r == 0x200e, r == 0x200f, r == 0x061c, // direction marks
		r >= 0xe0000 && r <= 0xe007f: // tags
		return true
	}
	return isControl(r) && r != '\r'
}

// DeceptiveRune returns the first character of s that can make it read differently from what it does (see
// isDeceptive) and whether there is one.
func DeceptiveRune(s string) (rune, bool) {
	for _, r := range s {
		if isDeceptive(r) {
			return r, true
		}
	}
	return 0, false
}

// Reveal makes every invisible or control character of s visible as «U+XXXX», so that a person who is asked to
// approve the text sees what is really in it. Newlines and tabs stay. Visible text is returned unchanged.
func Reveal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isControl(r) || isInvisible(r) {
			fmt.Fprintf(&b, "«U+%04X»", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeText removes every escape sequence (OSC, DCS/SOS/PM/APC, CSI, short ESC, 7- and 8-bit forms), every other
// control character and the invisible characters. Newlines survive (CR and CRLF become a newline), a tab becomes two
// spaces. Printable text is otherwise unchanged and is not trimmed.
func SanitizeText(s string) string {
	s = osc.ReplaceAllString(s, "")
	s = stringCommand.ReplaceAllString(s, "")
	s = csi.ReplaceAllString(s, "")
	s = shortEscape.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "  ")
	return strings.Map(func(r rune) rune {
		if isControl(r) || isInvisible(r) {
			return -1
		}
		return r
	}, s)
}

// SanitizeOutput is SanitizeText for what a process printed. One difference: a bare carriage return is a progress
// bar redrawing its line, not a line break, so only the text after the last one on each line is kept, as a real
// terminal would show it.
func SanitizeOutput(s string) string {
	if s == "" {
		return s
	}
	if strings.Contains(s, "\r") {
		lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
		for i, line := range lines {
			if j := strings.LastIndex(line, "\r"); j >= 0 {
				lines[i] = line[j+1:]
			}
		}
		s = strings.Join(lines, "\n")
	}
	return SanitizeText(s)
}
