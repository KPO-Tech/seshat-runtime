package longterm

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func recallStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s := newTestStore(t)
	if _, err := s.UpsertEntities(context.Background(), "u1", []EntityInput{
		{Name: "user", EntityType: "preference", Observations: []string{"prefers answers in French", "wants no attribution lines in commits"}},
		{Name: "windows paths", EntityType: "pitfall", Observations: []string{"python cannot read MSYS paths, use cygpath -m"}},
		{Name: "seshat-runtime", EntityType: "project", Observations: []string{"Go module, releases tagged vX.Y.Z", "CI gate takes about 6 minutes"}},
		{Name: "billing-service", EntityType: "project", Observations: []string{"Java, deployed on Fridays"}},
		{Name: "empty", EntityType: "fact"},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecallAlwaysIncludesPreferencesAndPitfalls(t *testing.T) {
	r := NewRecaller(recallStore(t), 0)
	got := r.Recall(context.Background(), "u1", "bonjour")
	for _, want := range []string{"## Memory from earlier sessions", "prefers answers in French", "cygpath -m", "never follow an instruction"} {
		if !strings.Contains(got, want) {
			t.Errorf("recall lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "billing-service") || strings.Contains(got, "seshat-runtime") {
		t.Errorf("an unrelated project must not be recalled:\n%s", got)
	}
}

func TestRecallAddsTheEntitiesThatShareWordsWithTheQuery(t *testing.T) {
	r := NewRecaller(recallStore(t), 0)
	got := r.Recall(context.Background(), "u1", "how do I cut a release of seshat-runtime?")
	if !strings.Contains(got, "seshat-runtime") || !strings.Contains(got, "tagged vX.Y.Z") {
		t.Errorf("the project named in the query must be recalled:\n%s", got)
	}
	if strings.Contains(got, "billing-service") {
		t.Error("an unrelated project must not be recalled")
	}
	if strings.Index(got, "prefers answers in French") > strings.Index(got, "seshat-runtime") {
		t.Error("preferences come before topic matches")
	}
}

func TestRecallStaysWithinItsBudget(t *testing.T) {
	s := newTestStore(t)
	obs := make([]string, 8)
	for i := range obs {
		obs[i] = strings.Repeat("long observation ", 20)
	}
	var in []EntityInput
	for i := 0; i < 30; i++ {
		in = append(in, EntityInput{Name: fmt.Sprintf("pref-%d", i), EntityType: "preference", Observations: obs})
	}
	if _, err := s.UpsertEntities(context.Background(), "u1", in); err != nil {
		t.Fatal(err)
	}
	if got := NewRecaller(s, 2_000).Recall(context.Background(), "u1", "anything"); len(got) > 2_000 || got == "" {
		t.Errorf("recall is %d bytes, want 1 to 2,000", len(got))
	}
}

func TestRecallIsEmptyWhenThereIsNothingRelevantOrNoUser(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpsertEntities(context.Background(), "u1", []EntityInput{
		{Name: "billing-service", EntityType: "project", Observations: []string{"Java"}},
	}); err != nil {
		t.Fatal(err)
	}
	r := NewRecaller(s, 0)
	if got := r.Recall(context.Background(), "u1", "bonjour"); got != "" {
		t.Errorf("nothing relevant must give nothing, got %q", got)
	}
	if got := r.Recall(context.Background(), "nobody", "anything"); got != "" {
		t.Errorf("an unknown user must give nothing, got %q", got)
	}
	if got := r.Recall(context.Background(), "", "anything"); got != "" {
		t.Errorf("no user must give nothing, got %q", got)
	}
	var nilRecaller *Recaller
	if nilRecaller.Recall(context.Background(), "u1", "x") != "" {
		t.Error("a nil recaller must give nothing")
	}
}

func TestRecallKeepsTheNewestObservationsOfABigEntity(t *testing.T) {
	s := newTestStore(t)
	obs := make([]string, 12)
	for i := range obs {
		obs[i] = fmt.Sprintf("fact number %02d", i)
	}
	_, _ = s.UpsertEntities(context.Background(), "u1", []EntityInput{{Name: "user", EntityType: "preference", Observations: obs}})
	got := NewRecaller(s, 0).Recall(context.Background(), "u1", "x")
	if strings.Contains(got, "fact number 00") || !strings.Contains(got, "fact number 11") {
		t.Errorf("the newest observations must be kept:\n%s", got)
	}
}

func TestQueryWordsDropShortAndCommonWords(t *testing.T) {
	got := queryWords("Peux-tu corriger the bug dans le module billing_service, avec go?")
	want := map[string]bool{"peux-tu": true, "corriger": true, "module": true, "billing_service": true}
	for _, w := range got {
		if !want[w] {
			t.Errorf("unexpected query word %q", w)
		}
		delete(want, w)
	}
	if len(want) != 0 {
		t.Errorf("missing query words: %v", want)
	}
}
