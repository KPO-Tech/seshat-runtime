package model

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
)

func newPromptForTest(value string) *UI {
	ta := textarea.New()
	ta.SetWidth(24)
	ta.SetHeight(10)
	ta.SetValue(value)
	return &UI{textarea: ta}
}

func TestCursorOffsetRoundTripsAcrossRowsAndWrappedLines(t *testing.T) {
	value := "aaaa bbbb cccc dddd eeee ffff gggg\nsecond line é here\nthird"
	m := newPromptForTest(value)
	for _, offset := range []int{0, 5, 20, 34, 35, 40, 50, len(value)} {
		m.setCursorOffset(offset)
		if got := m.cursorOffset(); got != offset {
			t.Errorf("setCursorOffset(%d) then cursorOffset() = %d", offset, got)
		}
	}
}

func TestInsertCompletionTextWorksInTheMiddleOfThePrompt(t *testing.T) {
	m := newPromptForTest("look at @rea and more")
	m.setCursorOffset(len("look at @rea"))
	m.completionsStartIndex = len("look at ")

	if !m.insertCompletionText("README.md") {
		t.Fatal("insertion refused")
	}
	got := m.textarea.Value()
	if !strings.HasPrefix(got, "look at README.md") || !strings.HasSuffix(got, "and more") {
		t.Fatalf("value = %q, want the word replaced in place and the rest kept", got)
	}
	if want := len("look at README.md "); m.cursorOffset() != want {
		t.Fatalf("cursor at %d, want %d (right after the inserted text), not at the end", m.cursorOffset(), want)
	}
}

func TestOpeningTriggerLooksAtTheCharacterBeforeTheCursor(t *testing.T) {
	// "fix this and @" typed in the middle of an existing sentence.
	m := newPromptForTest("fix this and then ship")
	m.setCursorOffset(len("fix this and "))
	if off := m.cursorOffset(); off != len("fix this and ") || !isWhitespace(m.textarea.Value()[off-1]) {
		t.Fatalf("cursor offset %d is not just after a space", off)
	}
	m.setCursorOffset(len("fix thi"))
	if off := m.cursorOffset(); isWhitespace(m.textarea.Value()[off-1]) {
		t.Fatalf("cursor offset %d is wrongly after a space", off)
	}
}
