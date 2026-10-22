package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func toolResultMessage(content string) types.Message {
	message := types.UserMessage("m", "")
	message.Content = []types.ContentBlock{types.ToolResultContent{ToolUseID: "t", Content: content}}
	return message
}

func TestWithoutBinaryPayloadsLeavesTheImageOutOfWhatTheModelReads(t *testing.T) {
	image := strings.Repeat("QUJD", 50000)
	content := `{"provider":"openai","model":"m","image_base64":"` + image + `","mime_type":"image/png","revised_prompt":"a cat"}`
	messages := []types.Message{toolResultMessage(content)}

	got := withoutBinaryPayloads(messages)

	text := got[0].Content[0].(types.ToolResultContent).Content
	if strings.Contains(text, image[:100]) {
		t.Fatal("the image is still in what the model reads")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("what the model reads is still JSON: %v (%s)", err, text)
	}
	if parsed["revised_prompt"] != "a cat" || parsed["mime_type"] != "image/png" {
		t.Fatalf("the other fields are kept, got %v", parsed)
	}
	if kept := messages[0].Content[0].(types.ToolResultContent).Content; kept != content {
		t.Fatal("the stored message must keep the image: the interface draws it from there")
	}
}

func TestWithoutBinaryPayloadsKeepsOrdinaryResults(t *testing.T) {
	messages := []types.Message{
		toolResultMessage(strings.Repeat("a line of output\n", 500)),
		toolResultMessage(`{"image_base64":"short"}`),
	}
	got := withoutBinaryPayloads(messages)
	if &got[0] != &messages[0] {
		t.Fatal("messages without a long base64 field are returned as they were, not copied")
	}
}

func TestSanitizeToolResultDoesNotTakeAJSONDocumentForAnInjection(t *testing.T) {
	content := `{"image_base64":"` + strings.Repeat("QUJD", 5000) + `"}`
	if _, detected, reason := sanitizeToolResult(content); detected {
		t.Fatalf("a JSON document with an image is not an injection, got %q", reason)
	}
	if _, detected, _ := sanitizeToolResult(strings.Repeat("word ", 1000)); !detected {
		t.Fatal("a long single-line prose is still looked at")
	}
}
