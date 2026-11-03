package access

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func TestCommandPolicyAsksForDangerousCommand(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate("rm -rf build")
	if result.Decision != DecisionAsk {
		t.Fatalf("expected ask, got %s", result.Decision)
	}
}

func TestCommandPolicyAsksForSafeAbsoluteTmpRemoval(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate("rm -rf /tmp/seshat-scratch")
	if result.Decision != DecisionAsk {
		t.Fatalf("expected ask, got %s", result.Decision)
	}
}

func TestCommandPolicyDeniesExplicitlyForbiddenCommand(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate("rm -rf /")
	if result.Decision != DecisionDeny {
		t.Fatalf("expected deny, got %s", result.Decision)
	}
}

func TestFilesystemPolicyAllowsWorkspaceRead(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "a.txt")
	if err := os.WriteFile(file, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	policy := NewDefaultFilesystemPolicy()
	decision, err := policy.EvaluatePath(Context{WorkspaceRoot: tmp}, "a.txt", AccessRead)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionAllow {
		t.Fatalf("expected allow, got %s (%s)", decision.Decision, decision.Reason)
	}
}

func TestFilesystemPolicyAllowsAbsolutePathOutsideWorkspace(t *testing.T) {
	// Absolute paths are no longer hard-blocked by workspace containment.
	// The user approves out-of-workspace access via the permission dialog;
	// dangerous-prefix blocking (writeDeniedPrefixes) still applies for writes.
	tmp := t.TempDir()
	outside := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(outside, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	policy := NewDefaultFilesystemPolicy()
	decision, err := policy.EvaluatePath(Context{WorkspaceRoot: tmp}, outside, AccessRead)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	// /etc is not in readDeniedPrefixes, so the decision must be Allow.
	if decision.Decision != DecisionAllow {
		t.Errorf("expected allow for absolute path outside workspace, got %s: %s", decision.Decision, decision.Reason)
	}
}

func TestFilesystemPolicyStillDeniesWriteToProtectedEtc(t *testing.T) {
	// Even with workspace relaxation, /etc writes must stay blocked.
	tmp := t.TempDir()
	policy := NewDefaultFilesystemPolicy()
	decision, err := policy.EvaluatePath(Context{WorkspaceRoot: tmp}, "/etc/passwd", AccessWrite)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionDeny {
		t.Errorf("expected deny for /etc write, got %s", decision.Decision)
	}
}

func TestFilesystemPolicyRejectsProtectedWritePrefix(t *testing.T) {
	policy := NewDefaultFilesystemPolicy()
	decision, err := policy.EvaluatePath(Context{}, "/etc/hosts", AccessWrite)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionDeny {
		t.Fatalf("expected deny, got %s", decision.Decision)
	}
}

func TestPermissionRequestValidateRequiresTarget(t *testing.T) {
	req := PermissionRequest{
		ToolName: "bash",
		Access:   AccessExecute,
		Scope:    ApprovalScopeTurn,
	}
	if err := req.Validate(); err == nil {
		t.Fatal("expected request validation to fail without command, paths, or network targets")
	}
}

func TestPermissionRequestValidateAcceptsCommand(t *testing.T) {
	req := PermissionRequest{
		ToolName: "bash",
		Access:   AccessExecute,
		Command:  "git status",
		Scope:    ApprovalScopeTurn,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestBuildPreviewCopiesRequestFields(t *testing.T) {
	req := PermissionRequest{
		ToolName:       "read_file",
		Environment:    sandbox.EnvironmentLocal,
		Access:         AccessRead,
		Paths:          []string{"/tmp/a.txt"},
		Justification:  "Need to inspect config",
		NetworkTargets: []string{"example.com"},
	}
	preview := BuildPreview(req)
	if preview.ToolName != req.ToolName {
		t.Fatalf("expected tool name %q, got %q", req.ToolName, preview.ToolName)
	}
	if len(preview.Paths) != 1 || preview.Paths[0] != "/tmp/a.txt" {
		t.Fatalf("unexpected preview paths: %#v", preview.Paths)
	}
}

func TestPermissionRequestDescriptionTextUsesCommand(t *testing.T) {
	req := PermissionRequest{ToolName: "bash", Command: "git status"}
	if got := req.DescriptionText(); got != "Execute command: git status" {
		t.Fatalf("unexpected description: %q", got)
	}
}

func TestPermissionRequestMetadataMapIncludesPreview(t *testing.T) {
	req := PermissionRequest{
		ToolName: "bash",
		Access:   AccessExecute,
		Command:  "git status",
		Scope:    ApprovalScopeTurn,
	}
	metadata := req.MetadataMap()
	if _, ok := metadata[MetadataRequestKey]; !ok {
		t.Fatal("expected metadata to include sandbox request")
	}
	if _, ok := metadata[MetadataPreviewKey]; !ok {
		t.Fatal("expected metadata to include sandbox preview")
	}
}

func TestBuildToolPermissionRequestPreservesSandboxMetadata(t *testing.T) {
	req := PermissionRequest{
		ToolName:      "bash",
		Environment:   sandbox.EnvironmentLocal,
		Access:        AccessExecute,
		Command:       "git status",
		Justification: "Inspect repository state",
		Scope:         ApprovalScopeToolCall,
		Metadata: map[string]any{
			"timeout_seconds": 30.0,
		},
	}
	toolReq, err := BuildToolPermissionRequest(req, ToolPermissionOptions{
		ToolInput:              map[string]any{"command": "git status"},
		WorkingDirectory:       "/workspace",
		IsToolRunningInSandbox: true,
		Metadata: map[string]any{
			"origin": "test",
		},
	})
	if err != nil {
		t.Fatalf("BuildToolPermissionRequest: %v", err)
	}
	if toolReq.Description != "Execute command: git status" {
		t.Fatalf("unexpected description: %q", toolReq.Description)
	}
	if toolReq.WorkingDirectory != "/workspace" {
		t.Fatalf("unexpected working directory: %q", toolReq.WorkingDirectory)
	}
	if !toolReq.IsToolRunningInSandbox {
		t.Fatal("expected sandbox flag to be preserved")
	}
	if toolReq.Stage != types.ToolPermissionStageGlobal {
		t.Fatalf("unexpected stage: %s", toolReq.Stage)
	}
	if toolReq.Intent != types.ToolPermissionIntentCheck {
		t.Fatalf("unexpected intent: %s", toolReq.Intent)
	}
	if _, ok := toolReq.Metadata[MetadataRequestKey]; !ok {
		t.Fatal("expected metadata to include normalized sandbox request")
	}
	if _, ok := toolReq.Metadata[MetadataPreviewKey]; !ok {
		t.Fatal("expected metadata to include normalized sandbox preview")
	}
	if got := toolReq.Metadata["origin"]; got != "test" {
		t.Fatalf("expected merged metadata, got %#v", got)
	}
	if got := toolReq.Metadata["timeout_seconds"]; got != 30.0 {
		t.Fatalf("expected request metadata to survive merge, got %#v", got)
	}
}

func TestResolveToolPermissionUsesChecker(t *testing.T) {
	req := PermissionRequest{
		ToolName: "read_file",
		Access:   AccessRead,
		Paths:    []string{"/tmp/a.txt"},
		Scope:    ApprovalScopeToolCall,
	}
	checker := types.CanUseToolFn(func(_ context.Context, request types.ToolPermissionRequest) types.PermissionResult {
		if request.ToolName != "read_file" {
			t.Fatalf("unexpected tool name: %s", request.ToolName)
		}
		if request.Description != "Access path: /tmp/a.txt" {
			t.Fatalf("unexpected description: %q", request.Description)
		}
		return types.AllowWithDecisionReason("ok", nil)
	})
	result, err := ResolveToolPermission(context.Background(), checker, req, ToolPermissionOptions{})
	if err != nil {
		t.Fatalf("ResolveToolPermission: %v", err)
	}
	if !result.IsAllowed() {
		t.Fatalf("expected allow, got %s", result.Behavior)
	}
}

func TestErrorForDecisionReturnsExpectedErrorTypes(t *testing.T) {
	if err := ErrorForDecision(DecisionResult{Decision: DecisionAllow}); err != nil {
		t.Fatalf("expected nil error for allow, got %v", err)
	}
	var asked *ApprovalRequiredError
	if !errors.As(ErrorForDecision(DecisionResult{Decision: DecisionAsk}), &asked) {
		t.Fatal("expected approval required error")
	}
	var denied *PermissionDeniedError
	if !errors.As(ErrorForDecision(DecisionResult{Decision: DecisionDeny}), &denied) {
		t.Fatal("expected permission denied error")
	}
}

// ─── IsKnownSafe tests ────────────────────────────────────────────────────────

func TestIsKnownSafeAllowsBasicReadCommands(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	safe := []string{
		"ls", "cat file.txt", "head -n 10 file.txt", "tail -f log.txt",
		"wc -l main.go", "grep foo bar.go", "pwd", "echo hello",
		"which go", "diff a.txt b.txt", "stat file.go",
	}
	for _, cmd := range safe {
		if !policy.IsKnownSafe(cmd) {
			t.Errorf("expected IsKnownSafe=true for %q", cmd)
		}
	}
}

func TestIsKnownSafeAllowsGitReadSubcommands(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	safe := []string{
		"git status", "git log -n 5", "git diff HEAD",
		"git show HEAD", "git branch -a", "git branch --show-current",
	}
	for _, cmd := range safe {
		if !policy.IsKnownSafe(cmd) {
			t.Errorf("expected IsKnownSafe=true for %q", cmd)
		}
	}
}

func TestIsKnownSafeRejectsGitMutatingCommands(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	unsafe := []string{
		"git push", "git fetch", "git pull", "git clone https://example.com/repo",
		"git branch -d feature", "git branch new-branch",
		"git -C /tmp status",         // unsafe global option
		"git --git-dir=.evil status", // unsafe global option
		"git log --output=/tmp/out",  // unsafe subcommand option
	}
	for _, cmd := range unsafe {
		if policy.IsKnownSafe(cmd) {
			t.Errorf("expected IsKnownSafe=false for %q", cmd)
		}
	}
}

func TestIsKnownSafeRejectsWriteCommands(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	unsafe := []string{
		"rm file.txt", "mv a.txt b.txt", "cp src dst",
		"mkdir /tmp/test", "touch newfile",
		"chmod 755 file", "chown root file",
	}
	for _, cmd := range unsafe {
		if policy.IsKnownSafe(cmd) {
			t.Errorf("expected IsKnownSafe=false for %q", cmd)
		}
	}
}

func TestIsKnownSafeShellWrapperTransparent(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	// Safe inner command
	if !policy.IsKnownSafe(`bash -c "git status"`) {
		t.Error("expected IsKnownSafe=true for bash -c 'git status'")
	}
	if !policy.IsKnownSafe(`bash -lc "ls -la && pwd"`) {
		t.Error("expected IsKnownSafe=true for bash -lc 'ls -la && pwd'")
	}
	// Unsafe inner command
	if policy.IsKnownSafe(`bash -c "rm -rf /"`) {
		t.Error("expected IsKnownSafe=false for bash -c 'rm -rf /'")
	}
	if policy.IsKnownSafe(`sh -c "git push origin main"`) {
		t.Error("expected IsKnownSafe=false for sh -c 'git push origin main'")
	}
}

func TestIsKnownSafeRejectsRedirections(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	if policy.IsKnownSafe(`bash -lc "ls > out.txt"`) {
		t.Error("expected IsKnownSafe=false for output redirection")
	}
}

func TestIsKnownSafeSedOnlyReadonly(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	if !policy.IsKnownSafe("sed -n 1,5p file.txt") {
		t.Error("expected safe: sed -n 1,5p")
	}
	if !policy.IsKnownSafe("sed -n 10p file.txt") {
		t.Error("expected safe: sed -n 10p")
	}
	if policy.IsKnownSafe("sed -i 's/old/new/g' file.txt") {
		t.Error("expected unsafe: sed -i (in-place)")
	}
}

func TestIsKnownSafeFindWithoutExec(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	if !policy.IsKnownSafe("find . -name '*.go'") {
		t.Error("expected safe: find without exec")
	}
	if policy.IsKnownSafe("find . -name file.txt -exec rm {} ;") {
		t.Error("expected unsafe: find -exec")
	}
	if policy.IsKnownSafe("find . -delete") {
		t.Error("expected unsafe: find -delete")
	}
}

func TestIsKnownSafeBase64Options(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	if !policy.IsKnownSafe("base64 file.txt") {
		t.Error("expected safe: base64 without output option")
	}
	if policy.IsKnownSafe("base64 -o out.bin file.txt") {
		t.Error("expected unsafe: base64 -o")
	}
	if policy.IsKnownSafe("base64 --output=out.bin file.txt") {
		t.Error("expected unsafe: base64 --output=")
	}
}

// ─── evaluateSingle: search commands not in safelist must Ask ────────────────

func TestEvaluateSearchCommandNotInSafelistAsks(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	// These search-type commands are NOT on the explicit safe list:
	// ag, ack, fd are unknown to isKnownSafeSegment.
	// find/rg with unsafe options fail IsKnownSafe and must Ask, not Allow.
	cases := []struct {
		cmd  string
		want Decision
	}{
		{"find . -exec rm {} ;", DecisionAsk},  // dangerous find option
		{"rg --pre=evil pattern", DecisionAsk}, // dangerous rg option
		{"ag foo bar", DecisionAsk},            // not in safelist → unknown → ask
		{"find . -delete", DecisionAsk},        // -delete is destructive
	}
	for _, tc := range cases {
		result := policy.Evaluate(tc.cmd)
		if result.Decision != tc.want {
			t.Errorf("Evaluate(%q): want %s, got %s (%s)", tc.cmd, tc.want, result.Decision, result.Reason)
		}
	}
}

func TestEvaluateReadCommandsAllowWithoutApproval(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	// Pure read/state commands must return Allow so the approval pipeline is skipped.
	cases := []string{
		"ls", "cat file.go", "head -n 5 file.txt",
		"echo hello", "pwd", "diff a.txt b.txt",
		"cd /tmp", "wc -l main.go",
	}
	for _, cmd := range cases {
		result := policy.Evaluate(cmd)
		if result.Decision != DecisionAllow {
			t.Errorf("Evaluate(%q): want Allow, got %s (%s)", cmd, result.Decision, result.Reason)
		}
	}
}

// ─── Wrapped destructive command tests ───────────────────────────────────────

func TestCommandPolicyDeniesWrappedDestructiveCommand(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	cases := []string{
		`bash -c "rm -rf /"`,
		`sh -c 'rm -rf /'`,
		`/bin/bash -c "rm -rf /*"`,
		`bash -lc "dd if=/dev/zero of=/dev/sda"`,
	}
	for _, cmd := range cases {
		result := policy.Evaluate(cmd)
		if result.Decision != DecisionDeny {
			t.Errorf("expected deny for %q, got %s", cmd, result.Decision)
		}
	}
}

func TestCommandPolicyAsksForWrappedAskCommand(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate(`bash -c "rm some_file.txt"`)
	if result.Decision != DecisionAsk {
		t.Errorf("expected ask for wrapped rm, got %s (%s)", result.Decision, result.Reason)
	}
}

func TestCommandPolicyAllowsWrappedSafeCommand(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate(`bash -c "git status"`)
	if result.Decision != DecisionAllow {
		t.Errorf("expected allow for wrapped git status, got %s (%s)", result.Decision, result.Reason)
	}
}

func TestCommandPolicyComposedDeniesIfAnySegmentDenied(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate(`bash -c "git status && rm -rf /"`)
	if result.Decision != DecisionDeny {
		t.Errorf("expected deny for composed command with rm -rf /, got %s", result.Decision)
	}
}

func TestCommandPolicyComposedAsksIfAnySegmentAsk(t *testing.T) {
	policy := NewDefaultCommandPolicy()
	result := policy.Evaluate(`bash -c "git status && rm file.txt"`)
	if result.Decision != DecisionAsk {
		t.Errorf("expected ask for composed command with rm, got %s", result.Decision)
	}
}

func TestErrorForPermissionResultReturnsExpectedErrorTypes(t *testing.T) {
	if err := ErrorForPermissionResult(types.AllowWithDecisionReason("ok", nil), "fallback"); err != nil {
		t.Fatalf("expected nil error for allow, got %v", err)
	}
	if err := ErrorForPermissionResult(types.Passthrough(nil), "fallback"); err != nil {
		t.Fatalf("expected nil error for passthrough, got %v", err)
	}
	var askedResult *ApprovalRequiredError
	if !errors.As(ErrorForPermissionResult(types.Ask("approval required"), "fallback"), &askedResult) {
		t.Fatal("expected approval required error")
	}
	var deniedResult *PermissionDeniedError
	if !errors.As(ErrorForPermissionResult(types.Deny("denied"), "fallback"), &deniedResult) {
		t.Fatal("expected permission denied error")
	}
}

// ─── Context.ResolvePath ──────────────────────────────────────────────────────

func TestContextResolvePath_EmptyPathReturnsError(t *testing.T) {
	ctx := Context{WorkspaceRoot: "/workspace"}
	_, err := ctx.ResolvePath("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestContextResolvePath_RelativeWithWorkingDirectory(t *testing.T) {
	workingDir := filepath.FromSlash("/home/user/project")
	ctx := Context{WorkingDirectory: workingDir}
	got, err := ctx.ResolvePath("src/main.go")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want, err := filepath.Abs(filepath.Join(workingDir, filepath.FromSlash("src/main.go")))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if got != want {
		t.Errorf("unexpected resolved path: %q", got)
	}
}

func TestContextResolvePath_AbsolutePathWithoutWorkspace(t *testing.T) {
	ctx := Context{}
	raw := filepath.FromSlash("/tmp/file.txt")
	got, err := ctx.ResolvePath(raw)
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestContextResolvePath_WhitespaceOnlyPathReturnsError(t *testing.T) {
	ctx := Context{}
	_, err := ctx.ResolvePath("   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only path")
	}
}

// ─── ResolveToolPermission ────────────────────────────────────────────────────

func TestResolveToolPermission_NilCheckerReturnsError(t *testing.T) {
	req := PermissionRequest{
		ToolName: "bash",
		Access:   AccessExecute,
		Command:  "git status",
		Scope:    ApprovalScopeToolCall,
	}
	_, err := ResolveToolPermission(context.Background(), nil, req, ToolPermissionOptions{})
	if err == nil {
		t.Fatal("expected error when checker is nil")
	}
}

func TestResolveToolPermission_DelegatesToChecker(t *testing.T) {
	req := PermissionRequest{
		ToolName: "bash",
		Access:   AccessExecute,
		Command:  "go build ./...",
		Scope:    ApprovalScopeToolCall,
	}
	var captured types.ToolPermissionRequest
	checker := types.CanUseToolFn(func(_ context.Context, r types.ToolPermissionRequest) types.PermissionResult {
		captured = r
		return types.AllowWithDecisionReason("test allow", nil)
	})
	result, err := ResolveToolPermission(context.Background(), checker, req, ToolPermissionOptions{
		SessionID:        "sess-42",
		WorkingDirectory: "/proj",
	})
	if err != nil {
		t.Fatalf("ResolveToolPermission: %v", err)
	}
	if result.Behavior != types.PermissionBehaviorAllow {
		t.Errorf("expected allow, got %s", result.Behavior)
	}
	if captured.ToolName != "bash" {
		t.Errorf("checker received wrong tool name: %q", captured.ToolName)
	}
	if string(captured.SessionID) != "sess-42" {
		t.Errorf("checker received wrong session: %q", captured.SessionID)
	}
	if captured.WorkingDirectory != "/proj" {
		t.Errorf("checker received wrong working dir: %q", captured.WorkingDirectory)
	}
}

// ─── FilesystemPolicy integration ────────────────────────────────────────────

func TestFilesystemPolicy_NilFallsBackToDefault(t *testing.T) {
	var p *FilesystemPolicy
	decision, err := p.EvaluatePath(Context{}, "/etc/hosts", AccessWrite)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionDeny {
		t.Errorf("nil policy must deny /etc/hosts write, got %s", decision.Decision)
	}
}

func TestFilesystemPolicyAllowsWriteToUnprotectedPath(t *testing.T) {
	tmp := t.TempDir()
	policy := NewDefaultFilesystemPolicy()
	decision, err := policy.EvaluatePath(Context{}, filepath.Join(tmp, "output.txt"), AccessWrite)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionAllow {
		t.Errorf("expected allow for write to temp dir, got %s: %s", decision.Decision, decision.Reason)
	}
}

func TestFilesystemPolicyDeniesReadFromBootPath(t *testing.T) {
	policy := NewDefaultFilesystemPolicy()
	decision, err := policy.EvaluatePath(Context{}, "/boot/grub/grub.cfg", AccessRead)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionDeny {
		t.Errorf("expected deny for /boot read, got %s", decision.Decision)
	}
}

func TestFilesystemPolicyDeniesWriteToMultipleProtectedPrefixes(t *testing.T) {
	policy := NewDefaultFilesystemPolicy()
	protected := []string{"/etc/passwd", "/usr/bin/env", "/sbin/init", "/bin/bash", "/boot/vmlinuz"}
	for _, p := range protected {
		decision, err := policy.EvaluatePath(Context{}, p, AccessWrite)
		if err != nil {
			t.Fatalf("EvaluatePath(%q): %v", p, err)
		}
		if decision.Decision != DecisionDeny {
			t.Errorf("expected deny for write to %q, got %s", p, decision.Decision)
		}
	}
}

func TestFilesystemPolicyUnsupportedAccessKindReturnsError(t *testing.T) {
	policy := NewDefaultFilesystemPolicy()
	_, err := policy.EvaluatePath(Context{}, "/tmp/file.txt", AccessKind("chmod"))
	if err == nil {
		t.Fatal("expected error for unsupported access kind")
	}
}
