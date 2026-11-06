package tasks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	taskTool "github.com/KPO-Tech/seshat-runtime/internal/tools/task"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// Manager manages background task execution.
type Manager struct {
	tasks  map[TaskID]*Task
	client *engine.Engine
	mu     sync.RWMutex
	config *ManagerConfig

	// completionCallback is called when any task completes
	completionCallback TaskCompletionCallback
}

// tasksStateFileName stores the lightweight manager snapshot. This is not a
// full job-control checkpoint: completed/failed tasks are restored as-is, while
// previously running tasks are refreshed from persisted pid/output state when possible.
const tasksStateFileName = "tasks.json"

// DefaultManagerConfig returns default manager configuration.
func DefaultManagerConfig() *ManagerConfig {
	return &ManagerConfig{
		MaxConcurrentTasks: 10,
		TaskTimeout:        5 * time.Minute,
		OutputDir:          runtimepath.TasksDir(""),
	}
}

// SetCompletionCallback sets a callback that is called when any task completes
func (m *Manager) SetCompletionCallback(cb TaskCompletionCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completionCallback = cb
}

func NewManager(client *engine.Engine, config *ManagerConfig) *Manager {
	if config == nil {
		config = DefaultManagerConfig()
	}
	_ = os.MkdirAll(config.OutputDir, 0755)
	manager := &Manager{tasks: make(map[TaskID]*Task), client: client, config: config}
	manager.restorePersistedTasks()
	return manager
}

var (
	globalManagerMu sync.RWMutex
	globalManager   *Manager
)

// SetGlobalManager sets the global task manager used by task tools.
func SetGlobalManager(manager *Manager) {
	globalManagerMu.Lock()
	defer globalManagerMu.Unlock()
	globalManager = manager
}

// GlobalManager returns the global task manager used by task tools.
func GlobalManager() *Manager {
	globalManagerMu.RLock()
	defer globalManagerMu.RUnlock()
	return globalManager
}

// NewDefaultManager creates and installs a global manager.
func NewDefaultManager(client *engine.Engine, config *ManagerConfig) *Manager {
	manager := NewManager(client, config)
	SetGlobalManager(manager)
	taskTool.SetRuntime(&runtimeAdapter{manager: manager})
	return manager
}

// activeTaskCountLocked returns the number of tasks in Pending or Running state.
// Caller must hold m.mu (read or write).
func (m *Manager) activeTaskCountLocked() int {
	count := 0
	for _, t := range m.tasks {
		if t.Status == TaskStatusPending || t.Status == TaskStatusRunning {
			count++
		}
	}
	return count
}

// CreateBashTask creates a new bash task.
func (m *Manager) CreateBashTask(ctx context.Context, command string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeTaskCountLocked() >= m.config.MaxConcurrentTasks {
		return nil, fmt.Errorf("maximum concurrent tasks reached (%d)", m.config.MaxConcurrentTasks)
	}
	taskID := TaskID(fmt.Sprintf("bash_%d", time.Now().UnixNano()))
	task := &Task{
		ID:           taskID,
		Type:         TaskTypeBash,
		Status:       TaskStatusPending,
		Command:      command,
		Description:  command,
		CreatedAt:    time.Now(),
		Progress:     0,
		OutputFile:   m.outputPath(taskID),
		ExitCodeFile: m.exitCodePath(taskID),
	}
	m.tasks[taskID] = task
	m.persistTasksLocked()
	go m.runBashTask(ctx, task)
	return cloneTask(task), nil
}

// CreateAgentTask creates a new agent task.
//
// permissionMode, when non-empty, is applied to the new session before it
// ever runs - a background task has no interactive prompt to show, and
// without this it silently inherited whatever permission mode m.client
// defaults new sessions to (typically onRequest), which routes any "ask"
// through the same engine-wide prompt function the foreground session
// uses. Callers should resolve this the same way every other sub-agent
// spawn path does (see agents.subAgentPermissionMode): bypass by default,
// or never when the spawning session is itself in never mode.
//
// systemPrompt, when non-empty, becomes the session's system prompt
// template - the caller's resolved agentDef.GetSystemPrompt(), so a
// background run actually gets the requested agent type's identity and
// instructions instead of silently running under the engine's default one
// (the type the model asked for was otherwise only used for display, never
// to pick a prompt). The session also gets the same "subagent" tool
// surface profile every other sub-agent spawn path uses (coreagent.RunAgent),
// instead of the top-level default a plain NewSession would give it.
//
// policy restricts the tools the session may use (allow lists and deny
// patterns, deny winning; see coreagent.EffectiveToolPolicy) - the same policy
// every synchronous sub-agent spawn path applies via RunAgent. It is enforced
// for tools registered up front, registered later and found through
// tool_search, and handed to the sub-agents this task spawns.
func (m *Manager) CreateAgentTask(ctx context.Context, prompt string, tools []tool.Tool, permissionMode types.PermissionMode, systemPrompt string, policy tool.ToolPolicy) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeTaskCountLocked() >= m.config.MaxConcurrentTasks {
		return nil, fmt.Errorf("maximum concurrent tasks reached (%d)", m.config.MaxConcurrentTasks)
	}
	if m.client == nil {
		return nil, fmt.Errorf("query engine not configured for agent tasks")
	}
	taskID := TaskID(fmt.Sprintf("agent_%d", time.Now().UnixNano()))
	subagentMetadata := types.NewSubagentSessionMetadata(tool.ToolSurfaceProfileSubagent, types.CallingSessionIDFromContext(ctx), time.Now())
	session, err := m.client.NewSessionFromState(ctx, "", subagentMetadata, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent session: %w", err)
	}
	// sessionOwned tracks whether the goroutine has taken ownership of the session.
	// If we return an error before launching the goroutine, we must close the session.
	sessionOwned := false
	defer func() {
		if !sessionOwned {
			_ = session.Close()
		}
	}()
	if permissionMode != "" {
		session.SetPermissionMode(permissionMode)
	}
	if systemPrompt != "" {
		session.SetSystemPromptTemplate(systemPrompt)
	}
	session.SetToolPolicy(policy)
	ctx = coreagent.WithToolPolicy(ctx, policy)
	if err := session.RegisterTools(tools); err != nil {
		return nil, fmt.Errorf("failed to register tools: %w", err)
	}
	task := &Task{
		ID:           taskID,
		Type:         TaskTypeAgent,
		Status:       TaskStatusPending,
		Prompt:       prompt,
		SessionID:    session.GetMetadata().ID,
		CreatedAt:    time.Now(),
		Progress:     0,
		Description:  prompt,
		OutputFile:   m.outputPath(taskID),
		ExitCodeFile: m.exitCodePath(taskID),
	}
	m.tasks[taskID] = task
	m.persistTasksLocked()
	sessionOwned = true
	go m.runAgentTask(ctx, task, session)
	return cloneTask(task), nil
}

func (m *Manager) outputPath(taskID TaskID) string {
	return filepath.Join(m.config.OutputDir, string(taskID)+".output")
}

func (m *Manager) exitCodePath(taskID TaskID) string {
	return filepath.Join(m.config.OutputDir, string(taskID)+".exit")
}

func (m *Manager) ensureOutputFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
}

func (m *Manager) setTaskRunning(task *Task, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	task.cancel = cancel
	task.Status = TaskStatusRunning
	task.StartedAt = &now
	task.Progress = 10
	m.persistTasksLocked()
}

func (m *Manager) finishTask(task *Task, status TaskStatus, output string, exitCode *int, err error) {
	m.mu.Lock()
	now := time.Now()
	task.Status = status
	task.Output = output
	task.CompletedAt = &now
	task.Progress = 100
	task.ExitCode = exitCode
	if err != nil {
		task.Error = err.Error()
	}
	m.persistTasksLocked()

	// Store callback reference before releasing lock
	callback := m.completionCallback
	m.mu.Unlock()

	// Call the callback (outside the lock)
	if callback != nil {
		callback(task)
	}
}

func (m *Manager) runBashTask(ctx context.Context, task *Task) {
	taskCtx, cancel := context.WithTimeout(ctx, m.config.TaskTimeout)
	m.setTaskRunning(task, cancel)
	file, err := m.ensureOutputFile(task.OutputFile)
	if err != nil {
		m.finishTask(task, TaskStatusFailed, "", nil, err)
		return
	}
	defer file.Close()

	cmd := exec.CommandContext(taskCtx, "bash", "-lc", task.Command)
	cmd.Stdout = file
	cmd.Stderr = file
	cmd.SysProcAttr = newProcessGroupAttr()

	m.mu.Lock()
	task.cmd = cmd
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.finishTask(task, TaskStatusFailed, "", nil, err)
		return
	}

	m.mu.Lock()
	if cmd.Process != nil {
		task.PID = cmd.Process.Pid
		m.persistTasksLocked()
	}
	m.mu.Unlock()

	waitErr := cmd.Wait()
	output := m.readOutputFile(task.OutputFile)
	exitCode := exitCodeFromCmd(cmd, waitErr)
	if exitCode != nil && task.ExitCodeFile != "" {
		_ = os.WriteFile(task.ExitCodeFile, []byte(strconv.Itoa(*exitCode)), 0644)
	}
	status := TaskStatusCompleted
	if errorsIsContext(taskCtx.Err()) {
		status = TaskStatusKilled
	} else if waitErr != nil {
		status = TaskStatusFailed
	}
	m.finishTask(task, status, output, exitCode, waitErr)
}

func (m *Manager) runAgentTask(ctx context.Context, task *Task, session *engine.Session) {
	defer session.Close()
	taskCtx, cancel := context.WithTimeout(ctx, m.config.TaskTimeout)
	m.setTaskRunning(task, cancel)
	response, err := session.SubmitMessage(taskCtx, task.Prompt)
	if err != nil {
		m.finishTask(task, TaskStatusFailed, "", nil, err)
		return
	}
	output := extractAssistantOutput(response.Messages)
	if writeErr := os.WriteFile(task.OutputFile, []byte(output), 0644); writeErr != nil {
		m.finishTask(task, TaskStatusFailed, output, nil, writeErr)
		return
	}
	m.finishTask(task, TaskStatusCompleted, output, nil, nil)
}

func (m *Manager) readOutputFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func exitCodeFromFile(path string) *int {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return nil
	}
	return &value
}

func (m *Manager) refreshRestoredTask(task *Task) {
	if task == nil || !task.restored {
		return
	}
	if task.Output == "" && task.OutputFile != "" {
		task.Output = m.readOutputFile(task.OutputFile)
	}
	if task.ExitCode == nil && task.ExitCodeFile != "" {
		task.ExitCode = exitCodeFromFile(task.ExitCodeFile)
	}
	if task.Status != TaskStatusRunning && task.Status != TaskStatusPending {
		return
	}
	if task.Type != TaskTypeBash {
		task.Status = TaskStatusFailed
		task.Error = "task manager restarted before task could be reattached"
		now := time.Now()
		task.CompletedAt = &now
		task.Progress = 100
		return
	}
	if processExists(task.PID) {
		task.Status = TaskStatusRunning
		if task.StartedAt == nil {
			now := task.CreatedAt
			task.StartedAt = &now
		}
		if task.Progress == 0 {
			task.Progress = 10
		}
		return
	}
	if task.ExitCode != nil {
		if *task.ExitCode == 0 {
			task.Status = TaskStatusCompleted
		} else {
			task.Status = TaskStatusFailed
			if task.Error == "" {
				task.Error = fmt.Sprintf("task exited with code %d after manager restart", *task.ExitCode)
			}
		}
	} else {
		task.Status = TaskStatusFailed
		if task.Error == "" {
			task.Error = "task manager restarted before task could be reattached"
		}
	}
	if task.CompletedAt == nil {
		now := time.Now()
		task.CompletedAt = &now
	}
	task.Progress = 100
}

func extractAssistantOutput(messages []types.Message) string {
	var builder strings.Builder
	for _, msg := range messages {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if text, ok := block.(types.TextContent); ok {
				builder.WriteString(text.Text)
			}
		}
	}
	return builder.String()
}

func exitCodeFromCmd(cmd *exec.Cmd, waitErr error) *int {
	if cmd == nil || cmd.ProcessState == nil {
		if waitErr == nil {
			return nil
		}
		code := 1
		return &code
	}
	code := cmd.ProcessState.ExitCode()
	return &code
}

func errorsIsContext(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func cloneTask(task *Task) *Task {
	if task == nil {
		return nil
	}
	cloned := *task
	return &cloned
}

// GetTask retrieves a task by ID.
func (m *Manager) GetTask(taskID TaskID) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, exists := m.tasks[taskID]
	if !exists {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	m.refreshRestoredTask(task)
	m.persistTasksLocked()
	return cloneTask(task), nil
}

// ListTasks lists all tasks in stable order.
func (m *Manager) ListTasks() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	tasks := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		m.refreshRestoredTask(task)
		tasks = append(tasks, cloneTask(task))
	}
	m.persistTasksLocked()
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	return tasks
}

// KillTask kills a running task.
func (m *Manager) KillTask(taskID TaskID) error {
	m.mu.Lock()
	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	if task.Status != TaskStatusRunning && task.Status != TaskStatusPending {
		m.mu.Unlock()
		return fmt.Errorf("task is not running (status: %s)", task.Status)
	}
	cancel := task.cancel
	cmd := task.cmd
	pid := task.PID
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = killProcessGroup(cmd.Process.Pid)
		_, _ = cmd.Process.Wait()
	} else if pid > 0 {
		_ = killProcessGroup(pid)
	}

	m.mu.Lock()
	now := time.Now()
	task.Status = TaskStatusKilled
	task.CompletedAt = &now
	task.Progress = 100
	m.persistTasksLocked()
	m.mu.Unlock()
	return nil
}

// WaitForTask waits for a task to reach a terminal status.
func (m *Manager) WaitForTask(ctx context.Context, taskID TaskID, timeout time.Duration) (*Task, error) {
	deadlineCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		deadlineCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := m.GetTask(taskID)
		if err != nil {
			return nil, err
		}
		if task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed || task.Status == TaskStatusKilled {
			return task, nil
		}
		select {
		case <-deadlineCtx.Done():
			return task, deadlineCtx.Err()
		case <-ticker.C:
		}
	}
}

// ReadTaskOutput reads the output file for a task.
func (m *Manager) ReadTaskOutput(taskID TaskID) (string, error) {
	task, err := m.GetTask(taskID)
	if err != nil {
		return "", err
	}
	if task.OutputFile == "" {
		return task.Output, nil
	}
	return m.readOutputFile(task.OutputFile), nil
}

// WatchTask watches a task for completion.
func (m *Manager) WatchTask(ctx context.Context, taskID TaskID) <-chan *Task {
	ch := make(chan *Task, 1)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				task, err := m.GetTask(taskID)
				if err != nil {
					return
				}
				ch <- task
				if task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed || task.Status == TaskStatusKilled {
					return
				}
			}
		}
	}()
	return ch
}

// CleanupTasks removes terminal tasks older than maxAge.
func (m *Manager) CleanupTasks(maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	now := time.Now()
	for id, task := range m.tasks {
		if task.Status == TaskStatusRunning || task.Status == TaskStatusPending {
			continue
		}
		completedAt := task.CreatedAt
		if task.CompletedAt != nil {
			completedAt = *task.CompletedAt
		}
		if now.Sub(completedAt) > maxAge {
			delete(m.tasks, id)
			if task.OutputFile != "" {
				_ = os.Remove(task.OutputFile)
			}
			if task.ExitCodeFile != "" {
				_ = os.Remove(task.ExitCodeFile)
			}
			removed++
		}
	}
	m.persistTasksLocked()
	if len(m.tasks) == 0 {
		_ = os.Remove(m.tasksStatePath())
	}
	return removed
}

// GetTaskStats returns statistics about tasks.
func (m *Manager) GetTaskStats() *TaskStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stats := &TaskStats{Total: len(m.tasks)}
	for _, task := range m.tasks {
		switch task.Status {
		case TaskStatusPending:
			stats.Pending++
		case TaskStatusRunning:
			stats.Running++
		case TaskStatusCompleted:
			stats.Completed++
		case TaskStatusFailed:
			stats.Failed++
		case TaskStatusKilled:
			stats.Killed++
		}
	}
	return stats
}

// TaskStats represents task statistics.
type TaskStats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Killed    int `json:"killed"`
}

// ReadTaskOutputTail reads the tail of a task output file.
func (m *Manager) ReadTaskOutputTail(taskID TaskID, maxLines int) (string, error) {
	task, err := m.GetTask(taskID)
	if err != nil {
		return "", err
	}
	if task.OutputFile == "" {
		return task.Output, nil
	}
	file, err := os.Open(task.OutputFile)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
	}
	return strings.Join(lines, "\n"), scanner.Err()
}
