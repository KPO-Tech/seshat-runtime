package agents

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	"github.com/KPO-Tech/seshat-runtime/internal/runtime/tasks"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	coretool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	pmNever       = types.PermissionModeNever
	pmGranular    = types.PermissionModeGranular
	pmOnRequest   = types.PermissionModeOnRequest
	pmAcceptEdits = types.PermissionModeAcceptEdits
	pmAuto        = types.PermissionModeAuto
	pmBypass      = types.PermissionModeBypass
)

func TestResolveSubAgentPermissionMode(t *testing.T) {
	tests := []struct {
		name   string
		parent types.PermissionMode
		def    *coreagent.AgentDefinition
		want   types.PermissionMode
	}{
		// Empty / absent definition mode keeps the legacy policy.
		{"nil definition, bypass parent", pmBypass, nil, pmBypass},
		{"nil definition, onRequest parent keeps legacy bypass", pmOnRequest, nil, pmBypass},
		{"empty mode, auto parent keeps legacy bypass", pmAuto, &coreagent.AgentDefinition{}, pmBypass},
		{"empty mode, never parent stays never", pmNever, &coreagent.AgentDefinition{}, pmNever},
		{"empty mode, unset parent keeps legacy bypass", "", &coreagent.AgentDefinition{}, pmBypass},

		// Stricter than the parent: honoured.
		{"onRequest under bypass parent", pmBypass, &coreagent.AgentDefinition{PermissionMode: pmOnRequest}, pmOnRequest},
		{"never under bypass parent", pmBypass, &coreagent.AgentDefinition{PermissionMode: pmNever}, pmNever},
		{"acceptEdits under auto parent", pmAuto, &coreagent.AgentDefinition{PermissionMode: pmAcceptEdits}, pmAcceptEdits},
		{"granular under onRequest parent", pmOnRequest, &coreagent.AgentDefinition{PermissionMode: pmGranular}, pmGranular},
		{"auto under bypass parent", pmBypass, &coreagent.AgentDefinition{PermissionMode: pmAuto}, pmAuto},

		// Equal to the parent: honoured.
		{"onRequest under onRequest parent", pmOnRequest, &coreagent.AgentDefinition{PermissionMode: pmOnRequest}, pmOnRequest},
		{"bypass under bypass parent", pmBypass, &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmBypass},
		{"never under never parent", pmNever, &coreagent.AgentDefinition{PermissionMode: pmNever}, pmNever},

		// More permissive than the parent: clamped to the parent.
		{"bypass under onRequest parent is clamped", pmOnRequest, &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmOnRequest},
		{"auto under onRequest parent is clamped", pmOnRequest, &coreagent.AgentDefinition{PermissionMode: pmAuto}, pmOnRequest},
		{"bypass under acceptEdits parent is clamped", pmAcceptEdits, &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmAcceptEdits},
		{"onRequest under granular parent is clamped to granular", pmGranular, &coreagent.AgentDefinition{PermissionMode: pmOnRequest}, pmGranular},
		{"bypass under granular parent is clamped to granular", pmGranular, &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmGranular},
		{"bypass under unset parent is clamped to onRequest", "", &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmOnRequest},
		{"bypass under unknown parent is clamped to onRequest", "plan", &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmOnRequest},

		// Never parent: never in every case.
		{"bypass under never parent", pmNever, &coreagent.AgentDefinition{PermissionMode: pmBypass}, pmNever},
		{"onRequest under never parent", pmNever, &coreagent.AgentDefinition{PermissionMode: pmOnRequest}, pmNever},
		{"granular under never parent", pmNever, &coreagent.AgentDefinition{PermissionMode: pmGranular}, pmNever},

		// Unknown definition value: never widened, never above onRequest.
		{"unknown value, bypass parent", pmBypass, &coreagent.AgentDefinition{PermissionMode: "yolo"}, pmOnRequest},
		{"unknown value, auto parent", pmAuto, &coreagent.AgentDefinition{PermissionMode: "Bypass"}, pmOnRequest},
		{"unknown value, never parent", pmNever, &coreagent.AgentDefinition{PermissionMode: "yolo"}, pmNever},
		{"unknown value, granular parent", pmGranular, &coreagent.AgentDefinition{PermissionMode: "yolo"}, pmGranular},
		{"unknown value, unset parent", "", &coreagent.AgentDefinition{PermissionMode: "yolo"}, pmOnRequest},
		{"legacy plan value is unknown", pmBypass, &coreagent.AgentDefinition{PermissionMode: "plan"}, pmOnRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSubAgentPermissionMode(tt.parent, tt.def); got != tt.want {
				t.Errorf("resolveSubAgentPermissionMode(%q, %+v) = %q, want %q", tt.parent, tt.def, got, tt.want)
			}
		})
	}
}

// The result may never be more permissive than a known parent, whatever the
// definition says, for every parent x declared-mode pair.
func TestResolveSubAgentPermissionMode_NeverExceedsParent(t *testing.T) {
	all := []types.PermissionMode{pmNever, pmGranular, pmOnRequest, pmAcceptEdits, pmAuto, pmBypass}
	for _, parent := range all {
		for _, declared := range append([]types.PermissionMode{"", "garbage"}, all...) {
			if declared == "" {
				continue // legacy path, covered above
			}
			got := resolveSubAgentPermissionMode(parent, &coreagent.AgentDefinition{PermissionMode: declared})
			rg, okG := types.PermissionModeStrictness(got)
			rp, _ := types.PermissionModeStrictness(parent)
			if !okG {
				t.Fatalf("parent=%q declared=%q produced unknown mode %q", parent, declared, got)
			}
			if rg > rp {
				t.Errorf("parent=%q declared=%q produced %q, more permissive than the parent", parent, declared, got)
			}
		}
	}
}

// pathCase drives AgentTool.Call down one spawn path with a registered
// definition and returns the PermissionMode the path put in its RunConfig.
type pathCase struct {
	name  string
	input map[string]any
}

func TestAgentTool_Call_AppliesDefinitionPermissionMode(t *testing.T) {
	paths := []pathCase{
		{"normal", map[string]any{}},
		{"fork", map[string]any{"fork": true}},
		{"worktree", map[string]any{"isolation": "worktree"}},
	}
	cases := []struct {
		name   string
		parent types.PermissionMode
		def    types.PermissionMode
		want   types.PermissionMode
	}{
		{"stricter honoured", pmBypass, pmOnRequest, pmOnRequest},
		{"equal honoured", pmAcceptEdits, pmAcceptEdits, pmAcceptEdits},
		{"more permissive clamped", pmOnRequest, pmBypass, pmOnRequest},
		{"empty keeps legacy bypass", pmOnRequest, "", pmBypass},
		{"unknown falls back to onRequest", pmBypass, "yolo", pmOnRequest},
		{"never parent", pmNever, pmBypass, pmNever},
	}

	for _, p := range paths {
		for _, c := range cases {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				if p.name == "worktree" {
					chdirToTempGitRepo(t)
				}
				var got types.PermissionMode
				calls := 0
				at := newPermissionTestTool(t, c.def, func(cfg *coreagent.RunConfig) {
					got = cfg.PermissionMode
					calls++
				})

				input := map[string]any{"type": "preset-x", "task": "do it"}
				for k, v := range p.input {
					input[k] = v
				}
				raw, _ := json.Marshal(input)
				if _, err := at.Call(context.Background(), tool.CallInput{
					Raw:         string(raw),
					ToolContext: &tool.ToolUseContext{PermissionMode: c.parent},
				}, nil); err != nil {
					t.Fatalf("Call: %v", err)
				}
				if calls != 1 {
					t.Fatalf("expected exactly one run, got %d", calls)
				}
				if got != c.want {
					t.Errorf("RunConfig.PermissionMode = %q, want %q", got, c.want)
				}
			})
		}
	}
}

// The background path hands the mode to the task manager (which applies it to
// the new session before it runs); the test observes that hand-off.
func TestAgentTool_Call_Background_AppliesDefinitionPermissionMode(t *testing.T) {
	cases := []struct {
		name   string
		parent types.PermissionMode
		def    types.PermissionMode
		want   types.PermissionMode
	}{
		{"stricter honoured", pmBypass, pmOnRequest, pmOnRequest},
		{"more permissive clamped", pmOnRequest, pmBypass, pmOnRequest},
		{"empty keeps legacy bypass", pmOnRequest, "", pmBypass},
		{"unknown falls back to onRequest", pmBypass, "yolo", pmOnRequest},
		{"never parent", pmNever, pmBypass, pmNever},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := newSkillForkTestEngine(t, "ok")
			mgr := tasks.NewManager(eng, nil)
			prev := tasks.GlobalManager()
			tasks.SetGlobalManager(mgr)
			t.Cleanup(func() { tasks.SetGlobalManager(prev) })

			var got types.PermissionMode
			at := newPermissionTestTool(t, c.def, nil)
			at.SetEngine(eng)
			at.createTaskFn = func(m types.PermissionMode, _ coretool.ToolPolicy) (*tasks.Task, error) {
				got = m
				return &tasks.Task{ID: "agent_test", Status: tasks.TaskStatusPending}, nil
			}

			raw, _ := json.Marshal(map[string]any{"type": "preset-x", "task": "do it", "run_in_background": true})
			res, err := at.Call(context.Background(), tool.CallInput{
				Raw:         string(raw),
				ToolContext: &tool.ToolUseContext{PermissionMode: c.parent},
			}, nil)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if data, _ := res.Data.(map[string]any); data["taskId"] != "agent_test" {
				t.Fatalf("background path not taken: %#v", res.Data)
			}
			if got != c.want {
				t.Errorf("permission mode handed to the task manager = %q, want %q", got, c.want)
			}
		})
	}
}

// newPermissionTestTool builds an AgentTool whose registry holds a "preset-x"
// definition declaring defMode. observe, when non-nil, replaces the agent run
// and receives the RunConfig the spawn path built.
func newPermissionTestTool(t *testing.T, defMode types.PermissionMode, observe func(*coreagent.RunConfig)) *AgentTool {
	t.Helper()
	reg := coreagent.NewAgentRegistry()
	reg.Register(&coreagent.AgentDefinition{
		AgentType:      "preset-x",
		WhenToUse:      "test",
		PermissionMode: defMode,
	})
	at := NewAgentTool(nil)
	at.SetRegistry(reg)
	// A non-nil engine is needed to get past the engine check; the observed run
	// never touches it.
	at.SetEngine(newSkillForkTestEngine(t, "ok"))
	if observe != nil {
		at.runFn = func(_ context.Context, cfg *coreagent.RunConfig) (*coreagent.RunResult, error) {
			observe(cfg)
			return &coreagent.RunResult{AgentType: cfg.AgentType, Success: true}, nil
		}
	}
	return at
}

// chdirToTempGitRepo makes the worktree path create its worktree in a
// throwaway repository instead of the repository under test.
func chdirToTempGitRepo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
}
