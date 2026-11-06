package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// capturingEngine builds an engine whose provider records the names of the
// tools every request offers the model, i.e. what a sub-agent can actually call.
func capturingEngine(t *testing.T, toolNames ...string) (*engine.Engine, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var last []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)
		names := make([]string, 0, len(req.Tools))
		for _, tl := range req.Tools {
			names = append(names, tl.Name)
		}
		sort.Strings(names)
		mu.Lock()
		last = names
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeStreamEvents(t, w, textStreamEvents("done"))
	}))
	t.Cleanup(server.Close)

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{Provider: types.APIProviderAnthropic, BaseURL: server.URL})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})

	reg := registry.NewRegistry()
	for _, name := range toolNames {
		built, err := registry.NewSimpleTool(name, name+" stub", func(context.Context, string) (string, error) { return "ok", nil })
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(built); err != nil {
			t.Fatal(err)
		}
	}
	cfg := engine.DefaultConfig()
	cfg.PermissionMode = types.PermissionModeBypass
	eng := engine.NewEngine(apiClient, execution.NewOrchestrator(), compact.NewEngine(apiClient, compact.DefaultConfig()),
		prompt.NewAssembler(), permissions.NewIntegrator(permissions.NewEngine()), reg, nil, cfg, nil, nil)
	eng.SetAPIClient(apiClient)
	return eng, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), last...)
	}
}

var stubTools = []string{"read_stub", "write_stub", "web_fetch", "web_search", "mcp__db__query"}

func TestRunAgent_EnforcesToolsAndDisallowedTools(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		disallow  []string
		filter    []string
		inherited registry.ToolPolicy
		want      []string
	}{
		{"tools only", []string{"read_stub", "web_*"}, nil, nil, registry.ToolPolicy{}, []string{"read_stub", "web_fetch", "web_search"}},
		{"disallowed only: empty Tools means all, deny subtracts", nil, []string{"write_stub"}, nil, registry.ToolPolicy{}, []string{"mcp__db__query", "read_stub", "web_fetch", "web_search"}},
		{"disallowed only with Tools star", []string{"*"}, []string{"write_stub"}, nil, registry.ToolPolicy{}, []string{"mcp__db__query", "read_stub", "web_fetch", "web_search"}},
		{"empty non-nil Tools also means all", []string{}, []string{"write_stub"}, nil, registry.ToolPolicy{}, []string{"mcp__db__query", "read_stub", "web_fetch", "web_search"}},
		{"both overlapping: deny wins", []string{"read_stub", "write_stub"}, []string{"write_stub"}, nil, registry.ToolPolicy{}, []string{"read_stub"}},
		{"wildcard deny", nil, []string{"mcp__*", "web_*"}, nil, registry.ToolPolicy{}, []string{"read_stub", "write_stub"}},
		{"suffix wildcard deny", nil, []string{"*_stub"}, nil, registry.ToolPolicy{}, []string{"mcp__db__query", "web_fetch", "web_search"}},
		{"deny beats the caller's tool list", nil, []string{"web_fetch"}, []string{"web_fetch", "read_stub"}, registry.ToolPolicy{}, []string{"read_stub"}},
		{"caller list narrows further", []string{"read_stub", "write_stub"}, nil, []string{"read_stub"}, registry.ToolPolicy{}, []string{"read_stub"}},
		{"empty non-nil caller list means no tool", nil, nil, []string{}, registry.ToolPolicy{}, nil},
		{"inherited deny holds although the child allows everything", []string{"*"}, nil, nil, registry.ToolPolicy{Deny: []string{"web_*"}}, []string{"mcp__db__query", "read_stub", "write_stub"}},
		{"inherited allow holds although the child asks for more", []string{"read_stub", "write_stub"}, nil, nil, registry.ToolPolicy{Allow: [][]string{{"read_stub"}}}, []string{"read_stub"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, seen := capturingEngine(t, stubTools...)
			reg := NewAgentRegistry()
			reg.Register(&AgentDefinition{AgentType: "policy-agent", Tools: tt.tools, DisallowedTools: tt.disallow})

			res, err := RunAgent(&RunConfig{
				AgentType: "policy-agent", Task: "go", Engine: eng, Context: context.Background(),
				Registry: reg, ToolFilter: tt.filter, InheritedToolPolicy: tt.inherited,
			})
			if err != nil || !res.Success {
				t.Fatalf("RunAgent: %v / %+v", err, res)
			}
			got := seen()
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tools offered to the model = %v, want %v", got, tt.want)
			}
		})
	}
}

// A sub-agent of a sub-agent runs under the policy the first one ran under,
// carried by the context RunAgent hands to its tools.
func TestRunAgent_SubSubAgentCannotRegainParentLostTool(t *testing.T) {
	parent := EffectiveToolPolicy(registry.ToolPolicy{}, &AgentDefinition{AgentType: "p", Tools: []string{"read_stub", "write_stub"}, DisallowedTools: []string{"write_stub"}}, nil)
	// This is the context a tool call inside the parent's turn sees.
	ctx := WithToolPolicy(context.Background(), parent)

	eng, seen := capturingEngine(t, stubTools...)
	reg := NewAgentRegistry()
	// The child asks for everything, including the tool the parent lost.
	reg.Register(&AgentDefinition{AgentType: "greedy-child", Tools: []string{"*"}})

	res, err := RunAgent(&RunConfig{AgentType: "greedy-child", Task: "go", Engine: eng, Context: ctx, Registry: reg})
	if err != nil || !res.Success {
		t.Fatalf("RunAgent: %v / %+v", err, res)
	}
	if got, want := seen(), []string{"read_stub"}; !reflect.DeepEqual(got, want) {
		t.Errorf("grandchild tools = %v, want %v", got, want)
	}
}

// What a tool running inside the sub-agent sees on its context is the policy
// the sub-agent runs under: that is what its own sub-agents inherit.
func TestRunAgent_ToolsSeeTheAgentsPolicyOnTheirContext(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeStreamEvents(t, w, toolUseStreamEvents("tu1", "probe", "{}"))
			return
		}
		writeStreamEvents(t, w, textStreamEvents("done"))
	}))
	t.Cleanup(server.Close)
	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{Provider: types.APIProviderAnthropic, BaseURL: server.URL})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})

	var seen registry.ToolPolicy
	probe, err := registry.NewBuilder("probe").WithDescription("records the policy on its context").
		WithHandler(func(ctx context.Context, _ registry.CallInput, _ registry.ToolUseContext) (registry.CallResult, error) {
			mu.Lock()
			seen = ToolPolicyFromContext(ctx)
			mu.Unlock()
			return registry.NewTextResult("ok"), nil
		}).Build()
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.NewRegistry()
	if err := reg.Register(probe); err != nil {
		t.Fatal(err)
	}
	cfg := engine.DefaultConfig()
	cfg.PermissionMode = types.PermissionModeBypass
	eng := engine.NewEngine(apiClient, execution.NewOrchestrator(), compact.NewEngine(apiClient, compact.DefaultConfig()),
		prompt.NewAssembler(), permissions.NewIntegrator(permissions.NewEngine()), reg, nil, cfg, nil, nil)
	eng.SetAPIClient(apiClient)

	agents := NewAgentRegistry()
	agents.Register(&AgentDefinition{AgentType: "no-web", DisallowedTools: []string{"web_*"}})
	res, err := RunAgent(&RunConfig{AgentType: "no-web", Task: "go", Engine: eng, Registry: agents, Context: context.Background(), MaxTurns: 2})
	if err != nil || !res.Success {
		t.Fatalf("RunAgent: %v / %+v", err, res)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("probe tool was not called (model calls: %d)", calls)
	}
	if seen.Permits("web_fetch") || !seen.Permits("probe") {
		t.Errorf("policy on the tool's context = %+v; want web_* denied and probe allowed", seen)
	}
}

func TestClampResumedPermissionMode(t *testing.T) {
	tests := []struct {
		name    string
		current types.PermissionMode
		def     *AgentDefinition
		want    types.PermissionMode
	}{
		{"no definition mode keeps the current one", types.PermissionModeBypass, &AgentDefinition{}, types.PermissionModeBypass},
		{"stricter definition wins", types.PermissionModeBypass, &AgentDefinition{PermissionMode: types.PermissionModeOnRequest}, types.PermissionModeOnRequest},
		{"looser definition cannot loosen", types.PermissionModeNever, &AgentDefinition{PermissionMode: types.PermissionModeBypass}, types.PermissionModeNever},
		{"unknown definition value is onRequest at most", types.PermissionModeBypass, &AgentDefinition{PermissionMode: "yolo"}, types.PermissionModeOnRequest},
		{"unset current takes the definition's", "", &AgentDefinition{PermissionMode: types.PermissionModeGranular}, types.PermissionModeGranular},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampResumedPermissionMode(tt.current, tt.def); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
