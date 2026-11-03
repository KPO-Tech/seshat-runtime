// Package access decides whether a tool may do something: the policies on commands and paths,
// and the permission requests that go with them. It runs nothing: internal/sandbox does that.
package access

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/workspace"
)

// Context carries the execution boundary relevant to sandbox decisions.
type Context struct {
	WorkingDirectory string
	WorkspaceRoot    string
	AdditionalRoots  []string
	Environment      sandbox.EnvironmentKind
	SandboxEnabled   bool
}

// ResolvePath resolves a candidate path according to the current execution context.
// When a WorkspaceRoot is configured, relative paths are anchored to the root and
// validated to stay inside it. Absolute paths bypass workspace containment so the
// user can approve writes to /tmp or other out-of-workspace directories — the
// FilesystemPolicy dangerous-prefix check still applies downstream.
func (c Context) ResolvePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}

	if strings.TrimSpace(c.WorkspaceRoot) != "" {
		if filepath.IsAbs(path) {
			abs, err := filepath.Abs(filepath.Clean(path))
			if err != nil {
				return "", fmt.Errorf("resolve path %q: %w", path, err)
			}
			return abs, nil
		}
		return workspace.Resolve(path, c.WorkspaceRoot)
	}

	if !filepath.IsAbs(path) {
		base := strings.TrimSpace(c.WorkingDirectory)
		if base == "" {
			abs, err := filepath.Abs(path)
			if err != nil {
				return "", fmt.Errorf("resolve path %q: %w", path, err)
			}
			path = abs
		} else {
			path = filepath.Join(base, path)
		}
	}

	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	return abs, nil
}
