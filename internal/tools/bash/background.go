package bash

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// TaskOutput manages task output storage.
type TaskOutput struct {
	Path         string
	StdoutBuffer []byte
	StderrBuffer []byte
	mu           sync.Mutex
}

// NewTaskOutput creates a new task output.
func NewTaskOutput(path string) *TaskOutput {
	return &TaskOutput{Path: path}
}

// BackgroundTask represents a background task.
type BackgroundTask struct {
	ID        string
	Command   string
	Process   *exec.Cmd
	Output    *TaskOutput
	Status    TaskStatus
	StartTime time.Time
	EndTime   *time.Time
	ExitCode  int

	mu          sync.Mutex
	done        chan struct{}
	cleanupStop chan struct{} // cancels the auto-remove timer
	stdinPipe   io.WriteCloser
	// cancel stops a task that runs through an executor rather than as Process.
	cancel context.CancelFunc
	// owner is the tool that started the task, which stops it when it closes. Guarded
	// by mu.
	owner *Tool
}

// TaskStatus represents background task status.
type TaskStatus int

const (
	TaskStatusRunning      TaskStatus = iota // Task is running
	TaskStatusBackgrounded                   // Task was backgrounded
	TaskStatusCompleted                      // Task completed normally
	TaskStatusKilled                         // Task was killed
	TaskStatusTimeout                        // Task timed out
)

// BackgroundTaskManager manages background tasks.
type BackgroundTaskManager struct {
	tasks   map[string]*BackgroundTask
	mu      sync.RWMutex
	taskDir string
}

// globalTaskManager is the one manager of the process, shared by every NewTool() so
// that a task stays reachable from job_output and job_kill whichever client asks: an
// embedder may hold one client per model. Package-level helpers (GlobalTaskManager,
// NewTaskOutputReader) use it for callers that don't hold a Tool reference (e.g. the
// monitor and task-list tools).
var (
	globalTaskManager   *BackgroundTaskManager
	globalTaskManagerMu sync.Mutex
)

// GlobalTaskManager returns the manager every NewTool() call shares.
func GlobalTaskManager() *BackgroundTaskManager {
	globalTaskManagerMu.Lock()
	defer globalTaskManagerMu.Unlock()
	return globalTaskManager
}

// sharedTaskManager returns the process's manager, making it on the first call.
func sharedTaskManager() *BackgroundTaskManager {
	globalTaskManagerMu.Lock()
	defer globalTaskManagerMu.Unlock()
	if globalTaskManager == nil {
		globalTaskManager = NewBackgroundTaskManager(runtimepath.BashTasksDir(""))
	}
	return globalTaskManager
}

// NewBackgroundTaskManager creates a new, isolated manager.
func NewBackgroundTaskManager(taskDir string) *BackgroundTaskManager {
	if taskDir == "" {
		taskDir = runtimepath.BashTasksDir("")
	}
	return &BackgroundTaskManager{
		tasks:   make(map[string]*BackgroundTask),
		taskDir: taskDir,
	}
}

// Init ensures task directory exists.
func (m *BackgroundTaskManager) Init() error {
	return os.MkdirAll(m.taskDir, 0o755)
}

// StartBackgroundTask starts a command in the background.
func (m *BackgroundTaskManager) StartBackgroundTask(
	ctx context.Context,
	command string,
	workingDir string,
	env []string,
	shell string,
) (*BackgroundTask, error) {
	if err := m.Init(); err != nil {
		return nil, fmt.Errorf("init task dir: %w", err)
	}

	taskID, err := generateTaskID()
	if err != nil {
		return nil, fmt.Errorf("generate task id: %w", err)
	}
	taskPath := filepath.Join(m.taskDir, taskID+".output")

	task := &BackgroundTask{
		ID:          taskID,
		Command:     command,
		Output:      NewTaskOutput(taskPath),
		Status:      TaskStatusRunning,
		StartTime:   time.Now(),
		done:        make(chan struct{}),
		cleanupStop: make(chan struct{}),
	}

	// Create the output file. We open it, pass it to the child, then close our
	// handle — the child inherits the fd through exec and keeps writing to it.
	file, err := os.Create(taskPath)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}

	cmdPath, cmdArgs, sandboxEnv, _ := commandWithLandlock(shell, shellArgs(shell, command), workingDir)
	cmd := exec.CommandContext(ctx, cmdPath, cmdArgs...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = append(env, sandboxEnv...)
	cmd.SysProcAttr = newProcessGroupAttr()
	cmd.Stdout = file
	cmd.Stderr = file

	// Create a stdin pipe so write_stdin can inject input later.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		file.Close()
		os.Remove(taskPath)
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		file.Close()
		os.Remove(taskPath)
		return nil, fmt.Errorf("start command: %w", err)
	}

	// Safe to close parent's handle — child already has its own fd copy.
	file.Close()

	task.stdinPipe = stdinPipe
	task.Process = cmd
	m.mu.Lock()
	m.tasks[taskID] = task
	m.mu.Unlock()

	go m.waitForTask(task)

	return task, nil
}

// StartExecutorTask starts a command through an executor of its own, so that it stays
// inside the sandbox. Its output goes to the task's file as it is produced, and killing
// the task cancels the run, which ends the command and what it started. The executor is
// brought up before this returns, so a sandbox that cannot start is an error here and
// not a task that dies later. A task through an executor has no stdin.
func (m *BackgroundTaskManager) StartExecutorTask(
	ctx context.Context,
	command string,
	workingDir string,
	newExecutor func() (sandbox.Executor, error),
) (*BackgroundTask, error) {
	if err := m.Init(); err != nil {
		return nil, fmt.Errorf("init task dir: %w", err)
	}
	taskID, err := generateTaskID()
	if err != nil {
		return nil, fmt.Errorf("generate task id: %w", err)
	}
	executor, err := newExecutor()
	if err != nil {
		return nil, fmt.Errorf("start the sandbox: %w", err)
	}
	stop := func() {
		if closer, ok := executor.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	if err := executor.Healthy(ctx); err != nil {
		stop()
		return nil, fmt.Errorf("start the sandbox: %w", err)
	}
	taskPath := filepath.Join(m.taskDir, taskID+".output")
	file, err := os.Create(taskPath)
	if err != nil {
		stop()
		return nil, fmt.Errorf("create output file: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	task := &BackgroundTask{
		ID:          taskID,
		Command:     command,
		Output:      NewTaskOutput(taskPath),
		Status:      TaskStatusRunning,
		StartTime:   time.Now(),
		done:        make(chan struct{}),
		cleanupStop: make(chan struct{}),
		cancel:      cancel,
	}
	m.mu.Lock()
	m.tasks[taskID] = task
	m.mu.Unlock()

	go func() {
		defer close(task.done)
		defer cancel()
		res, runErr := executor.Run(runCtx, sandbox.RunRequest{
			Command: command,
			WorkDir: workingDir,
			OnOutput: func(_ string, data []byte) {
				_, _ = file.Write(data)
			},
		})
		if runErr != nil && runCtx.Err() == nil {
			_, _ = fmt.Fprintf(file, "%v\n", runErr)
		}
		file.Close()
		stop()

		task.mu.Lock()
		task.ExitCode = res.ExitCode
		if runErr != nil {
			task.ExitCode = 1
		}
		task.Status = TaskStatusCompleted
		if runCtx.Err() != nil {
			task.Status = TaskStatusKilled
		}
		now := time.Now()
		task.EndTime = &now
		task.mu.Unlock()

		select {
		case <-task.cleanupStop:
		case <-time.After(5 * time.Minute):
			m.RemoveTask(task.ID)
		}
	}()

	return task, nil
}

// StartBackgroundTaskArgv starts a background task from an argv array
// (executable + arguments) instead of a shell command string - no shell
// involved, so callers building a command from untrusted/dynamic pieces
// (e.g. a search pattern that might contain quotes or shell metacharacters)
// don't need to shell-quote anything. Otherwise identical to
// StartBackgroundTask: same task bookkeeping, output file, stdin pipe, and
// process-group handling, and it goes through commandWithLandlock the same
// way (that function wraps any executable+args pair, not just `sh -c`).
func (m *BackgroundTaskManager) StartBackgroundTaskArgv(
	ctx context.Context,
	name string,
	args []string,
	workingDir string,
	env []string,
) (*BackgroundTask, error) {
	if err := m.Init(); err != nil {
		return nil, fmt.Errorf("init task dir: %w", err)
	}

	taskID, err := generateTaskID()
	if err != nil {
		return nil, fmt.Errorf("generate task id: %w", err)
	}
	taskPath := filepath.Join(m.taskDir, taskID+".output")

	task := &BackgroundTask{
		ID:          taskID,
		Command:     name + " " + strings.Join(args, " "),
		Output:      NewTaskOutput(taskPath),
		Status:      TaskStatusRunning,
		StartTime:   time.Now(),
		done:        make(chan struct{}),
		cleanupStop: make(chan struct{}),
	}

	file, err := os.Create(taskPath)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}

	cmdPath, cmdArgs, sandboxEnv, _ := commandWithLandlock(name, args, workingDir)
	cmd := exec.CommandContext(ctx, cmdPath, cmdArgs...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = append(env, sandboxEnv...)
	cmd.SysProcAttr = newProcessGroupAttr()
	cmd.Stdout = file
	cmd.Stderr = file

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		file.Close()
		os.Remove(taskPath)
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		file.Close()
		os.Remove(taskPath)
		return nil, fmt.Errorf("start command: %w", err)
	}
	file.Close()

	task.stdinPipe = stdinPipe
	task.Process = cmd
	m.mu.Lock()
	m.tasks[taskID] = task
	m.mu.Unlock()

	go m.waitForTask(task)

	return task, nil
}

func (m *BackgroundTaskManager) waitForTask(task *BackgroundTask) {
	defer close(task.done)

	// Do NOT close the stdin pipe here before Wait(): this goroutine starts
	// the instant the task launches, so closing it up front handed every
	// interactive process EOF on stdin within microseconds of starting,
	// before write_stdin ever got a chance to write anything - the process
	// would see an immediately-closed stdin and exit (or read empty input)
	// long before any real interaction could happen. cmd.Wait() already
	// closes the StdinPipe pipe itself once the process has actually exited
	// (see the os/exec docs for StdinPipe), so no manual close is needed -
	// this just waits for the real exit.
	err := task.Process.Wait()

	task.mu.Lock()
	task.stdinPipe = nil
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			task.ExitCode = status.ExitStatus()
		} else {
			task.ExitCode = 1
		}
	}
	task.Status = TaskStatusCompleted
	now := time.Now()
	task.EndTime = &now
	task.mu.Unlock()

	// Auto-remove after 5 minutes unless KillTask already cancelled the timer.
	select {
	case <-task.cleanupStop:
	case <-time.After(5 * time.Minute):
		m.RemoveTask(task.ID)
	}
}

// GetTask retrieves a task by ID.
func (m *BackgroundTaskManager) GetTask(id string) *BackgroundTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tasks[id]
}

// ListTasks lists all background tasks.
func (m *BackgroundTaskManager) ListTasks() []*BackgroundTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tasks := make([]*BackgroundTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	return tasks
}

// KillTask kills a background task and cancels its auto-remove timer.
func (m *BackgroundTaskManager) KillTask(id string) error {
	task := m.GetTask(id)
	if task == nil {
		return fmt.Errorf("task not found: %s", id)
	}

	// Cancel the auto-cleanup goroutine before killing so it doesn't race.
	// Under the task's lock: job_kill and a client closing can get here together.
	task.mu.Lock()
	select {
	case <-task.cleanupStop: // already stopped
	default:
		close(task.cleanupStop)
	}
	task.mu.Unlock()

	return KillTaskProcess(task)
}

// killOwnedBy stops the running tasks owner started.
func (m *BackgroundTaskManager) killOwnedBy(owner *Tool) {
	for _, task := range m.ListTasks() {
		task.mu.Lock()
		mine := task.owner == owner && task.Status == TaskStatusRunning
		task.mu.Unlock()
		if mine {
			_ = m.KillTask(task.ID)
		}
	}
}

// KillTaskProcess kills a task's entire process group.
func KillTaskProcess(task *BackgroundTask) error {
	if task.cancel != nil {
		task.cancel()
		return nil
	}
	if task.Process == nil || task.Process.Process == nil {
		return nil
	}

	if err := killProcessGroup(task.Process.Process.Pid); err != nil {
		return task.Process.Process.Kill()
	}

	task.mu.Lock()
	task.Status = TaskStatusKilled
	task.mu.Unlock()
	return nil
}

// RemoveTask removes a task and its output file.
func (m *BackgroundTaskManager) RemoveTask(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task, ok := m.tasks[id]; ok {
		if task.Output != nil && task.Output.Path != "" {
			os.Remove(task.Output.Path)
		}
		delete(m.tasks, id)
	}
}

// WaitForTask waits for a task to complete within timeout.
func (m *BackgroundTaskManager) WaitForTask(id string, timeout time.Duration) (*BackgroundTask, error) {
	task := m.GetTask(id)
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", id)
	}
	select {
	case <-task.done:
		return task, nil
	case <-time.After(timeout):
		return task, fmt.Errorf("timeout waiting for task %s", id)
	}
}

// WriteStdin writes data to a running task's stdin pipe.
// Returns an error if the task has no stdin pipe (already finished or stdin
// was not set up) or if the write fails (e.g. broken pipe).
func (m *BackgroundTaskManager) WriteStdin(id string, data string) error {
	task := m.GetTask(id)
	if task == nil {
		return fmt.Errorf("task not found: %s", id)
	}

	task.mu.Lock()
	pipe := task.stdinPipe
	status := task.Status
	task.mu.Unlock()

	if pipe == nil {
		if status == TaskStatusCompleted || status == TaskStatusKilled || status == TaskStatusTimeout {
			return fmt.Errorf("task %s has already finished", id)
		}
		return fmt.Errorf("task %s has no stdin pipe", id)
	}

	_, err := io.WriteString(pipe, data)
	if err != nil {
		return fmt.Errorf("write to task %s stdin: %w", id, err)
	}
	return nil
}

// Thread-safe getters on BackgroundTask.

func (t *BackgroundTask) GetStatus() TaskStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Status
}

func (t *BackgroundTask) GetExitCode() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ExitCode
}

func (t *BackgroundTask) GetEndTime() *time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.EndTime
}

// ── Output reading ─────────────────────────────────────────────────────────────

// TaskOutputReader reads task output incrementally.
type TaskOutputReader struct {
	task *BackgroundTask
	pos  int64
}

// NewTaskOutputReader creates a reader using the global task manager.
// Pass a non-nil manager to use a specific instance instead.
func NewTaskOutputReader(taskID string) (*TaskOutputReader, error) {
	return NewTaskOutputReaderFrom(GlobalTaskManager(), taskID)
}

// NewTaskOutputReaderFrom creates a reader from a specific manager.
func NewTaskOutputReaderFrom(manager *BackgroundTaskManager, taskID string) (*TaskOutputReader, error) {
	if manager == nil {
		return nil, fmt.Errorf("no background task manager available")
	}
	task := manager.GetTask(taskID)
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	return &TaskOutputReader{task: task}, nil
}

// ReadOutput reads new output since the last call.
func (r *TaskOutputReader) ReadOutput() (string, error) {
	if r.task.Output == nil || r.task.Output.Path == "" {
		return "", nil
	}
	content, err := os.ReadFile(r.task.Output.Path)
	if err != nil {
		return "", err
	}
	if r.pos >= int64(len(content)) {
		return "", nil
	}
	newOutput := string(content[r.pos:])
	r.pos = int64(len(content))
	return newOutput, nil
}

// IsRunning checks if the task is still running.
func (r *TaskOutputReader) IsRunning() bool {
	st := r.task.GetStatus()
	return st == TaskStatusRunning || st == TaskStatusBackgrounded
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// parseBackgroundCommand strips a trailing & from the command.
func parseBackgroundCommand(command string) (isBackground bool, actualCommand string) {
	command = strings.TrimSpace(command)
	if strings.HasSuffix(command, "&") {
		return true, strings.TrimSpace(command[:len(command)-1])
	}
	fields := strings.Fields(command)
	if len(fields) > 0 && fields[len(fields)-1] == "&" {
		return true, strings.TrimSpace(strings.Join(fields[:len(fields)-1], " "))
	}
	return false, command
}

// generateTaskID returns a cryptographically random 16-byte hex task ID.
func generateTaskID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "task_" + hex.EncodeToString(b), nil
}
