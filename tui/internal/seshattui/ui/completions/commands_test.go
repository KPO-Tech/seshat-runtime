package completions

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCommandItemsOpenAndReturnTheChosenPayload(t *testing.T) {
	c := New(Styles{})
	c.SetCommandItems([]CommandCompletionValue{
		{Title: "New Session", Shortcut: "ctrl+n", Payload: "new"},
		{Title: "Memory: Show Notes", Payload: "memory"},
	})
	if !c.IsOpen() || !c.HasItems() {
		t.Fatal("the popup must open with the commands")
	}
	c.Filter("memory")
	msg, handled := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatal("enter must be handled")
	}
	sel, ok := msg.(SelectionMsg[CommandCompletionValue])
	if !ok {
		t.Fatalf("got %T, want a command selection", msg)
	}
	if sel.Value.Payload != "memory" || sel.Value.Title != "Memory: Show Notes" {
		t.Fatalf("selected %+v, want the memory command", sel.Value)
	}
	if c.IsOpen() {
		t.Fatal("choosing a command must close the popup")
	}
}
