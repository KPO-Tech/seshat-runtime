package agent

import (
	"context"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

type toolPolicyKey struct{}

// WithToolPolicy returns a context carrying the tool policy of the agent that
// runs under it. Any sub-agent that agent spawns inherits it (see
// EffectiveToolPolicy), which is what stops a child from regaining a tool its
// parent lost.
func WithToolPolicy(ctx context.Context, policy tool.ToolPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolPolicyKey{}, policy)
}

// ToolPolicyFromContext returns the tool policy of the running agent; the zero
// policy (no restriction) when ctx carries none, e.g. for a top-level session.
func ToolPolicyFromContext(ctx context.Context) tool.ToolPolicy {
	if ctx == nil {
		return tool.ToolPolicy{}
	}
	policy, _ := ctx.Value(toolPolicyKey{}).(tool.ToolPolicy)
	return policy
}

// EffectiveToolPolicy is the policy a sub-agent of type def runs under:
//
//   - everything its parent was already restricted by (inherited);
//   - allow: the caller's filter when non-nil (the model's tools:[...] already
//     intersected with the definition by the agent tool; a non-nil empty filter
//     means no tool at all), otherwise the definition's own Tools;
//   - deny: the definition's DisallowedTools. Deny wins over every allow list.
//
// def may be nil (only inherited and filter apply).
func EffectiveToolPolicy(inherited tool.ToolPolicy, def *AgentDefinition, filter []string) tool.ToolPolicy {
	allow := filter
	var deny []string
	if def != nil {
		if allow == nil {
			allow = def.GetToolPatterns()
		}
		deny = def.DisallowedTools
	}
	return inherited.Narrow(allow, deny)
}
