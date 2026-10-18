package mcp

import "testing"

func TestBuildInputSchemaKeepsTheServersProperties(t *testing.T) {
	w := &Wrapper{}
	got := w.buildInputSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "what to search"},
			"limit": map[string]any{"type": "integer"},
		},
		"required": []any{"query"},
	})
	if got.Type != "object" {
		t.Errorf("type = %q, want object", got.Type)
	}
	if len(got.Properties) != 2 || got.Properties["query"].Description != "what to search" {
		t.Errorf("the server's properties were lost: %+v", got.Properties)
	}
	if len(got.Required) != 1 || got.Required[0] != "query" {
		t.Errorf("required = %v, want [query]", got.Required)
	}
}

func TestBuildInputSchemaWithoutASchemaIsAnEmptyObject(t *testing.T) {
	w := &Wrapper{}
	for _, in := range []map[string]any{nil, {}} {
		if got := w.buildInputSchema(in); got.Type != "object" {
			t.Errorf("type = %q, want object", got.Type)
		}
	}
	if got := w.buildInputSchema(map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}}}); got.Type != "object" || len(got.Properties) != 1 {
		t.Errorf("a schema without a type is an object: %+v", got)
	}
}
