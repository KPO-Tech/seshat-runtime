package write

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	fileReadTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/read"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	// ToolName is the name of the write tool
	ToolName = "write_file"

	// ToolDescription is the description of the write tool
	ToolDescription = "Write the full content of a file. Creates a new file or completely replaces an existing one.\n\n" +
		"## When to use\n\n" +
		"- Creating a new file that does not yet exist\n" +
		"- Rewriting a file from scratch when the changes are so extensive that edit_file would require many individual edits\n\n" +
		"## When NOT to use\n\n" +
		"- Partial modifications to existing files — use edit_file instead (safer, only sends the diff)\n" +
		"- Never create documentation (*.md) or README files unless explicitly requested\n\n" +
		"## Rules\n\n" +
		"- If the file already exists, you must read it with read_file first. This tool errors if you modify an existing file without reading it first.\n" +
		"- This replaces the entire file content. Double-check you are not discarding sections you intended to keep.\n"
	patchContextLines = 3
)

// FileReadInfo tracks information about a file that was read
type FileReadInfo struct {
	Path          string    // Absolute path to the file
	Timestamp     time.Time // When the file was read
	ModTime       time.Time // File modification time when read
	Content       string    // Content that was read (for verification)
	IsFullRead    bool      // Whether the entire file was read
	IsPartialView bool      // Whether the read was a partial view (offset/limit used)
}

// StructuredPatchHunk represents a single structured diff hunk.
type StructuredPatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// GitDiff is the shared git diff summary, re-aliased here for backward compat.
type GitDiff = shared.GitDiff

// FileReadHistory tracks files that have been read in the session.
// It is kept as a compatibility wrapper over the shared fileReadTool cache.
type FileReadHistory struct{}

// NewFileReadHistory creates a new file read history tracker.
func NewFileReadHistory() *FileReadHistory {
	return &FileReadHistory{}
}

// RecordRead records that a file was read.
func (h *FileReadHistory) RecordRead(path string, modTime time.Time, content string, isFullRead bool) {
	fileReadTool.RecordExternalRead(path, modTime, content, isFullRead)
}

// GetReadInfo retrieves read information for a file.
func (h *FileReadHistory) GetReadInfo(path string) (*FileReadInfo, bool) {
	state, exists := fileReadTool.GetLastReadState(path)
	if !exists {
		return nil, false
	}

	return &FileReadInfo{
		Path:          path,
		Timestamp:     state.CachedAt,
		ModTime:       time.Unix(state.Timestamp, 0),
		Content:       state.Content,
		IsFullRead:    !state.IsPartialView,
		IsPartialView: state.IsPartialView,
	}, true
}

// Tool implements the write file tool
type Tool struct {
	// workingDir is the current working directory
	workingDir string
	// readHistory is a compatibility wrapper over shared read state
	readHistory *FileReadHistory

	// filesystemPolicy centralizes common path access checks.
	filesystemPolicy *access.FilesystemPolicy
}

// NewWriteTool creates a new write file tool
func NewWriteTool(workingDir string) *Tool {
	return &Tool{
		workingDir:       workingDir,
		readHistory:      NewFileReadHistory(),
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

// SetReadHistory sets the file read history tracker
func (w *Tool) SetReadHistory(history *FileReadHistory) {
	w.readHistory = history
}

// GetReadHistory returns the file read history tracker
func (w *Tool) GetReadHistory() *FileReadHistory {
	return w.readHistory
}

// Definition returns the tool definition
func (w *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Write File",
		SearchHint:  SearchHint,
		Description: ToolDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "The path to the file to write",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The content to write to the file",
				},
			},
			"required": []string{"file_path", "content"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		IsDestructive:      true,
		RequiresPermission: true,
	}
}

// Call executes the tool
func (w *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	filePath, ok := input.Parsed["file_path"].(string)
	if !ok || filePath == "" {
		return tool.NewErrorResult(fmt.Errorf("file_path is required and must be a string")), nil
	}
	if err := shared.ValidateFilePath(filePath, "writing"); err != nil {
		return tool.NewErrorResult(err), nil
	}

	content, ok := input.Parsed["content"].(string)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("content is required and must be a string")), nil
	}

	absolutePath, err := w.resolvePath(filePath, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	if err := w.validateWritePath(input.ToolContextValue(), absolutePath); err != nil {
		return tool.NewErrorResult(fmt.Errorf("path validation failed: %w", err)), nil
	}

	if err := shared.ValidateFilePath(absolutePath, "writing"); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if err := shared.ValidateUNCPathSecurity(absolutePath); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if err := shared.ValidateSensitivePath(absolutePath, "writeTool", content); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if info, err := os.Stat(absolutePath); err == nil && info.IsDir() {
		return tool.NewErrorResult(fmt.Errorf("path is a directory, not a file: %s", filePath)), nil
	}

	// Retrieve read history once; reused for both the pre-permission guard and
	// the post-permission content staleness check.
	readInfo, readInfoExists := w.readHistory.GetReadInfo(absolutePath)

	// Fast guard: existing file that was never read in this session → error
	// immediately, before spending any time on a permission dialog.
	if !readInfoExists {
		if _, err := os.Stat(absolutePath); err == nil {
			return tool.NewErrorResult(fmt.Errorf("file has not been read yet. Read it first before writing to it: %s", filePath)), nil
		}
	}

	// Permission check comes BEFORE reading the full file content so we never
	// do unnecessary I/O (large file reads) for a denied operation.
	if permissionCheck != nil {
		req := access.PermissionRequest{
			ToolName:      ToolName,
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessWrite,
			Paths:         []string{absolutePath},
			Justification: "Write file contents",
			Scope:         access.ApprovalScopeToolCall,
			Metadata: map[string]any{
				"content_bytes": len(content),
			},
		}
		toolCtx := input.ToolContextValue()
		permResult, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput: map[string]any{
				"file_path": absolutePath,
				"content":   content,
			},
			ToolUseID:              toolCtx.ToolUseID,
			SessionID:              toolCtx.SessionID,
			TurnID:                 toolCtx.TurnID,
			PermissionMode:         toolCtx.PermissionMode,
			WorkingDirectory:       w.effectiveWorkingDir(toolCtx),
			IsToolRunningInSandbox: toolCtx.EnableSandbox,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := access.ErrorForPermissionResult(permResult, "file write requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// Post-permission staleness check: verify the file has not been modified by
	// a third party between when it was read and now. We read the content in a
	// single os.ReadFile call (atomic from the OS perspective) to avoid the
	// stat→read race present in the previous two-step approach.
	if readInfoExists {
		currentContent, readErr := os.ReadFile(absolutePath)
		if readErr == nil {
			currentInfo, statErr := os.Stat(absolutePath)
			if statErr == nil && currentInfo.ModTime().After(readInfo.ModTime) {
				if normalizeLineEndings(string(currentContent)) != normalizeLineEndings(readInfo.Content) {
					return tool.NewErrorResult(fmt.Errorf("file has been modified since read (read at %s, file modified at %s). Read it again first: %s",
						readInfo.ModTime.Format(time.RFC3339),
						currentInfo.ModTime().Format(time.RFC3339),
						filePath)), nil
				}
			} else if readErr == nil && normalizeLineEndings(string(currentContent)) != normalizeLineEndings(readInfo.Content) {
				return tool.NewErrorResult(fmt.Errorf("file content has changed unexpectedly since it was read: %s", filePath)), nil
			}
		}
	}

	parentDir := filepath.Dir(absolutePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to create parent directories: %w", err)), nil
	}

	fileExists := false
	var oldContent string
	if info, err := os.Stat(absolutePath); err == nil && !info.IsDir() {
		fileExists = true
		oldContentBytes, readErr := os.ReadFile(absolutePath)
		if readErr == nil {
			oldContent = string(oldContentBytes)
		}
	}

	normalizedOldContent := normalizeLineEndings(oldContent)
	normalizedNewContent := normalizeLineEndings(content)
	preferredLineEnding := detectPreferredLineEnding(oldContent)
	if preferredLineEnding == "" {
		preferredLineEnding = "\n"
	}

	contentToWrite := applyLineEnding(normalizedNewContent, preferredLineEnding)
	if err := os.WriteFile(absolutePath, []byte(contentToWrite), 0644); err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to write file: %w", err)), nil
	}

	info, err := os.Stat(absolutePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to stat file: %w", err)), nil
	}
	fileReadTool.RecordExternalRead(absolutePath, info.ModTime(), normalizedNewContent, true)

	operationType := "create"
	if fileExists {
		operationType = "update"
	}

	oldLines := countLines(normalizedOldContent)
	newLines := countLines(normalizedNewContent)
	linesAdded := 0
	linesRemoved := 0
	if newLines > oldLines {
		linesAdded = newLines - oldLines
	} else {
		linesRemoved = oldLines - newLines
	}

	structuredPatch := buildStructuredPatch(normalizedOldContent, normalizedNewContent)
	output := map[string]any{
		"file_path":        filePath,
		"size":             info.Size(),
		"bytes":            len(contentToWrite),
		"success":          true,
		"type":             operationType,
		"lines_added":      linesAdded,
		"lines_removed":    linesRemoved,
		"content":          normalizedNewContent,
		"original_file":    nullableOriginal(normalizedOldContent, fileExists),
		"structured_patch": structuredPatch,
		"user_modified":    false,
	}
	if gitDiff, ok := shared.ComputeGitDiff(ctx, absolutePath); ok {
		output["git_diff"] = gitDiff
	}

	res := tool.NewJSONResult(output)
	res.Metadata = &tool.ResultMetadata{Additional: output}
	return res, nil
}

func nullableOriginal(oldContent string, fileExists bool) any {
	if !fileExists {
		return nil
	}
	return oldContent
}

func buildStructuredPatch(oldContent, newContent string) []StructuredPatchHunk {
	if oldContent == newContent {
		return []StructuredPatchHunk{}
	}

	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	minLen := len(oldLines)
	if len(newLines) < minLen {
		minLen = len(newLines)
	}

	prefix := 0
	for prefix < minLen && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < minLen-prefix && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	beforeStart := maxInt(0, prefix-patchContextLines)
	oldChangeEnd := len(oldLines) - suffix
	newChangeEnd := len(newLines) - suffix
	afterOldEnd := minInt(len(oldLines), oldChangeEnd+patchContextLines)
	afterNewEnd := minInt(len(newLines), newChangeEnd+patchContextLines)

	lines := make([]string, 0)
	for _, line := range oldLines[beforeStart:prefix] {
		lines = append(lines, " "+line)
	}
	for _, line := range oldLines[prefix:oldChangeEnd] {
		lines = append(lines, "-"+line)
	}
	for _, line := range newLines[prefix:newChangeEnd] {
		lines = append(lines, "+"+line)
	}
	for _, line := range newLines[newChangeEnd:afterNewEnd] {
		lines = append(lines, " "+line)
	}

	return []StructuredPatchHunk{{
		OldStart: beforeStart + 1,
		OldLines: afterOldEnd - beforeStart,
		NewStart: beforeStart + 1,
		NewLines: afterNewEnd - beforeStart,
		Lines:    lines,
	}}
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func detectPreferredLineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(s, "\n") {
		return "\n"
	}
	return ""
}

func applyLineEnding(s string, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

// countLines counts the number of lines in a string
func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := 0
	for _, c := range s {
		if c == '\n' {
			count++
		}
	}
	if !strings.HasSuffix(s, "\n") && len(s) > 0 {
		count++
	}
	return count
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Description returns the tool description
func (w *Tool) Description(ctx context.Context) (string, error) {
	return ToolDescription, nil
}

// ValidateInput validates and normalizes write input.
func (w *Tool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	_ = ctx
	filePath, ok := input["file_path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("file_path is required and must be a string")
	}
	if _, ok := input["content"].(string); !ok {
		return nil, fmt.Errorf("content is required and must be a string")
	}
	return input, nil
}

// CheckPermissions performs write-specific permission checks before the global pipeline.
func (w *Tool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	filePath, _ := input["file_path"].(string)
	absolutePath, err := w.resolvePath(filePath, toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	if err := shared.ValidateFilePath(filePath, "writing"); err != nil {
		return types.Deny(err.Error())
	}
	if err := shared.ValidateFilePath(absolutePath, "writing"); err != nil {
		return types.Deny(err.Error())
	}
	content, _ := input["content"].(string)

	// AcceptEdits mode: Auto-allow file operations in working directory
	// Aligned with OpenClaude's AcceptEdits mode (filesystem.ts:1369-1384)
	if toolCtx.PermissionMode == types.PermissionModeAcceptEdits {
		return w.checkAcceptEditsPermissions(ctx, absolutePath, content, w.effectiveWorkingDir(toolCtx))
	}

	// Plan mode is a read-only exploration/design phase (see
	// modes.ExecutionModePlan's doc comment and enter_plan_mode's own
	// instructions to the model, which already say not to write files here).
	// Deny rather than the AcceptEdits-style auto-allow this used to fall
	// into - that made a plan-mode session ask fewer questions about writes
	// than a normal session would, the opposite of what "plan" promises.
	// The plan's own content never goes through this tool: exit_plan_mode
	// takes it as a parameter and persists it internally (execution.SetPlan).
	if toolCtx.ExecutionMode == "plan" {
		return types.DenyWithDecisionReason(
			fmt.Sprintf("file writes are not allowed in plan mode: %s", absolutePath),
			&types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonMode,
				Source: "plan",
				Reason: "plan mode is read-only - get the plan approved with submit_plan before writing",
			},
		)
	}

	if err := shared.ValidateSensitivePath(absolutePath, "writeTool", content); err != nil {
		return types.Deny(err.Error())
	}

	return types.Passthrough(input)
}

// checkAcceptEditsPermissions implements AcceptEdits mode-specific logic for file writes.
// Aligned with OpenClaude's file system auto-allow (filesystem.ts:1369-1384).
func (w *Tool) checkAcceptEditsPermissions(ctx context.Context, absolutePath, content, workingDir string) types.PermissionResult {
	// Check if path is in working directory
	if !shared.IsInWorkingDirectory(absolutePath, workingDir) {
		return types.AskWithDecisionReason(
			fmt.Sprintf("file path outside working directory requires approval: %s", absolutePath),
			&types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonMode,
				Source: "acceptEdits",
				Reason: fmt.Sprintf("path is outside working directory: %s", absolutePath),
			},
		)
	}

	// Check for dangerous files
	if shared.IsDangerousFile(absolutePath) {
		return types.AskWithDecisionReason(
			fmt.Sprintf("cannot auto-edit dangerous file: %s", absolutePath),
			&types.PermissionDecisionReason{
				Type:                 types.PermissionDecisionReasonSafetyCheck,
				Source:               "acceptEdits",
				Reason:               fmt.Sprintf("file is protected: %s", absolutePath),
				ClassifierApprovable: false,
			},
		)
	}

	// Check for dangerous directories
	if shared.IsDangerousDirectory(absolutePath) {
		return types.AskWithDecisionReason(
			fmt.Sprintf("cannot auto-edit dangerous directory: %s", absolutePath),
			&types.PermissionDecisionReason{
				Type:                 types.PermissionDecisionReasonSafetyCheck,
				Source:               "acceptEdits",
				Reason:               fmt.Sprintf("directory is protected: %s", absolutePath),
				ClassifierApprovable: false,
			},
		)
	}

	// Check for suspicious path patterns
	if shared.HasSuspiciousPattern(absolutePath) {
		return types.AskWithDecisionReason(
			fmt.Sprintf("suspicious path pattern detected: %s", absolutePath),
			&types.PermissionDecisionReason{
				Type:                 types.PermissionDecisionReasonSafetyCheck,
				Source:               "acceptEdits",
				Reason:               fmt.Sprintf("path has suspicious pattern: %s", absolutePath),
				ClassifierApprovable: false,
			},
		)
	}

	// All checks passed - allow the write operation
	return types.AllowWithDecisionReason(
		"file write is safe in AcceptEdits mode",
		&types.PermissionDecisionReason{
			Type:   types.PermissionDecisionReasonMode,
			Source: "acceptEdits",
			Reason: "file is in working directory and passes safety checks",
		},
	)
}

// IsConcurrencySafe reports that writes are not safe to run concurrently.
func (w *Tool) IsConcurrencySafe(input map[string]any) bool {
	_ = input
	return false
}

// IsReadOnly reports that writes modify state.
func (w *Tool) IsReadOnly(input map[string]any) bool {
	_ = input
	return false
}

// IsEnabled returns whether this tool is currently active.
func (w *Tool) IsEnabled() bool { return true }

// FormatResult serialises the tool output into the tool_result content string.
func (w *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput enriches a shallow clone of the parsed input with derived fields.
func (w *Tool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

// SetWorkingDir sets the working directory for the tool
func (w *Tool) SetWorkingDir(dir string) {
	w.workingDir = dir
}

// GetWorkingDir returns the current working directory
func (w *Tool) GetWorkingDir() string {
	return w.workingDir
}

func (w *Tool) validateWritePath(toolCtx tool.ToolUseContext, path string) error {
	sandboxCtx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if toolCtx.Workspace != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(toolCtx.Workspace.Root)
	}

	decision, err := w.filesystemPolicy.EvaluatePath(sandboxCtx, path, access.AccessWrite)
	if err != nil {
		return err
	}
	return access.ErrorForDecision(decision.DecisionResult)
}

func (w *Tool) resolvePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := w.effectiveWorkingDir(toolCtx)
	if filepath.IsAbs(path) || strings.TrimSpace(workingDir) == "" {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

func (w *Tool) effectiveWorkingDir(toolCtx tool.ToolUseContext) string {
	if strings.TrimSpace(toolCtx.WorkingDirectory) != "" {
		return toolCtx.WorkingDirectory
	}
	if strings.TrimSpace(w.workingDir) != "" {
		return w.workingDir
	}
	return "."
}

// ── Tool metadata ──────────────────────────────────────────────────────────────

// SearchHint is a hint for tool search functionality.
const SearchHint = "write files to the filesystem"
