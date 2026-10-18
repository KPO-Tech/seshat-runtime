package toolsearch

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// SearchMatch is one result returned by tool_search.
// Mirrors Codex's LoadableToolSpec (with defer_loading) but includes
// the full Seshat definition so the model can inspect the schema immediately.
type SearchMatch struct {
	Name        string  `json:"name"`
	Namespace   string  `json:"namespace"`
	Score       float64 `json:"score"`
	Description string  `json:"description"`
	SearchHint  string  `json:"search_hint,omitempty"`
	Category    string  `json:"category,omitempty"`
	IsDeferred  bool    `json:"is_deferred"`
	IsMCP       bool    `json:"is_mcp"`
}

// ToolSearchOutput is the JSON payload returned by tool_search.
type ToolSearchOutput struct {
	Query      string        `json:"query"`
	Matches    []SearchMatch `json:"matches"`
	TotalTools int           `json:"total_tools"`
	Scored     int           `json:"scored"`
}

// ToolSearchTool implements the BM25-powered tool_search tool.
// Mirrors Codex's ToolSearchHandler (core/src/tools/handlers/tool_search.rs).
type ToolSearchTool struct {
	registry *registry.Registry

	// BM25 index — built lazily on first Call, rebuilt when registry changes.
	mu      sync.Mutex
	engine  *BM25Engine
	indexed []contract.Tool // snapshot used to build the current index
}

func NewToolSearchTool(reg *registry.Registry) *ToolSearchTool {
	return &ToolSearchTool{registry: reg}
}

func (t *ToolSearchTool) Definition() contract.Definition {
	return contract.Definition{
		Name:        ToolSearchToolName,
		DisplayName: "ToolSearch",
		SearchHint:  "search deferred tools by BM25 keyword ranking",
		Description: ToolSearchDescription,
		Category:    "meta",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query for deferred tools. Use 'select:Name1,Name2' for direct lookup, or free-text keywords for BM25 ranking.",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": fmt.Sprintf("Maximum number of results to return (default: %d).", DefaultMaxResults),
					"default":     DefaultMaxResults,
				},
			},
			"required": []string{"query"},
		}),
		IsReadOnly:        true,
		IsConcurrencySafe: true,
		IsMCP:             false,
		AlwaysLoad:        true,
	}
}

func (t *ToolSearchTool) Call(ctx context.Context, input contract.CallInput, _ types.CanUseToolFn) (contract.CallResult, error) {
	if input.Parsed == nil {
		return contract.NewErrorResult(fmt.Errorf("no input provided")), nil
	}

	query, _ := input.Parsed["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return contract.NewErrorResult(fmt.Errorf("query is required")), nil
	}

	limit := DefaultMaxResults
	if v, ok := input.Parsed["max_results"].(float64); ok && v >= 1 {
		limit = int(v)
	}

	if t.registry == nil {
		return contract.NewErrorResult(fmt.Errorf("tool registry not available")), nil
	}

	deferred := withScopedTools(ctx, t.registry, t.registry.ListDeferred())
	if len(deferred) == 0 {
		res := contract.NewTextResult("No deferred tools available. All registered tools are already loaded.")
		return res, nil
	}

	// Handle select: prefix — direct lookup by exact name (bypass BM25).
	if strings.HasPrefix(strings.ToLower(query), "select:") {
		matches := t.selectByName(query, deferred, limit)
		out := ToolSearchOutput{
			Query:      query,
			Matches:    matches,
			TotalTools: len(deferred),
			Scored:     len(matches),
		}
		res := contract.NewJSONResult(out)
		res.Content = formatSearchResult(out)
		return res, nil
	}

	// BM25 search.
	engine := t.getOrBuildEngine(deferred)
	hits := engine.Search(query, limit)

	matches := make([]SearchMatch, 0, len(hits))
	for _, hit := range hits {
		if hit.ID < 0 || hit.ID >= len(deferred) {
			continue
		}
		def := deferred[hit.ID].Definition()
		matches = append(matches, SearchMatch{
			Name:        def.Name,
			Namespace:   toolNamespace(def),
			Score:       hit.Score,
			Description: def.Description,
			SearchHint:  def.SearchHint,
			Category:    def.Category,
			IsDeferred:  def.ShouldDefer || def.IsMCP,
			IsMCP:       def.IsMCP,
		})
	}

	out := ToolSearchOutput{
		Query:      query,
		Matches:    matches,
		TotalTools: len(deferred),
		Scored:     len(matches),
	}

	if len(matches) == 0 {
		res := contract.NewTextResult(fmt.Sprintf(
			"No deferred tools matched %q (searched %d tools). Try broader keywords.", query, len(deferred)))
		return res, nil
	}

	res := contract.NewJSONResult(out)
	res.Content = formatSearchResult(out)
	return res, nil
}

// ─── Index management ─────────────────────────────────────────────────────────

// getOrBuildEngine returns the current BM25 engine, rebuilding it if the
// deferred tool set has changed since the last index build.
func (t *ToolSearchTool) getOrBuildEngine(tools []contract.Tool) *BM25Engine {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.engine != nil && sameToolNames(t.indexed, tools) {
		// Rebuild when the set changed: a tool registered or unregistered, or another scope (see WithScope).
		return t.engine
	}

	texts := make([]string, len(tools))
	for i, tool := range tools {
		texts[i] = buildSearchText(tool.Definition())
	}

	t.engine = NewBM25Engine(texts)
	t.indexed = tools
	return t.engine
}

// sameToolNames reports whether both lists hold the same tools in the same order.
func sameToolNames(a, b []contract.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Definition().Name != b[i].Definition().Name {
			return false
		}
	}
	return true
}

// ─── select: shortcut ────────────────────────────────────────────────────────

// selectByName handles "select:Name1,Name2" queries — exact name lookup, no ranking.
// Mirrors Codex's coalesce_loadable_tool_specs for explicit tool loading.
func (t *ToolSearchTool) selectByName(query string, tools []contract.Tool, limit int) []SearchMatch {
	names := strings.Split(strings.TrimPrefix(strings.ToLower(query), "select:"), ",")
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[strings.TrimSpace(n)] = true
	}

	var matches []SearchMatch
	for _, tool := range tools {
		def := tool.Definition()
		if nameSet[strings.ToLower(def.Name)] {
			matches = append(matches, SearchMatch{
				Name:        def.Name,
				Namespace:   toolNamespace(def),
				Score:       1.0,
				Description: def.Description,
				SearchHint:  def.SearchHint,
				Category:    def.Category,
				IsDeferred:  def.ShouldDefer || def.IsMCP,
				IsMCP:       def.IsMCP,
			})
		}
		if len(matches) >= limit {
			break
		}
	}
	return matches
}

// ─── Formatting ───────────────────────────────────────────────────────────────

func formatSearchResult(out ToolSearchOutput) string {
	if len(out.Matches) == 0 {
		return fmt.Sprintf("No tools matched %q", out.Query)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d tool(s) matched %q (of %d deferred):\n", len(out.Matches), out.Query, out.TotalTools)
	for _, m := range out.Matches {
		fmt.Fprintf(&sb, "\n  %s", m.Name)
		if m.Namespace != "" && m.Namespace != "builtin" {
			fmt.Fprintf(&sb, " [%s]", m.Namespace)
		}
		if m.SearchHint != "" {
			fmt.Fprintf(&sb, " — %s", truncateText(m.SearchHint, 80))
		} else if m.Description != "" {
			fmt.Fprintf(&sb, " — %s", truncateText(m.Description, 80))
		}
	}
	return sb.String()
}

func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	// Find first sentence end
	if idx := strings.IndexAny(s, ".\n"); idx > 0 && idx < max {
		return s[:idx+1]
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ─── Tool interface boilerplate ───────────────────────────────────────────────

func (t *ToolSearchTool) Description(_ context.Context) (string, error) {
	return ToolSearchDescription, nil
}
func (t *ToolSearchTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *ToolSearchTool) CheckPermissions(_ context.Context, in map[string]any, _ contract.ToolUseContext) types.PermissionResult {
	return types.PermissionResult{Behavior: types.PermissionBehaviorPassthrough}
}
func (t *ToolSearchTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *ToolSearchTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *ToolSearchTool) IsEnabled() bool {
	// The environment variable switches the tool on, and so does the client's EnableToolSearch option, which hides the deferred
	// tools: without this the model would be told to search for tools by a tool it does not have.
	return IsToolSearchEnabledOptimistic() || t.registry.ToolSearchEnabled()
}
func (t *ToolSearchTool) FormatResult(data any) string {
	if out, ok := data.(ToolSearchOutput); ok {
		return formatSearchResult(out)
	}
	return fmt.Sprintf("%v", data)
}
func (t *ToolSearchTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
