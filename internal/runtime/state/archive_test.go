package state

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/KPO-Tech/seshat-runtime/internal/db"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func archiveStores(t *testing.T) map[string]*Store {
	t.Helper()
	stores := map[string]*Store{}

	mem, err := NewStoreWithBackend(NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	stores["memory"] = mem

	fs, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stores["filesystem"] = fs

	database, err := dbpkg.Open(context.Background(), dbpkg.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	backend, err := NewSQLiteBackend(database)
	if err != nil {
		t.Fatal(err)
	}
	sqlite, err := NewStoreWithBackend(backend)
	if err != nil {
		t.Fatal(err)
	}
	stores["sqlite"] = sqlite
	return stores
}

func plainMessages(from, to int) []types.Message {
	var out []types.Message
	for i := from; i < to; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		out = append(out, types.Message{ID: types.MessageID(fmt.Sprintf("m%02d", i)), Role: role,
			Content: []types.ContentBlock{types.TextContent{Text: fmt.Sprintf("message %02d", i)}}})
	}
	return out
}

// compactedFrom is what a compaction leaves: a summary that carries the compaction metadata, a marker, and the
// messages kept from keepFrom on.
func compactedFrom(all []types.Message, keepFrom int, summaryText string) []types.Message {
	summary := types.SystemMessage("compact_summary", summaryText)
	summary.Metadata = &types.MessageMetadata{TurnID: "compact", Compaction: &types.CompactionMetadata{Kind: "summary"}}
	marker := types.SystemMessage("compact_marker", "[Context compacted for token pressure]")
	marker.Metadata = &types.MessageMetadata{TurnID: "compact"}
	return append([]types.Message{summary, marker}, all[keepFrom:]...)
}

func texts(messages []types.Message) []string {
	var out []string
	for _, m := range messages {
		var b strings.Builder
		for _, c := range m.Content {
			if t, ok := c.(types.TextContent); ok {
				b.WriteString(t.Text)
			}
		}
		out = append(out, b.String())
	}
	return out
}

func TestACompactionArchivesWhatItDropsInEveryStore(t *testing.T) {
	for name, store := range archiveStores(t) {
		t.Run(name, func(t *testing.T) {
			id := types.SessionID("s1")
			meta := &types.SessionMetadata{ID: id, Status: types.SessionStatusActive}
			original := plainMessages(0, 8)
			if err := store.SaveSessionState(id, meta, nil, original); err != nil {
				t.Fatal(err)
			}
			compacted := compactedFrom(original, 6, "summary of messages 0 to 5")
			if err := store.SaveSessionState(id, meta, original, compacted); err != nil {
				t.Fatal(err)
			}

			archived, current, err := store.LoadHistory(id)
			if err != nil {
				t.Fatal(err)
			}
			if got := texts(archived); strings.Join(got, "|") != "message 00|message 01|message 02|message 03|message 04|message 05" {
				t.Errorf("archived = %v, want messages 00 to 05 in order", got)
			}
			if got := texts(current); strings.Join(got, "|") != "summary of messages 0 to 5|[Context compacted for token pressure]|message 06|message 07" {
				t.Errorf("current = %v", got)
			}

			// The session restores to what the model sees: the archive is not part of it.
			_, restored, err := store.RestoreSessionState(id)
			if err != nil {
				t.Fatalf("RestoreSessionState: %v", err)
			}
			if len(restored) != 4 {
				t.Errorf("restored %d messages, want the 4 of the compacted conversation", len(restored))
			}
			for _, m := range restored {
				for _, a := range archived {
					if m.ID == a.ID {
						t.Errorf("archived message %s must not be restored into the model's context", a.ID)
					}
				}
			}
		})
	}
}

func TestASecondCompactionAddsToTheArchive(t *testing.T) {
	for name, store := range archiveStores(t) {
		t.Run(name, func(t *testing.T) {
			id := types.SessionID("s2")
			meta := &types.SessionMetadata{ID: id, Status: types.SessionStatusActive}
			original := plainMessages(0, 8)
			_ = store.SaveSessionState(id, meta, nil, original)
			first := compactedFrom(original, 6, "first summary")
			_ = store.SaveSessionState(id, meta, original, first)

			// The conversation goes on, then compacts again, dropping the first summary and messages 06 to 09.
			grown := append(append([]types.Message(nil), first...), plainMessages(8, 12)...)
			_ = store.SaveSessionState(id, meta, first, grown)
			second := compactedFrom(plainMessages(0, 12), 10, "second summary")
			if err := store.SaveSessionState(id, meta, grown, second); err != nil {
				t.Fatal(err)
			}

			archived, current, err := store.LoadHistory(id)
			if err != nil {
				t.Fatal(err)
			}
			want := "message 00|message 01|message 02|message 03|message 04|message 05|message 06|message 07|message 08|message 09"
			if got := strings.Join(texts(archived), "|"); got != want {
				t.Errorf("archived = %s\nwant       %s", got, want)
			}
			if got := texts(current); got[0] != "second summary" || len(got) != 4 {
				t.Errorf("current = %v", got)
			}
			for _, m := range archived {
				if types.IsCompactionArtifact(m) {
					t.Errorf("the first summary must not be archived as history: %s", m.ID)
				}
			}
		})
	}
}

func TestARewriteThatIsNotACompactionStillReplacesTheTranscript(t *testing.T) {
	for name, store := range archiveStores(t) {
		t.Run(name, func(t *testing.T) {
			id := types.SessionID("s3")
			meta := &types.SessionMetadata{ID: id, Status: types.SessionStatusActive}
			original := plainMessages(0, 6)
			_ = store.SaveSessionState(id, meta, nil, original)
			// A rewind: the last two messages are removed. Nothing was compacted, so nothing is archived.
			rewound := original[:4]
			if err := store.SaveSessionState(id, meta, original, rewound); err != nil {
				t.Fatal(err)
			}
			archived, current, err := store.LoadHistory(id)
			if err != nil {
				t.Fatal(err)
			}
			if len(archived) != 0 || len(current) != 4 {
				t.Errorf("archived %d, current %d; a rewind archives nothing", len(archived), len(current))
			}
		})
	}
}

func TestPlainGrowthAndAnEmptyHistoryHaveNoArchive(t *testing.T) {
	for name, store := range archiveStores(t) {
		t.Run(name, func(t *testing.T) {
			id := types.SessionID("s4")
			meta := &types.SessionMetadata{ID: id, Status: types.SessionStatusActive}
			msgs := plainMessages(0, 4)
			_ = store.SaveSessionState(id, meta, nil, msgs[:2])
			_ = store.SaveSessionState(id, meta, msgs[:2], msgs)
			archived, current, err := store.LoadHistory(id)
			if err != nil || len(archived) != 0 || len(current) != 4 {
				t.Errorf("got %d archived, %d current, err %v", len(archived), len(current), err)
			}
		})
	}
}
