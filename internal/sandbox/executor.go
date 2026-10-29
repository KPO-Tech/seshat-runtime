package sandbox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

// Executor is the interface all sandbox backends implement.
// The engine routes bash/shell execution through the active Executor.
//
// Backends:
//   - NoopExecutor     — runs commands directly on the host OS (default, no isolation)
//   - DockerExecutor   — runs commands inside an isolated Docker container
//   - SandboxdExecutor — runs commands through the seshat-sandboxd sidecar
//     (rust/sandboxd), which owns OS-level confinement and resource limits
//
// Selection at startup via ExecutorConfig.Kind. Tools that are read-only
// (file read, glob, grep) bypass the Executor and go directly to the host FS.
type Executor interface {
	// Run executes a command inside the sandbox and streams its output.
	// Implementations must respect ctx cancellation and RunRequest.Timeout.
	Run(ctx context.Context, req RunRequest) (RunResult, error)

	// Kind identifies which backend this is.
	Kind() EnvironmentKind

	// Healthy returns nil when the executor is ready to accept work.
	// A non-nil error means the backend is unavailable (Dagger engine down,
	// Docker socket missing, etc). The engine calls this once at startup.
	Healthy(ctx context.Context) error
}

// RunRequest is the input to Executor.Run.
type RunRequest struct {
	// Command is the shell command string to execute.
	// Passed as-is to the interpreter defined by Shell.
	Command string

	// Shell specifies the interpreter (default: /bin/sh -c).
	// Ignored by DockerExecutor which always uses sh inside the container.
	Shell []string

	// Env is a set of KEY=VALUE pairs injected into the command environment
	// on top of the executor's base environment.
	Env map[string]string

	// WorkDir is the host working directory for the command. NoopExecutor
	// and RemoteExecutor use it directly as the process's cwd. DockerExecutor
	// additionally treats it as the bind-mount source for the environment's
	// container-side working directory (see DockerExecutorConfig.WorkDir) —
	// fixed at the environment's first Run call and reused on every
	// subsequent call to the same Docker.EnvironmentID.
	WorkDir string

	// Stdin is connected to the command's standard input (may be nil).
	Stdin io.Reader

	// Timeout is the maximum duration for the command.
	// Zero means no timeout beyond the parent context deadline.
	Timeout time.Duration

	// Background requests a fire-and-forget execution (no output captured).
	// Used for long-running processes (servers, watchers).
	Background bool

	// Docker-specific extensions — ignored by NoopExecutor.
	Docker *DockerRunOptions
}

// DockerRunOptions carries Docker-specific per-run parameters.
// These are only meaningful when the active Executor is a DockerExecutor.
type DockerRunOptions struct {
	// EnvironmentID selects a named, persistent environment (one long-lived
	// container reused across calls via `docker exec`). Empty means the
	// default session environment.
	//
	// Port publishing is NOT a per-call option here — Docker only allows
	// publishing ports at container-creation time, not on an already-running
	// container, and an environment's container may already exist by the
	// time a given RunRequest is issued. See
	// DockerExecutorConfig.PublishPorts, which is declared once for the
	// environment instead.
	EnvironmentID string
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
	// command; empty for one-shot-per-call executors like NoopExecutor/
	// DockerExecutor where WorkDir never changes out from under the caller.
	Cwd string

	// Endpoints maps container port numbers to "host:port" strings — where
	// on the host a service listening on that container port is reachable.
	// Populated on every call by DockerExecutor when
	// DockerExecutorConfig.PublishPorts is non-empty (the mapping is fixed
	// at environment creation, so it's the same on every call for a given
	// environment, not something a specific RunRequest controls).
	Endpoints map[int]string
}

// ExecutorConfig is the configuration passed to NewExecutor at startup.
type ExecutorConfig struct {
	// Kind selects the backend. Defaults to EnvironmentLocal (noop).
	Kind EnvironmentKind

	// Docker holds Docker-specific configuration.
	// Only used when Kind == EnvironmentDocker.
	Docker DockerExecutorConfig

	// Sandboxd holds sidecar configuration.
	// Only used when Kind == EnvironmentSandboxed.
	Sandboxd SandboxdConfig
}

// DockerExecutorConfig holds static configuration for the DockerExecutor.
// All fields have sensible defaults and can be overridden by the operator.
type DockerExecutorConfig struct {
	// BaseImage is the OCI image used for new environments.
	// Default: "ubuntu:24.04"
	BaseImage string

	// SetupCommands are shell commands run once when an environment's
	// container is first created.
	// Example: ["apt-get update -y", "apt-get install -y python3 nodejs"]
	SetupCommands []string

	// WorkDir is the working directory inside the container that
	// RunRequest.WorkDir (a host path) is bind-mounted to.
	// Default: "/workdir"
	WorkDir string

	// Env is a set of KEY=VALUE pairs always injected into the container.
	Env map[string]string

	// NetworkAccess controls whether the container can reach the network.
	// Default: true (default Docker bridge network).
	// Set to false to run with --network=none.
	NetworkAccess bool

	// MemoryLimitMB caps container memory (docker run --memory).
	// Default: 2048 (2 GiB). 0 means no additional default is applied
	// (Docker's own daemon-level default, if any, still applies).
	MemoryLimitMB int

	// CPULimit caps container CPU (docker run --cpus). Fractional values
	// are allowed (e.g. 1.5). Default: 2.0. 0 means no limit is passed.
	CPULimit float64

	// DockerBinary is the path to the docker CLI. Default: "docker"
	// (resolved via PATH).
	DockerBinary string

	// Runtime selects a non-default OCI runtime (docker run --runtime=…),
	// e.g. "runsc" for gVisor. Empty (default) uses Docker's own default
	// runtime — plain runc, which shares the host kernel with the
	// container. A stronger isolation runtime like gVisor must already be
	// installed and registered with the Docker daemon separately (this is
	// an operator/host setup step, not something DockerExecutor can do) —
	// if Runtime is set but not registered, Healthy(ctx) fails rather than
	// silently falling back to running without it, so an explicit request
	// for stronger isolation never gets silently downgraded.
	Runtime string

	// PublishPorts lists container ports to publish to random host ports
	// when an environment's container is created (docker run -p). Useful
	// for e.g. previewing a dev server an agent starts inside the sandbox.
	// Fixed for the lifetime of an environment — declared here rather than
	// per RunRequest because Docker only allows publishing ports at
	// container-creation time. The actual host-side ports are reported back
	// via RunResult.Endpoints on every call to that environment.
	PublishPorts []int

	// TrackFileChanges enables git-tracked snapshots of RunRequest.WorkDir
	// (the host directory bind-mounted into the environment) after every
	// Run call that actually changed something. Default: false — this is an
	// audit/undo convenience feature, not a security property, and requires
	// git on the HOST (not inside the container). If true and git isn't
	// found on the host, Healthy(ctx) fails rather than silently tracking
	// nothing — same "don't silently downgrade a requested capability"
	// contract as Runtime.
	//
	// Snapshots go into a separate, bare git repository — NOT the
	// project's own .git, if it has one — keyed by a hash of the absolute
	// WorkDir path (stable across environment/container restarts for the
	// same directory, see HistoryDir). The project's own .git, if present,
	// is excluded from what gets snapshotted.
	TrackFileChanges bool

	// HistoryDir is the base directory shadow tracking repos are stored
	// under when TrackFileChanges is true. Default (DefaultDockerConfig):
	// os.UserConfigDir()/seshat/sandbox-history.
	HistoryDir string

	// GitBinary is the path to the git CLI used for TrackFileChanges.
	// Default: "git" (resolved via PATH).
	GitBinary string
}

// DefaultDockerConfig returns a DockerExecutorConfig with sensible,
// security-conscious defaults — callers only need to override what they
// actually want different.
func DefaultDockerConfig() DockerExecutorConfig {
	return DockerExecutorConfig{
		BaseImage:     "ubuntu:24.04",
		WorkDir:       "/workdir",
		NetworkAccess: true,
		MemoryLimitMB: 2048,
		CPULimit:      2.0,
		DockerBinary:  "docker",
		HistoryDir:    defaultHistoryDir(),
		GitBinary:     "git",
	}
}

func defaultHistoryDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "seshat", "sandbox-history")
}

// dockerExecutorCache reuses a single *DockerExecutor across calls with the
// same DockerExecutorConfig, instead of constructing (and orphan-reaping) a
// brand new one on every call. This matters because a host embedding the SDK
// as a library can end up calling NewExecutor far more often than "once at
// process startup", which newDockerExecutor's own doc comment assumes:
// seshat-backend rebuilds its whole sdk.Client (and therefore the bash tool,
// and therefore this) fresh on every single chat turn. Without this cache,
// every turn paid a `docker ps -a` (orphan reap) subprocess spawn for
// nothing - the previous instance built seconds earlier had already reaped
// everything there was to reap in this process's lifetime.
var (
	dockerExecutorCacheMu  sync.Mutex
	dockerExecutorCacheCfg DockerExecutorConfig
	dockerExecutorCached   *DockerExecutor
)

// NewExecutor creates the Executor selected by cfg.Kind.
// Returns an error when the requested backend is unavailable.
func NewExecutor(cfg ExecutorConfig) (Executor, error) {
	switch cfg.Kind {
	case EnvironmentSandboxed:
		return newSandboxdExecutor(cfg.Sandboxd)
	case EnvironmentDocker:
		dockerExecutorCacheMu.Lock()
		defer dockerExecutorCacheMu.Unlock()
		if dockerExecutorCached != nil && reflect.DeepEqual(dockerExecutorCacheCfg, cfg.Docker) {
			return dockerExecutorCached, nil
		}
		executor, err := newDockerExecutor(cfg.Docker)
		if err != nil {
			return nil, err
		}
		dockerExecutorCached = executor
		dockerExecutorCacheCfg = cfg.Docker
		return executor, nil
	default:
		return NewNoopExecutor(), nil
	}
}
