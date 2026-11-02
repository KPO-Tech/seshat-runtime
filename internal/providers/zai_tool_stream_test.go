package providers

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func streamingBody(t *testing.T, provider types.APIProvider, stream bool, withTools bool) string {
	t.Helper()
	c := NewClientWithConfig("k", &Config{Provider: provider, BaseURL: "http://localhost"})
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: provider, Model: "m"},
		Messages:  []types.Message{types.UserMessage("m1", "hi")},
		MaxTokens: 10,
		Stream:    stream,
	}
	if withTools {
		req.Tools = []types.APIToolDefinition{{Name: "write_file", Description: "Write a file", InputSchema: schema.FromMap(map[string]any{"type": "object"})}}
	}
	r, err := c.buildOpenAIRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

// Without it z.ai holds the whole tool call until it is written: 68.7 s of silence for a file of about 1 000 words, measured on glm-4.7.
func TestZAiIsAskedToStreamToolArguments(t *testing.T) {
	if got := streamingBody(t, types.APIProviderZAi, true, true); !strings.Contains(got, `"tool_stream":true`) {
		t.Errorf("a streamed z.ai request with tools must ask for the arguments as they are written: %s", got)
	}
}

func TestToolStreamIsSentOnlyWhereItMeansSomething(t *testing.T) {
	if got := streamingBody(t, types.APIProviderZAi, true, false); strings.Contains(got, "tool_stream") {
		t.Errorf("no tools, nothing to stream: %s", got)
	}
	if got := streamingBody(t, types.APIProviderZAi, false, true); strings.Contains(got, "tool_stream") {
		t.Errorf("not a streamed request: %s", got)
	}
	// Providers that do not know the parameter must not receive it: some reject an unknown one.
	for _, p := range []types.APIProvider{types.APIProviderOpenAI, types.APIProviderOpenRouter, types.APIProviderMistral} {
		if got := streamingBody(t, p, true, true); strings.Contains(got, "tool_stream") {
			t.Errorf("%s has no such parameter: %s", p, got)
		}
	}
}

// A real answer of z.ai (glm-4.7, tool_stream on): the arguments of the call come as 19 small fragments.
func TestAToolCallStreamedInFragmentsByZAiIsAssembled(t *testing.T) {
	file, err := os.Open("testdata/zai_tool_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var inputFragments int
	result, err := parseOpenAISSEStream(context.Background(), file, types.ModelIdentifier{Provider: types.APIProviderZAi, Model: "glm-4.7"}, func(chunk types.APIResponseChunk) {
		if chunk.DeltaType == "input_json_delta" {
			inputFragments++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if inputFragments < 5 {
		t.Errorf("the fragments must reach the client as they come, got %d", inputFragments)
	}
	var found bool
	for _, block := range result.Response.Content {
		use, ok := block.(types.ToolUseContent)
		if !ok || use.Name != "write_file" {
			continue
		}
		found = true
		if use.Input["path"] != "a.txt" || use.Input["content"] != "hello world" {
			t.Errorf("the assembled input is %v", use.Input)
		}
	}
	if !found {
		t.Errorf("no write_file call in %+v", result.Response.Content)
	}
}
