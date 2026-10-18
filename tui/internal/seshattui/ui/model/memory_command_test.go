package model

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/KPO-Tech/seshat-runtime/internal/db"
	longtermStore "github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
)

func TestParseMemoryInput(t *testing.T) {
	cases := []struct {
		in     string
		action memoryAction
		name   string
	}{
		{"/memory", memoryList, ""},
		{"  /memory  ", memoryList, ""},
		{"/memory undo", memoryUndo, ""},
		{"/memory forget editor", memoryForget, "editor"},
		{"/memory forget reply language", memoryForget, "reply language"},
		{"/memory forget", memoryNone, ""},
		{"/memory undo now", memoryNone, ""},
		{"/memoryx", memoryNone, ""},
		{"memory", memoryNone, ""},
		{"/help", memoryNone, ""},
	}
	for _, c := range cases {
		action, name := parseMemoryInput(c.in)
		if action != c.action || name != c.name {
			t.Errorf("parseMemoryInput(%q) = (%d, %q), want (%d, %q)", c.in, action, name, c.action, c.name)
		}
	}
}

func TestMemoryReplyListsForgetsAndUndoes(t *testing.T) {
	ctx := context.Background()
	database, err := dbpkg.Open(ctx, dbpkg.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "m.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := longtermStore.NewSQLiteStore(database.SQL())
	if _, err := store.UpsertEntities(ctx, memoryUser, []longtermStore.EntityInput{
		{Name: "editor", EntityType: "preference", Observations: []string{"uses neovim"}},
	}); err != nil {
		t.Fatal(err)
	}

	text, err := memoryReply(ctx, store, memoryList, "")
	if err != nil || !strings.Contains(text, "editor") || !strings.Contains(text, "uses neovim") {
		t.Fatalf("list = %q, %v, want the note and its observation", text, err)
	}
	if _, err := memoryReply(ctx, store, memoryForget, "editor"); err != nil {
		t.Fatal(err)
	}
	if text, _ := memoryReply(ctx, store, memoryList, ""); !strings.Contains(text, "nothing yet") {
		t.Fatalf("after forgetting, list = %q", text)
	}
	if _, err := memoryReply(ctx, store, memoryUndo, ""); err != nil {
		t.Fatal(err)
	}
	if text, _ := memoryReply(ctx, store, memoryList, ""); !strings.Contains(text, "editor") {
		t.Fatalf("after undo, list = %q, want the note back", text)
	}
}

func TestMemoryReplyWithoutStore(t *testing.T) {
	if _, err := memoryReply(context.Background(), nil, memoryList, ""); err == nil {
		t.Fatal("want an error when there is no store")
	}
}
