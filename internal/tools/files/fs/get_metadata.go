package fs

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// FileMetadata is the structured metadata returned by GetMetadataTool.
type FileMetadata struct {
	Path        string `json:"path"`
	IsFile      bool   `json:"is_file"`
	IsDirectory bool   `json:"is_directory"`
	IsSymlink   bool   `json:"is_symlink"`
	// SymlinkTarget is set when IsSymlink is true.
	SymlinkTarget string    `json:"symlink_target,omitempty"`
	SizeBytes     int64     `json:"size_bytes"`
	Mode          string    `json:"mode"`
	ModifiedAt    time.Time `json:"modified_at"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	Exists        bool      `json:"exists"`
}

// GetMetadataTool returns metadata (stat) for a path without reading its content.
type GetMetadataTool struct {
	workingDir string
}

func NewGetMetadataTool(workingDir string) *GetMetadataTool {
	return &GetMetadataTool{workingDir: workingDir}
}

func (t *GetMetadataTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "get_file_metadata",
		SearchHint:  "file size, modified date, permissions, type of a file",
		DisplayName: "Get File Metadata",
		Description: "Return metadata for a file or directory: size, type (file/dir/symlink), permissions, timestamps. Does not read file content.",
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to inspect",
				},
			},
			"required": []string{"path"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: false,
	}
}

func (t *GetMetadataTool) Call(
	ctx context.Context,
	input tool.CallInput,
	_ types.CanUseToolFn,
) (tool.CallResult, error) {
	rawPath, ok := input.Parsed["path"].(string)
	if !ok || rawPath == "" {
		return tool.NewErrorResult(fmt.Errorf("path is required")), nil
	}

	absPath, err := resolvePathIn(rawPath, t.workingDir, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	// Use Lstat so we can detect symlinks before following them.
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			meta := FileMetadata{Path: absPath, Exists: false}
			return tool.NewJSONResult(meta), nil
		}
		return tool.NewErrorResult(fmt.Errorf("stat failed: %w", err)), nil
	}

	meta := FileMetadata{
		Path:        absPath,
		Exists:      true,
		IsFile:      info.Mode().IsRegular(),
		IsDirectory: info.IsDir(),
		IsSymlink:   info.Mode()&os.ModeSymlink != 0,
		SizeBytes:   info.Size(),
		Mode:        info.Mode().String(),
		ModifiedAt:  info.ModTime(),
	}

	if meta.IsSymlink {
		target, err2 := os.Readlink(absPath)
		if err2 == nil {
			meta.SymlinkTarget = target
		}
	}

	_ = shared.GetAbsolutePath // ensure import used

	return tool.NewJSONResult(meta), nil
}

func (t *GetMetadataTool) IsEnabled() bool                         { return true }
func (t *GetMetadataTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *GetMetadataTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *GetMetadataTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *GetMetadataTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *GetMetadataTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	if p, _ := in["path"].(string); p == "" {
		return nil, fmt.Errorf("path is required")
	}
	return in, nil
}
func (t *GetMetadataTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(nil)
}
func (t *GetMetadataTool) PreparePermissionMatcher(_ context.Context, _ map[string]any) (func(string) bool, error) {
	return nil, nil
}

func (t *GetMetadataTool) Description(_ context.Context) (string, error) {
	return "Return metadata for a file or directory (size, type, permissions, timestamps).", nil
}
