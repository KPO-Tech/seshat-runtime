package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/chat"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/dialog"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// handleShellPanelAction carries out a request about the command panel from the command palette.
func (m *UI) handleShellPanelAction(msg dialog.ActionShellPanel) tea.Cmd {
	m.dialog.CloseFrontDialog()
	p := &m.shellPanel
	switch msg.Op {
	case "toggle":
		if len(p.runs) == 0 {
			return util.ReportWarn("No command has been run yet: type !command at the start of the prompt.")
		}
		p.visible = !p.visible
	case "previous":
		p.pick(-1)
	case "next":
		p.pick(1)
	case "clear":
		p.clear()
	case "share":
		return m.shareSelectedRun()
	}
	m.updateLayoutAndSize()
	return nil
}

// shareSelectedRun keeps the output of the run on show for the next message to the agent.
func (m *UI) shareSelectedRun() tea.Cmd {
	run := m.shellPanel.selected()
	switch {
	case run == nil:
		return util.ReportWarn("No command output to share.")
	case run.running:
		return util.ReportWarn("That command is still running.")
	case run.shared:
		return util.ReportInfo("That output is already shared with your next message.")
	}
	run.shared = true
	m.pendingShell = append(m.pendingShell, chat.ShellRun{Command: run.command, Output: run.output, ExitCode: run.exitCode})
	return util.ReportInfo("The output goes in front of your next message to the agent.")
}
