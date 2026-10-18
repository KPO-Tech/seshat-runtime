package memory

import (
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Observation masking: before a conversation is summarized, the cheapest saving is to clear the results of old tool
// calls. The study "The Complexity Trap" (JetBrains, SWE-agent on SWE-bench Verified, five model configurations)
// found it about halves the cost of an agent and solves as many tasks as summarizing with a model, and a hybrid of the
// two costs less than either. A cleared result keeps a short start of itself and the name of the tool, so the agent
// knows what it was and can run the tool again; the model's own words and the user's messages are never touched.

const (
	// maskKeepRecentResults is how many of the most recent tool results are always kept whole.
	maskKeepRecentResults = 6
	// maskMinResultChars is the smallest result worth clearing.
	maskMinResultChars = 800
	// maskMinBatchChars is how much has to be freed for masking to run at all. Masking changes the start of the
	// conversation, which a provider caches; doing it in batches rather than one result at a time keeps that cache
	// valid in between.
	maskMinBatchChars = 20_000
	// maskPreviewChars is how much of a cleared result is kept as a reminder.
	maskPreviewChars = 160

	maskedPrefix = "[Result cleared to save context"
)

// MaskResult says what a masking pass did.
type MaskResult struct {
	Cleared    int `json:"cleared"`
	FreedChars int `json:"freed_chars"`
}

// maskPosition locates a block in a conversation.
type maskPosition struct{ msg, block int }

// MaskOldObservations clears the large results of old tool calls, keeping the most recent maskKeepRecentResults whole.
// It returns messages unchanged when what could be cleared is under maskMinBatchChars; otherwise a copy with the
// results cleared. The tool call and its result stay paired.
func MaskOldObservations(messages []types.Message) ([]types.Message, MaskResult) {
	toolName := map[string]string{}
	var results []maskPosition
	for i, m := range messages {
		for j, b := range m.Content {
			switch c := b.(type) {
			case types.ToolUseContent:
				toolName[c.ID] = c.Name
			case types.ToolResultContent:
				results = append(results, maskPosition{i, j})
			}
		}
	}
	if len(results) <= maskKeepRecentResults {
		return messages, MaskResult{}
	}

	var eligible []maskPosition
	freed := 0
	for _, p := range results[:len(results)-maskKeepRecentResults] {
		r := messages[p.msg].Content[p.block].(types.ToolResultContent)
		if len(r.Content) < maskMinResultChars || strings.HasPrefix(r.Content, maskedPrefix) {
			continue
		}
		eligible = append(eligible, p)
		freed += len(r.Content)
	}
	if freed < maskMinBatchChars {
		return messages, MaskResult{}
	}

	out := types.CloneMessages(messages)
	kept := 0
	copied := map[int]bool{}
	for _, p := range eligible {
		// CloneMessages shares the content slices with its input: copy one before writing into it.
		if !copied[p.msg] {
			out[p.msg].Content = append([]types.ContentBlock(nil), out[p.msg].Content...)
			copied[p.msg] = true
		}
		r := out[p.msg].Content[p.block].(types.ToolResultContent)
		r.Content = maskedResult(toolName[r.ToolUseID], r.Content)
		kept += len(r.Content)
		out[p.msg].Content[p.block] = r
	}
	return out, MaskResult{Cleared: len(eligible), FreedChars: freed - kept}
}

// maskedResult is what stands in for a cleared result: the tool, the size, and the start of it.
func maskedResult(tool, content string) string {
	if tool == "" {
		tool = "a tool"
	}
	preview := strings.Join(strings.Fields(clipForSummary(content, maskPreviewChars)), " ")
	return fmt.Sprintf("%s: %s returned %d characters. It started with %q. Run the tool again if you need it.]", maskedPrefix, tool, len(content), preview)
}
