package registry

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Registry manages tool registration and discovery
type Registry struct {
	// tools are the registered tools by name
	tools map[string]Tool

	// categories groups tools by category
	categories map[string][]Tool

	// mu protects concurrent access
	mu sync.RWMutex

	// config is the registry configuration
	config *RegistryConfig
}

// RegistryConfig represents registry configuration
type RegistryConfig struct {
	// AllowDuplicates controls whether duplicate tool names are allowed
	AllowDuplicates bool `json:"allow_duplicates"`

	// CaseSensitive controls whether tool names are case-sensitive
	CaseSensitive bool `json:"case_sensitive"`

	// EnableToolSearch keeps deferred tools out of the initial model-visible
	// surface (BuildSurface) so the model must discover them through
	// ToolSearch first. True enables it regardless of the ENABLE_TOOL_SEARCH
	// environment variable; false leaves that variable as the decider, so it
	// still works for a registry built without going through pkg/sdk.
	EnableToolSearch bool `json:"enable_tool_search"`
}

// DefaultRegistryConfig returns default registry configuration
func DefaultRegistryConfig() *RegistryConfig {
	return &RegistryConfig{
		AllowDuplicates: false,
		CaseSensitive:   true,
	}
}

// NewRegistry creates a new tool registry
func NewRegistry() *Registry {
	return &Registry{
		tools:      make(map[string]Tool),
		categories: make(map[string][]Tool),
		config:     DefaultRegistryConfig(),
	}
}

// NewRegistryWithConfig creates a new registry with custom configuration
func NewRegistryWithConfig(config *RegistryConfig) *Registry {
	if config == nil {
		config = DefaultRegistryConfig()
	}

	return &Registry{
		tools:      make(map[string]Tool),
		categories: make(map[string][]Tool),
		config:     config,
	}
}

// Register registers a tool in the registry
func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	def := tool.Definition()
	name := r.normalizeName(def.Name)

	// Check for duplicates
	if !r.config.AllowDuplicates {
		if _, exists := r.tools[name]; exists {
			return fmt.Errorf("tool '%s' already registered", name)
		}
	}

	// Register tool
	r.tools[name] = tool

	// Register aliases
	for _, alias := range def.Aliases {
		aliasName := r.normalizeName(alias)
		r.tools[aliasName] = tool
	}

	// Add to category
	if def.Category != "" {
		r.categories[def.Category] = append(r.categories[def.Category], tool)
	}

	return nil
}

// RegisterBatch registers multiple tools
func (r *Registry) RegisterBatch(tools []Tool) error {
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			return fmt.Errorf("failed to register tool '%s': %w", tool.Definition().Name, err)
		}
	}
	return nil
}

// Unregister removes a tool from the registry
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name = r.normalizeName(name)

	tool, exists := r.tools[name]
	if !exists {
		return fmt.Errorf("tool '%s' not found", name)
	}

	// Remove tool and its aliases
	def := tool.Definition()
	delete(r.tools, name)

	for _, alias := range def.Aliases {
		aliasName := r.normalizeName(alias)
		delete(r.tools, aliasName)
	}

	// Remove from category
	if def.Category != "" {
		category := r.categories[def.Category]
		for i, t := range category {
			if t.Definition().Name == def.Name {
				// Remove from slice
				r.categories[def.Category] = append(category[:i], category[i+1:]...)
				break
			}
		}
	}

	return nil
}

// Resolve resolves a tool by name
func (r *Registry) Resolve(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name = r.normalizeName(name)
	tool, exists := r.tools[name]
	return tool, exists
}

// Get retrieves a tool by name.
// Kept as a compatibility alias for older call sites.
func (r *Registry) Get(name string) (Tool, bool) {
	return r.Resolve(name)
}

// List returns all registered tools
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Deduplicate tools (due to aliases)
	seen := make(map[string]bool)
	tools := make([]Tool, 0)

	for _, tool := range r.tools {
		name := tool.Definition().Name
		if !seen[name] {
			seen[name] = true
			tools = append(tools, tool)
		}
	}

	return tools
}

// IsDeferred checks if a tool should be deferred based on its definition.
func IsDeferred(tool Tool) bool {
	def := tool.Definition()
	if def.AlwaysLoad {
		return false
	}
	if def.IsMCP {
		return true
	}
	if def.Name == "tool_search" {
		return false
	}
	return def.ShouldDefer
}

// ListNonDeferred returns tools that should NOT be deferred.
func (r *Registry) ListNonDeferred() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	tools := make([]Tool, 0)
	for _, tool := range r.tools {
		def := tool.Definition()
		if seen[def.Name] {
			continue
		}
		seen[def.Name] = true
		if IsDeferred(tool) {
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

// ListDeferred returns tools that should be deferred (loaded via ToolSearch).
func (r *Registry) ListDeferred() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	tools := make([]Tool, 0)
	for _, tool := range r.tools {
		def := tool.Definition()
		if seen[def.Name] {
			continue
		}
		seen[def.Name] = true
		if IsDeferred(tool) {
			tools = append(tools, tool)
		}
	}
	return tools
}

// ListByCategory returns tools in a specific category
func (r *Registry) ListByCategory(category string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if tools, ok := r.categories[category]; ok {
		return tools
	}

	return []Tool{}
}

// ListCategories returns all categories
func (r *Registry) ListCategories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	categories := make([]string, 0, len(r.categories))
	for category := range r.categories {
		categories = append(categories, category)
	}

	return categories
}

// Search searches for tools by name or description
func (r *Registry) Search(query string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	query = strings.ToLower(query)
	results := make([]Tool, 0)

	seen := make(map[string]bool)

	for name, tool := range r.tools {
		def := tool.Definition()

		// Skip if we've already seen this tool (due to aliases)
		if seen[def.Name] {
			continue
		}

		// Check if query matches name or description
		if strings.Contains(strings.ToLower(name), query) ||
			strings.Contains(strings.ToLower(def.Name), query) ||
			strings.Contains(strings.ToLower(def.Description), query) {

			seen[def.Name] = true
			results = append(results, tool)
		}
	}

	return results
}

// Count returns the number of unique tools
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Count unique tools (excluding aliases)
	seen := make(map[string]bool)
	for _, tool := range r.tools {
		seen[tool.Definition().Name] = true
	}

	return len(seen)
}

// Clear removes all tools from the registry
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]Tool)
	r.categories = make(map[string][]Tool)
}

// BuildSurface builds a tool surface for a given context
func (r *Registry) BuildSurface(ctx *SurfaceContext) (*Surface, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	surface := &Surface{
		Tools: make([]ToolDefinition, 0),
	}

	seen := make(map[string]bool)

	// Filter tools based on context
	for _, tool := range r.tools {
		def := tool.Definition()

		// Skip duplicates so aliases never produce extra surface entries.
		if seen[def.Name] {
			continue
		}
		seen[def.Name] = true

		// Disabled tools are not part of the model-visible surface.
		if !tool.IsEnabled() {
			continue
		}

		// When ToolSearch is active, deferred tools must stay out of the initial
		// model-visible surface and be discovered through ToolSearch instead.
		if r.isToolSearchSurfaceEnabled() && IsDeferred(tool) {
			continue
		}

		// Check permissions filter
		if ctx != nil && ctx.PermissionCheck != nil {
			// Create a dummy input to check permissions
			dummyInput := map[string]any{
				"__permission_check__": true,
			}

			result := ctx.PermissionCheck(context.Background(), types.ToolPermissionRequest{
				ToolName:  def.Name,
				ToolInput: dummyInput,
			})
			if result.IsDenied() {
				continue // Skip tools that are denied
			}
		}

		// Check category filter
		if ctx != nil && ctx.CategoryFilter != "" {
			if def.Category != ctx.CategoryFilter {
				continue
			}
		}

		if ctx != nil && !ctx.IncludeReadOnly && def.IsReadOnly {
			continue
		}
		if ctx != nil && !ctx.IncludeDestructive && def.IsDestructive {
			continue
		}

		surfaceDef := ToolDefinition{
			Name:        def.Name,
			Description: def.Description,
			Category:    def.Category,
			InputSchema: def.InputSchema,
			Metadata:    def.Metadata,
		}
		if ctx != nil && !visibleInSurfaceProfile(surfaceDef, ctx.SurfaceProfile) {
			continue
		}

		// Add tool to surface
		surface.Tools = append(surface.Tools, surfaceDef)
	}

	return surface, nil
}

// ToolSearchEnabled reports whether deferred tools are kept out of the model-visible surface: the client's EnableToolSearch
// option, or the ENABLE_TOOL_SEARCH environment variable. The tool_search tool follows it, so a host that defers tools also
// gives the model the way to find them.
func (r *Registry) ToolSearchEnabled() bool {
	return r != nil && r.isToolSearchSurfaceEnabled()
}

func (r *Registry) isToolSearchSurfaceEnabled() bool {
	if r.config != nil && r.config.EnableToolSearch {
		return true
	}
	value := strings.TrimSpace(os.Getenv("ENABLE_TOOL_SEARCH"))
	if value == "" || value == "false" {
		return false
	}
	return true
}

// normalizeName normalizes a tool name based on registry config
func (r *Registry) normalizeName(name string) string {
	if !r.config.CaseSensitive {
		return strings.ToLower(name)
	}
	return name
}

// SurfaceContext provides context for building a tool surface
type SurfaceContext struct {
	// PermissionCheck is the permission check function
	PermissionCheck types.CanUseToolFn

	// SurfaceProfile selects the intended tool surface, e.g. mono_run or subagent.
	SurfaceProfile string

	// CategoryFilter filters tools by category
	CategoryFilter string

	// IncludeReadOnly controls whether to include read-only tools
	IncludeReadOnly bool

	// IncludeDestructive controls whether to include destructive tools
	IncludeDestructive bool
}

// Surface represents a collection of tool definitions
type Surface struct {
	// Tools are the tool definitions
	Tools []ToolDefinition `json:"tools"`
}

// ToolDefinition represents a simplified tool definition for the surface
type ToolDefinition struct {
	// Name is the tool name
	Name string `json:"name"`

	// Description explains what the tool does
	Description string `json:"description"`

	// Category groups related tools
	Category string `json:"category,omitempty"`

	// InputSchema is the JSON schema for the tool input
	InputSchema schema.JSONSchema `json:"input_schema,omitempty"`

	// Metadata contains additional information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// GetToolNames returns all tool names (excluding aliases)
func (r *Registry) GetToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	names := make([]string, 0)

	for _, tool := range r.tools {
		if !tool.IsEnabled() {
			continue
		}
		name := tool.Definition().Name
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	sort.Strings(names)
	return names
}

// HasTool checks if a tool is registered
func (r *Registry) HasTool(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name = r.normalizeName(name)
	_, exists := r.tools[name]
	return exists
}

// GetToolsByAlias returns tools that have a specific alias
func (r *Registry) GetToolsByAlias(alias string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	alias = r.normalizeName(alias)
	tools := make([]Tool, 0)

	for name, tool := range r.tools {
		if name == alias {
			// Check if this is an alias (not the primary name)
			if tool.Definition().Name != alias {
				tools = append(tools, tool)
			}
		}
	}

	return tools
}

// Merge merges another registry into this one
func (r *Registry) Merge(other *Registry) error {
	if other == nil {
		return nil
	}

	tools := other.List()
	return r.RegisterBatch(tools)
}

// Clone creates a copy of the registry
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clone := NewRegistryWithConfig(r.config)

	// Copy all tools
	for name, tool := range r.tools {
		clone.tools[name] = tool
	}

	// Copy categories
	for category, tools := range r.categories {
		clone.categories[category] = append([]Tool{}, tools...)
	}

	return clone
}
