// Package searchsession provides a cancellable, streaming search: unlike
// the grep/glob tools (which block until ripgrep finishes and return
// everything at once), search_start spawns ripgrep as a background job and
// returns immediately with a job_id - job_output (offset/tail-capable,
// see internal/tools/bash) paginates through matches as they accumulate,
// job_kill cancels a search that's taking too long or targeting too much,
// and task_list shows what's running. This reuses the existing bash
// background-task infrastructure wholesale (a ripgrep search is just
// another subprocess) rather than building a parallel job-tracking system.
//
// It also covers a gap ripgrep can't: .docx/.xlsx are zipped XML, invisible
// to a line-oriented text search tool. search_start additionally searches
// the *extracted* text of any Office files under the search path (via
// internal/officetext, the same native reader read_file uses) and returns
// those matches immediately in its own response - a literal substring scan,
// not regex, both because it's fast enough for the modest number of Office
// files a typical search path has and to avoid a hostile pattern causing
// catastrophic backtracking against document text.
package searchsession

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	bashTool "github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/grep"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	officetext "github.com/KPO-Tech/seshat-runtime/pkg/officetext"
)

const (
	// ToolName is the name of the streaming search tool.
	ToolName = "search_start"

	// SearchHint is a hint for tool search functionality.
	SearchHint = "start a cancellable background content search over a large directory tree"

	// ToolDescription is the description of the search_start tool.
	ToolDescription = "Start a content search as a background job instead of blocking until it finishes - use this instead of grep for a search that might be slow (huge directory, expensive regex) or where you want to see partial results / cancel early.\n\n" +
		"## When to use\n\n" +
		"- Large repositories or directory trees where a grep call might take a while\n" +
		"- You want to start a search, do something else, and check back on it later\n" +
		"- You want to search inside .docx/.xlsx file contents too — grep can't (they're zipped XML, not plain text), search_start extracts and searches their text natively\n\n" +
		"## When NOT to use\n\n" +
		"- A quick, targeted search in a small area — grep is simpler and returns results directly\n\n" +
		"## Workflow\n\n" +
		"1. search_start returns a job_id immediately, plus any Office-document (.docx/.xlsx) matches found right away (a fast, separate pass).\n" +
		"2. job_output(job_id) reads ripgrep's matches as they accumulate — offset works the same as read_file (negative = tail, i.e. last N matches).\n" +
		"3. job_kill(job_id) cancels the search early once you have enough, or if it's taking too long.\n" +
		"4. task_list shows all running searches (and other background jobs).\n"
)

// Tool implements search_start.
type Tool struct {
	workingDir       string
	filesystemPolicy *access.FilesystemPolicy
}

// NewTool creates the search_start tool.
func NewTool(workingDir string) *Tool {
	return &Tool{
		workingDir:       workingDir,
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Start Background Search",
		SearchHint:  SearchHint,
		Description: ToolDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "The regular expression pattern to search for in file contents",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "File or directory to search in. Defaults to current working directory",
				},
				"output_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"content", "files_with_matches"},
					"description": "content shows matching lines, files_with_matches shows file paths only",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Glob pattern to filter files (e.g. '*.js', '*.{ts,tsx}')",
				},
				"-i": map[string]any{
					"type":        "boolean",
					"description": "Case insensitive search",
				},
				"type": map[string]any{
					"type":        "string",
					"description": "File type to search (rg --type). Common types: js, py, rust, go, java, ts, etc.",
				},
			},
			"required": []string{"pattern"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

func (t *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	toolCtx := input.ToolContextValue()

	pattern, ok := input.Parsed["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return tool.NewErrorResult(fmt.Errorf("pattern is required and must be a string")), nil
	}

	searchPath, err := t.resolvePath("", toolCtx)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	if p, ok := input.Parsed["path"].(string); ok && p != "" {
		searchPath, err = t.resolvePath(p, toolCtx)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
	}
	if err := shared.ValidateUNCPathSecurity(searchPath); err != nil {
		return tool.NewErrorResult(err), nil
	}
	if _, statErr := os.Stat(searchPath); statErr != nil {
		return tool.NewErrorResult(shared.FormatNotFoundError(searchPath, t.workingDir)), nil
	}

	sandboxCtx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if ws := toolCtx.Workspace; ws != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(ws.Root)
	}
	policyDecision, policyErr := t.filesystemPolicy.EvaluatePath(sandboxCtx, searchPath, access.AccessSearch)
	if policyErr != nil {
		return tool.NewErrorResult(policyErr), nil
	}
	if err := access.ErrorForDecision(policyDecision.DecisionResult); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if permissionCheck != nil {
		req := access.PermissionRequest{
			ToolName:      ToolName,
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessSearch,
			Paths:         []string{searchPath},
			Justification: "Search file contents in the background",
			Scope:         access.ApprovalScopeToolCall,
		}
		permResult, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput:              map[string]any{"pattern": pattern, "path": searchPath},
			ToolUseID:              toolCtx.ToolUseID,
			SessionID:              toolCtx.SessionID,
			TurnID:                 toolCtx.TurnID,
			PermissionMode:         toolCtx.PermissionMode,
			WorkingDirectory:       t.effectiveWorkingDir(toolCtx),
			IsToolRunningInSandbox: toolCtx.EnableSandbox,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := access.ErrorForPermissionResult(permResult, "background search requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	outputMode := grep.OutputModeContent
	if v, _ := input.Parsed["output_mode"].(string); v == string(grep.OutputModeFilesWithMatches) {
		outputMode = grep.OutputModeFilesWithMatches
	}
	globPattern, _ := input.Parsed["glob"].(string)
	caseInsensitive, _ := input.Parsed["-i"].(bool)

	// Office content pass: immediate, not part of the background job.
	officeMatches, officeErr := searchOfficeFiles(searchPath, pattern, globPattern, caseInsensitive)

	// Ripgrep pass: backgrounded via the same job infrastructure write_stdin/
	// job_output/job_kill already drive for bash commands.
	rgArgs := grep.BuildRipgrepArgs(pattern, searchPath, input.Parsed, outputMode)
	mgr := bashTool.GlobalTaskManager()
	if mgr == nil {
		return tool.NewErrorResult(fmt.Errorf("no background task manager available")), nil
	}
	task, err := mgr.StartBackgroundTaskArgv(ctx, "rg", rgArgs, t.effectiveWorkingDir(toolCtx), nil)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			return tool.NewErrorResult(shared.RipgrepNotFoundError()), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to start search: %w", err)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Search started in background (job_id: %s)\n", task.ID)
	fmt.Fprintf(&b, "Use job_output(job_id=%q) to read ripgrep matches as they accumulate, job_kill to cancel.\n", task.ID)

	if officeErr != nil {
		fmt.Fprintf(&b, "\nOffice-document search (.docx/.xlsx) failed: %v\n", officeErr)
	} else if len(officeMatches) > 0 {
		fmt.Fprintf(&b, "\nOffice-document matches (%d, searched immediately, not part of the background job):\n", len(officeMatches))
		for _, m := range officeMatches {
			b.WriteString(m)
			b.WriteByte('\n')
		}
	}

	return tool.NewTextResult(b.String()), nil
}

// searchOfficeFiles walks searchPath for .docx/.pptx/.xlsx files (matching
// globPattern against the basename, if given), extracts each with
// officetext, and returns "file:line: text" matches for lines containing
// pattern as a literal substring (case-insensitive if requested). Bounded
// by the same VCS-directory exclusions as ripgrep so it doesn't descend
// into .git/node_modules/etc.
func searchOfficeFiles(searchPath, pattern, globPattern string, caseInsensitive bool) ([]string, error) {
	needle := pattern
	if caseInsensitive {
		needle = strings.ToLower(needle)
	}
	excluded := make(map[string]bool)
	for _, d := range shared.GetVCSDirectoriesToExclude() {
		excluded[d] = true
	}

	var matches []string
	root, _ := os.OpenRoot(searchPath) // nil when searchPath is a file
	if root != nil {
		defer root.Close()
	}
	walkErr := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort walk: skip unreadable entries, don't abort the whole search
		}
		if d.IsDir() {
			if excluded[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// regular files only: following a symlink found while walking would read a file outside the searched directory
		if !d.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !officetext.SupportedExtensions[ext] {
			return nil
		}
		if globPattern != "" {
			if ok, _ := filepath.Match(globPattern, d.Name()); !ok {
				return nil
			}
		}

		data, readErr := readUnderRoot(root, searchPath, path)
		if readErr != nil {
			return nil
		}
		// sparse (a PPTX with implausibly little text - see
		// officetext.MinCharsPerSlide) is irrelevant to a literal grep: it's
		// fine to search over whatever text natively extracted, without
		// forcing an OCR round-trip just to search a couple of slides.
		text, ok, _, extractErr := officetext.Extract(path, data)
		if !ok || extractErr != nil {
			return nil
		}

		for i, line := range strings.Split(text, "\n") {
			haystack := line
			if caseInsensitive {
				haystack = strings.ToLower(haystack)
			}
			if strings.Contains(haystack, needle) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	return matches, walkErr
}

// ── Tool interface plumbing ────────────────────────────────────────────────

func (t *Tool) Description(_ context.Context) (string, error) {
	return ToolDescription, nil
}

func (t *Tool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	pattern, ok := input["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is required and must be a string")
	}
	return input, nil
}

func (t *Tool) CheckPermissions(_ context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	searchPath, err := t.resolvePath("", toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	if p, ok := input["path"].(string); ok && p != "" {
		searchPath, err = t.resolvePath(p, toolCtx)
		if err != nil {
			return types.Deny(err.Error())
		}
	}
	if err := shared.ValidateUNCPathSecurity(searchPath); err != nil {
		return types.Deny(err.Error())
	}
	return types.Passthrough(input)
}

func (t *Tool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *Tool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *Tool) IsEnabled() bool                         { return true }
func (t *Tool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *Tool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}

func (t *Tool) resolvePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := t.effectiveWorkingDir(toolCtx)
	if strings.TrimSpace(path) == "" {
		return workingDir, nil
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

func (t *Tool) effectiveWorkingDir(toolCtx tool.ToolUseContext) string {
	if strings.TrimSpace(toolCtx.WorkingDirectory) != "" {
		return toolCtx.WorkingDirectory
	}
	if strings.TrimSpace(t.workingDir) != "" {
		return t.workingDir
	}
	return "."
}

// readUnderRoot reads a file found while walking base: through the root when base is a directory, so that a file swapped for a
// symlink between the walk and the read cannot take the read outside of base; directly when base is itself a file.
func readUnderRoot(root *os.Root, base, path string) ([]byte, error) {
	if root == nil {
		return os.ReadFile(path)
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return nil, err
	}
	return root.ReadFile(rel)
}
