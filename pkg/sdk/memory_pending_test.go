package sdk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type recordingExtractor struct {
	calls []string
	fail  bool
}

func (r *recordingExtractor) Extract(_ context.Context, user string, messages []types.Message) error {
	if r.fail {
		return errors.New("model down")
	}
	r.calls = append(r.calls, user)
	return nil
}

func savePendingSession(t *testing.T, store SessionStore, id string, age time.Duration, turns int) {
	t.Helper()
	at := time.Now().Add(-age)
	meta := &SessionMetadata{ID: SessionID(id), Status: types.SessionStatusActive, CreatedAt: at, UpdatedAt: at, TotalTurns: turns}
	msgs := []types.Message{{ID: "u1", Role: types.RoleUser, Content: []types.ContentBlock{types.TextContent{Text: "hello"}}}}
	if err := store.SaveSessionState(SessionID(id), meta, nil, msgs); err != nil {
		t.Fatal(err)
	}
}

func TestExtractPendingMemoriesReadsIdleSessionsOnce(t *testing.T) {
	store, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	savePendingSession(t, store, "idle", time.Hour, 2)
	savePendingSession(t, store, "fresh", time.Minute, 2)
	savePendingSession(t, store, "ancient", 30*24*time.Hour, 2)
	savePendingSession(t, store, "empty", time.Hour, 0)

	extractor := &recordingExtractor{}
	client := &Client{store: store, memoryExtractor: extractor}

	read, err := client.ExtractPendingMemories(context.Background(), "u1", PendingMemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if read != 1 || len(extractor.calls) != 1 || extractor.calls[0] != "u1" {
		t.Fatalf("read=%d calls=%v, want only the idle session read for u1", read, extractor.calls)
	}

	read, err = client.ExtractPendingMemories(context.Background(), "u1", PendingMemoryOptions{})
	if err != nil || read != 0 {
		t.Fatalf("second call read=%d err=%v, want 0: the session is marked", read, err)
	}
}

func TestExtractPendingMemoriesLeavesFailedSessionsForNextTime(t *testing.T) {
	store, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	savePendingSession(t, store, "idle", time.Hour, 1)

	extractor := &recordingExtractor{fail: true}
	client := &Client{store: store, memoryExtractor: extractor}
	if read, err := client.ExtractPendingMemories(context.Background(), "", PendingMemoryOptions{}); err == nil || read != 0 {
		t.Fatalf("read=%d err=%v, want an error and no session counted", read, err)
	}
	extractor.fail = false
	if read, err := client.ExtractPendingMemories(context.Background(), "", PendingMemoryOptions{}); err != nil || read != 1 {
		t.Fatalf("retry read=%d err=%v, want the session read", read, err)
	}
}

func TestExtractPendingMemoriesRespectsLimitAndOffSwitch(t *testing.T) {
	store, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"a", "b", "c"} {
		savePendingSession(t, store, id, time.Hour, 1)
	}
	extractor := &recordingExtractor{}
	client := &Client{store: store, memoryExtractor: extractor}
	if read, _ := client.ExtractPendingMemories(context.Background(), "", PendingMemoryOptions{Limit: 2}); read != 2 {
		t.Fatalf("read=%d, want the limit of 2", read)
	}
	off := &Client{store: store}
	if read, err := off.ExtractPendingMemories(context.Background(), "", PendingMemoryOptions{}); read != 0 || err != nil {
		t.Fatalf("off client read=%d err=%v, want a no-op", read, err)
	}
}
