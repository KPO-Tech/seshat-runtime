package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	toolsearch "github.com/KPO-Tech/seshat-runtime/internal/tools/special/tool_search"
)

func callToolSearch(t *testing.T, ctx context.Context, query string) string {
	t.Helper()
	reg, err := NewBuiltinRegistryWithConfig(&Config{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	search, ok := reg.Resolve("tool_search")
	if !ok {
		t.Fatal("tool_search is not registered")
	}
	res, err := search.Call(ctx, contract.CallInput{Parsed: map[string]any{"query": query}}, nil)
	if err != nil {
		t.Fatalf("tool_search: %v", err)
	}
	return res.Content
}

// found reports whether the result lists the tool as a match (the "No deferred tools matched ..." message repeats the
// query, so a plain substring test would be fooled by it).
func found(out, tool string) bool {
	return strings.Contains(out, "\n  "+tool)
}

func TestToolSearchOnlyFindsWhatTheScopeOffers(t *testing.T) {
	// Without a scope, a tool the registry does not flag as deferred is not found: sub-agents are in this case.
	if out := callToolSearch(t, context.Background(), "select:calculator"); found(out, "calculator") {
		t.Errorf("calculator must not be found without a scope, got %q", out)
	}

	// The main agent's scope offers its tools, by name and by keywords, and nothing else.
	scoped := toolsearch.WithScope(context.Background(), []string{"calculator", "unit_convert", "statistics", "list_directory"})
	if out := callToolSearch(t, scoped, "select:calculator"); !found(out, "calculator") {
		t.Errorf("calculator must be found by name in scope, got %q", out)
	}
	if out := callToolSearch(t, scoped, "convert between units of measurement"); !found(out, "unit_convert") {
		t.Errorf("a keyword search must find unit_convert in scope, got %q", out)
	}
	if out := callToolSearch(t, scoped, "select:financial_calc"); found(out, "financial_calc") {
		t.Errorf("financial_calc is outside the scope and must not be found, got %q", out)
	}
}

func TestToolSearchScopeIgnoresUnknownAndDisabledTools(t *testing.T) {
	scoped := toolsearch.WithScope(context.Background(), []string{"not_a_tool", "list_directory"})
	if out := callToolSearch(t, scoped, "select:not_a_tool"); found(out, "not_a_tool") {
		t.Errorf("an unknown name must not be found, got %q", out)
	}
	if out := callToolSearch(t, scoped, "select:list_directory"); !found(out, "list_directory") {
		t.Errorf("a known, enabled tool of the scope must be found, got %q", out)
	}
}
