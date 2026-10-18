// Package contextusage holds the latest size of each session's context window and the tool that lets the agent read it.
package contextusage

import (
	"context"
	"fmt"
	"sync"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ToolName is the name of the tool that reports the remaining context.
const ToolName = "get_context_remaining"

const (
	searchHint  = "how much of the context window is left, tokens used, when the conversation will be compacted"
	description = `Report how full your context window is.

Returns the tokens used by the next request (system prompt, tool definitions and conversation), the window of the model, how many tokens remain, and the size at which the conversation is compacted automatically.

Use it before starting a long read or a large batch of tool calls, to decide whether to do the work in smaller pieces or to ask the user to compact first. The figure is exact when it starts from what the provider reported for the last request, and an estimate otherwise (` + "`source`" + ` says which).`
)

// Snapshot is where a session's context window stands.
type Snapshot struct {
	UsedTokens          int    `json:"used_tokens"`
	WindowTokens        int    `json:"window_tokens"`
	AutoCompactAtTokens int    `json:"auto_compact_at_tokens"`
	BlockingLimitTokens int    `json:"blocking_limit_tokens"`
	Source              string `json:"source"`
}

// Remaining is the tokens left before the window is full.
func (s Snapshot) Remaining() int {
	if r := s.WindowTokens - s.UsedTokens; r > 0 {
		return r
	}
	return 0
}

// UntilCompaction is the tokens left before the conversation is compacted automatically.
func (s Snapshot) UntilCompaction() int {
	if r := s.AutoCompactAtTokens - s.UsedTokens; r > 0 {
		return r
	}
	return 0
}

var snapshots sync.Map // types.SessionID -> Snapshot

// Record stores the latest snapshot of a session.
func Record(id types.SessionID, s Snapshot) { snapshots.Store(id, s) }

// Forget drops a session's snapshot, when the session ends.
func Forget(id types.SessionID) { snapshots.Delete(id) }

// Get returns the latest snapshot of a session.
func Get(id types.SessionID) (Snapshot, bool) {
	v, ok := snapshots.Load(id)
	if !ok {
		return Snapshot{}, false
	}
	return v.(Snapshot), true
}

// Tool implements get_context_remaining.
type Tool struct{}

// NewTool creates the get_context_remaining tool.
func NewTool() *Tool { return &Tool{} }

// Definition returns the tool definition.
func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "GetContextRemaining",
		SearchHint:  searchHint,
		Description: description,
		Category:    "context",
		InputSchema: schema.FromMap(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		RequiresPermission: false,
	}
}

func (t *Tool) IsEnabled() bool                         { return true }
func (t *Tool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *Tool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *Tool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *Tool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *Tool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *Tool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.AllowWithInput("", in)
}
func (t *Tool) Description(_ context.Context) (string, error) { return description, nil }

// Call reports the session's latest snapshot.
func (t *Tool) Call(_ context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	snap, ok := Get(input.ToolContextValue().SessionID)
	if !ok {
		res := tool.NewJSONResult(map[string]any{"known": false})
		res.Content = "The size of the context is not known yet: no request has been sized in this session."
		return res, nil
	}
	res := tool.NewJSONResult(map[string]any{
		"known":                   true,
		"used_tokens":             snap.UsedTokens,
		"window_tokens":           snap.WindowTokens,
		"remaining_tokens":        snap.Remaining(),
		"auto_compact_at_tokens":  snap.AutoCompactAtTokens,
		"tokens_until_compaction": snap.UntilCompaction(),
		"source":                  snap.Source,
	})
	res.Content = fmt.Sprintf(
		"Context: %d of %d tokens used (%d remaining). The conversation is compacted automatically at %d tokens (%d from now). Figure: %s.",
		snap.UsedTokens, snap.WindowTokens, snap.Remaining(), snap.AutoCompactAtTokens, snap.UntilCompaction(), snap.Source)
	return res, nil
}
