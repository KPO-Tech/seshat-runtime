// Package config provides the get_config tool: a read-only view of seshat's
// effective security policy (the same values write_file, bash, and every
// other write-capable tool actually enforce, not a separately maintained
// description of them), mirroring DesktopCommanderMCP's get_config. Unlike
// DesktopCommanderMCP, there is no set_config_value counterpart - seshat's
// policy isn't user-editable at runtime, so exposing it read-only avoids
// implying a control surface that doesn't exist.
package config

import (
	"context"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	bashTool "github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	fileReadTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/read"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	// ToolName is the name of the get_config tool.
	ToolName = "get_config"

	// SearchHint is a hint for tool search functionality.
	SearchHint = "view the effective security policy (blocked commands, protected paths, read limits)"

	// ToolDescription is the description of the get_config tool.
	ToolDescription = "Get the currently effective security policy: which commands need approval or are always blocked, " +
		"which paths are protected from reads/writes, file-read size/line limits, the sandbox status, and the active working directory/shell.\n\n" +
		"## When to use\n\n" +
		"- Before running a command or touching a path you suspect might be restricted, to check without trial-and-error\n" +
		"- To explain to the user why a prior tool call was blocked or required approval\n\n" +
		"## Notes\n\n" +
		"- Read-only — this tool cannot change any policy setting.\n"
)

// Tool implements the get_config tool.
type Tool struct {
	workingDir string
}

// NewTool creates a new get_config tool.
func NewTool(workingDir string) *Tool {
	return &Tool{workingDir: workingDir}
}

func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Get Config",
		SearchHint:  SearchHint,
		Description: ToolDescription,
		Category:    "configuration",
		InputSchema: schema.FromMap(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: false,
	}
}

func (t *Tool) Call(
	_ context.Context,
	input tool.CallInput,
	_ types.CanUseToolFn,
) (tool.CallResult, error) {
	toolCtx := input.ToolContextValue()

	cmdPolicy := sandbox.NewDefaultCommandPolicy()
	fsPolicy := sandbox.NewDefaultFilesystemPolicy()

	workingDirectory := toolCtx.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = t.workingDir
	}
	workspaceRoot := ""
	if toolCtx.Workspace != nil {
		workspaceRoot = toolCtx.Workspace.Root
	}

	output := map[string]any{
		"working_directory": workingDirectory,
		"workspace_root":    workspaceRoot,
		"sandbox_enabled":   toolCtx.EnableSandbox,
		"sandbox_available": bashTool.SandboxAvailable(),
		"sandbox_backend":   bashTool.SandboxBackend(),
		"default_shell":     bashTool.DetectShell(),
		"commands": map[string]any{
			"always_denied_fragments": cmdPolicy.DenyFragments(),
			"always_denied_patterns":  cmdPolicy.DenyPatterns(),
			"requires_approval":       cmdPolicy.AskCommands(),
		},
		"filesystem": map[string]any{
			"read_denied_path_prefixes":  fsPolicy.ReadDeniedPrefixes(),
			"write_denied_path_prefixes": fsPolicy.WriteDeniedPrefixes(),
		},
		"file_read_limits": map[string]any{
			"default_lines":     fileReadTool.DefaultLimit,
			"max_lines":         fileReadTool.MaxLimit,
			"max_file_size_mb":  fileReadTool.MaxFileSize / (1024 * 1024),
			"max_image_size_mb": fileReadTool.MaxImageSize / (1024 * 1024),
		},
	}

	return tool.NewJSONResult(output), nil
}

// ── Tool interface plumbing ────────────────────────────────────────────────

func (t *Tool) Description(_ context.Context) (string, error) {
	return ToolDescription, nil
}

func (t *Tool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	return input, nil
}

func (t *Tool) CheckPermissions(_ context.Context, input map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(input)
}

func (t *Tool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *Tool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *Tool) IsEnabled() bool                         { return true }
func (t *Tool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *Tool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}
