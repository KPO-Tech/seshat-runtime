package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/commands"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/chat"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/dialog"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// memoryUser is the owner of the memories the terminal app keeps: there is one user on a machine.
const memoryUser = "local"

// memoryAction is what a memory request asks for.
type memoryAction int

const (
	memoryNone memoryAction = iota
	memoryList
	memoryForget
	memoryUndo
)

// handleMemoryAction carries out a memory request from the command palette. Forgetting asks for the note's name first.
func (m *UI) handleMemoryAction(msg dialog.ActionMemory) tea.Cmd {
	if msg.Op == "forget" && msg.Name == "" {
		m.dialog.CloseFrontDialog()
		m.dialog.OpenDialog(dialog.NewArguments(
			m.com,
			"Forget a Memory Note",
			"The name of the note, as Memory: Show Notes lists it. Memory is copied first, so Memory: Undo Last Change puts it back.",
			[]commands.Argument{{ID: "name", Title: "Note name", Required: true}},
			msg,
		))
		return nil
	}
	m.dialog.CloseFrontDialog()
	switch msg.Op {
	case "forget":
		return m.runMemory(memoryForget, msg.Name, "Memory: forget "+msg.Name)
	case "undo":
		return m.runMemory(memoryUndo, "", "Memory: undo last change")
	}
	return m.runMemory(memoryList, "", "Memory: notes")
}

// runMemory carries out a memory action and shows the answer in the chat. The answer is shown to the user only: it is never
// sent to the agent.
func (m *UI) runMemory(action memoryAction, name, label string) tea.Cmd {
	if !m.hasSession() {
		if err := m.startEmptySession(); err != nil {
			return util.ReportError(err)
		}
	}
	m.shellSeq++
	id := fmt.Sprintf("shell-%d", m.shellSeq)
	item := chat.NewShellOutputItem(m.com.Styles, id, label, true)
	m.shellItems[id] = item
	m.chat.AppendMessages(item)
	m.chat.ScrollToBottom()

	store := m.com.Workspace.LongTermMemory()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := memoryReply(ctx, store, action, name)
		code := 0
		if err != nil {
			text, code = err.Error(), 1
		}
		// shared is false: the answer is not added to the agent's context.
		return shellDoneMsg{id: id, command: label, output: text, exitCode: code}
	}
}

// memoryReply carries out a "/memory" action and returns the text to show.
func memoryReply(ctx context.Context, store sdk.LongTermMemory, action memoryAction, name string) (string, error) {
	if store == nil {
		return "", errors.New("long-term memory is not available in this session")
	}
	switch action {
	case memoryForget:
		if err := sdk.ForgetMemoryNote(ctx, store, memoryUser, name); err != nil {
			return "", err
		}
		return fmt.Sprintf("Forgot %q. /memory undo puts it back.", name), nil
	case memoryUndo:
		snaps, err := sdk.ListMemorySnapshots(ctx, store, memoryUser)
		if err != nil {
			return "", err
		}
		if len(snaps) == 0 {
			return "", errors.New("there is no earlier copy of memory to go back to")
		}
		if err := sdk.RestoreMemorySnapshot(ctx, store, memoryUser, snaps[0].ID); err != nil {
			return "", err
		}
		return "Memory is back as it was before the last change.", nil
	}
	notes, err := sdk.ListMemoryNotes(ctx, store, memoryUser)
	if err != nil {
		return "", err
	}
	return formatMemoryNotes(notes), nil
}

// formatMemoryNotes lists the notes, one per entity, with what is known about each.
func formatMemoryNotes(notes []sdk.MemoryNote) string {
	if len(notes) == 0 {
		return "Memory holds nothing yet."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Memory holds %d notes. /memory forget NAME removes one, /memory undo goes back.\n", len(notes))
	for _, n := range notes {
		fmt.Fprintf(&b, "\n%s (%s)\n", n.Name, n.EntityType)
		for _, o := range n.Observations {
			fmt.Fprintf(&b, "  - %s\n", o)
		}
	}
	return b.String()
}
