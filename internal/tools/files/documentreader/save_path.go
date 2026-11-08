package documentreader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

// saveTarget resolves the save_path of read_document_url to the file the markdown may be written to, or says why it may not.
//
// The text comes from a page the agent does not control, and the tool is not one the user is asked about as a write, so the file has to
// be somewhere a write could not do harm: inside the workspace (or the working directory when there is none), not a file the platform
// protects, and not one that exists (a page must not be able to replace a source file by being read). An absolute path is accepted only
// when it lands inside that folder.
func saveTarget(savePath string, toolCtx tool.ToolUseContext) (string, error) {
	root := strings.TrimSpace(toolCtx.WorkingDirectory)
	if toolCtx.Workspace != nil && strings.TrimSpace(toolCtx.Workspace.Root) != "" {
		root = strings.TrimSpace(toolCtx.Workspace.Root)
	}
	if root == "" {
		return "", fmt.Errorf("there is no workspace to save in")
	}
	target := savePath
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)

	inside, err := shared.IsPathWithin(root, target)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", savePath, err)
	}
	if !inside {
		return "", fmt.Errorf("%q is outside the workspace", savePath)
	}
	if shared.IsDangerousFile(target) || shared.IsDangerousDirectory(target) {
		return "", fmt.Errorf("%q is a protected path", savePath)
	}
	decision, err := access.NewDefaultFilesystemPolicy().EvaluatePath(access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		WorkspaceRoot:    root,
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}, target, access.AccessWrite)
	if err != nil {
		return "", err
	}
	if err := access.ErrorForDecision(decision.DecisionResult); err != nil {
		return "", err
	}
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("%q already exists: save to a new file", savePath)
	}
	return target, nil
}
