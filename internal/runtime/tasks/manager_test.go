package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// newTestManager creates a minimal Manager for unit tests (no real engine needed for
// quota and status-counting tests since we only call CreateBashTask which does not
// require the engine client).
//
// Uses os.MkdirTemp instead of t.TempDir so that background goroutines started by
// CreateBashTask can finish writing to the output directory after the test returns
// without triggering the Go 1.25 strict TempDir cleanup check.
func newTestManager(t *testing.T, maxConcurrent int) *Manager {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "tasks-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	cfg := &ManagerConfig{
		MaxConcurrentTasks: maxConcurrent,
		TaskTimeout:        30 * time.Second,
		OutputDir:          tmpDir,
	}
	return &Manager{
		tasks:  make(map[TaskID]*Task),
		config: cfg,
	}
}

// injectTask directly inserts a task with a given status into the manager map,
// bypassing the goroutine launcher — useful for setting up quota-check scenarios.
func injectTask(m *Manager, id TaskID, status TaskStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[id] = &Task{
		ID:        id,
		Status:    status,
		CreatedAt: time.Now(),
	}
}

// TestActiveTaskCountLocked verifies that only Pending/Running tasks are counted.
func TestActiveTaskCountLocked(t *testing.T) {
	m := newTestManager(t, 10)
	m.mu.Lock()

	m.tasks["t1"] = &Task{ID: "t1", Status: TaskStatusPending}
	m.tasks["t2"] = &Task{ID: "t2", Status: TaskStatusRunning}
	m.tasks["t3"] = &Task{ID: "t3", Status: TaskStatusCompleted}
	m.tasks["t4"] = &Task{ID: "t4", Status: TaskStatusFailed}
	m.tasks["t5"] = &Task{ID: "t5", Status: TaskStatusKilled}

	count := m.activeTaskCountLocked()
	m.mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 active tasks, got %d", count)
	}
}

// TestCreateBashTask_QuotaCountsOnlyActiveTasks verifies that completed tasks do NOT
// count toward the quota limit — this is the bug we fixed.
func TestCreateBashTask_QuotaCountsOnlyActiveTasks(t *testing.T) {
	m := newTestManager(t, 2)

	// Seed two COMPLETED tasks — should not consume quota slots.
	injectTask(m, "done1", TaskStatusCompleted)
	injectTask(m, "done2", TaskStatusFailed)

	// With len(m.tasks)=2 and MaxConcurrentTasks=2, the OLD code would reject new tasks.
	// With the fix, active count is 0, so the task should be created.
	// Pre-cancel so the background bash goroutine exits quickly and doesn't leave
	// open files after the test returns.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.CreateBashTask(ctx, "echo hello")
	// The task creation itself must succeed — we only care about the quota check.
	if err != nil && err.Error() == "maximum concurrent tasks reached (2)" {
		t.Error("BUG: completed tasks incorrectly consumed quota slots")
	}
}

// TestCreateBashTask_QuotaBlocksWhenActiveTasksFull verifies that when the active
// limit is reached, new tasks are correctly rejected.
func TestCreateBashTask_QuotaBlocksWhenActiveTasksFull(t *testing.T) {
	m := newTestManager(t, 2)

	// Inject two RUNNING tasks to fill the quota.
	injectTask(m, "run1", TaskStatusRunning)
	injectTask(m, "run2", TaskStatusRunning)

	ctx := context.Background()
	_, err := m.CreateBashTask(ctx, "echo should-be-blocked")
	if err == nil {
		t.Error("expected error when active quota is full, got nil")
	}
	if err != nil && err.Error() != "maximum concurrent tasks reached (2)" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestCreateBashTask_QuotaAllowsNewAfterCompletion verifies the common lifecycle:
// fill quota → tasks complete → new tasks succeed.
func TestCreateBashTask_QuotaAllowsNewAfterCompletion(t *testing.T) {
	m := newTestManager(t, 1)

	// One completed task does not block new creation.
	injectTask(m, "old1", TaskStatusCompleted)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so the goroutine exits quickly

	_, err := m.CreateBashTask(ctx, "echo lifecycle")
	if err != nil && err.Error() == "maximum concurrent tasks reached (1)" {
		t.Error("BUG: completed task should not block new task creation")
	}
}

// TestGetTaskStats verifies stats are consistent with inserted tasks.
func TestGetTaskStats(t *testing.T) {
	m := newTestManager(t, 10)
	injectTask(m, "p1", TaskStatusPending)
	injectTask(m, "r1", TaskStatusRunning)
	injectTask(m, "r2", TaskStatusRunning)
	injectTask(m, "c1", TaskStatusCompleted)
	injectTask(m, "f1", TaskStatusFailed)
	injectTask(m, "k1", TaskStatusKilled)

	stats := m.GetTaskStats()
	if stats.Total != 6 {
		t.Errorf("expected Total=6, got %d", stats.Total)
	}
	if stats.Pending != 1 {
		t.Errorf("expected Pending=1, got %d", stats.Pending)
	}
	if stats.Running != 2 {
		t.Errorf("expected Running=2, got %d", stats.Running)
	}
	if stats.Completed != 1 {
		t.Errorf("expected Completed=1, got %d", stats.Completed)
	}
	if stats.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", stats.Failed)
	}
	if stats.Killed != 1 {
		t.Errorf("expected Killed=1, got %d", stats.Killed)
	}
}

// ---------------------------------------------------------------------------
// CreateAgentTask must actually apply the caller's resolved systemPrompt to
// the background session, not just use it for display - see TODO.md's M5c
// entry (2026-10-11 follow-up): agentType used to be ignored by the
// background path entirely, so a requested agent type's identity never
// reached the model.
// ---------------------------------------------------------------------------

// newCapturingTestEngine builds a real *engine.Engine backed by a mock HTTP
// server that records the last request body it received, instead of
// returning a canned response immediately - CreateAgentTask's goroutine
// needs to actually complete for WaitForTask to return, so the mock must
// answer like a real (if trivial) streaming provider.
func newCapturingTestEngine(t *testing.T) (*engine.Engine, func() string) {
	t.Helper()
	var lastBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{"type": "content_block_start", "content_block": map[string]any{"type": "text", "text": ""}},
			{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "done"}},
			{"type": "content_block_stop"},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}},
			{"type": "message_stop"},
		}
		for _, event := range events {
			payload, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", payload)
		}
	}))
	t.Cleanup(server.Close)

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})

	engineConfig := engine.DefaultConfig()
	engineConfig.PermissionMode = types.PermissionModeBypass

	eng := engine.NewEngine(
		apiClient,
		execution.NewOrchestrator(),
		compact.NewEngine(apiClient, compact.DefaultConfig()),
		prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()),
		registry.NewRegistry(),
		nil,
		engineConfig,
		nil,
		nil,
	)
	eng.SetAPIClient(apiClient)
	return eng, func() string { return lastBody }
}

func TestCreateAgentTask_AppliesSystemPromptToTheModelRequest(t *testing.T) {
	eng, lastBody := newCapturingTestEngine(t)
	tmpDir, err := os.MkdirTemp("", "tasks-systemprompt-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	m := NewManager(eng, &ManagerConfig{MaxConcurrentTasks: 5, TaskTimeout: 10 * time.Second, OutputDir: tmpDir})

	const marker = "YOU ARE THE SPECIAL MARKER AGENT"
	task, err := m.CreateAgentTask(context.Background(), "do the thing", nil, types.PermissionModeBypass, marker, registry.ToolPolicy{})
	if err != nil {
		t.Fatalf("CreateAgentTask: %v", err)
	}

	if _, err := m.WaitForTask(context.Background(), task.ID, 5*time.Second); err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}

	if !strings.Contains(lastBody(), marker) {
		t.Errorf("expected the resolved system prompt in the request sent to the model, got body: %s", lastBody())
	}
}
