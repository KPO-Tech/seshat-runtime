package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// RemoveTool deletes a file or directory with safety checks.
type RemoveTool struct {
	workingDir       string
	filesystemPolicy *access.FilesystemPolicy
}

func NewRemoveTool(workingDir string) *RemoveTool {
	return &RemoveTool{
		workingDir:       workingDir,
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

func (t *RemoveTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "remove_file",
		DisplayName: "Remove File",
		Description: "Delete a file or directory. Requires explicit recursive=true to remove non-empty directories. Protected paths (.ssh, .git, .env, etc.) are always refused.",
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path of the file or directory to remove",
				},
				"recursive": map[string]any{
					"type":        "boolean",
					"description": "Allow removing non-empty directories (default false)",
				},
			},
			"required": []string{"path"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		IsDestructive:      true,
		RequiresPermission: true,
	}
}

func (t *RemoveTool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	rawPath, ok := input.Parsed["path"].(string)
	if !ok || rawPath == "" {
		return tool.NewErrorResult(fmt.Errorf("path is required")), nil
	}
	recursive, _ := input.Parsed["recursive"].(bool)

	absPath, err := resolvePathIn(rawPath, t.workingDir, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	// ── Safety checks ──────────────────────────────────────────────────────
	if err := shared.CheckDangerousRemovalPath(absPath, t.workingDir); err != nil {
		return tool.NewErrorResult(fmt.Errorf("dangerous removal path: %w", err)), nil
	}
	if shared.IsDangerousFile(absPath) || shared.IsDangerousDirectory(absPath) {
		return tool.NewErrorResult(fmt.Errorf("refusing to remove protected path: %s", absPath)), nil
	}
	if hasWildcard(absPath) {
		return tool.NewErrorResult(fmt.Errorf("wildcards are not allowed in remove path: %s", absPath)), nil
	}

	// Refuse to remove anything outside the working directory.
	base := t.workingDir
	if d := input.ToolContextValue().WorkingDirectory; d != "" {
		base = d
	}
	realBase, _ := filepath.EvalSymlinks(base)
	realTarget, _ := filepath.EvalSymlinks(filepath.Dir(absPath))
	if realBase != "" && realTarget != "" {
		rel, relErr := filepath.Rel(realBase, realTarget)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return tool.NewErrorResult(fmt.Errorf("path is outside working directory: %s", absPath)), nil
		}
	}

	// Check that path exists.
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.NewTextResult(fmt.Sprintf("path does not exist (nothing to remove): %s", absPath)), nil
		}
		return tool.NewErrorResult(fmt.Errorf("stat failed: %w", err)), nil
	}

	// Non-empty directory needs recursive flag.
	if info.IsDir() && !recursive {
		des, _ := os.ReadDir(absPath)
		if len(des) > 0 {
			return tool.NewErrorResult(fmt.Errorf("directory is not empty; set recursive=true to remove it: %s", absPath)), nil
		}
	}

	// ── Permission ─────────────────────────────────────────────────────────
	toolCtx := input.ToolContextValue()
	sandboxCtx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if ws := toolCtx.Workspace; ws != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(ws.Root)
	}
	decision, err := t.filesystemPolicy.EvaluatePath(sandboxCtx, absPath, access.AccessDelete)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	if err := access.ErrorForDecision(decision.DecisionResult); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if permissionCheck != nil {
		effectiveDir := strings.TrimSpace(toolCtx.WorkingDirectory)
		if effectiveDir == "" {
			effectiveDir = strings.TrimSpace(t.workingDir)
		}
		req := access.PermissionRequest{
			ToolName:      "remove_file",
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessDelete,
			Paths:         []string{absPath},
			Justification: "Remove file or directory",
			Scope:         access.ApprovalScopeToolCall,
			Metadata:      map[string]any{"recursive": recursive},
		}
		res, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput:              map[string]any{"path": absPath, "recursive": recursive},
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
		if err := access.ErrorForPermissionResult(res, "file removal requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// ── Execute ────────────────────────────────────────────────────────────
	if recursive {
		err = os.RemoveAll(absPath)
	} else {
		err = os.Remove(absPath)
	}
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("remove failed: %w", err)), nil
	}

	return tool.NewTextResult(fmt.Sprintf("Removed: %s", absPath)), nil
}

func hasWildcard(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func (t *RemoveTool) IsEnabled() bool                         { return true }
func (t *RemoveTool) IsReadOnly(_ map[string]any) bool        { return false }
func (t *RemoveTool) IsConcurrencySafe(_ map[string]any) bool { return false }
func (t *RemoveTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *RemoveTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *RemoveTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	if p, _ := in["path"].(string); p == "" {
		return nil, fmt.Errorf("path is required")
	}
	return in, nil
}
func (t *RemoveTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(nil)
}
func (t *RemoveTool) PreparePermissionMatcher(_ context.Context, _ map[string]any) (func(string) bool, error) {
	return nil, nil
}

func (t *RemoveTool) Description(_ context.Context) (string, error) {
	return "Delete a file or directory with safety checks.", nil
}
