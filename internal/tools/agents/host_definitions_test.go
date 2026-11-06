package agents

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	"github.com/KPO-Tech/seshat-runtime/internal/runtime/tasks"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	coretool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func enumOf(t *testing.T, def coretool.Definition) []string {
	t.Helper()
	raw, err := json.Marshal(def.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "agent_type"} {
		if p, ok := schema.Properties[key]; ok {
			return p.Enum
		}
	}
	t.Fatal("no type property in the schema")
	return nil
}

func hostTestRegistry(t *testing.T) *coreagent.AgentRegistry {
	t.Helper()
	reg := coreagent.NewAgentRegistry()
	err := reg.ReplaceHostDefinitions([]*coreagent.AgentDefinition{{
		AgentType:       "org-reviewer",
		WhenToUse:       "Reviews code\nfor the organisation",
		Tools:           []string{"read_file", "grep", "bash"},
		DisallowedTools: []string{"bash"},
		PermissionMode:  types.PermissionModeOnRequest,
		MaxTurns:        7,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// The agent tool, spawn_agent and resume_agent's schemas list the registered
// types, in a stable order, and follow the registry live.
func TestAgentToolSchema_FollowsRegisteredTypes(t *testing.T) {
	reg := hostTestRegistry(t)
	at := NewAgentTool(nil)
	at.SetRegistry(reg)
	spawn := NewSpawnAgentTool(nil, nil, reg)

	for name, def := range map[string]coretool.Definition{"agent": at.Definition(), "spawn_agent": spawn.Definition()} {
		enum := enumOf(t, def)
		if want := append(append([]string(nil), builtInAgentTypeEnum...), "org-reviewer"); !reflect.DeepEqual(enum, want) {
			t.Errorf("%s enum = %v", name, enum)
		}
		if !strings.Contains(def.Description, "org-reviewer") || !strings.Contains(def.Description, "Reviews code for the organisation") {
			t.Errorf("%s description does not tell the model when to use the type", name)
		}
	}

	// Removing the set removes the type from both.
	if err := reg.ReplaceHostDefinitions(nil); err != nil {
		t.Fatal(err)
	}
	for name, def := range map[string]coretool.Definition{"agent": at.Definition(), "spawn_agent": spawn.Definition()} {
		if enum := enumOf(t, def); !reflect.DeepEqual(enum, []string{"general-purpose", "explore", "browse", "plan", "verify"}) {
			t.Errorf("%s enum after removal = %v", name, enum)
		}
	}

	// Without a registry the built-in list is unchanged.
	if enum := enumOf(t, NewAgentTool(nil).Definition()); !reflect.DeepEqual(enum, builtInAgentTypeEnum) {
		t.Errorf("no-registry enum = %v", enum)
	}
}

func TestAgentToolDescription_SanitisesHostText(t *testing.T) {
	reg := coreagent.NewAgentRegistry()
	long := strings.Repeat("x", 1000)
	_ = reg.ReplaceHostDefinitions([]*coreagent.AgentDefinition{{AgentType: "wordy", WhenToUse: "line one\n\n## Injected heading\n" + long}})
	section := hostAgentTypesSection(reg)
	if strings.Contains(section, "\n## Injected") || len(section) > 600 {
		t.Errorf("host text not flattened/truncated: %q", section)
	}
}

// End to end through the agent tool: a registered definition's permission
// mode (clamped), tools, disallowed tools and max turns all reach the run, on
// every spawn path.
func TestAgentTool_Call_AppliesRegisteredDefinition(t *testing.T) {
	paths := []struct {
		name  string
		input map[string]any
	}{
		{"normal", map[string]any{}},
		{"fork", map[string]any{"fork": true}},
		{"worktree", map[string]any{"isolation": "worktree"}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			if p.name == "worktree" {
				chdirToTempGitRepo(t)
			}
			reg := hostTestRegistry(t)
			at := NewAgentTool(nil)
			at.SetRegistry(reg)
			at.SetEngine(newSkillForkTestEngine(t, "ok"))

			var got *coreagent.RunConfig
			var policy coretool.ToolPolicy
			at.runFn = func(ctx context.Context, cfg *coreagent.RunConfig) (*coreagent.RunResult, error) {
				got = cfg
				def, ok := cfg.Registry.Get(cfg.AgentType)
				if !ok {
					t.Fatalf("RunConfig.Registry does not resolve %q", cfg.AgentType)
				}
				policy = coreagent.EffectiveToolPolicy(coreagent.ToolPolicyFromContext(ctx), def, cfg.ToolFilter)
				return &coreagent.RunResult{AgentType: cfg.AgentType, Success: true}, nil
			}

			input := map[string]any{"type": "org-reviewer", "task": "review"}
			for k, v := range p.input {
				input[k] = v
			}
			raw, _ := json.Marshal(input)
			// The parent is in bypass: the definition's stricter mode must win.
			if _, err := at.Call(context.Background(), tool.CallInput{
				Raw: string(raw), ToolContext: &tool.ToolUseContext{PermissionMode: types.PermissionModeBypass},
			}, nil); err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("the run was not started")
			}
			if got.PermissionMode != types.PermissionModeOnRequest {
				t.Errorf("PermissionMode = %q, want onRequest", got.PermissionMode)
			}
			if got.MaxTurns != 7 {
				t.Errorf("MaxTurns = %d, want 7", got.MaxTurns)
			}
			if !reflect.DeepEqual(got.ToolFilter, []string{"read_file", "grep", "bash"}) {
				t.Errorf("ToolFilter = %v", got.ToolFilter)
			}
			for _, name := range []string{"read_file", "grep"} {
				if !policy.Permits(name) {
					t.Errorf("%s should be available", name)
				}
			}
			for _, name := range []string{"bash", "write_file"} {
				if policy.Permits(name) {
					t.Errorf("%s must not be available (bash is in both lists: deny wins; write_file is not allowed)", name)
				}
			}
		})
	}
}

func TestAgentTool_Call_BackgroundAppliesRegisteredDefinition(t *testing.T) {
	reg := hostTestRegistry(t)
	at := NewAgentTool(nil)
	at.SetRegistry(reg)
	eng := newSkillForkTestEngine(t, "ok")
	at.SetEngine(eng)
	mgr := tasks.NewManager(eng, nil)
	prev := tasks.GlobalManager()
	tasks.SetGlobalManager(mgr)
	t.Cleanup(func() { tasks.SetGlobalManager(prev) })

	var mode types.PermissionMode
	var policy coretool.ToolPolicy
	at.createTaskFn = func(m types.PermissionMode, p coretool.ToolPolicy) (*tasks.Task, error) {
		mode, policy = m, p
		return &tasks.Task{ID: "agent_x", Status: tasks.TaskStatusPending}, nil
	}
	raw, _ := json.Marshal(map[string]any{"type": "org-reviewer", "task": "review", "run_in_background": true})
	if _, err := at.Call(context.Background(), tool.CallInput{
		Raw: string(raw), ToolContext: &tool.ToolUseContext{PermissionMode: types.PermissionModeBypass},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if mode != types.PermissionModeOnRequest {
		t.Errorf("mode = %q", mode)
	}
	if policy.Permits("bash") || !policy.Permits("grep") || policy.Permits("write_file") {
		t.Errorf("background policy wrong: %+v", policy)
	}
}

// A sub-agent started from inside a restricted sub-agent inherits that
// sub-agent's restrictions (the policy rides on the context).
func TestAgentTool_Call_ChildInheritsParentPolicy(t *testing.T) {
	reg := coreagent.NewAgentRegistry()
	_ = reg.ReplaceHostDefinitions([]*coreagent.AgentDefinition{{AgentType: "wide-child", Tools: []string{"*"}}})
	at := NewAgentTool(nil)
	at.SetRegistry(reg)
	at.SetEngine(newSkillForkTestEngine(t, "ok"))

	var policy coretool.ToolPolicy
	at.runFn = func(ctx context.Context, cfg *coreagent.RunConfig) (*coreagent.RunResult, error) {
		def, _ := cfg.Registry.Get(cfg.AgentType)
		// RunAgent merges the policy of cfg.Context with the definition's.
		policy = coreagent.EffectiveToolPolicy(coreagent.ToolPolicyFromContext(cfg.Context), def, cfg.ToolFilter)
		return &coreagent.RunResult{AgentType: cfg.AgentType, Success: true}, nil
	}
	parentPolicy := coretool.ToolPolicy{}.Narrow([]string{"read_file", "grep"}, []string{"grep"})
	ctx := coreagent.WithToolPolicy(context.Background(), parentPolicy)

	raw, _ := json.Marshal(map[string]any{"type": "wide-child", "task": "x"})
	if _, err := at.Call(ctx, tool.CallInput{Raw: string(raw), ToolContext: &tool.ToolUseContext{}}, nil); err != nil {
		t.Fatal(err)
	}
	if !policy.Permits("read_file") || policy.Permits("grep") || policy.Permits("bash") {
		t.Errorf("child policy = %+v; want only read_file", policy)
	}
}

func TestAgentTool_Call_UnregisteredTypeRefused(t *testing.T) {
	reg := hostTestRegistry(t)
	at := NewAgentTool(nil)
	at.SetRegistry(reg)
	at.SetEngine(newSkillForkTestEngine(t, "ok"))
	called := false
	at.runFn = func(context.Context, *coreagent.RunConfig) (*coreagent.RunResult, error) {
		called = true
		return &coreagent.RunResult{Success: true}, nil
	}
	_ = reg.ReplaceHostDefinitions(nil) // removed after the model saw the schema

	raw, _ := json.Marshal(map[string]any{"type": "org-reviewer", "task": "x"})
	res, err := at.Call(context.Background(), tool.CallInput{Raw: string(raw), ToolContext: &tool.ToolUseContext{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(res.Content, "unknown agent type") {
		t.Errorf("removed type was run or not refused: called=%v content=%v", called, res.Content)
	}
}
