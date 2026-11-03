package permissions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// ─── mock classifier for e2e tests ───────────────────────────────────────────

type e2eClassifier struct {
	allowed bool
	reason  string
}

func (c *e2eClassifier) Classify(_ context.Context, _ string, _ map[string]any) (Classification, error) {
	return Classification{Allowed: c.allowed, Confidence: 0.95, Reason: c.reason}, nil
}

func TestResolverUsesPromptFnApproval(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	if err := engine.AddRule(PermissionRule{
		Value:    PermissionRuleValue{ToolName: "bash", RuleContent: "echo *"},
		Behavior: types.PermissionBehaviorAsk,
		Priority: 100,
		Reason:   "echo commands require approval in this test",
		Source:   types.PermissionSourceStatic,
	}); err != nil {
		t.Fatalf("failed to add permission rule: %v", err)
	}

	integrator := NewIntegrator(engine)
	promptCalled := false
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalled = true
		if request.Type != types.PromptTypeConfirm {
			t.Fatalf("expected confirm prompt, got %q", request.Type)
		}
		if got := request.Metadata["tool_name"]; got != "bash" {
			t.Fatalf("expected tool metadata for bash, got %#v", got)
		}
		return types.PromptResponse{Value: true}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)
	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo hi"},
		"tool-1",
		"session-1",
		"turn-1",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if !promptCalled {
		t.Fatal("expected prompt function to be called")
	}
	if !result.IsAllowed() {
		t.Fatalf("expected prompt approval to allow tool use, got %#v", result)
	}
	if result.DecisionReason == nil || result.DecisionReason.Source != "prompt" {
		t.Fatalf("expected prompt decision reason, got %#v", result.DecisionReason)
	}
	if got := result.UpdatedInput["command"]; got != "echo hi" {
		t.Fatalf("expected updated input to preserve command, got %#v", got)
	}
}

// TestIntegratorAutoModeClassifierAllows verifies the full path:
// engine + auto-mode + mock classifier → allow decision.
func TestIntegratorAutoModeClassifierAllows(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	engine.SetClassifier(&e2eClassifier{allowed: true, reason: "safe operation"})

	integrator := NewIntegrator(engine)
	resolver := integrator.ResolverWithContext("s1", "t1", nil, nil)

	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "ls"},
		"tu-1",
		"s1",
		"t1",
		types.PermissionModeAuto,
		"",
		nil,
	))

	if !result.IsAllowed() {
		t.Fatalf("expected allow from classifier, got %+v (reason: %v)", result.Behavior, result.DecisionReason)
	}
}

// TestIntegratorAutoModeClassifierDenies verifies the full path:
// engine + auto-mode + mock classifier → deny decision.
func TestIntegratorAutoModeClassifierDenies(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	engine.SetClassifier(&e2eClassifier{allowed: false, reason: "dangerous command"})

	integrator := NewIntegrator(engine)
	resolver := integrator.ResolverWithContext("s1", "t1", nil, nil)

	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "rm -rf /"},
		"tu-2",
		"s1",
		"t1",
		types.PermissionModeAuto,
		"",
		nil,
	))

	if !result.IsDenied() {
		t.Fatalf("expected deny from classifier, got %+v (reason: %v)", result.Behavior, result.DecisionReason)
	}
}

// TestIntegratorAutoModeNoClassifierAsksInteractively guards TODO.md M2: a
// fresh engine that never had SetClassifier/SetAdvancedClassifier called at
// all - the common case for any deployment that doesn't wire the optional
// ML classifier - used to approve every tool call in auto mode, including
// destructive ones. An interactive session now asks instead.
func TestIntegratorAutoModeNoClassifierAsksInteractively(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	// Deliberately never call SetClassifier/SetAdvancedClassifier.

	integrator := NewIntegrator(engine)
	resolver := integrator.ResolverWithContext("s1", "t1", nil, nil)

	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "rm -rf /"},
		"tu-3",
		"s1",
		"t1",
		types.PermissionModeAuto,
		"",
		nil,
	))

	if !result.IsAsk() {
		t.Fatalf("expected Ask when no classifier was ever configured, got %+v (reason: %v)", result.Behavior, result.DecisionReason)
	}
}

// TestIntegratorAutoModeNoClassifierDeniesHeadless is the headless
// counterpart: a background agent cannot show the ask prompt above, so the
// same case denies instead of asking into a void it can never answer.
func TestIntegratorAutoModeNoClassifierDeniesHeadless(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()

	integrator := NewIntegrator(engine)
	resolver := integrator.ResolverWithContext("s1", "t1", nil, nil)

	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "rm -rf /"},
		"tu-4",
		"s1",
		"t1",
		types.PermissionModeAuto,
		"",
		map[string]any{"should_avoid_permission_prompts": true},
	))

	if !result.IsDenied() {
		t.Fatalf("expected Deny when no classifier was ever configured in a headless context, got %+v (reason: %v)", result.Behavior, result.DecisionReason)
	}
}

// TestIntegratorAlwaysSafeToolSkipsClassifierEvenWhenClassifierDenies proves
// get_file_metadata (and the other tools in isAlwaysSafeTool's file
// read-only group) never reach the auto-mode classifier at all: with a
// classifier configured to deny everything, get_file_metadata must still be
// allowed. get_file_metadata was missing from isAlwaysSafeTool despite being
// IsReadOnly/RequiresPermission:false in its own tool.Definition() - every
// call went through the two-stage LLM classifier in Auto mode, adding
// unnecessary API calls (and, if the classifier's own response failed to
// parse, an incorrect "blocking for safety" deny) for a plain stat() call
// that's strictly less sensitive than read_file, which was already exempt.
func TestIntegratorAlwaysSafeToolSkipsClassifierEvenWhenClassifierDenies(t *testing.T) {
	// Every tool added to isAlwaysSafeTool's 2026-08-01 audit pass, in
	// addition to get_file_metadata itself - each verified individually to
	// be IsReadOnly:true/RequiresPermission:false/IsDestructive:false with a
	// trivial Passthrough/AllowWithInput CheckPermissions before being added,
	// same standard get_file_metadata was held to.
	tools := []string{
		"get_file_metadata", "notebook_read",
		"devto_feed", "devto_article", "hn_stories", "hn_item", "hn_search",
		"reddit_search", "reddit_posts", "twitter_search",
		"get_config", "get_goal", "rag_search", "repo_map", "workflow_draft",
		"seshat_list_skills", "seshat_read_skill", "seshat_validate_skill",
		"list_agents", "wait_agent",
	}

	for _, toolName := range tools {
		t.Run(toolName, func(t *testing.T) {
			t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
			engine := NewEngine()
			engine.SetClassifier(&e2eClassifier{allowed: false, reason: "classifier would deny everything"})

			integrator := NewIntegrator(engine)
			resolver := integrator.ResolverWithContext("s1", "t1", nil, nil)

			result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
				toolName,
				map[string]any{},
				"tu-safe-"+toolName,
				"s1",
				"t1",
				types.PermissionModeAuto,
				"",
				nil,
			))

			if !result.IsAllowed() {
				t.Fatalf("expected %q to be allowed as an always-safe tool without consulting the classifier, got %+v (reason: %v)", toolName, result.Behavior, result.DecisionReason)
			}
		})
	}
}

// TestIntegratorDenyRuleTakesPrecedenceOverAutoMode verifies that an explicit
// deny rule fires before the auto-mode classifier is consulted.
func TestIntegratorDenyRuleTakesPrecedenceOverAutoMode(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	// Classifier would allow, but deny rule should win.
	engine.SetClassifier(&e2eClassifier{allowed: true, reason: "would allow"})
	if err := engine.AddRule(PermissionRule{
		Value:    PermissionRuleValue{ToolName: "bash", RuleContent: "rm *"},
		Behavior: types.PermissionBehaviorDeny,
		Priority: 1000,
		Reason:   "rm commands are always denied",
		Source:   types.PermissionSourceStatic,
	}); err != nil {
		t.Fatalf("failed to add rule: %v", err)
	}

	integrator := NewIntegrator(engine)
	resolver := integrator.ResolverWithContext("s1", "t1", nil, nil)

	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "rm foo"},
		"tu-3",
		"s1",
		"t1",
		types.PermissionModeAuto,
		"",
		nil,
	))

	if !result.IsDenied() {
		t.Fatalf("expected deny rule to fire, got %+v", result.Behavior)
	}
}

func TestResolverUsesPromptFnDenial(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	if err := engine.AddRule(PermissionRule{
		Value:    PermissionRuleValue{ToolName: "bash", RuleContent: "echo *"},
		Behavior: types.PermissionBehaviorAsk,
		Priority: 100,
		Reason:   "echo commands require approval in this test",
		Source:   types.PermissionSourceStatic,
	}); err != nil {
		t.Fatalf("failed to add permission rule: %v", err)
	}

	integrator := NewIntegrator(engine)
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		return types.PromptResponse{Value: false}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)
	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo hi"},
		"tool-1",
		"session-1",
		"turn-1",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if !result.IsDenied() {
		t.Fatalf("expected prompt denial to deny tool use, got %#v", result)
	}
	if result.DecisionReason == nil || result.DecisionReason.Source != "prompt" {
		t.Fatalf("expected prompt decision reason, got %#v", result.DecisionReason)
	}
}

func TestResolverUsesPromptFnDenialReason(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	if err := engine.AddRule(PermissionRule{
		Value:    PermissionRuleValue{ToolName: "bash", RuleContent: "echo *"},
		Behavior: types.PermissionBehaviorAsk,
		Priority: 100,
		Reason:   "echo commands require approval in this test",
		Source:   types.PermissionSourceStatic,
	}); err != nil {
		t.Fatalf("failed to add permission rule: %v", err)
	}

	integrator := NewIntegrator(engine)
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		return types.PromptResponse{
			Value:    false,
			Metadata: map[string]any{"reason": "please use a different tool instead"},
		}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)
	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo hi"},
		"tool-1",
		"session-1",
		"turn-1",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if !result.IsDenied() {
		t.Fatalf("expected prompt denial to deny tool use, got %#v", result)
	}
	if result.Reason != "please use a different tool instead" {
		t.Fatalf("expected human-supplied deny reason to flow through, got %q", result.Reason)
	}
}

func TestResolverSessionAutoApproval(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	if err := engine.AddRule(PermissionRule{
		Value:    PermissionRuleValue{ToolName: "bash", RuleContent: "echo *"},
		Behavior: types.PermissionBehaviorAsk,
		Priority: 100,
		Reason:   "echo commands require approval in this test",
		Source:   types.PermissionSourceStatic,
	}); err != nil {
		t.Fatalf("failed to add permission rule: %v", err)
	}

	integrator := NewIntegrator(engine)
	promptCalls := 0
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalls++
		return types.PromptResponse{Value: "always"}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)

	// First call should call promptFn and return "always", which should allow it and remember it for session-1.
	result1 := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo first"},
		"tool-1",
		"session-1",
		"turn-1",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if promptCalls != 1 {
		t.Fatalf("expected promptFn to be called once, got %d", promptCalls)
	}
	if !result1.IsAllowed() {
		t.Fatalf("expected prompt approval to allow tool use, got %#v", result1)
	}

	// Second call with same session-1 and same tool "bash" should NOT trigger a prompt.
	result2 := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo second"},
		"tool-2",
		"session-1",
		"turn-2",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if promptCalls != 1 {
		t.Fatalf("expected promptFn NOT to be called again, got %d calls total", promptCalls)
	}
	if !result2.IsAllowed() {
		t.Fatalf("expected auto-approval for session to allow tool use, got %#v", result2)
	}
	if result2.DecisionReason == nil || result2.DecisionReason.Source != "session" {
		t.Fatalf("expected session decision reason, got %#v", result2.DecisionReason)
	}

	// Third call with DIFFERENT session "session-2" should trigger the prompt.
	result3 := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo third"},
		"tool-3",
		"session-2",
		"turn-3",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if promptCalls != 2 {
		t.Fatalf("expected promptFn to be called again for session-2, got %d calls total", promptCalls)
	}
	if !result3.IsAllowed() {
		t.Fatalf("expected prompt approval to allow tool use, got %#v", result3)
	}
}

// TestRequestPermissionsSessionScopeAutoApproves verifies that a
// request_permissions call granted with scope=session is remembered and
// auto-approved on a later call asking for the *same* escalation, but still
// prompts for a call asking for something different — the grant must be
// scoped to what was actually approved, not to the request_permissions tool
// as a whole.
func TestRequestPermissionsSessionScopeAutoApproves(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := NewEngine()
	if err := engine.AddRule(PermissionRule{
		Value:    PermissionRuleValue{ToolName: "request_permissions"},
		Behavior: types.PermissionBehaviorAsk,
		Priority: 100,
		Reason:   "escalations always require approval in this test",
		Source:   types.PermissionSourceStatic,
	}); err != nil {
		t.Fatalf("failed to add permission rule: %v", err)
	}

	integrator := NewIntegrator(engine)
	promptCalls := 0
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalls++
		return types.PromptResponse{Value: true}, nil
	})

	sessionInput := func(path string) map[string]any {
		return map[string]any{
			"reason": "test",
			"permissions": map[string]any{
				"filesystem": map[string]any{
					"paths":  []any{path},
					"access": []any{"read"},
				},
			},
			"scope": "session",
		}
	}
	sessionMetadata := map[string]any{"grant_scope": "session"}

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)

	// First call for ~/.ssh/config: prompts, gets approved, remembered.
	result1 := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"request_permissions", sessionInput("/home/user/.ssh/config"), "tool-1",
		"session-1", "turn-1", types.PermissionModeOnRequest, "", sessionMetadata,
	))
	if promptCalls != 1 {
		t.Fatalf("expected promptFn to be called once, got %d", promptCalls)
	}
	if !result1.IsAllowed() {
		t.Fatalf("expected first escalation to be allowed, got %#v", result1)
	}

	// Second call, SAME session, SAME path: must NOT prompt again.
	result2 := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"request_permissions", sessionInput("/home/user/.ssh/config"), "tool-2",
		"session-1", "turn-2", types.PermissionModeOnRequest, "", sessionMetadata,
	))
	if promptCalls != 1 {
		t.Fatalf("expected promptFn NOT to be called again for the same escalation, got %d calls total", promptCalls)
	}
	if !result2.IsAllowed() {
		t.Fatalf("expected repeat escalation to auto-approve, got %#v", result2)
	}
	if result2.DecisionReason == nil || result2.DecisionReason.Source != "session" {
		t.Fatalf("expected session decision reason, got %#v", result2.DecisionReason)
	}

	// Third call, SAME session, DIFFERENT path: must prompt — the session
	// grant should not blanket-cover every future request_permissions call.
	result3 := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"request_permissions", sessionInput("/etc/passwd"), "tool-3",
		"session-1", "turn-3", types.PermissionModeOnRequest, "", sessionMetadata,
	))
	if promptCalls != 2 {
		t.Fatalf("expected promptFn to be called again for a different escalation target, got %d calls total", promptCalls)
	}
	if !result3.IsAllowed() {
		t.Fatalf("expected third escalation (after prompt approval) to be allowed, got %#v", result3)
	}
}

func TestResolverSessionAutoApprovalPersistence(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("SESHAT_RUNTIME_ROOT", tempRoot)

	engine := NewEngine()
	integrator := NewIntegrator(engine)

	sessionID := types.SessionID("session-persistent")
	grantsPath := runtimepath.SessionPermissionsPath(tempRoot, string(sessionID))
	if err := os.MkdirAll(filepath.Dir(grantsPath), 0700); err != nil {
		t.Fatalf("failed to create permissions dir: %v", err)
	}

	// What "always allow" writes for a shell command: the program, not the tool
	// (the old whole-tool key "bash" no longer approves commands).
	mockPerms := map[string]bool{"bash:cmd:echo": true}
	data, err := json.Marshal(mockPerms)
	if err != nil {
		t.Fatalf("failed to marshal mock perms: %v", err)
	}
	if err := os.WriteFile(grantsPath, data, 0600); err != nil {
		t.Fatalf("failed to write permissions.json: %v", err)
	}

	promptCalled := false
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalled = true
		return types.PromptResponse{Value: false}, nil
	})

	resolver := integrator.Resolver(sessionID, "turn-1", types.PermissionModeOnRequest)
	result := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"bash",
		map[string]any{"command": "echo hi"},
		"tool-1",
		sessionID,
		"turn-1",
		types.PermissionModeOnRequest,
		"",
		nil,
	))

	if promptCalled {
		t.Fatal("expected promptFn NOT to be called because permissions were pre-loaded from permissions.json")
	}
	if !result.IsAllowed() {
		t.Fatalf("expected tool use to be allowed from disk persistence, got %#v", result)
	}
	if result.DecisionReason == nil || result.DecisionReason.Source != "session" {
		t.Fatalf("expected session decision reason, got %#v", result.DecisionReason)
	}
}

// writeFileRequest builds a permission request shaped the way write.go's own
// access.ResolveToolPermission call actually produces one - the thing a
// request_permissions grant needs to be findable by, not the tool-name/input
// shape request_permissions itself uses.
func writeFileRequest(path, toolUseID string, sessionID types.SessionID, turnID types.TurnID) types.ToolPermissionRequest {
	return types.GlobalToolPermissionRequest(
		"write_file",
		map[string]any{"file_path": path, "content": "x"},
		toolUseID,
		sessionID,
		turnID,
		types.PermissionModeOnRequest,
		"",
		map[string]any{
			access.MetadataRequestKey: access.PermissionRequest{
				ToolName: "write_file",
				Access:   access.AccessWrite,
				Paths:    []string{path},
				Scope:    access.ApprovalScopeToolCall,
			},
		},
	)
}

func requestPermissionsInput(path, access string) map[string]any {
	return map[string]any{
		"reason": "test",
		"permissions": map[string]any{
			"filesystem": map[string]any{
				"paths":  []any{path},
				"access": []any{access},
			},
		},
	}
}

func newAskEngine(t *testing.T, toolNames ...string) *Engine {
	t.Helper()
	engine := NewEngine()
	for i, name := range toolNames {
		if err := engine.AddRule(PermissionRule{
			Value:    PermissionRuleValue{ToolName: name},
			Behavior: types.PermissionBehaviorAsk,
			Priority: 100,
			Reason:   "requires approval in this test",
			Source:   types.PermissionSourceStatic,
		}); err != nil {
			t.Fatalf("failed to add rule %d for %s: %v", i, name, err)
		}
	}
	return engine
}

// TestRequestPermissionsGrantCoversDownstreamToolSameTurn verifies the actual
// point of request_permissions: an approved grant must be findable by the
// operation it was requested FOR (write_file), not just by a repeat
// request_permissions call asking for the same thing. Default scope="turn".
func TestRequestPermissionsGrantCoversDownstreamToolSameTurn(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := newAskEngine(t, "request_permissions", "write_file")
	integrator := NewIntegrator(engine)
	promptCalls := 0
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalls++
		return types.PromptResponse{Value: true}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)

	grant := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"request_permissions", requestPermissionsInput("/workspace/out.py", "write"), "tool-1",
		"session-1", "turn-1", types.PermissionModeOnRequest, "", map[string]any{"grant_scope": "turn"},
	))
	if promptCalls != 1 || !grant.IsAllowed() {
		t.Fatalf("expected the request_permissions call itself to prompt once and be allowed, got calls=%d result=%#v", promptCalls, grant)
	}

	write := resolver.ResolvePermission(context.Background(), writeFileRequest("/workspace/out.py", "tool-2", "session-1", "turn-1"))
	if promptCalls != 1 {
		t.Fatalf("expected write_file to be auto-approved from the just-granted turn-scoped escalation without prompting, got %d calls total", promptCalls)
	}
	if !write.IsAllowed() {
		t.Fatalf("expected write_file to be allowed, got %#v", write)
	}

	// Different turn: the turn-scoped grant must not carry over.
	writeNextTurn := resolver.ResolvePermission(context.Background(), writeFileRequest("/workspace/out.py", "tool-3", "session-1", "turn-2"))
	if promptCalls != 2 {
		t.Fatalf("expected write_file in a different turn to prompt again (turn-scoped grant expired), got %d calls total", promptCalls)
	}
	if !writeNextTurn.IsAllowed() {
		t.Fatalf("expected write_file to be allowed after re-prompting, got %#v", writeNextTurn)
	}
}

// TestRequestPermissionsSessionScopeGrantCoversDownstreamToolAcrossTurns
// verifies scope="session" grants survive across turns (and, unlike the
// turn-scoped case, don't need to be re-approved next turn).
func TestRequestPermissionsSessionScopeGrantCoversDownstreamToolAcrossTurns(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := newAskEngine(t, "request_permissions", "write_file")
	integrator := NewIntegrator(engine)
	promptCalls := 0
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalls++
		return types.PromptResponse{Value: true}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)

	grant := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"request_permissions", requestPermissionsInput("/workspace/out.py", "write"), "tool-1",
		"session-1", "turn-1", types.PermissionModeOnRequest, "", map[string]any{"grant_scope": "session"},
	))
	if promptCalls != 1 || !grant.IsAllowed() {
		t.Fatalf("expected the request_permissions call itself to prompt once and be allowed, got calls=%d result=%#v", promptCalls, grant)
	}

	// A later turn in the same session must still find the grant.
	write := resolver.ResolvePermission(context.Background(), writeFileRequest("/workspace/out.py", "tool-2", "session-1", "turn-7"))
	if promptCalls != 1 {
		t.Fatalf("expected write_file in a later turn to be auto-approved from the session-scoped grant, got %d calls total", promptCalls)
	}
	if !write.IsAllowed() {
		t.Fatalf("expected write_file to be allowed, got %#v", write)
	}

	// A different path never granted must still prompt.
	other := resolver.ResolvePermission(context.Background(), writeFileRequest("/workspace/other.py", "tool-3", "session-1", "turn-7"))
	if promptCalls != 2 {
		t.Fatalf("expected write_file for an ungranted path to prompt, got %d calls total", promptCalls)
	}
	if !other.IsAllowed() {
		t.Fatalf("expected write_file to be allowed after re-prompting, got %#v", other)
	}
}

// TestRequestPermissionsGrantCoversPathsUnderGrantedDirectory verifies a
// grant declared on a directory covers files underneath it, not only that
// exact path - the natural way an agent would request "let me write into
// this workspace" once instead of once per file.
func TestRequestPermissionsGrantCoversPathsUnderGrantedDirectory(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	engine := newAskEngine(t, "request_permissions", "write_file")
	integrator := NewIntegrator(engine)
	promptCalls := 0
	integrator.SetPromptFn(func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
		promptCalls++
		return types.PromptResponse{Value: true}, nil
	})

	resolver := integrator.Resolver("session-1", "turn-1", types.PermissionModeOnRequest)

	grant := resolver.ResolvePermission(context.Background(), types.GlobalToolPermissionRequest(
		"request_permissions", requestPermissionsInput(filepath.Clean("/workspace/artifacts"), "write"), "tool-1",
		"session-1", "turn-1", types.PermissionModeOnRequest, "", map[string]any{"grant_scope": "turn"},
	))
	if promptCalls != 1 || !grant.IsAllowed() {
		t.Fatalf("expected the request_permissions call itself to prompt once and be allowed, got calls=%d result=%#v", promptCalls, grant)
	}

	nested := filepath.Join("/workspace/artifacts", "sub", "out.py")
	write := resolver.ResolvePermission(context.Background(), writeFileRequest(nested, "tool-2", "session-1", "turn-1"))
	if promptCalls != 1 {
		t.Fatalf("expected write_file under the granted directory to be auto-approved, got %d calls total", promptCalls)
	}
	if !write.IsAllowed() {
		t.Fatalf("expected write_file to be allowed, got %#v", write)
	}
}
