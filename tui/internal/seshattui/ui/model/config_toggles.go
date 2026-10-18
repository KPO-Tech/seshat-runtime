package model

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// toggleMemoryCmd flips the option that lets Seshat learn from conversations. The client reads it at start, so the
// change applies from the next one.
func (m *UI) toggleMemoryCmd() tea.Cmd {
	return func() tea.Msg {
		cfg := m.com.Config()
		if cfg == nil {
			return util.ReportError(errors.New("configuration not found"))()
		}
		enabled := cfg.Options != nil && cfg.Options.MemoryEnabled != nil && *cfg.Options.MemoryEnabled
		if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.memory_enabled", !enabled); err != nil {
			return util.ReportError(err)()
		}
		if enabled {
			return util.NewInfoMsg("Learning from conversations disabled (from the next start)")
		}
		return util.NewInfoMsg("Learning from conversations enabled (from the next start): preferences and lessons are kept and recalled")
	}
}

// toggleTransparentCmd flips the transparent background of the interface.
func (m *UI) toggleTransparentCmd() tea.Cmd {
	return func() tea.Msg {
		cfg := m.com.Config()
		if cfg == nil {
			return util.ReportError(errors.New("configuration not found"))()
		}

		isTransparent := cfg.Options != nil && cfg.Options.TUI.Transparent != nil && *cfg.Options.TUI.Transparent
		newValue := !isTransparent
		if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.transparent", newValue); err != nil {
			return util.ReportError(err)()
		}
		m.isTransparent = newValue

		status := "disabled"
		if newValue {
			status = "enabled"
		}
		return util.NewInfoMsg("Transparent background " + status)
	}
}
