package sandbox

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake sidecar is this test binary, re-executed with a role in its
// environment: no second toolchain, no build step, no port to bind.
const fakeSidecarRoleEnv = "SESHAT_SANDBOXD_TEST_ROLE"

func TestMain(m *testing.M) {
	if role := os.Getenv(fakeSidecarRoleEnv); role != "" {
		os.Exit(runFakeSidecar(role))
	}
	os.Exit(m.Run())
}

func runFakeSidecar(role string) int {
	for {
		frame, err := readSandboxdFrame(os.Stdin)
		if err != nil {
			return 0
		}

		switch frame.Type {
		case sandboxdTypeHello:
			switch role {
			case "silent":
				// Never answers, so the client's handshake timeout is what ends it.
				time.Sleep(10 * time.Second)
				return 0
			case "error":
				_ = writeSandboxdFrame(os.Stdout, &sandboxdFrame{
					Type:  sandboxdTypeError,
					Error: &sandboxdErrorBody{Code: "refused", Message: "no confinement today"},
				})
				continue
			}

			major := uint32(sandboxdProtocolMajor)
			if role == "mismatch" {
				major = sandboxdProtocolMajor + 1
			}
			level := sandboxdLevelFull
			var reasons []string
			switch role {
			case "degraded":
				level = sandboxdLevelDegraded
				reasons = []string{"network confinement unavailable"}
			case "none":
				level = sandboxdLevelNone
				reasons = []string{"no OS backend"}
			}
			_ = writeSandboxdFrame(os.Stdout, &sandboxdFrame{
				Type: sandboxdTypeHelloAck,
				HelloAck: &sandboxdHelloAck{
					ProtocolMajor:   major,
					ProtocolMinor:   sandboxdProtocolMinor,
					ServerVersion:   "fake-0",
					Level:           level,
					Capabilities:    []string{"limits"},
					DegradedReasons: reasons,
				},
			})

		case sandboxdTypeRun:
			if frame.Run == nil {
				return 1
			}
			out := base64.StdEncoding.EncodeToString([]byte(frame.Run.Command))
			_ = writeSandboxdFrame(os.Stdout, &sandboxdFrame{
				Type:  sandboxdTypeChunk,
				Chunk: &sandboxdChunk{RunID: frame.Run.RunID, Stream: sandboxdStreamStdout, Data: out},
			})
			_ = writeSandboxdFrame(os.Stdout, &sandboxdFrame{
				Type: sandboxdTypeExit,
				Exit: &sandboxdExit{RunID: frame.Run.RunID, ExitCode: 0, StdoutBytes: uint64(len(frame.Run.Command))},
			})
		}
	}
}

func newFakeExecutor(t *testing.T, role string) *SandboxdExecutor {
	t.Helper()
	t.Setenv(fakeSidecarRoleEnv, role)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cfg := DefaultSandboxdConfig()
	cfg.BinaryPath = exe
	cfg.HandshakeTimeout = 2 * time.Second

	executor, err := NewSandboxdExecutor(cfg)
	if err != nil {
		t.Fatalf("NewSandboxdExecutor: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	return executor
}

func TestSandboxdExecutorRunsACommandThroughTheSidecar(t *testing.T) {
	executor := newFakeExecutor(t, "full")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := executor.Run(ctx, RunRequest{Command: "echo hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != "echo hello" {
		t.Fatalf("stdout = %q, want the command echoed back by the fake sidecar", result.Stdout)
	}
	if executor.Kind() != EnvironmentSandboxed {
		t.Fatalf("Kind() = %q", executor.Kind())
	}
}

func TestSandboxdExecutorReportsADegradedSidecarButRuns(t *testing.T) {
	executor := newFakeExecutor(t, "degraded")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := executor.Healthy(ctx); err != nil {
		t.Fatalf("a degraded sidecar is usable, got %v", err)
	}
	status := executor.Status()
	if status == nil || status.Level != sandboxdLevelDegraded {
		t.Fatalf("Status() = %+v, want the degraded level", status)
	}
	if len(status.DegradedReasons) == 0 {
		t.Fatal("a degraded sidecar must say what it is missing")
	}
}

func TestSandboxdExecutorFailsClosedWhenTheSidecarConfinesNothing(t *testing.T) {
	executor := newFakeExecutor(t, "none")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := executor.Healthy(ctx); err == nil {
		t.Fatal("a sidecar reporting no confinement must be refused, not accepted")
	}
	// And the refusal holds for the run too: there is no unconfined fallback.
	if _, err := executor.Run(ctx, RunRequest{Command: "echo hello"}); err == nil {
		t.Fatal("Run must refuse once the handshake was refused")
	}
}

func TestSandboxdExecutorRejectsAProtocolMismatch(t *testing.T) {
	executor := newFakeExecutor(t, "mismatch")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := executor.Healthy(ctx)
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("Healthy() = %v, want a protocol mismatch", err)
	}
}

func TestSandboxdExecutorReportsAHandshakeRefusal(t *testing.T) {
	executor := newFakeExecutor(t, "error")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := executor.Healthy(ctx)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("Healthy() = %v, want the sidecar's refusal", err)
	}
}

func TestSandboxdExecutorGivesUpOnASilentSidecar(t *testing.T) {
	executor := newFakeExecutor(t, "silent")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := executor.Healthy(ctx)
	if err == nil {
		t.Fatal("a sidecar that never answers must fail the handshake")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handshake took %s, want the 2s timeout to end it", elapsed)
	}
}

func TestSandboxdExecutorRefusesWhenTheBinaryIsMissing(t *testing.T) {
	cfg := DefaultSandboxdConfig()
	cfg.BinaryPath = filepath.Join(t.TempDir(), "not-a-sidecar")
	executor, err := NewSandboxdExecutor(cfg)
	if err != nil {
		t.Fatalf("NewSandboxdExecutor: %v", err)
	}
	defer func() { _ = executor.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := executor.Healthy(ctx); err == nil {
		t.Fatal("a missing binary must fail, not be ignored")
	}
}

func TestNewSandboxdExecutorRequiresABinaryPath(t *testing.T) {
	if _, err := NewSandboxdExecutor(SandboxdConfig{}); err == nil {
		t.Fatal("an empty BinaryPath must be rejected at construction")
	}
}

func TestSandboxdExecutorRefusesStdinRatherThanRunningWithoutIt(t *testing.T) {
	executor := newFakeExecutor(t, "full")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := executor.Run(ctx, RunRequest{Command: "cat", Stdin: strings.NewReader("input")})
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("Run() = %v, want a refusal about stdin", err)
	}
}

func TestUnavailableExecutorRefusesEveryRun(t *testing.T) {
	executor := NewUnavailableExecutor(EnvironmentSandboxed, os.ErrNotExist)

	if _, err := executor.Run(context.Background(), RunRequest{Command: "echo hi"}); err == nil {
		t.Fatal("an unavailable executor must refuse to run")
	}
	if err := executor.Healthy(context.Background()); err == nil {
		t.Fatal("an unavailable executor must not report itself healthy")
	}
	if executor.Kind() != EnvironmentSandboxed {
		t.Fatalf("Kind() = %q", executor.Kind())
	}
}

func TestSandboxdFramesRoundTrip(t *testing.T) {
	var buf strings.Builder
	original := &sandboxdFrame{
		Type: sandboxdTypeRun,
		Run: &sandboxdRun{
			RunID:          "run-1",
			Command:        "echo hello",
			Shell:          []string{"/bin/sh", "-c"},
			WorkDir:        "/tmp",
			Env:            map[string]string{"PATH": "/usr/bin"},
			TimeoutMS:      1500,
			MaxOutputBytes: 4096,
		},
	}
	if err := writeSandboxdFrame(&buf, original); err != nil {
		t.Fatalf("write: %v", err)
	}

	decoded, err := readSandboxdFrame(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if decoded.Type != sandboxdTypeRun || decoded.Run == nil {
		t.Fatalf("decoded %+v", decoded)
	}
	if decoded.Run.Command != "echo hello" || decoded.Run.TimeoutMS != 1500 {
		t.Fatalf("decoded run = %+v", decoded.Run)
	}
}

func TestSandboxdRejectsAnOversizedFrame(t *testing.T) {
	// A length header claiming more than the cap must be refused before
	// anything is allocated for it.
	header := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	if _, err := readSandboxdFrame(strings.NewReader(string(header))); err == nil {
		t.Fatal("an oversized frame must be refused")
	}
}

// TestSandboxdAgainstTheRealSidecar is the phase 0 acceptance test: a command
// travels Go -> seshat-sandboxd -> Go and comes back with its output and code.
// It needs the Rust binary, so it is skipped when the workspace was not built.
func TestSandboxdAgainstTheRealSidecar(t *testing.T) {
	path := os.Getenv("SESHAT_SANDBOXD_BIN")
	if path == "" {
		t.Skip("SESHAT_SANDBOXD_BIN is not set; build rust/sandboxd first")
	}

	cfg := DefaultSandboxdConfig()
	cfg.BinaryPath = path
	cfg.MaxOutputBytes = 1 << 20
	executor, err := NewSandboxdExecutor(cfg)
	if err != nil {
		t.Fatalf("NewSandboxdExecutor: %v", err)
	}
	defer func() { _ = executor.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := executor.Healthy(ctx); err != nil {
		t.Fatalf("Healthy: %v", err)
	}

	result, err := executor.Run(ctx, RunRequest{Command: "echo hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", result.ExitCode, result.Stderr)
	}
	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Fatalf("stdout = %q, want \"hello\"", result.Stdout)
	}
}
