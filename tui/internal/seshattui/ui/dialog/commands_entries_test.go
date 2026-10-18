package dialog

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/list"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

func TestEntriesFromKeepsCommandsAndLeavesSkillsOut(t *testing.T) {
	sty := styles.SeshatDark()
	items := []list.FilterableItem{
		NewCommandItem(&sty, "new_session", "New Session", "ctrl+n", ActionNewSession{}),
		NewCommandItem(&sty, "skill_x", "x", "", ActionAttachSkill{ID: "x", Name: "x"}),
		NewCommandItem(&sty, "memory_show", "Memory: Show Notes", "", ActionMemory{Op: "show"}),
	}
	got := entriesFrom(items)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (the skill is left out): %+v", len(got), got)
	}
	if got[0].Title != "New Session" || got[0].Shortcut != "ctrl+n" {
		t.Fatalf("first entry = %+v", got[0])
	}
	if a, ok := got[1].Action.(ActionMemory); !ok || a.Op != "show" {
		t.Fatalf("second entry action = %#v, want the memory action", got[1].Action)
	}
}
