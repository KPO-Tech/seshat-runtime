package model

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/internal/modes"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/workspace"
)

// selectedLargeModel returns the currently selected large language model.
func (m *UI) selectedLargeModel() *workspace.AgentModel {
	if m.com.Workspace.AgentIsReady() {
		model := m.com.Workspace.AgentModel()
		return &model
	}
	return nil
}

// landingView renders the landing page: braille logo + tagline + shortcuts, centered.
func (m *UI) landingView() string {
	t := m.com.Styles
	width := m.layout.main.Dx()
	height := m.layout.main.Dy()

	// Braille logo in orange (use Logo.FieldColor which is set to primary)
	orange := lipgloss.NewStyle().Foreground(t.Logo.FieldColor)
	muted := t.Sidebar.WorkingDir
	subtle := t.Section.Title

	// The mark only appears when the window is tall enough to keep the tagline
	// and the shortcuts on screen with it.
	logo := ""
	if height >= landingMarkMinHeight {
		logo = m.renderLandingMark()
	}
	wordmark := orange.Bold(true).Render("SESHAT")
	tagline := muted.Render("One runtime. Any LLM. Any language.")
	cwd := subtle.Render(m.com.Workspace.WorkingDir())

	shortcuts := strings.Join([]string{
		orange.Bold(true).Render("ctrl+n") + " " + muted.Render("new session"),
		orange.Bold(true).Render("ctrl+s") + " " + muted.Render("sessions"),
		orange.Bold(true).Render("ctrl+p") + " " + muted.Render("settings"),
		orange.Bold(true).Render("ctrl+c") + " " + muted.Render("quit"),
	}, "  ")

	parts := []string{}
	if logo != "" {
		parts = append(parts, logo, "")
	}
	parts = append(parts, wordmark, "", tagline, "", shortcuts, "", cwd)
	block := lipgloss.JoinVertical(lipgloss.Center, parts...)

	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(block)
}

// landingHeaderView renders the minimal one-line header for the landing page:
// "SESHAT  provider:model" on the left, "ready" on the right.
func (m *UI) landingHeaderView(width int) string {
	t := m.com.Styles
	orange := lipgloss.NewStyle().Foreground(t.Logo.FieldColor)
	muted := t.Sidebar.WorkingDir

	left := orange.Bold(true).Render("SESHAT")
	model := m.selectedLargeModel()
	if model != nil {
		modelName := model.ModelCfg.Model
		if model.ModelCfg.Provider != "" {
			modelName = model.ModelCfg.Provider + ":" + modelName
		}
		left += "  " + muted.Render(modelName)
	}

	right := lipgloss.NewStyle().Foreground(t.ToolCallSuccess.GetForeground()).Render("ready")
	right = m.sandboxChip() + "  " + right

	gap := max(0, width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// chatHeaderView renders the one-line chat page header:
// "SESHAT  provider:model" on the left, execution mode on the right.
// Mode colors: execute=orange (default), plan=info blue, pair=success green.
// A spinner appears to the left of the mode label while the agent is running.
func (m *UI) chatHeaderView(width int) string {
	t := m.com.Styles
	orange := lipgloss.NewStyle().Foreground(t.Logo.FieldColor)
	muted := t.Sidebar.WorkingDir

	left := orange.Bold(true).Render("SESHAT")
	model := m.selectedLargeModel()
	if model != nil {
		modelName := model.ModelCfg.Model
		if model.ModelCfg.Provider != "" {
			modelName = model.ModelCfg.Provider + ":" + modelName
		}
		left += "  " + muted.Render(modelName)
	}

	mode := m.com.Workspace.ExecutionMode()
	modeLabel := orange.Bold(true).Render("execute")
	if modes.IsPlanModeString(mode) {
		modeLabel = lipgloss.NewStyle().Foreground(t.LSP.InfoDiagnostic.GetForeground()).Bold(true).Render("plan")
	}

	// Show spinner to the left of the mode label while the agent is running.
	right := modeLabel
	if m.isAgentBusy() {
		spinnerStyle := lipgloss.NewStyle().Foreground(t.Pills.TodoSpinner.GetForeground())
		right = spinnerStyle.Render(m.todoSpinner.View()) + " " + modeLabel
	}

	right = m.sandboxChip() + "  " + right
	gap := max(0, width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	return " " + left + strings.Repeat(" ", gap) + right + " "
}
