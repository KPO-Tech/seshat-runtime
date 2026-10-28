package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/envfilter"
)

// SandboxdConfig configures the seshat-sandboxd backend: the Rust sidecar that
// owns confinement and execution.
//
// The binary is a separate process on purpose. The alternative — a cgo library —
// would make `go build` depend on a C toolchain on every platform and would let
// a fault in the sandbox take the runtime down with it. A process boundary costs
// one extra binary in the release and buys a boundary that survives a crash.
type SandboxdConfig struct {
	// BinaryPath is the seshat-sandboxd executable. Required.
	BinaryPath string

	// Shell is the interpreter used when a RunRequest does not name one,
	// e.g. []string{"/bin/bash", "-c"}. Empty lets the sidecar pick the
	// platform default (/bin/sh on Unix, cmd.exe /C on Windows).
	Shell []string

	// MaxOutputBytes caps each stream *inside* the sidecar, protecting this
	// process from a runaway command. Zero means no cap: the tools' own
	// output handling applies, exactly as on the unsandboxed path.
	MaxOutputBytes int64

	// DefaultTimeout applies when RunRequest.Timeout is zero. Zero means no
	// deadline beyond the caller's context.
	DefaultTimeout time.Duration

	// HandshakeTimeout bounds startup plus the hello exchange.
	HandshakeTimeout time.Duration
}

// DefaultSandboxdConfig returns the configuration used when an embedder enables
// the backend without tuning anything.
func DefaultSandboxdConfig() SandboxdConfig {
	return SandboxdConfig{
		HandshakeTimeout: 10 * time.Second,
	}
}

// SandboxdExecutor runs commands through the seshat-sandboxd sidecar.
//
// One sidecar per executor, one run at a time: the protocol is a single
// conversation, and a sandbox that interleaves commands makes a violation
// report ambiguous about which command caused it.
type SandboxdExecutor struct {
	cfg SandboxdConfig

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *lockedBuffer
	ack    *sandboxdHelloAck
	closed bool

	runSeq atomic.Uint64
}

// lockedBuffer collects the sidecar's stderr without racing the os/exec copier.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Diagnostics only: keep the beginning, which is where the cause is.
	if b.buf.Len() < 32<<10 {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// NewSandboxdExecutor returns an executor that starts its sidecar lazily, on the
// first Healthy or Run call.
func NewSandboxdExecutor(cfg SandboxdConfig) (*SandboxdExecutor, error) {
	cfg.BinaryPath = strings.TrimSpace(cfg.BinaryPath)
	if cfg.BinaryPath == "" {
		return nil, errors.New("sandboxd: BinaryPath is required")
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultSandboxdConfig().HandshakeTimeout
	}
	return &SandboxdExecutor{cfg: cfg}, nil
}

// newSandboxdExecutor is the constructor NewExecutor uses, kept private so the
// public entry point stays the configuration-driven one.
func newSandboxdExecutor(cfg SandboxdConfig) (Executor, error) {
	return NewSandboxdExecutor(cfg)
}

func (e *SandboxdExecutor) Kind() EnvironmentKind { return EnvironmentSandboxed }

// Status reports what the sidecar said it can enforce, or nil before a
// successful handshake.
func (e *SandboxdExecutor) Status() *sandboxdHelloAck {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ack
}

// Healthy starts the sidecar if needed and confirms the handshake.
//
// It fails when the binary is missing, when the protocol major version differs,
// or when the sidecar reports that it confines nothing. In each case running is
// refused; there is no fall back to an unconfined path, because a sandbox that
// silently degrades is the failure mode this backend exists to remove.
func (e *SandboxdExecutor) Healthy(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startLocked(ctx)
}

func (e *SandboxdExecutor) startLocked(ctx context.Context) error {
	if e.closed {
		return errors.New("sandboxd: executor is closed")
	}
	if e.cmd != nil {
		return nil
	}

	cmd := exec.Command(e.cfg.BinaryPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("sandboxd: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("sandboxd: stdout pipe: %w", err)
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sandboxd: start %s: %w", e.cfg.BinaryPath, err)
	}
	e.cmd = cmd
	e.stdin = stdin
	e.stdout = stdout
	e.stderr = stderr

	if err := writeSandboxdFrame(e.stdin, &sandboxdFrame{
		Type: sandboxdTypeHello,
		Hello: &sandboxdHello{
			ProtocolMajor: sandboxdProtocolMajor,
			ProtocolMinor: sandboxdProtocolMinor,
			ClientVersion: "seshat-runtime",
		},
	}); err != nil {
		e.stopLocked()
		return err
	}

	ack, err := e.readHelloAckLocked(ctx)
	if err != nil {
		e.stopLocked()
		return err
	}
	if ack.ProtocolMajor != sandboxdProtocolMajor {
		e.stopLocked()
		return fmt.Errorf("sandboxd: protocol mismatch: sidecar speaks v%d.%d, this runtime speaks v%d.%d",
			ack.ProtocolMajor, ack.ProtocolMinor, sandboxdProtocolMajor, sandboxdProtocolMinor)
	}
	if ack.Level == sandboxdLevelNone {
		e.stopLocked()
		return fmt.Errorf("sandboxd: sidecar reports no confinement (%s)", strings.Join(ack.DegradedReasons, "; "))
	}
	if ack.Level != sandboxdLevelFull {
		slog.Warn("sandboxd: running with a degraded sandbox",
			"level", ack.Level,
			"missing", strings.Join(ack.DegradedReasons, "; "),
			"capabilities", strings.Join(ack.Capabilities, ","))
	}

	e.ack = ack
	return nil
}

// readHelloAckLocked reads the handshake answer, giving up after the handshake
// timeout. The read itself cannot be interrupted, so a timeout kills the process
// to unblock it.
func (e *SandboxdExecutor) readHelloAckLocked(ctx context.Context) (*sandboxdHelloAck, error) {
	type outcome struct {
		ack *sandboxdHelloAck
		err error
	}
	done := make(chan outcome, 1)

	go func() {
		frame, err := readSandboxdFrame(e.stdout)
		if err != nil {
			done <- outcome{err: fmt.Errorf("sandboxd: handshake read: %w", err)}
			return
		}
		switch {
		case frame.Type == sandboxdTypeError && frame.Error != nil:
			done <- outcome{err: fmt.Errorf("sandboxd: handshake refused (%s): %s", frame.Error.Code, frame.Error.Message)}
		case frame.Type != sandboxdTypeHelloAck || frame.HelloAck == nil:
			done <- outcome{err: fmt.Errorf("sandboxd: expected hello_ack, got %q", frame.Type)}
		default:
			done <- outcome{ack: frame.HelloAck}
		}
	}()

	timer := time.NewTimer(e.cfg.HandshakeTimeout)
	defer timer.Stop()

	select {
	case got := <-done:
		if got.err != nil {
			return nil, got.err
		}
		return got.ack, nil
	case <-ctx.Done():
		e.killLocked()
		return nil, ctx.Err()
	case <-timer.C:
		e.killLocked()
		return nil, fmt.Errorf("sandboxd: handshake timed out after %s", e.cfg.HandshakeTimeout)
	}
}

// Run executes req through the sidecar.
func (e *SandboxdExecutor) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.startLocked(ctx); err != nil {
		return RunResult{}, err
	}

	// The protocol has no stdin channel yet. Refusing is the only honest
	// answer: quietly running a command without the input it expects would
	// look like a command that failed on its own.
	if req.Stdin != nil {
		return RunResult{}, errors.New("sandboxd: stdin is not supported by the sandboxed backend yet")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = e.cfg.DefaultTimeout
	}
	shell := req.Shell
	if len(shell) == 0 {
		shell = e.cfg.Shell
	}
	if shell == nil {
		// Arrays are always encoded, never null: the other side decodes into
		// a sequence, and "absent" and "empty" must not be two shapes.
		shell = []string{}
	}
	maxOutput := e.cfg.MaxOutputBytes
	if maxOutput < 0 {
		maxOutput = 0
	}

	runID := fmt.Sprintf("%d-%d", os.Getpid(), e.runSeq.Add(1))
	started := time.Now()

	if err := writeSandboxdFrame(e.stdin, &sandboxdFrame{
		Type: sandboxdTypeRun,
		Run: &sandboxdRun{
			RunID:          runID,
			Command:        req.Command,
			Shell:          shell,
			WorkDir:        req.WorkDir,
			Env:            e.environment(req.Env),
			AllowNetwork:   false,
			TimeoutMS:      uint64(timeout.Milliseconds()),
			MaxOutputBytes: uint64(maxOutput),
		},
	}); err != nil {
		e.stopLocked()
		return RunResult{}, err
	}

	// A cancel is the only way to stop a run that is already executing, and the
	// reader is blocked while it streams: send it from here.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = writeSandboxdFrame(e.stdin, &sandboxdFrame{
				Type:   sandboxdTypeCancel,
				Cancel: &sandboxdCancel{RunID: runID},
			})
		case <-stopWatch:
		}
	}()

	var stdout, stderr bytes.Buffer
	for {
		frame, err := readSandboxdFrame(e.stdout)
		if err != nil {
			e.stopLocked()
			return RunResult{}, fmt.Errorf("sandboxd: run %s: %w (%s)", runID, err, strings.TrimSpace(e.stderr.String()))
		}

		switch frame.Type {
		case sandboxdTypeChunk:
			if frame.Chunk == nil {
				continue
			}
			data, decErr := base64.StdEncoding.DecodeString(frame.Chunk.Data)
			if decErr != nil {
				return RunResult{}, fmt.Errorf("sandboxd: run %s: decode %s chunk: %w", runID, frame.Chunk.Stream, decErr)
			}
			if frame.Chunk.Stream == sandboxdStreamStderr {
				stderr.Write(data)
			} else {
				stdout.Write(data)
			}

		case sandboxdTypeExit:
			if frame.Exit == nil {
				return RunResult{}, fmt.Errorf("sandboxd: run %s: exit frame carried no payload", runID)
			}
			if len(frame.Exit.Violations) > 0 {
				slog.Warn("sandboxd: confinement refused something",
					"run", runID, "violations", strings.Join(frame.Exit.Violations, "; "))
			}
			if ctx.Err() != nil {
				return RunResult{}, ctx.Err()
			}
			return RunResult{
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: int(frame.Exit.ExitCode),
				Duration: time.Since(started),
			}, nil

		case sandboxdTypeError:
			if frame.Error != nil {
				return RunResult{}, fmt.Errorf("sandboxd: run %s: %s: %s", runID, frame.Error.Code, frame.Error.Message)
			}
			return RunResult{}, fmt.Errorf("sandboxd: run %s: unspecified error", runID)

		default:
			// Unknown but well-formed frames are ignored so that a newer
			// sidecar can add telemetry without breaking an older runtime.
			continue
		}
	}
}

// environment builds the child environment explicitly: the sidecar clears its
// own, so nothing the runtime holds can reach a model-issued command unless it
// is named here.
func (e *SandboxdExecutor) environment(extra map[string]string) map[string]string {
	env := make(map[string]string, len(extra)+16)
	for _, kv := range envfilter.Environ() {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			env[kv[:idx]] = kv[idx+1:]
		}
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// Close stops the sidecar. It is idempotent.
func (e *SandboxdExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.stopLocked()
	return nil
}

func (e *SandboxdExecutor) stopLocked() {
	if e.stdin != nil {
		_ = e.stdin.Close()
	}
	cmd := e.cmd
	e.cmd = nil
	e.stdin = nil
	e.stdout = nil
	e.ack = nil
	if cmd == nil {
		return
	}
	if cmd.Process == nil {
		return
	}

	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		e.killLocked()
		<-waited
	}
}

func (e *SandboxdExecutor) killLocked() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
}

// unavailableExecutor refuses every run, for the case where a backend was
// requested and could not be brought up.
//
// This exists so that asking for a sandbox and not getting one is an error the
// user sees, not a command that quietly ran unconfined on the host.
type unavailableExecutor struct {
	kind EnvironmentKind
	err  error
}

// NewUnavailableExecutor returns an Executor that fails every call with err.
func NewUnavailableExecutor(kind EnvironmentKind, err error) Executor {
	return &unavailableExecutor{kind: kind, err: err}
}

func (e *unavailableExecutor) Run(context.Context, RunRequest) (RunResult, error) {
	return RunResult{}, e.err
}

func (e *unavailableExecutor) Kind() EnvironmentKind { return e.kind }

func (e *unavailableExecutor) Healthy(context.Context) error { return e.err }
