package longterm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type scriptedCaller struct {
	reply string
	calls int
	err   error
}

func (c *scriptedCaller) CreateMessage(_ context.Context, _ types.APIRequest) (*types.APIResponse, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return &types.APIResponse{Content: []types.ContentBlock{types.TextContent{Text: c.reply}}}, nil
}

// seedMemory stores a user's memory: two entities for one project under different names, one preference, and filler
// so the memory is over the consolidation threshold.
func seedMemory(t *testing.T, s *SQLiteStore, user string) {
	t.Helper()
	ctx := context.Background()
	filler := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		filler = append(filler, fmt.Sprintf("filler fact %d", i))
	}
	if _, err := s.UpsertEntities(ctx, user, []EntityInput{
		{Name: "seshat-runtime", EntityType: "project", Observations: []string{"written in Go", "releases are tagged vX.Y.Z"}},
		{Name: "seshat runtime", EntityType: "project", Observations: []string{"written in Go", "uses make for tasks"}},
		{Name: "user", EntityType: "preference", Observations: []string{"prefers French", "wants no attribution in commits"}},
		{Name: "misc", EntityType: "fact", Observations: filler},
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{{"user", "seshat runtime"}, {"seshat runtime", "seshat-runtime"}} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO memory_relations (id, user_id, from_name, to_name, relation_type, created_at_unix) VALUES (?, ?, ?, ?, 'works_on', 1)`,
			r[0]+r[1], user, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
}

const mergeReply = `{"entities":[
 {"name":"seshat-runtime","entity_type":"project","observations":["written in Go","releases are tagged vX.Y.Z","uses make for tasks"],"merged_from":["seshat runtime"]},
 {"name":"user","entity_type":"preference","observations":["prefers French","wants no attribution in commits"]}
]}`

func newConsolidator(s *SQLiteStore, reply string) (*Consolidator, *scriptedCaller) {
	caller := &scriptedCaller{reply: reply}
	return NewConsolidator(s, caller, ConsolidatorConfig{MinObservations: 10}), caller
}

func TestConsolidationMergesDuplicatesAndKeepsRelations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	c, _ := newConsolidator(s, mergeReply)

	report, err := c.Consolidate(ctx, "u1")
	if err != nil || report == nil {
		t.Fatalf("Consolidate = %v, %v", report, err)
	}
	if report.EntitiesBefore != 4 || report.EntitiesAfter != 3 {
		t.Errorf("entities %d -> %d, want 4 -> 3 (misc was not mentioned, so it is kept)", report.EntitiesBefore, report.EntitiesAfter)
	}
	got, _ := s.ListEntities(ctx, "u1")
	names := map[string]Entity{}
	for _, e := range got {
		names[e.Name] = e
	}
	if _, ok := names["seshat runtime"]; ok {
		t.Error("the merged-away entity must be gone")
	}
	if len(names["seshat-runtime"].Observations) != 3 || len(names["misc"].Observations) != 60 {
		t.Errorf("merged entity and the untouched one are wrong: %+v", names)
	}
	graph, _ := s.OpenNodes(ctx, "u1", []string{"user", "seshat-runtime"})
	if len(graph.Relations) != 1 || graph.Relations[0].From != "user" || graph.Relations[0].To != "seshat-runtime" {
		t.Errorf("the relation to the merged entity must follow it, and the self-relation must go: %+v", graph.Relations)
	}
}

func TestConsolidationSkipsSmallMemoryAndRecentRuns(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _ = s.UpsertEntities(ctx, "u1", []EntityInput{{Name: "a", EntityType: "fact", Observations: []string{"x"}}})
	c, caller := newConsolidator(s, mergeReply)
	if r, err := c.MaybeConsolidate(ctx, "u1"); r != nil || err != nil || caller.calls != 0 {
		t.Errorf("a small memory must not reach the model: %v %v calls=%d", r, err, caller.calls)
	}

	seedMemory(t, s, "u2")
	if r, _ := c.MaybeConsolidate(ctx, "u2"); r == nil {
		t.Fatal("a big memory must be consolidated")
	}
	if r, _ := c.MaybeConsolidate(ctx, "u2"); r != nil || caller.calls != 1 {
		t.Errorf("a second run within the interval must be skipped, calls=%d", caller.calls)
	}
}

func TestConsolidationRefusesAnAnswerThatKeepsTooLittle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	c, _ := newConsolidator(s, `{"entities":[{"name":"user","entity_type":"preference","observations":["prefers French"]}],"dropped":["misc"]}`)

	_, err := c.Consolidate(ctx, "u1")
	if !errors.Is(err, ErrRefusedConsolidation) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	got, _ := s.ListEntities(ctx, "u1")
	if len(got) != 4 {
		t.Errorf("a refused result must change nothing, %d entities left", len(got))
	}
}

func TestConsolidationRefusesTooManyDrops(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	c, _ := newConsolidator(s, `{"entities":[],"dropped":["user","misc","seshat-runtime"]}`)
	if _, err := c.Consolidate(ctx, "u1"); !errors.Is(err, ErrRefusedConsolidation) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

func TestConsolidationDropsAnExplicitlyDroppedEntity(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	// Dropping the 60-observation filler is allowed here: the keep ratio is lowered, as a host may choose to.
	caller := &scriptedCaller{reply: `{"entities":[
	  {"name":"seshat-runtime","entity_type":"project","observations":["written in Go","releases are tagged vX.Y.Z","uses make for tasks"],"merged_from":["seshat runtime"]},
	  {"name":"user","entity_type":"preference","observations":["prefers French","wants no attribution in commits"]}],
	  "dropped":["misc"]}`}
	c := NewConsolidator(s, caller, ConsolidatorConfig{MinObservations: 10, MinKeepRatio: 0.01})
	if _, err := c.Consolidate(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListEntities(ctx, "u1")
	for _, e := range got {
		if e.Name == "misc" {
			t.Error("a dropped entity must be removed")
		}
	}
}

func TestConsolidationNeverStoresSecretsOrInventions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	c, _ := newConsolidator(s, `{"entities":[
	  {"name":"seshat-runtime","entity_type":"project","observations":["written in Go","releases are tagged vX.Y.Z","uses make for tasks","key sk-abcdefghijklmnopqrstuvwxyz123456"],"merged_from":["seshat runtime"]},
	  {"name":"invented","entity_type":"fact","observations":["never existed"]},
	  {"name":"user","entity_type":"preference","observations":["prefers French","wants no attribution in commits"]}]}`)
	if _, err := c.Consolidate(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListEntities(ctx, "u1")
	for _, e := range got {
		if e.Name == "invented" {
			t.Error("an entity that merges nothing and did not exist must not be created")
		}
		for _, o := range e.Observations {
			if strings.Contains(o, "sk-") {
				t.Errorf("a credential reached memory: %q", o)
			}
		}
	}
}

func TestConsolidationIsDroppedWhenMemoryChangedMeanwhile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	snapshot, _ := s.ListEntities(ctx, "u1")

	// Another session adds an observation while the model was thinking.
	if _, err := s.AddObservations(ctx, "u1", []ObservationInput{{EntityName: "user", Contents: []string{"uses Windows"}}}); err != nil {
		t.Fatal(err)
	}
	err := s.ReplaceEntities(ctx, "u1", snapshot, []EntityInput{{Name: "user", EntityType: "preference", Observations: []string{"prefers French"}}}, nil)
	if !errors.Is(err, ErrChangedDuringConsolidation) {
		t.Fatalf("err = %v, want ErrChangedDuringConsolidation", err)
	}
	got, _ := s.ListEntities(ctx, "u1")
	if len(got) != 4 {
		t.Errorf("nothing may change when the snapshot is stale, %d entities left", len(got))
	}
}

func TestEntitiesCreatedMeanwhileSurviveAConsolidation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	snapshot, _ := s.ListEntities(ctx, "u1")
	_, _ = s.UpsertEntities(ctx, "u1", []EntityInput{{Name: "newcomer", EntityType: "fact", Observations: []string{"added later"}}})

	keep := make([]EntityInput, 0, len(snapshot))
	for _, e := range snapshot {
		keep = append(keep, EntityInput{Name: e.Name, EntityType: e.EntityType, Observations: e.Observations})
	}
	if err := s.ReplaceEntities(ctx, "u1", snapshot, keep[:1], nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListEntities(ctx, "u1")
	found := false
	for _, e := range got {
		found = found || e.Name == "newcomer"
	}
	if !found {
		t.Error("an entity written after the snapshot is not the consolidation's to remove")
	}
}

func TestConsolidationSurvivesABrokenModel(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	for _, reply := range []string{"not json at all", `{"entities": "oops"}`} {
		c, _ := newConsolidator(s, reply)
		if _, err := c.Consolidate(ctx, "u1"); err == nil {
			t.Errorf("reply %q must be an error", reply)
		}
	}
	c, caller := newConsolidator(s, "")
	caller.err = errors.New("provider down")
	if _, err := c.Consolidate(ctx, "u1"); err == nil {
		t.Error("a provider error must be returned")
	}
	if got, _ := s.ListEntities(ctx, "u1"); len(got) != 4 {
		t.Errorf("memory must be untouched after failures, %d entities", len(got))
	}
}

func TestWithinInputBudgetKeepsAtLeastOneEntity(t *testing.T) {
	big := []Entity{{Name: "a", Observations: []string{strings.Repeat("x", 500)}}, {Name: "b", Observations: []string{"y"}}}
	if got := withinInputBudget(big, 10); len(got) != 1 {
		t.Errorf("kept %d entities, want 1", len(got))
	}
	if got := withinInputBudget(big, 100_000); len(got) != 2 {
		t.Errorf("kept %d entities, want 2", len(got))
	}
}

func TestExtractionTriggersConsolidationWhenMemoryIsBigEnough(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	consolidation := &scriptedCaller{reply: mergeReply}
	// The extractor's own call finds nothing; the consolidator then rewrites the big memory.
	extraction := &scriptedCaller{reply: `{"entities":[]}`}
	ex := NewExtractor(s, extraction, ExtractorConfig{MinTurns: 1})
	ex.SetConsolidator(NewConsolidator(s, consolidation, ConsolidatorConfig{MinObservations: 10}))

	msgs := []types.Message{types.UserMessage("m1", "hello"), types.UserMessage("m2", "again")}
	if err := ex.Extract(ctx, "u1", msgs); err != nil {
		t.Fatal(err)
	}
	if consolidation.calls != 1 {
		t.Errorf("consolidation calls = %d, want 1", consolidation.calls)
	}
	if got, _ := s.ListEntities(ctx, "u1"); len(got) != 3 {
		t.Errorf("memory has %d entities after consolidation, want 3", len(got))
	}
}

func TestExtractionWithoutAConsolidatorNeverConsolidates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedMemory(t, s, "u1")
	ex := NewExtractor(s, &scriptedCaller{reply: `{"entities":[]}`}, ExtractorConfig{MinTurns: 1})
	if err := ex.Extract(ctx, "u1", []types.Message{types.UserMessage("m1", "hello")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListEntities(ctx, "u1"); len(got) != 4 {
		t.Errorf("memory must be untouched, %d entities", len(got))
	}
}
