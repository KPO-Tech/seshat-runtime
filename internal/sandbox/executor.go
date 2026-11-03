package sandbox

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Executor is the interface all sandbox backends implement.
// The engine routes bash/shell execution through the active Executor.
//
// Backends:
//   - SandboxdExecutor — runs commands through the seshat-sandboxd sidecar
//     (rust/sandboxd), which owns OS-level confinement and resource limits
//   - MicroVMExecutor  — runs commands in a Linux microVM per project, through msb
//
// With none of them, the bash tool runs commands itself, under Landlock where the host has it.
// Selection at startup via ExecutorConfig.Kind. Tools that are read-only
// (file read, glob, grep) bypass the Executor and go directly to the host FS.
type Executor interface {
	// Run executes a command inside the sandbox and streams its output.
	// Implementations must respect ctx cancellation and RunRequest.Timeout.
	Run(ctx context.Context, req RunRequest) (RunResult, error)

	// Kind identifies which backend this is.
	Kind() EnvironmentKind

	// Healthy returns nil when the executor is ready to accept work.
	// A non-nil error means the backend is unavailable. The engine calls this once at startup.
	Healthy(ctx context.Context) error
}

// RunRequest is the input to Executor.Run.
type RunRequest struct {
	// Command is the shell command string to execute.
	// Passed as-is to the interpreter defined by Shell.
	Command string

	// Shell specifies the interpreter. Empty means the backend's default.
	Shell []string

	// Env is a set of KEY=VALUE pairs injected into the command environment
	// on top of the executor's base environment.
	Env map[string]string

	// WorkDir is the host working directory for the command. A microVM maps it into
	// the guest, and mounts it if it is a new project.
	WorkDir string

	// Stdin is connected to the command's standard input (may be nil).
	Stdin io.Reader

	// Timeout is the maximum duration for the command.
	// Zero means no timeout beyond the parent context deadline.
	Timeout time.Duration

	// OnOutput, when set, receives output as the command produces it, with the
	// stream ("stdout" or "stderr"). The RunResult still holds all of it. A remote
	// executor may hand output over only when the command ends.
	OnOutput func(stream string, data []byte)
}

// RunResult is the output of Executor.Run.
type RunResult struct {
	// Stdout and Stderr capture the command output.
	// Empty when Background=true.
	Stdout string
	Stderr string

	// ExitCode is the process exit code. Non-zero indicates failure.
	ExitCode int

	// Duration is the wall-clock time the command took.
	Duration time.Duration

	// Cwd is the working directory the command left the environment in.
	// Only populated by executors backed by a persistent shell (e.g.
	// RemoteExecutor) where a `cd` genuinely changes state for the next
	// command; empty for one-shot-per-call executors, where WorkDir never changes
	// out from under the caller.
	Cwd string
}

// ExecutorConfig is the configuration passed to NewExecutor at startup.
type ExecutorConfig struct {
	// Kind selects the backend.
	Kind EnvironmentKind

	// Sandboxd holds sidecar configuration.
	// Only used when Kind == EnvironmentSandboxed.
	Sandboxd SandboxdConfig

	// MicroVM configures the microVM backend when Kind is EnvironmentMicroVM.
	MicroVM MicroVMConfig
}

// NewExecutor creates the Executor selected by cfg.Kind.
// Returns an error when the requested backend is unavailable.
func NewExecutor(cfg ExecutorConfig) (Executor, error) {
	switch cfg.Kind {
	case EnvironmentSandboxed:
		return newSandboxdExecutor(cfg.Sandboxd)
	case EnvironmentMicroVM:
		return NewMicroVMExecutor(cfg.MicroVM)
	default:
		return nil, fmt.Errorf("sandbox: no executor for %q", cfg.Kind)
	}
}
