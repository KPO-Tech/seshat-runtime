package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const maxListEntries = 500

// DirEntry represents a single entry in a directory listing.
type DirEntry struct {
	Name        string `json:"name"`
	IsDirectory bool   `json:"is_directory"`
	IsFile      bool   `json:"is_file"`
	IsSymlink   bool   `json:"is_symlink"`
	SizeBytes   int64  `json:"size_bytes"`
	Mode        string `json:"mode"`
}

// ListDirectoryTool lists the entries of a directory.
type ListDirectoryTool struct {
	workingDir string
}

func NewListDirectoryTool(workingDir string) *ListDirectoryTool {
	return &ListDirectoryTool{workingDir: workingDir}
}

func (t *ListDirectoryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "list_directory",
		SearchHint:  "list the files and folders inside a directory",
		DisplayName: "List Directory",
		Description: "List the files and directories inside a path. Returns name, type (file/dir/symlink), size and permissions for each entry.",
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path to list",
				},
				"show_hidden": map[string]any{
					"type":        "boolean",
					"description": "Include hidden files (starting with dot). Default false.",
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

func (t *ListDirectoryTool) Call(
	ctx context.Context,
	input tool.CallInput,
	_ types.CanUseToolFn,
) (tool.CallResult, error) {
	rawPath, ok := input.Parsed["path"].(string)
	if !ok || rawPath == "" {
		return tool.NewErrorResult(fmt.Errorf("path is required")), nil
	}
	showHidden, _ := input.Parsed["show_hidden"].(bool)

	absPath, err := resolvePathIn(rawPath, t.workingDir, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("path does not exist: %s", absPath)), nil
	}
	if !info.IsDir() {
		return tool.NewErrorResult(fmt.Errorf("path is not a directory: %s", absPath)), nil
	}

	des, err := os.ReadDir(absPath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to read directory: %w", err)), nil
	}

	entries := make([]DirEntry, 0, len(des))
	for _, de := range des {
		if !showHidden && strings.HasPrefix(de.Name(), ".") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		entry := DirEntry{
			Name:        de.Name(),
			IsDirectory: de.IsDir(),
			IsFile:      fi.Mode().IsRegular(),
			IsSymlink:   fi.Mode()&os.ModeSymlink != 0 || de.Type()&os.ModeSymlink != 0,
			SizeBytes:   fi.Size(),
			Mode:        fi.Mode().String(),
		}
		entries = append(entries, entry)
		if len(entries) >= maxListEntries {
			break
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		return entries[i].Name < entries[j].Name
	})

	result := map[string]any{
		"path":      absPath,
		"entries":   entries,
		"count":     len(entries),
		"truncated": len(entries) >= maxListEntries,
	}

	_ = shared.GetAbsolutePath
	return tool.NewJSONResult(result), nil
}

func (t *ListDirectoryTool) IsEnabled() bool                         { return true }
func (t *ListDirectoryTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *ListDirectoryTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *ListDirectoryTool) FormatResult(data any) string {
	if b, err := json.Marshal(data); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", data)
}
func (t *ListDirectoryTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *ListDirectoryTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	if p, _ := in["path"].(string); p == "" {
		return nil, fmt.Errorf("path is required")
	}
	return in, nil
}
func (t *ListDirectoryTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(nil)
}
func (t *ListDirectoryTool) PreparePermissionMatcher(_ context.Context, _ map[string]any) (func(string) bool, error) {
	return nil, nil
}

func (t *ListDirectoryTool) Description(_ context.Context) (string, error) {
	return "List the files and directories inside a path.", nil
}
