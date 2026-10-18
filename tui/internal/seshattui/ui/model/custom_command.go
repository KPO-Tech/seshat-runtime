package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/dialog"
)

// runCustomCommand runs a command or skill chosen in the command palette. A command that takes arguments first asks for
// them in a dialog, which sends the action back with the values.
func (m *UI) runCustomCommand(msg dialog.ActionRunCustomCommand) tea.Cmd {
	if len(msg.Arguments) > 0 && msg.Args == nil {
		m.dialog.CloseFrontDialog()
		m.dialog.OpenDialog(dialog.NewArguments(
			m.com,
			"Custom Command Arguments",
			"",
			msg.Arguments,
			msg, // Pass the action as the result
		))
		return nil
	}
	content := msg.Content
	if msg.Args != nil {
		content = substituteArgs(content, msg.Args)
	}
	// If this is a skill command, format it using the skill's FormatInvocation method
	if msg.Skill != nil {
		content = msg.Skill.FormatInvocation()
	}
	cmd := m.sendMessage(content)
	m.dialog.CloseFrontDialog()
	return cmd
}
