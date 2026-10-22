package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

// fakeRuntime is a task runtime with one background agent whose state the test controls.
type fakeRuntime struct {
	task      RuntimeTask
	output    string
	finishes  bool          // WaitForTask ends with the task completed
	waited    time.Duration // what WaitForTask was asked for
	waitCalls int
}

func (f *fakeRuntime) GetTask(context.Context, string) (*RuntimeTask, error) {
	t := f.task
	return &t, nil
}
func (f *fakeRuntime) ListTasks(context.Context) ([]*RuntimeTask, error) { return nil, nil }
func (f *fakeRuntime) ReadTaskOutput(context.Context, string) (string, error) {
	return f.output, nil
}
func (f *fakeRuntime) WaitForTask(_ context.Context, _ string, timeout time.Duration) (*RuntimeTask, error) {
	f.waitCalls++
	f.waited = timeout
	if f.finishes {
		f.task.Status = RuntimeTaskStatusCompleted
		done := time.Now()
		f.task.CompletedAt = &done
		t := f.task
		return &t, nil
	}
	t := f.task
	return &t, context.DeadlineExceeded
}
func (f *fakeRuntime) KillTask(context.Context, string) error { return errors.New("unused") }

func getBackground(t *testing.T, rt *fakeRuntime, extra map[string]any) map[string]any {
	t.Helper()
	previous := RuntimeImpl()
	SetRuntime(rt)
	t.Cleanup(func() { SetRuntime(previous) })

	parsed := map[string]any{"task_id": "agent_1", "taskType": "background"}
	for k, v := range extra {
		parsed[k] = v
	}
	result, err := NewTaskGetTool().Call(context.Background(), tool.CallInput{Parsed: parsed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := result.Data.(map[string]any)
	task, _ := data["task"].(map[string]any)
	if task == nil {
		t.Fatalf("no task in %#v", result.Data)
	}
	return task
}

func runningAgent() *fakeRuntime {
	started := time.Now().Add(-time.Minute)
	return &fakeRuntime{
		task: RuntimeTask{
			ID: "agent_1", Type: "agent", Status: RuntimeTaskStatusRunning,
			Description: "Research how OpenCode handles permissions\nand sandboxing", CreatedAt: started, StartedAt: &started,
		},
		output: "Final report: OpenCode asks before every write.",
	}
}

// A background agent could only be polled, so a session waited for six of them with
// "Start-Sleep 120" in a shell and learned nothing from it.
func TestTaskGetWaitsForABackgroundAgentAndReturnsItsAnswer(t *testing.T) {
	rt := runningAgent()
	rt.finishes = true
	task := getBackground(t, rt, map[string]any{"wait_seconds": float64(90)})

	if rt.waitCalls != 1 || rt.waited != 90*time.Second {
		t.Fatalf("WaitForTask calls = %d for %v, want one call for 90s", rt.waitCalls, rt.waited)
	}
	if task["status"] != "completed" {
		t.Fatalf("status = %v, want completed", task["status"])
	}
	if task["output"] != rt.output {
		t.Fatalf("a finished agent must return its answer, got %v", task["output"])
	}
	if !strings.HasPrefix(task["description"].(string), "Research how OpenCode") {
		t.Fatalf("description = %v", task["description"])
	}
	if _, timedOut := task["timedOut"]; timedOut {
		t.Fatal("a finished task did not time out")
	}
}

func TestTaskGetWaitThatRunsOutReportsTheTaskStillRunning(t *testing.T) {
	rt := runningAgent()
	task := getBackground(t, rt, map[string]any{"wait_seconds": float64(5)})
	if task["status"] != "running" || task["timedOut"] != true {
		t.Fatalf("status = %v timedOut = %v, want running and timedOut", task["status"], task["timedOut"])
	}
	if _, has := task["output"]; has {
		t.Fatal("an unfinished task has no output to return")
	}
}

func TestTaskGetDoesNotWaitUnlessAskedAndCapsTheWait(t *testing.T) {
	rt := runningAgent()
	getBackground(t, rt, nil)
	if rt.waitCalls != 0 {
		t.Fatal("without wait_seconds task_get must answer at once")
	}

	rt = runningAgent()
	getBackground(t, rt, map[string]any{"wait_seconds": float64(99999)})
	if rt.waited != maxWaitSeconds*time.Second {
		t.Fatalf("wait = %v, want it capped at %ds", rt.waited, maxWaitSeconds)
	}

	// An agent that is already over is not waited for.
	rt = runningAgent()
	rt.task.Status = RuntimeTaskStatusFailed
	task := getBackground(t, rt, map[string]any{"wait_seconds": float64(30)})
	if rt.waitCalls != 0 || task["output"] != rt.output {
		t.Fatalf("a finished task returns at once with its output: calls=%d output=%v", rt.waitCalls, task["output"])
	}
}

func TestTaskGetCutsAHugeOutput(t *testing.T) {
	rt := runningAgent()
	rt.task.Status = RuntimeTaskStatusCompleted
	rt.output = strings.Repeat("é", maxTaskOutputRunes+500)
	task := getBackground(t, rt, nil)
	out, _ := task["output"].(string)
	if len([]rune(out)) != maxTaskOutputRunes || task["outputTruncated"] != true {
		t.Fatalf("output = %d runes, truncated = %v", len([]rune(out)), task["outputTruncated"])
	}
}

// What the model reads is the formatted text, not the data: a finished agent's answer that is in the data but not in the text is an
// answer the parent agent never sees. A real conversation had two sub-agents answer correctly and the parent say their output was not
// included in the results.
func TestTaskGetTextCarriesTheAnswerOfAFinishedAgent(t *testing.T) {
	rt := runningAgent()
	rt.finishes = true
	task := getBackground(t, rt, map[string]any{"wait_seconds": float64(90)})

	text := (&TaskGetTool{}).FormatResult(map[string]any{"task": task})
	for _, want := range []string{"Status: completed", "Description: Research how OpenCode", "Output:", rt.output} {
		if !strings.Contains(text, want) {
			t.Errorf("the text the model reads must contain %q, got:\n%s", want, text)
		}
	}

	running := getBackground(t, runningAgent(), map[string]any{"wait_seconds": float64(5)})
	if text := (&TaskGetTool{}).FormatResult(map[string]any{"task": running}); !strings.Contains(text, "call task_get again") {
		t.Errorf("a wait that ran out must say so in the text, got:\n%s", text)
	}
}
