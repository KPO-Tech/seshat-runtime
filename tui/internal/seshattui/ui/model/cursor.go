package model

import (
	"strings"
	"unicode/utf8"
)

// maxCursorSteps bounds the moves setCursorOffset makes to reach a row, so a surprise in the text area cannot loop.
const maxCursorSteps = 10000

// cursorOffset is the byte offset of the cursor in the prompt's text, which joins its rows with a line break.
func (m *UI) cursorOffset() int {
	lines := strings.Split(m.textarea.Value(), "\n")
	row := min(m.textarea.Line(), len(lines)-1)
	offset := 0
	for _, l := range lines[:row] {
		offset += len(l) + 1
	}
	line := lines[row]
	col := m.textarea.Column()
	for i := 0; i < col && len(line) > 0; i++ {
		_, size := utf8.DecodeRuneInString(line)
		offset += size
		line = line[size:]
	}
	return offset
}

// setCursorOffset puts the cursor at a byte offset of the prompt's text. The text area moves down by screen lines, so
// it keeps stepping until it is on the row, then sets the column.
func (m *UI) setCursorOffset(offset int) {
	value := m.textarea.Value()
	offset = min(max(offset, 0), len(value))
	before := value[:offset]
	row := strings.Count(before, "\n")
	col := utf8.RuneCountInString(before[strings.LastIndex(before, "\n")+1:])

	m.textarea.MoveToBegin()
	for steps := 0; m.textarea.Line() < row && steps < maxCursorSteps; steps++ {
		m.textarea.CursorDown()
	}
	m.textarea.SetCursorColumn(col)
}
