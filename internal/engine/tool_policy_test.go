package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	toolsearch "github.com/KPO-Tech/seshat-runtime/internal/tools/special/tool_search"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func okHandler(context.Context, registry.CallInput, registry.ToolUseContext) (registry.CallResult, error) {
	return registry.NewTextResult("ok"), nil
}

func policyTestSession(t *testing.T, names ...string) *Session {
	t.Helper()
	reg := registry.NewRegistry()
	for _, name := range names {
		built, err := registry.NewBuilder(name).WithAliases(strings.ToUpper(name)).WithDescription(name).WithHandler(okHandler).Build()
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(built); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultConfig()
	cfg.PermissionMode = types.PermissionModeBypass
	eng := NewEngine(nil, execution.NewOrchestrator(), nil, prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()), reg, nil, cfg, nil, nil)
	session, err := eng.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func hasName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func TestSessionSetToolPolicy(t *testing.T) {
	tests := []struct {
		name     string
		policy   registry.ToolPolicy
		wantKept []string
		wantGone []string
	}{
		{"deny removes the tool", registry.ToolPolicy{Deny: []string{"bravo"}}, []string{"alpha"}, []string{"bravo"}},
		{"deny by alias name removes the canonical tool too", registry.ToolPolicy{Deny: []string{"BRAVO"}}, []string{"alpha"}, []string{"bravo"}},
		{"deny wildcard", registry.ToolPolicy{Deny: []string{"br*"}}, []string{"alpha"}, []string{"bravo"}},
		{"allow keeps only the listed tool", registry.ToolPolicy{Allow: [][]string{{"alpha"}}}, []string{"alpha"}, []string{"bravo"}},
		{"deny beats allow", registry.ToolPolicy{Allow: [][]string{{"*"}}, Deny: []string{"alpha"}}, []string{"bravo"}, []string{"alpha"}},
		{"empty non-nil allow keeps nothing", registry.ToolPolicy{Allow: [][]string{{}}}, nil, []string{"alpha", "bravo"}},
		{"zero policy keeps everything", registry.ToolPolicy{}, []string{"alpha", "bravo"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := policyTestSession(t, "alpha", "bravo")
			defer session.Close()
			session.SetToolPolicy(tt.policy)
			names := session.GetToolNames()
			for _, n := range tt.wantKept {
				if !hasName(names, n) {
					t.Errorf("%q should survive, got %v", n, names)
				}
			}
			for _, n := range tt.wantGone {
				if hasName(names, n) {
					t.Errorf("%q should be gone, got %v", n, names)
				}
			}
		})
	}
}

func TestSessionToolPolicyOnlyNarrows(t *testing.T) {
	session := policyTestSession(t, "alpha", "bravo")
	defer session.Close()

	session.SetToolPolicy(registry.ToolPolicy{Deny: []string{"alpha"}})
	// A later, looser policy cannot bring the tool back.
	session.SetToolPolicy(registry.ToolPolicy{})
	session.SetToolPolicy(registry.ToolPolicy{Allow: [][]string{{"*"}}})
	if hasName(session.GetToolNames(), "alpha") {
		t.Fatal("a denied tool came back after a looser policy")
	}

	again, err := registry.NewBuilder("alpha").WithDescription("again").WithHandler(okHandler).Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RegisterTool(again); err == nil {
		t.Fatal("RegisterTool accepted a tool the policy denies")
	}
}

func TestEffectiveToolSurface_PolicyFiltersDiscoveredDeferred(t *testing.T) {
	reg := registry.NewRegistry()
	for _, name := range []string{"deferred_ok", "deferred_denied"} {
		built, err := registry.NewBuilder(name).WithDescription(name).WithHandler(okHandler).Build()
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(built); err != nil {
			t.Fatal(err)
		}
	}
	state := &SessionState{
		Tools:              map[string]registry.Tool{},
		DiscoveredDeferred: []string{"deferred_denied", "deferred_ok"},
		ToolPolicy:         registry.ToolPolicy{Deny: []string{"deferred_denied"}},
	}
	surface := state.EffectiveToolSurface(reg)
	if _, ok := surface["deferred_denied"]; ok {
		t.Error("a discovered deferred tool the policy denies reached the surface (e.g. restored from persisted metadata)")
	}
	if _, ok := surface["deferred_ok"]; !ok {
		t.Error("an allowed discovered deferred tool is missing")
	}
}

// A tool the policy denies cannot be loaded through tool_search, and is not
// advertised as loadable either.
func TestSessionSubmitMessage_PolicyBlocksToolSearchDiscovery(t *testing.T) {
	t.Setenv(toolsearch.ToolSearchEnvVar, "tst")

	reg := registry.NewRegistry()
	if err := reg.Register(loopToolSearchTestTool{matches: []string{"deferred"}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(deferredLoopTestTool{}); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.PermissionMode = types.PermissionModeBypass
	eng := NewEngine(nil, execution.NewOrchestrator(), nil, prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()), reg, nil, config, nil, nil)
	eng.loop.config.AutoCompact = false

	calls := 0
	eng.loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		calls++
		if calls == 1 {
			if strings.Contains(req.SystemPrompt, "deferred: deferred tool") {
				t.Errorf("a denied deferred tool is advertised: %q", req.SystemPrompt)
			}
			return &types.APIResponse{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					types.ToolUseContent{ID: "s1", Name: toolsearch.ToolSearchToolName, Input: map[string]any{"query": "select:deferred"}},
				},
				StopReason: types.StopReasonToolUse, Model: req.Model, ID: "r1",
			}, nil
		}
		if _, ok := req.Tools["deferred"]; ok {
			t.Error("tool_search loaded a tool the session policy denies")
		}
		return &types.APIResponse{
			Role:       types.RoleAssistant,
			Content:    []types.ContentBlock{types.TextContent{Text: "done"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r2",
		}, nil
	}

	session, err := eng.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.SetToolPolicy(registry.ToolPolicy{Deny: []string{"deferred"}})
	if _, err := session.SubmitMessage(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(session.state.DiscoveredDeferred) != 0 {
		t.Errorf("denied tool recorded as discovered: %v", session.state.DiscoveredDeferred)
	}
}
