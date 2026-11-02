package bash

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
)

// fakeExecutor stands for a sandbox: it records what reaches it. With wait set, a run
// lasts until it is cancelled, like a dev server.
type fakeExecutor struct {
	kind   sandbox.EnvironmentKind
	wait   bool
	runs   atomic.Int32
	last   atomic.Value
	closed atomic.Bool
}

func (f *fakeExecutor) Kind() sandbox.EnvironmentKind   { return f.kind }
func (f *fakeExecutor) Healthy(_ context.Context) error { return nil }
func (f *fakeExecutor) Close() error                    { f.closed.Store(true); return nil }
func (f *fakeExecutor) Run(ctx context.Context, req sandbox.RunRequest) (sandbox.RunResult, error) {
	f.runs.Add(1)
	f.last.Store(req.Command)
	if req.OnOutput != nil {
		req.OnOutput("stdout", []byte("from the sandbox\n"))
	}
	if f.wait {
		<-ctx.Done()
		return sandbox.RunResult{}, ctx.Err()
	}
	return sandbox.RunResult{Stdout: "from the sandbox\n"}, nil
}

func toolWithSandbox(t *testing.T, executor sandbox.Executor) *Tool {
	t.Helper()
	cfg := DefaultToolConfig()
	cfg.WorkingDirectory = t.TempDir()
	tool := NewTool(cfg)
	tool.sandboxExecutor = executor
	tool.backgroundManager = NewBackgroundTaskManager(t.TempDir())
	return tool
}

func waitDone(t *testing.T, task *BackgroundTask) {
	t.Helper()
	select {
	case <-task.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the task did not end")
	}
}

// A marker a command writes when it really ran on the host: the sandbox is a fake, so
// nothing should ever create it.
func hostMarker(t *testing.T) (command, path string) {
	t.Helper()
	path = t.TempDir() + "/ran-on-the-host"
	return "echo x > '" + path + "'", path
}

func TestBackgroundCommandStaysInTheSandbox(t *testing.T) {
	shared := &fakeExecutor{kind: sandbox.EnvironmentSandboxed}
	own := &fakeExecutor{kind: sandbox.EnvironmentSandboxed, wait: true}
	tool := toolWithSandbox(t, shared)
	tool.newBackgroundExecutor = func() (sandbox.Executor, error) { return own, nil }
	command, marker := hostMarker(t)

	result, err := tool.executeBackground(context.Background(), &ExecutionContext{Command: command, WorkingDirectory: t.TempDir()})
	if err != nil || result.Error != nil {
		t.Fatalf("result %+v, err %v", result, err)
	}
	tasks := tool.backgroundManager.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("%d tasks", len(tasks))
	}
	task := tasks[0]
	if task.Process != nil {
		t.Fatal("a process was started directly on the host")
	}

	deadline := time.Now().Add(5 * time.Second)
	for own.runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if own.last.Load() != command {
		t.Fatalf("the task's executor ran %v", own.last.Load())
	}
	if shared.runs.Load() != 0 {
		t.Fatal("the task took the shared executor, and would hold every later command")
	}

	if err := tool.backgroundManager.KillTask(task.ID); err != nil {
		t.Fatal(err)
	}
	waitDone(t, task)
	if task.GetStatus() != TaskStatusKilled {
		t.Fatalf("status %v", task.GetStatus())
	}
	if !own.closed.Load() {
		t.Fatal("the task's executor was left running")
	}
	output, err := os.ReadFile(task.Output.Path)
	if err != nil || !strings.Contains(string(output), "from the sandbox") {
		t.Fatalf("output %q, err %v", output, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran on the host")
	}
}

func TestBackgroundCommandIsRefusedWhenTheSandboxCannotRunIt(t *testing.T) {
	tool := toolWithSandbox(t, &fakeExecutor{kind: sandbox.EnvironmentDocker})
	command, marker := hostMarker(t)

	result, err := tool.executeBackground(context.Background(), &ExecutionContext{Command: command, WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || !strings.Contains(result.Content, "foreground") {
		t.Fatalf("expected a refusal naming the way out, got %+v", result)
	}
	if len(tool.backgroundManager.ListTasks()) != 0 {
		t.Fatal("a task was started")
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran on the host")
	}
}

func TestABackgroundSandboxThatCannotStartIsAnError(t *testing.T) {
	tool := toolWithSandbox(t, &fakeExecutor{kind: sandbox.EnvironmentSandboxed})
	tool.newBackgroundExecutor = func() (sandbox.Executor, error) { return nil, errors.New("no sidecar") }

	result, err := tool.executeBackground(context.Background(), &ExecutionContext{Command: "true", WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == nil {
		t.Fatalf("expected an error, got %+v", result)
	}
}

// A session's terminal is the user's, with all their rights.
func TestTheSandboxWinsOverTheSessionTerminal(t *testing.T) {
	inSandbox := &fakeExecutor{kind: sandbox.EnvironmentSandboxed}
	terminal := &fakeExecutor{kind: sandbox.EnvironmentLocal}
	sandbox.RegisterRemoteExecutor("escape-test", terminal)
	t.Cleanup(func() { sandbox.UnregisterRemoteExecutor("escape-test") })
	tool := toolWithSandbox(t, inSandbox)

	result, err := tool.executeCommand(context.Background(), &ExecutionContext{Command: "echo hi", WorkingDirectory: t.TempDir(), SessionID: "escape-test", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.runs.Load() != 0 {
		t.Fatal("the command left the sandbox through the session terminal")
	}
	if inSandbox.runs.Load() != 1 || !result.Sandboxed {
		t.Fatalf("runs %d, sandboxed %v", inSandbox.runs.Load(), result.Sandboxed)
	}
}

func TestTheSidecarOutputReachesTheChunkCallback(t *testing.T) {
	tool := toolWithSandbox(t, &fakeExecutor{kind: sandbox.EnvironmentSandboxed})
	var got strings.Builder
	_, err := tool.executeCommand(context.Background(), &ExecutionContext{
		Command: "echo hi", WorkingDirectory: t.TempDir(), Timeout: time.Minute,
		OutputChunkCallback: func(chunk, stream string) { got.WriteString(stream + ":" + chunk) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "stdout:from the sandbox\n" {
		t.Fatalf("callback got %q", got.String())
	}
}
