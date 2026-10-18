package longterm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestASnapshotCopiesEntitiesAndRelationsAndRestoresThem(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")

	snap, err := s.SaveSnapshot(ctx, "u1", "test copy")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Entities != 4 || snap.Observations != 66 {
		t.Errorf("snapshot = %+v, want 4 entities and 66 observations", snap)
	}

	// Damage the memory: remove an entity (and so its relations) and add another.
	if err := s.ReplaceEntities(ctx, "u1", mustList(t, s, "u1"), []EntityInput{{Name: "only", EntityType: "fact", Observations: []string{"x"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := mustList(t, s, "u1"); len(got) != 1 {
		t.Fatalf("setup: %d entities, want 1", len(got))
	}

	if err := s.RestoreSnapshot(ctx, "u1", snap.ID); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	got := mustList(t, s, "u1")
	if len(got) != 4 {
		t.Fatalf("restored %d entities, want 4", len(got))
	}
	graph, _ := s.OpenNodes(ctx, "u1", []string{"user", "seshat runtime", "seshat-runtime"})
	if len(graph.Relations) != 2 {
		t.Errorf("restored %d relations, want the 2 of the snapshot", len(graph.Relations))
	}
	// Restoring is itself reversible: a copy of what was there was taken first.
	infos, _ := s.ListSnapshots(ctx, "u1")
	found := false
	for _, i := range infos {
		found = found || (i.Reason != "" && i.ID != snap.ID && i.Entities == 1)
	}
	if !found {
		t.Errorf("a restore must first copy the memory it replaces, snapshots: %+v", infos)
	}
}

func mustList(t *testing.T, s *SQLiteStore, user string) []Entity {
	t.Helper()
	got, err := s.ListEntities(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSnapshotsBelongToTheirUserAndAreNotFoundForAnother(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	snap, _ := s.SaveSnapshot(ctx, "u1", "mine")
	if err := s.RestoreSnapshot(ctx, "u2", snap.ID); !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("another user must not restore it: %v", err)
	}
	if err := s.RestoreSnapshot(ctx, "u1", "no-such-id"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if infos, _ := s.ListSnapshots(ctx, "u2"); len(infos) != 0 {
		t.Errorf("u2 sees %d snapshots of another user", len(infos))
	}
}

func TestOnlyTheNewestSnapshotsAreKept(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	var ids []string
	for i := 0; i < maxSnapshotsPerUser+4; i++ {
		snap, err := s.SaveSnapshot(ctx, "u1", fmt.Sprintf("copy %d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, snap.ID)
	}
	infos, _ := s.ListSnapshots(ctx, "u1")
	if len(infos) != maxSnapshotsPerUser {
		t.Fatalf("kept %d snapshots, want %d", len(infos), maxSnapshotsPerUser)
	}
	if infos[0].ID != ids[len(ids)-1] {
		t.Error("the newest snapshot comes first and is kept")
	}
	if err := s.RestoreSnapshot(ctx, "u1", ids[0]); !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("the oldest snapshot must have been pruned: %v", err)
	}
}

func TestConsolidationTakesACopyFirstAndCanBeUndone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	c, _ := newConsolidator(s, mergeReply)

	report, err := c.Consolidate(ctx, "u1")
	if err != nil || report == nil || report.SnapshotID == "" {
		t.Fatalf("Consolidate = %+v, %v; want a report naming the snapshot", report, err)
	}
	if got := mustList(t, s, "u1"); len(got) != 3 {
		t.Fatalf("after consolidation %d entities, want 3", len(got))
	}

	if err := c.UndoLast(ctx, "u1"); err != nil {
		t.Fatalf("UndoLast: %v", err)
	}
	got := mustList(t, s, "u1")
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if len(got) != 4 || !names["seshat runtime"] {
		t.Errorf("undo must bring back the 4 original entities, got %v", names)
	}
}

func TestUndoWithoutAConsolidationIsNotFound(t *testing.T) {
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	c, _ := newConsolidator(s, mergeReply)
	if err := c.UndoLast(context.Background(), "u1"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("err = %v, want ErrSnapshotNotFound", err)
	}
}

func TestConsolidationRefusesToRunWithoutAWayBack(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	caller := &scriptedCaller{reply: mergeReply}
	c := NewConsolidator(noSnapshotStore{s}, caller, ConsolidatorConfig{MinObservations: 10})

	if _, err := c.Consolidate(ctx, "u1"); !errors.Is(err, ErrNoSnapshotSupport) {
		t.Fatalf("err = %v, want ErrNoSnapshotSupport", err)
	}
	if caller.calls != 0 {
		t.Error("the model must not be called when the result could not be undone")
	}
	if got := mustList(t, s, "u1"); len(got) != 4 {
		t.Errorf("memory must be untouched, %d entities", len(got))
	}
}

// noSnapshotStore exposes only the Consolidatable methods of a SQLiteStore.
type noSnapshotStore struct{ inner *SQLiteStore }

func (n noSnapshotStore) UpsertEntities(ctx context.Context, u string, in []EntityInput) ([]Entity, error) {
	return n.inner.UpsertEntities(ctx, u, in)
}
func (n noSnapshotStore) AddObservations(ctx context.Context, u string, in []ObservationInput) ([]ObservationResult, error) {
	return n.inner.AddObservations(ctx, u, in)
}
func (n noSnapshotStore) SearchNodes(ctx context.Context, u, q string) (*Graph, error) {
	return n.inner.SearchNodes(ctx, u, q)
}
func (n noSnapshotStore) OpenNodes(ctx context.Context, u string, names []string) (*Graph, error) {
	return n.inner.OpenNodes(ctx, u, names)
}
func (n noSnapshotStore) RetrieveForContext(ctx context.Context, u, q string, max int) (string, error) {
	return n.inner.RetrieveForContext(ctx, u, q, max)
}
func (n noSnapshotStore) ListEntities(ctx context.Context, u string) ([]Entity, error) {
	return n.inner.ListEntities(ctx, u)
}
func (n noSnapshotStore) ReplaceEntities(ctx context.Context, u string, snap []Entity, e []EntityInput, r map[string]string) error {
	return n.inner.ReplaceEntities(ctx, u, snap, e, r)
}
