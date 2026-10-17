package toolsearch

import (
	"context"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

type scopeKey struct{}

// WithScope returns a context in which tool_search can also find the named tools, besides the ones the registry flags as
// deferred (MCP tools, skills). It is how a host gives its main agent a short list of tools that are always loaded and a
// longer one that is only found on demand, without flagging those tools for every agent: a sub-agent never gets this scope,
// so it cannot find through tool_search what its own tool list leaves out.
func WithScope(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, append([]string(nil), names...))
}

// ScopeNames returns the names set by WithScope, or nil.
func ScopeNames(ctx context.Context) []string {
	names, _ := ctx.Value(scopeKey{}).([]string)
	return names
}

// withScopedTools adds to tools the enabled registry tools named by the scope of ctx, without duplicates.
func withScopedTools(ctx context.Context, reg *registry.Registry, tools []contract.Tool) []contract.Tool {
	scope := ScopeNames(ctx)
	if len(scope) == 0 || reg == nil {
		return tools
	}
	have := make(map[string]bool, len(tools))
	for _, t := range tools {
		have[t.Definition().Name] = true
	}
	out := append([]contract.Tool(nil), tools...)
	for _, name := range scope {
		if have[name] {
			continue
		}
		resolved, ok := reg.Resolve(name)
		if !ok || !resolved.IsEnabled() {
			continue
		}
		have[name] = true
		out = append(out, resolved)
	}
	return out
}
