package catalog

import (
	"fmt"
	"strings"
	"testing"
)

func TestEncodeKeepsTheRicherFieldsAndReadsVersion1(t *testing.T) {
	in := &Catalog{Providers: map[string]Provider{"p": {ID: "p", Name: "P", BaseURL: "https://p.test/v1", Protocol: protocolOpenAICompatible, Models: map[string]Model{
		"m": {
			ID: "m", Name: "M", ContextWindow: 200000, MaxInput: 150000, MaxOutput: 8000, Reasoning: true, Attachment: true, StructuredOutput: true,
			NoTemperature: true, Interleaved: "reasoning_details", OpenWeights: true, Knowledge: "2025-01", LastUpdated: "2026-01-02",
			InputModalities: []string{"image", "pdf"}, Cost: Cost{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.2, Reasoning: 3, InputAudio: 4, OutputAudio: 5},
		},
	}}}}
	data, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeSnapshot(data, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Providers["p"].Models["m"]
	want := in.Providers["p"].Models["m"]
	if got.MaxInput != want.MaxInput || !got.Attachment || !got.StructuredOutput || !got.NoTemperature || got.Interleaved != "reasoning_details" ||
		!got.OpenWeights || got.Knowledge != "2025-01" || got.LastUpdated != "2026-01-02" || got.Cost != want.Cost || len(got.InputModalities) != 2 {
		t.Fatalf("round trip lost fields: %+v", got)
	}
}

func TestParseReadsTemperatureInterleavedAndInputLimit(t *testing.T) {
	var providers []string
	for i := 0; i < minProviders; i++ {
		providers = append(providers, fmt.Sprintf(`"p%d": {"name": "P", "npm": "@ai-sdk/openai-compatible", "api": "https://p.test/v1", "models": {"chat": {"name": "Chat", "tool_call": true,
			"temperature": false, "interleaved": {"field": "reasoning_content"}, "structured_output": true, "limit": {"context": 100000, "input": 90000, "output": 4000}}}}`, i))
	}
	cat, err := Parse([]byte("{"+strings.Join(providers, ",")+"}"), "test")
	if err != nil {
		t.Fatal(err)
	}
	m := cat.Providers["p0"].Models["chat"]
	if !m.NoTemperature || m.Interleaved != "reasoning_content" || !m.StructuredOutput || m.MaxInput != 90000 {
		t.Fatalf("model = %+v", m)
	}
}

func TestInterleavedFieldAcceptsBoolAndObject(t *testing.T) {
	for raw, want := range map[string]string{``: "", `false`: "", `true`: "true", `{"field":"reasoning_details"}`: "reasoning_details", `{}`: ""} {
		if got := interleavedField([]byte(raw)); got != want {
			t.Errorf("interleavedField(%q) = %q, want %q", raw, got, want)
		}
	}
}

// models.dev leaves "decision" models (classifiers answering typed questions) out of its default file; a catalog fetched with
// ?type=all has them, and they must not appear in a chat model picker.
func TestParseLeavesOutSpecialisedModels(t *testing.T) {
	var providers []string
	for i := 0; i < minProviders; i++ {
		providers = append(providers, fmt.Sprintf(`"p%d": {"name": "P", "npm": "@ai-sdk/openai-compatible", "api": "https://p.test/v1", "models": {
			"chat": {"name": "Chat", "tool_call": true},
			"judge": {"name": "Judge", "type": "decision", "tool_call": true}}}`, i))
	}
	cat, err := Parse([]byte("{"+strings.Join(providers, ",")+"}"), "test")
	if err != nil {
		t.Fatal(err)
	}
	models := cat.Providers["p0"].Models
	if _, ok := models["judge"]; ok || len(models) != 1 {
		t.Fatalf("models = %v, want only the chat model", models)
	}
}
