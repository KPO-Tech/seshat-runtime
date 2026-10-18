package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/completions"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/dialog"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// commandsTrigger opens the popup of palette commands over the prompt, like "/" does for skills. It only does so at the very
// start of the prompt: a backslash elsewhere is most often part of a Windows path.
const commandsTrigger = "\\"

// maybeOpenCompletions opens the popup a key asks for: "@" for files and MCP resources and "/" for skills, at the start of
// the prompt or after a space, and a backslash at the very start for the palette commands.
func (m *UI) maybeOpenCompletions(keyName, curValue string) []tea.Cmd {
	if m.completionsOpen {
		return nil
	}
	curIdx := len(curValue)
	atWordStart := curIdx == 0 || isWhitespace(curValue[curIdx-1])
	var cmds []tea.Cmd
	switch {
	case (keyName == "@" || keyName == "/") && atWordStart:
		m.startCompletions(keyName, curIdx)
		if keyName == "@" {
			depth, limit := m.com.Config().Options.TUI.Completions.Limits()
			cmds = append(cmds, m.completions.Open(depth, limit))
		} else {
			m.completions.SetSkillItems(skillCompletionValues(m.skillCommands))
		}
	case keyName == commandsTrigger && curIdx == 0:
		m.startCompletions(keyName, curIdx)
		m.completions.SetCommandItems(m.commandCompletionValues())
	}
	return cmds
}

// startCompletions records that a popup was opened by a trigger typed at curIdx.
func (m *UI) startCompletions(trigger string, curIdx int) {
	m.completionsOpen = true
	m.completionsTrigger = trigger
	m.completionsQuery = ""
	m.completionsStartIndex = curIdx
	m.completionsPositionStart = m.completionsPosition()
}

// commandCompletionValues lists the palette commands for the popup, ending with a way into the full palette.
func (m *UI) commandCompletionValues() []completions.CommandCompletionValue {
	var values []completions.CommandCompletionValue
	if palette, err := m.newCommandsDialog(); err == nil {
		for _, e := range palette.Entries() {
			values = append(values, completions.CommandCompletionValue{Title: e.Title, Shortcut: e.Shortcut, Payload: e.Action})
		}
	}
	return append(values, completions.CommandCompletionValue{Title: "All Commands…", Payload: dialog.ActionOpenDialog{DialogID: dialog.CommandsID}})
}

// applyCompletionSelection acts on what the user chose in a popup.
func (m *UI) applyCompletionSelection(msg tea.Msg) []tea.Cmd {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case completions.SelectionMsg[completions.FileCompletionValue]:
		cmds = append(cmds, m.insertFileCompletion(msg.Value.Path))
		if !msg.KeepOpen {
			m.closeCompletions()
		}
	case completions.SelectionMsg[completions.ResourceCompletionValue]:
		cmds = append(cmds, m.insertMCPResourceCompletion(msg.Value))
		if !msg.KeepOpen {
			m.closeCompletions()
		}
	case completions.SelectionMsg[completions.SkillCompletionValue]:
		cmds = append(cmds, m.insertSkillCompletion(msg.Value.Name))
		if !msg.KeepOpen {
			m.closeCompletions()
		}
	case completions.SelectionMsg[completions.CommandCompletionValue]:
		cmds = append(cmds, m.runCommandCompletion(msg.Value.Payload))
		if !msg.KeepOpen {
			m.closeCompletions()
		}
	case completions.ClosedMsg:
		m.completionsOpen = false
	}
	return cmds
}

// runCommandCompletion removes the typed "\query" from the prompt and runs the chosen command as the palette would.
func (m *UI) runCommandCompletion(payload any) tea.Cmd {
	prevHeight := m.textarea.Height()
	value := m.textarea.Value()
	if start := m.completionsStartIndex; start <= len(value) {
		end := min(start+len(m.textareaWord()), len(value))
		m.textarea.SetValue(value[:start] + value[end:])
		m.textarea.MoveToEnd()
	}
	cmds := []tea.Cmd{m.handleTextareaHeightChange(prevHeight)}
	if action, ok := payload.(dialog.Action); ok {
		cmds = append(cmds, m.handleDialogMsg(action))
	} else {
		cmds = append(cmds, util.ReportWarn("That command cannot be run from here."))
	}
	return tea.Batch(cmds...)
}
