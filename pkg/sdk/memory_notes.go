package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
)

// MemoryNote is one thing long-term memory holds about a user: a named entity (a preference, a project, a tool) and what is
// known about it.
type MemoryNote = longterm.Entity

// errMemoryNotListable is returned when the store cannot list or rewrite its entities.
var errMemoryNotListable = errors.New("the long-term memory store cannot list or rewrite its notes")

// ErrMemoryNoteNotFound is returned by ForgetMemoryNote when the user has no note with that name.
var ErrMemoryNoteNotFound = errors.New("memory note not found")

// LongTermMemory returns the store this client reads and writes memory in, or nil when it has none.
func (c *Client) LongTermMemory() LongTermMemory {
	if c == nil || c.config == nil {
		return nil
	}
	return c.config.LongTermMemory
}

// ListMemoryNotes returns everything memory holds about a user, ordered by name.
func ListMemoryNotes(ctx context.Context, store LongTermMemory, userID string) ([]MemoryNote, error) {
	lister, ok := store.(longterm.Lister)
	if !ok {
		return nil, errMemoryNotListable
	}
	return lister.ListEntities(ctx, userID)
}

// ForgetMemoryNote removes the note called name from a user's memory, with its relations. The memory is copied first
// (see ListMemorySnapshots and RestoreMemorySnapshot), so forgetting can be undone.
func ForgetMemoryNote(ctx context.Context, store LongTermMemory, userID, name string) error {
	rewritable, ok := store.(longterm.Consolidatable)
	if !ok {
		return errMemoryNotListable
	}
	current, err := rewritable.ListEntities(ctx, userID)
	if err != nil {
		return err
	}
	kept := make([]longterm.EntityInput, 0, len(current))
	found := false
	for _, e := range current {
		if e.Name == name {
			found = true
			continue
		}
		kept = append(kept, longterm.EntityInput{Name: e.Name, EntityType: e.EntityType, Observations: e.Observations})
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrMemoryNoteNotFound, strings.TrimSpace(name))
	}
	if snapshotter, ok := store.(longterm.Snapshotter); ok {
		if _, err := snapshotter.SaveSnapshot(ctx, userID, "forget "+name); err != nil {
			return fmt.Errorf("copy memory before forgetting: %w", err)
		}
	}
	return rewritable.ReplaceEntities(ctx, userID, current, kept, nil)
}
