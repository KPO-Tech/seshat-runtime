package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/dialog"
)

// runAll runs a command and the commands a batch holds, and reports whether one of them returned the marker.
func runAll(cmd tea.Cmd, marker any) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if runAll(c, marker) {
				return true
			}
		}
	case nil:
	default:
		return msg == marker
	}
	return false
}

func TestChoosingACommandInThePopupRunsItAndRemovesTheTypedText(t *testing.T) {
	m := newPromptForTest(commandsTrigger + "mem")
	m.dialog = dialog.NewOverlay()
	m.setCursorOffset(len(commandsTrigger + "mem"))
	m.completionsStartIndex = 0

	type ran struct{}
	marker := ran{}
	cmd := m.runCommandCompletion(dialog.ActionCmd{Cmd: func() tea.Msg { return marker }})

	if got := m.textarea.Value(); got != "" {
		t.Fatalf("prompt = %q, want the typed backslash and filter removed", got)
	}
	if !runAll(cmd, marker) {
		t.Fatal("the chosen command was not run: the popup must apply the action itself, not hand it to the dialogs (which have none open)")
	}
}
