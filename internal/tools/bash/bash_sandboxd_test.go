package bash

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
)

// TestNewToolWithUnavailableSandboxdRefusesInsteadOfFallingBack is the contract
// that separates this backend from the Docker one: asking for the sidecar and
// not getting it must never end with the command running on the host. A nil
// sandboxExecutor would do exactly that — nil is the value that routes execution
// through the unsandboxed path — so the tool has to carry a refusing executor
// instead.
func TestNewToolWithUnavailableSandboxdRefusesInsteadOfFallingBack(t *testing.T) {
	cfg := DefaultToolConfig()
	cfg.SandboxKind = sandbox.EnvironmentSandboxed
	cfg.Sandboxd = sandbox.DefaultSandboxdConfig()
	cfg.Sandboxd.BinaryPath = filepath.Join(t.TempDir(), "seshat-sandboxd-does-not-exist")

	tool := NewTool(cfg)
	t.Cleanup(func() {
		if closer, ok := tool.sandboxExecutor.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})

	if tool.sandboxExecutor == nil {
		t.Fatal("sandboxExecutor is nil: the tool would run unconfined on the host")
	}
	if got := tool.sandboxExecutor.Kind(); got != sandbox.EnvironmentSandboxed {
		t.Fatalf("Kind() = %q, want %q", got, sandbox.EnvironmentSandboxed)
	}
	if _, err := tool.sandboxExecutor.Run(context.Background(), sandbox.RunRequest{Command: "echo hello"}); err == nil {
		t.Fatal("an unavailable sandboxd must refuse to run")
	}
}
