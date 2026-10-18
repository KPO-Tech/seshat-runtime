package sdk

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/db"
	"github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
)

func newNotesStore(t *testing.T) LongTermMemory {
	t.Helper()
	database, err := db.Open(context.Background(), db.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "notes.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return longterm.NewSQLiteStore(database.SQL())
}

func TestForgetMemoryNoteRemovesItAndCanBeUndone(t *testing.T) {
	ctx := context.Background()
	store := newNotesStore(t)
	if _, err := store.UpsertEntities(ctx, "u1", []longterm.EntityInput{
		{Name: "reply-language", EntityType: "preference", Observations: []string{"answers in French"}},
		{Name: "editor", EntityType: "preference", Observations: []string{"uses neovim"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := ForgetMemoryNote(ctx, store, "u1", "editor"); err != nil {
		t.Fatal(err)
	}
	notes, err := ListMemoryNotes(ctx, store, "u1")
	if err != nil || len(notes) != 1 || notes[0].Name != "reply-language" {
		t.Fatalf("notes = %+v, err = %v, want only reply-language", notes, err)
	}

	snaps, err := ListMemorySnapshots(ctx, store, "u1")
	if err != nil || len(snaps) == 0 {
		t.Fatalf("snapshots = %v, err = %v, want a copy taken before forgetting", snaps, err)
	}
	if err := RestoreMemorySnapshot(ctx, store, "u1", snaps[0].ID); err != nil {
		t.Fatal(err)
	}
	notes, _ = ListMemoryNotes(ctx, store, "u1")
	if len(notes) != 2 {
		t.Fatalf("after restore there are %d notes, want 2", len(notes))
	}
}

func TestForgetMemoryNoteUnknownNameAndOtherUsers(t *testing.T) {
	ctx := context.Background()
	store := newNotesStore(t)
	if _, err := store.UpsertEntities(ctx, "u1", []longterm.EntityInput{{Name: "a", EntityType: "x", Observations: []string{"o"}}}); err != nil {
		t.Fatal(err)
	}
	if err := ForgetMemoryNote(ctx, store, "u1", "missing"); !errors.Is(err, ErrMemoryNoteNotFound) {
		t.Fatalf("err = %v, want ErrMemoryNoteNotFound", err)
	}
	if err := ForgetMemoryNote(ctx, store, "u2", "a"); !errors.Is(err, ErrMemoryNoteNotFound) {
		t.Fatalf("another user's note must not be reachable, err = %v", err)
	}
	if notes, _ := ListMemoryNotes(ctx, store, "u1"); len(notes) != 1 {
		t.Fatalf("u1 lost a note: %+v", notes)
	}
}
