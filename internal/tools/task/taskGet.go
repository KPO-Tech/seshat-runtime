package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// TaskDetails represents detailed information about a background task.
type TaskDetails struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	// Description is what the task was asked to do: the prompt of a background agent,
	// which has no command.
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	StartTime   int64  `json:"startTime"`
	EndTime     *int64 `json:"endTime,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
}

const (
	// maxWaitSeconds caps wait_seconds.
	maxWaitSeconds = 600
	// maxTaskOutputRunes caps the output returned for a finished background task.
	maxTaskOutputRunes = 20000
)

func isTerminalRuntimeStatus(status RuntimeTaskStatus) bool {
	return status == RuntimeTaskStatusCompleted || status == RuntimeTaskStatusFailed || status == RuntimeTaskStatusKilled
}

// truncateRunes keeps the first n runes of s and says so when it cuts.
func truncateRunes(s string, n int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= n {
		return s, false
	}
	return string(runes[:n]), true
}

// TaskGetTool implements the TaskGet tool for getting task details.
type TaskGetTool struct{}

// NewTaskGetTool creates a new TaskGet tool.
func NewTaskGetTool() *TaskGetTool {
	return &TaskGetTool{}
}

// Definition returns the tool definition.
func (t *TaskGetTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolNameTaskGet,
		DisplayName: "TaskGet",
		SearchHint:  SearchHintTaskGet,
		Description: ToolDescriptionTaskGet,
		Category:    "task",
		Metadata:    taskSurfaceMetadata(),
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The ID of the task to retrieve",
				},
				"taskType": map[string]any{
					"type":        "string",
					"enum":        []string{"todo", "background"},
					"description": "Optional task kind override. Defaults to todo first, then background fallback.",
				},
				"wait_seconds": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     maxWaitSeconds,
					"description": "For a background task or agent: wait up to this many seconds for it to finish before answering. Use it instead of sleeping in a shell. If it is still running when the time is up, the current status comes back.",
				},
			},
			"required": []string{"task_id"},
		}),
	}
}

// Call executes the TaskGet tool.
func (t *TaskGetTool) Call(ctx context.Context, input tool.CallInput, permissionCheck types.CanUseToolFn) (tool.CallResult, error) {
	parsed := input.Parsed
	if parsed == nil {
		parsed = make(map[string]any)
	}

	taskID, ok := parsed["task_id"].(string)
	if !ok || taskID == "" {
		return tool.CallResult{Error: fmt.Errorf("task_id is required")}, nil
	}

	taskType := resolveTaskKind(parsed, "")
	sessionID := resolveOptionalTaskSessionID(input)

	if taskType != "background" {
		if sessionID != "" {
			todoTask, err := GlobalTaskStore().GetTask(ctx, sessionID, taskID)
			if err == nil && todoTask != nil {
				details := todoTaskDetails(todoTask)
				return tool.CallResult{
					Data: map[string]any{
						"task": map[string]any{
							"type":        "todo",
							"id":          details.ID,
							"subject":     details.Subject,
							"description": details.Description,
							"status":      details.Status,
							"activeForm":  details.ActiveForm,
							"owner":       details.Owner,
							"blocks":      details.Blocks,
							"blockedBy":   details.BlockedBy,
							"createdAt":   details.CreatedAt,
							"updatedAt":   details.UpdatedAt,
						},
					},
					Metadata: &tool.ResultMetadata{Additional: map[string]any{
						"task_get": taskGetRenderMetadata{TaskType: "todo", Todo: details},
					}},
				}, nil
			}
		} else if taskType == "todo" {
			return tool.CallResult{Error: fmt.Errorf("session ID is required for todo task lookup")}, nil
		}
	}

	if taskType == "todo" {
		return tool.CallResult{Data: map[string]any{"task": nil}}, nil
	}

	runtime, err := requireRuntime()
	if err != nil {
		return tool.CallResult{Data: map[string]any{"task": nil}}, nil
	}
	backgroundTask, err := runtime.GetTask(ctx, taskID)
	if err != nil {
		return tool.CallResult{Data: map[string]any{"task": nil}}, nil
	}
	// A background agent has no way to be waited for other than polling it, which is
	// how a session ended up running "Start-Sleep 120" in a shell between checks.
	timedOut := false
	if wait, ok := parsed["wait_seconds"].(float64); ok && wait >= 1 && !isTerminalRuntimeStatus(backgroundTask.Status) {
		if wait > maxWaitSeconds {
			wait = maxWaitSeconds
		}
		waited, waitErr := runtime.WaitForTask(ctx, taskID, time.Duration(wait)*time.Second)
		if waited != nil {
			backgroundTask = waited
		}
		timedOut = waitErr != nil && !isTerminalRuntimeStatus(backgroundTask.Status)
	}
	details := runtimeTaskDetails(backgroundTask)
	task := map[string]any{
		"type":      "background",
		"id":        details.ID,
		"command":   details.Command,
		"status":    details.Status,
		"startTime": details.StartTime,
		"endTime":   details.EndTime,
		"exitCode":  details.ExitCode,
	}
	if details.Description != "" {
		task["description"] = details.Description
	}
	if timedOut {
		task["timedOut"] = true
	}
	// Once it is over, the model needs what it produced: for an agent that is its
	// final answer, which until now could only be found by reading its output file.
	if isTerminalRuntimeStatus(backgroundTask.Status) {
		if output, readErr := runtime.ReadTaskOutput(ctx, taskID); readErr == nil && output != "" {
			text, cut := truncateRunes(output, maxTaskOutputRunes)
			task["output"] = text
			if cut {
				task["outputTruncated"] = true
			}
		}
	}
	return tool.CallResult{
		Data: map[string]any{"task": task},
		Metadata: &tool.ResultMetadata{Additional: map[string]any{
			"task_get": taskGetRenderMetadata{TaskType: "background", Background: details},
		}},
	}, nil
}

func todoTaskDetails(task *Task) *TaskGetTodoDetails {
	if task == nil {
		return nil
	}
	return &TaskGetTodoDetails{
		ID:          task.ID,
		Subject:     task.Subject,
		Description: task.Description,
		Status:      task.Status,
		ActiveForm:  task.ActiveForm,
		Owner:       task.Owner,
		Blocks:      append([]string(nil), task.Blocks...),
		BlockedBy:   append([]string(nil), task.BlockedBy...),
		CreatedAt:   task.CreatedAt.Unix(),
		UpdatedAt:   task.UpdatedAt.Unix(),
	}
}

func runtimeTaskDetails(task *RuntimeTask) *TaskDetails {
	if task == nil {
		return nil
	}
	details := &TaskDetails{
		ID:          task.ID,
		Command:     task.Command,
		Description: task.Description,
		Status:      string(task.Status),
		StartTime:   task.CreatedAt.Unix(),
	}
	if task.StartedAt != nil {
		details.StartTime = task.StartedAt.Unix()
	}
	if task.CompletedAt != nil {
		endTimeUnix := task.CompletedAt.Unix()
		details.EndTime = &endTimeUnix
	}
	if task.ExitCode != nil {
		details.ExitCode = task.ExitCode
	}
	return details
}

// Description returns a human-readable description.
func (t *TaskGetTool) Description(ctx context.Context) (string, error) {
	return ToolDescriptionTaskGet, nil
}

// ValidateInput validates the input.
func (t *TaskGetTool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	return input, nil
}

// CheckPermissions checks permissions.
func (t *TaskGetTool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	taskID, ok := input["task_id"].(string)
	if !ok || taskID == "" {
		return types.Deny("task_id is required")
	}
	taskType := resolveTaskKind(input, "")
	if taskType != "background" && toolCtx.SessionID != "" {
		if _, err := GlobalTaskStore().GetTask(ctx, string(toolCtx.SessionID), taskID); err == nil {
			return types.PermissionResult{Behavior: types.PermissionBehaviorAllow}
		}
	}
	if taskType == "todo" {
		return types.Deny("todo task not found")
	}
	return types.PermissionResult{Behavior: types.PermissionBehaviorAllow}
}

// IsConcurrencySafe returns whether the tool is concurrency safe.
func (t *TaskGetTool) IsConcurrencySafe(input map[string]any) bool {
	return true
}

// IsReadOnly returns whether the tool is read-only.
func (t *TaskGetTool) IsReadOnly(input map[string]any) bool {
	return true
}

// IsEnabled returns whether the tool is enabled.
func (t *TaskGetTool) IsEnabled() bool {
	return true
}

// FormatResult formats the result.
func (t *TaskGetTool) FormatResult(data any) string {
	if data == nil {
		return "Task not found"
	}
	m, ok := data.(map[string]any)
	if !ok {
		return fmt.Sprintf("%v", data)
	}
	rawTask, ok := m["task"].(map[string]any)
	if !ok || rawTask == nil {
		return "Task not found"
	}
	taskType, _ := rawTask["type"].(string)
	if taskType == "todo" {
		subject, _ := rawTask["subject"].(string)
		status, _ := rawTask["status"].(string)
		description, _ := rawTask["description"].(string)
		parts := []string{fmt.Sprintf("Task: %s", subject), fmt.Sprintf("Status: %s", status)}
		if owner, _ := rawTask["owner"].(string); owner != "" {
			parts = append(parts, fmt.Sprintf("Owner: %s", owner))
		}
		if activeForm, _ := rawTask["activeForm"].(string); activeForm != "" {
			parts = append(parts, fmt.Sprintf("Active: %s", activeForm))
		}
		if strings.TrimSpace(description) != "" {
			parts = append(parts, "", description)
		}
		return JoinLines(parts)
	}
	id, _ := rawTask["id"].(string)
	cmd, _ := rawTask["command"].(string)
	status, _ := rawTask["status"].(string)
	startTime, _ := rawTask["startTime"].(int64)
	lines := []string{
		fmt.Sprintf("Task: %s", id),
		fmt.Sprintf("Status: %s", status),
		fmt.Sprintf("Command: %s", cmd),
		fmt.Sprintf("Started: %s", time.Unix(startTime, 0).Format(time.RFC3339)),
	}
	if description, _ := rawTask["description"].(string); description != "" {
		lines = append(lines, fmt.Sprintf("Description: %s", description))
	}
	// The details carry the end time and the exit code as pointers (absent while the task runs).
	switch endTime := rawTask["endTime"].(type) {
	case *int64:
		if endTime != nil && *endTime > 0 {
			lines = append(lines, fmt.Sprintf("Ended: %s", time.Unix(*endTime, 0).Format(time.RFC3339)))
		}
	case int64:
		if endTime > 0 {
			lines = append(lines, fmt.Sprintf("Ended: %s", time.Unix(endTime, 0).Format(time.RFC3339)))
		}
	}
	switch exitCode := rawTask["exitCode"].(type) {
	case *int:
		if exitCode != nil {
			lines = append(lines, fmt.Sprintf("Exit Code: %d", *exitCode))
		}
	case int:
		lines = append(lines, fmt.Sprintf("Exit Code: %d", exitCode))
	}
	if timedOut, _ := rawTask["timedOut"].(bool); timedOut {
		lines = append(lines, "The wait ran out before the task ended: call task_get again to keep waiting.")
	}
	// What the task produced is the point of asking: for a background agent it is its final answer, which the model only gets if it
	// is in this text.
	if output, _ := rawTask["output"].(string); output != "" {
		lines = append(lines, "", "Output:", output)
		if cut, _ := rawTask["outputTruncated"].(bool); cut {
			lines = append(lines, "(The output was cut: read the rest from the task's output file.)")
		}
	}
	return JoinLines(lines)
}

// BackfillInput backfills input.
func (t *TaskGetTool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}
