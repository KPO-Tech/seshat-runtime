package bash

import (
	"context"
	"os"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

// TestMain lets this test binary double as a "hangs forever" fake docker
// binary for TestDockerHealthCheckIsBoundedWhenDockerHangs - the standard
// exec.Command re-exec test pattern (see Go's own os/exec test suite),
// portable across every OS this SDK targets, unlike shelling out to a
// platform-specific "sleep" command. Every other test in this package is
// unaffected: this branch only triggers when BASH_TEST_HANG_FOREVER is set,
// which only that one test's re-exec'd child process ever has.
func TestMain(m *testing.M) {
	if os.Getenv("BASH_TEST_HANG_FOREVER") == "1" {
		select {}
	}
	os.Exit(m.Run())
}

// TestSandboxKindReportsLocalWhenDockerNotRequested proves SandboxKind()
// doesn't default to reporting Docker just because it's the "better"
// backend — a Tool that never requested it must report EnvironmentLocal,
// matching what NewTool actually initialized (see NewTool's nil-executor
// path when config.SandboxKind isn't EnvironmentDocker).
func TestSandboxKindReportsLocalWhenNoneIsRequested(t *testing.T) {
	tool := NewTool(DefaultToolConfig())
	if got := tool.SandboxKind(); got != sandbox.EnvironmentLocal {
		t.Fatalf("expected SandboxKind() to report EnvironmentLocal when Docker was never requested, got %v", got)
	}
}

// TestCallRefusesUnconfinedExecutionWhenRequireSandboxIsSet is the one test
// this package was missing for its own central multi-tenant-safety
// mechanism (grepping the whole package for "RequireSandbox" before this
// test existed turned up only the field/config plumbing, never an actual
// exercise of the enforcement branch in executeLocalCommand). Deliberately
// carries no //go:build constraint, unlike bash_test.go's Landlock-specific
// suite — on any host where no sandboxing actually
// applies (this test suite's own CI/dev machines included, whenever they
// aren't Linux-with-Landlock), RequireSandbox must refuse instead of
// running the command unconfined; that's exactly the platform this test
// exercises for real, no mocking needed.
func TestCallRefusesUnconfinedExecutionWhenRequireSandboxIsSet(t *testing.T) {
	if landlockAvailable() {
		t.Skip("this host has Landlock available — RequireSandbox would apply real confinement instead of refusing, see the Linux-specific suite in bash_test.go for that path")
	}

	cfg := DefaultToolConfig()
	cfg.RequireSandbox = true
	tl := NewTool(cfg)

	result, err := tl.Call(context.Background(), tool.CallInput{
		Parsed: map[string]any{"command": "echo hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Call returned a Go error instead of a tool-result error: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected RequireSandbox to refuse execution when no OS-level sandbox is available on this platform, got a successful result")
	}
}
