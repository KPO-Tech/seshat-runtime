package registry

import (
	"context"
	"sort"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// SurfaceBuilder is the canonical way to derive the model-visible tool surface
// from the registry. It centralizes filtering and stable ordering so prompt
// assembly, provider requests, and runtime execution all agree on the same set
// of primary tool names.
type SurfaceBuilder struct {
	registry *Registry
}

type SurfaceBuildRequest struct {
	PermissionCheck    types.CanUseToolFn
	CategoryFilter     string
	IncludeReadOnly    bool
	IncludeDestructive bool
	SurfaceProfile     string
	AllowedNames       []string
	BlockedNames       []string
}

func NewSurfaceBuilder(registry *Registry) *SurfaceBuilder {
	return &SurfaceBuilder{registry: registry}
}

func (b *SurfaceBuilder) Build(ctx context.Context, req SurfaceBuildRequest) (*Surface, error) {
	_ = ctx
	// Surface ordering is not cosmetic. Downstream prompt assembly and provider
	// request building rely on these primary names staying stable from one call to
	// the next so cache-safe prompt prefixes do not drift.
	if b == nil || b.registry == nil {
		return &Surface{Tools: []ToolDefinition{}}, nil
	}
	baseSurface, err := b.registry.BuildSurface(&SurfaceContext{
		PermissionCheck:    req.PermissionCheck,
		CategoryFilter:     req.CategoryFilter,
		IncludeReadOnly:    req.IncludeReadOnly,
		IncludeDestructive: req.IncludeDestructive,
		SurfaceProfile:     req.SurfaceProfile,
	})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	blocked := make(map[string]bool)
	for _, name := range req.AllowedNames {
		allowed[name] = true
	}
	for _, name := range req.BlockedNames {
		blocked[name] = true
	}

	tools := make([]ToolDefinition, 0, len(baseSurface.Tools))
	for _, def := range baseSurface.Tools {
		if len(allowed) > 0 && !allowed[def.Name] && !b.isEagerMCPTool(def.Name) {
			continue
		}
		if blocked[def.Name] {
			continue
		}
		tools = append(tools, def)
	}
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return &Surface{Tools: tools}, nil
}

// isEagerMCPTool reports whether a tool comes from an MCP server small enough to be loaded with every request. A host's list of
// tools for its agent names built-in tools only: the tools of a server the person added are not on it, and a server that is small
// is offered without the search tool_search would otherwise need.
func (b *SurfaceBuilder) isEagerMCPTool(name string) bool {
	if b.registry == nil {
		return false
	}
	resolved, ok := b.registry.Resolve(name)
	if !ok || resolved == nil {
		return false
	}
	def := resolved.Definition()
	return def.IsMCP && def.AlwaysLoad
}

func (b *SurfaceBuilder) BuildToolMap(ctx context.Context, req SurfaceBuildRequest) (map[string]Tool, error) {
	surface, err := b.Build(ctx, req)
	if err != nil {
		return nil, err
	}
	result := make(map[string]Tool, len(surface.Tools))
	for _, def := range surface.Tools {
		if resolved, ok := b.registry.Resolve(def.Name); ok {
			result[def.Name] = resolved
		}
	}
	return result, nil
}
