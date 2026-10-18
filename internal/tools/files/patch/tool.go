package patch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ApplyPatchTool applies a structured multi-file patch in one atomic call.
type ApplyPatchTool struct {
	workingDir       string
	filesystemPolicy *sandbox.FilesystemPolicy
}

func NewApplyPatchTool(workingDir string) *ApplyPatchTool {
	return &ApplyPatchTool{
		workingDir:       workingDir,
		filesystemPolicy: sandbox.NewDefaultFilesystemPolicy(),
	}
}

func (t *ApplyPatchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "apply_patch",
		SearchHint:  "apply a unified diff patch to several files at once",
		DisplayName: "Apply Patch",
		Description: applyPatchDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch": map[string]any{
					"type":        "string",
					"description": "The patch text in apply_patch format (see tool description).",
				},
			},
			"required": []string{"patch"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

func (t *ApplyPatchTool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	patchText, ok := input.Parsed["patch"].(string)
	if !ok || strings.TrimSpace(patchText) == "" {
		return tool.NewErrorResult(fmt.Errorf("patch is required")), nil
	}

	// Parse before asking for permission (gives better error messages early).
	p, err := Parse(patchText)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("patch parse error: %w", err)), nil
	}

	// Determine effective working dir.
	workingDir := t.workingDir
	if d := input.ToolContextValue().WorkingDirectory; d != "" {
		workingDir = d
	}

	// Compute the structured change set BEFORE applying (Codex pattern:
	// verify_apply_patch_args / ApplyPatchAction). Used for approval and events.
	changes, err := p.AnalyzeChanges(workingDir)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("patch analysis error: %w", err)), nil
	}

	// Build a preview for the permission check.
	preview := buildPermissionPreview(p, workingDir)
	touchedPaths, err := collectTouchedPaths(p, workingDir)
	if err != nil {
		return tool.NewErrorResult(err), nil
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
	for _, path := range touchedPaths {
		decision, err := t.filesystemPolicy.EvaluatePath(sandboxCtx, path, sandbox.AccessWrite)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := sandbox.ErrorForDecision(decision.DecisionResult); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	if permissionCheck != nil {
		req := sandbox.PermissionRequest{
			ToolName:      "apply_patch",
			Description:   buildApprovalDescription(changes),
			Environment:   sandbox.EnvironmentLocal,
			Access:        sandbox.AccessWrite,
			Paths:         touchedPaths,
			Justification: preview,
			Scope:         sandbox.ApprovalScopeToolCall,
			Metadata: map[string]any{
				"preview": preview,
				"changes": changes, // structured []PatchChange for UI rendering
			},
		}
		res, err := sandbox.ResolveToolPermission(ctx, permissionCheck, req, sandbox.ToolPermissionOptions{
			ToolInput:              map[string]any{"patch": patchText, "preview": preview},
			ToolUseID:              toolCtx.ToolUseID,
			SessionID:              toolCtx.SessionID,
			TurnID:                 toolCtx.TurnID,
			PermissionMode:         toolCtx.PermissionMode,
			WorkingDirectory:       workingDir,
			IsToolRunningInSandbox: toolCtx.EnableSandbox,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := sandbox.ErrorForPermissionResult(res, "patch application requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	summary, err := p.Apply(ctx, workingDir, input.ToolContextValue().WorkingDirectory)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("apply_patch failed: %w", err)), nil
	}

	return tool.NewTextResult(formatSummary(summary)), nil
}

// buildApprovalDescription builds a human-readable approval description from
// the structured PatchChange list, analogous to Codex's FileChangeRequestApprovalParams.
func buildApprovalDescription(changes []PatchChange) string {
	if len(changes) == 0 {
		return "Apply patch (no file changes detected)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Apply patch touching %d file(s):\n", len(changes))
	for _, c := range changes {
		switch c.Kind {
		case ChangeKindAdd:
			fmt.Fprintf(&sb, "  + %s %s\n", filepath.Base(c.Path), c.DiffPreview)
		case ChangeKindDelete:
			fmt.Fprintf(&sb, "  - %s\n", filepath.Base(c.Path))
		case ChangeKindMove:
			fmt.Fprintf(&sb, "  ~ %s → %s\n", filepath.Base(c.Path), filepath.Base(c.MovePath))
		case ChangeKindUpdate:
			fmt.Fprintf(&sb, "  ~ %s\n", filepath.Base(c.Path))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// buildPermissionPreview lists the files that will be touched.
func buildPermissionPreview(p *Patch, workingDir string) string {
	var sb strings.Builder
	for _, h := range p.hunks {
		switch h.typ {
		case hunkAdd:
			fmt.Fprintf(&sb, "+ %s\n", h.path)
		case hunkDelete:
			fmt.Fprintf(&sb, "- %s\n", h.path)
		case hunkUpdate:
			if h.moveTo != "" {
				fmt.Fprintf(&sb, "~ %s → %s\n", h.path, h.moveTo)
			} else {
				fmt.Fprintf(&sb, "~ %s\n", h.path)
			}
		}
	}
	_ = workingDir
	return strings.TrimRight(sb.String(), "\n")
}

func collectTouchedPaths(p *Patch, workingDir string) ([]string, error) {
	seen := make(map[string]struct{})
	paths := make([]string, 0, len(p.hunks))
	for _, h := range p.hunks {
		abs, err := resolvePatchPath(workingDir, h.path)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", h.path, err)
		}
		if _, ok := seen[abs]; !ok {
			seen[abs] = struct{}{}
			paths = append(paths, abs)
		}
		if h.moveTo != "" {
			dst, err := resolvePatchPath(workingDir, h.moveTo)
			if err != nil {
				return nil, fmt.Errorf("resolve move target %q: %w", h.moveTo, err)
			}
			if _, ok := seen[dst]; !ok {
				seen[dst] = struct{}{}
				paths = append(paths, dst)
			}
		}
	}
	return paths, nil
}

func resolvePatchPath(workingDir string, raw string) (string, error) {
	if filepath.IsAbs(raw) {
		return shared.GetAbsolutePath(raw)
	}
	return shared.GetAbsolutePath(filepath.Join(workingDir, raw))
}

func formatSummary(s *ApplySummary) string {
	var parts []string
	for _, p := range s.Added {
		parts = append(parts, "Added: "+p)
	}
	for _, p := range s.Deleted {
		parts = append(parts, "Deleted: "+p)
	}
	for _, p := range s.Updated {
		parts = append(parts, "Updated: "+p)
	}
	for _, p := range s.Moved {
		parts = append(parts, "Moved: "+p)
	}
	if len(parts) == 0 {
		return "Patch applied (no files changed)."
	}
	return strings.Join(parts, "\n")
}

// ─── tool.Tool interface ──────────────────────────────────────────────────────

func (t *ApplyPatchTool) IsEnabled() bool                         { return true }
func (t *ApplyPatchTool) IsReadOnly(_ map[string]any) bool        { return false }
func (t *ApplyPatchTool) IsConcurrencySafe(_ map[string]any) bool { return false }
func (t *ApplyPatchTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *ApplyPatchTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *ApplyPatchTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	if p, _ := in["patch"].(string); strings.TrimSpace(p) == "" {
		return nil, fmt.Errorf("patch is required")
	}
	return in, nil
}
func (t *ApplyPatchTool) CheckPermissions(_ context.Context, input map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	patchText, _ := input["patch"].(string)
	if strings.TrimSpace(patchText) == "" {
		return types.Deny("patch is required")
	}
	return types.Passthrough(nil)
}
func (t *ApplyPatchTool) PreparePermissionMatcher(_ context.Context, input map[string]any) (func(string) bool, error) {
	patchText, _ := input["patch"].(string)
	p, err := Parse(patchText)
	if err != nil {
		return nil, err
	}
	// Build the set of paths touched by this patch.
	paths := make(map[string]bool)
	for _, h := range p.hunks {
		abs := h.path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(t.workingDir, abs)
		}
		abs, _ = shared.GetAbsolutePath(abs)
		paths[abs] = true
		if h.moveTo != "" {
			dst := h.moveTo
			if !filepath.IsAbs(dst) {
				dst = filepath.Join(t.workingDir, dst)
			}
			dst, _ = shared.GetAbsolutePath(dst)
			paths[dst] = true
		}
	}
	return func(pattern string) bool {
		for path := range paths {
			if matched, _ := filepath.Match(pattern, path); matched {
				return true
			}
			if strings.HasPrefix(path, pattern) {
				return true
			}
		}
		return false
	}, nil
}
func (t *ApplyPatchTool) Description(_ context.Context) (string, error) {
	return "Apply a structured multi-file patch (add, delete, update, rename files) in one call.", nil
}

const applyPatchDescription = `Apply a structured multi-file patch that can add, delete, update, and rename files in one atomic call.

## Format

` + "```" + `
*** Begin Patch
*** Add File: path/to/new_file.go
+package main
+
+func hello() {}
*** Delete File: path/to/obsolete.go
*** Update File: path/to/existing.go
@@ optional context hint
-old line to remove
+new line to add
 context line (must match exactly)
*** Update File: path/to/another.go
*** Move to: path/to/renamed.go
@@ func bar
-func bar() {
+func barRenamed() {
 	return nil
 }
*** End Patch
` + "```" + `

## Rules
- Every update block needs enough context lines (starting with a space) to uniquely identify the location.
- Use multiple @@ sections within one Update File block for non-adjacent changes.
- ` + "`*** Move to:`" + ` renames the file after applying the changes.
- Protected paths (.ssh, .env, etc.) are always refused.`
