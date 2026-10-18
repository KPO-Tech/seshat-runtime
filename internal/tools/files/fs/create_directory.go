package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// CreateDirectoryTool creates directories (mkdir -p equivalent).
type CreateDirectoryTool struct {
	workingDir       string
	filesystemPolicy *sandbox.FilesystemPolicy
}

func NewCreateDirectoryTool(workingDir string) *CreateDirectoryTool {
	return &CreateDirectoryTool{
		workingDir:       workingDir,
		filesystemPolicy: sandbox.NewDefaultFilesystemPolicy(),
	}
}

func (t *CreateDirectoryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "create_directory",
		SearchHint:  "create a new folder or directory",
		DisplayName: "Create Directory",
		Description: "Create a directory and any necessary parent directories. Equivalent to mkdir -p.",
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path of the directory to create",
				},
			},
			"required": []string{"path"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

func (t *CreateDirectoryTool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	rawPath, ok := input.Parsed["path"].(string)
	if !ok || rawPath == "" {
		return tool.NewErrorResult(fmt.Errorf("path is required")), nil
	}

	absPath, err := resolvePathIn(rawPath, t.workingDir, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	if err := shared.ValidateFilePath(absPath, "creating directory"); err != nil {
		return tool.NewErrorResult(err), nil
	}
	if shared.IsDangerousDirectory(absPath) {
		return tool.NewErrorResult(fmt.Errorf("refusing to create in protected directory: %s", absPath)), nil
	}

	toolCtx := input.ToolContextValue()
	sandboxCtx := sandbox.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if ws := toolCtx.Workspace; ws != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(ws.Root)
	}
	decision, err := t.filesystemPolicy.EvaluatePath(sandboxCtx, absPath, sandbox.AccessCreate)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	if err := sandbox.ErrorForDecision(decision.DecisionResult); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if permissionCheck != nil {
		effectiveDir := strings.TrimSpace(toolCtx.WorkingDirectory)
		if effectiveDir == "" {
			effectiveDir = strings.TrimSpace(t.workingDir)
		}
		req := sandbox.PermissionRequest{
			ToolName:      "create_directory",
			Environment:   sandbox.EnvironmentLocal,
			Access:        sandbox.AccessCreate,
			Paths:         []string{absPath},
			Justification: "Create directory",
			Scope:         sandbox.ApprovalScopeToolCall,
		}
		res, err := sandbox.ResolveToolPermission(ctx, permissionCheck, req, sandbox.ToolPermissionOptions{
			ToolInput:              map[string]any{"path": absPath},
			ToolUseID:              toolCtx.ToolUseID,
			SessionID:              toolCtx.SessionID,
			TurnID:                 toolCtx.TurnID,
			PermissionMode:         toolCtx.PermissionMode,
			WorkingDirectory:       effectiveDir,
			IsToolRunningInSandbox: toolCtx.EnableSandbox,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := sandbox.ErrorForPermissionResult(res, "directory creation requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to create directory: %w", err)), nil
	}

	return tool.NewTextResult(fmt.Sprintf("Directory created: %s", absPath)), nil
}

func (t *CreateDirectoryTool) IsEnabled() bool                         { return true }
func (t *CreateDirectoryTool) IsReadOnly(_ map[string]any) bool        { return false }
func (t *CreateDirectoryTool) IsConcurrencySafe(_ map[string]any) bool { return false }
func (t *CreateDirectoryTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *CreateDirectoryTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *CreateDirectoryTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	if p, _ := in["path"].(string); p == "" {
		return nil, fmt.Errorf("path is required")
	}
	return in, nil
}
func (t *CreateDirectoryTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(nil)
}
func (t *CreateDirectoryTool) PreparePermissionMatcher(_ context.Context, _ map[string]any) (func(string) bool, error) {
	return nil, nil
}

// resolvePathIn resolves rawPath relative to workingDir (or toolCtx if set),
// then applies EvalSymlinks to prevent escape.
func resolvePathIn(rawPath, workingDir string, toolCtx tool.ToolUseContext) (string, error) {
	base := workingDir
	if d := toolCtx.WorkingDirectory; d != "" {
		base = d
	}
	if !filepath.IsAbs(rawPath) {
		rawPath = filepath.Join(base, rawPath)
	}
	return shared.GetAbsolutePath(rawPath)
}

func (t *CreateDirectoryTool) Description(_ context.Context) (string, error) {
	return "Create a directory and any necessary parent directories.", nil
}
