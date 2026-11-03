package glob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	// MaxResults is the maximum number of files to return
	MaxResults = 100

	// MaxResultSizeBytes is the maximum total size in bytes for all results
	MaxResultSizeBytes = 1024 * 1024 // 1MB
)

// Tool implements the glob tool for finding files by pattern
type Tool struct {
	workingDir       string
	filesystemPolicy *access.FilesystemPolicy
}

// NewGlobTool creates a new glob tool
func NewGlobTool(workingDir string) *Tool {
	return &Tool{
		workingDir:       workingDir,
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

// Definition returns the tool definition
func (g *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Glob",
		SearchHint:  SearchHint,
		Description: ToolDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "The glob pattern to match files against (e.g., '*.go', '**/*.md')",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "The directory to search in. If not specified, the current working directory will be used",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "Skip first N results before applying MaxResults limit (default: 0)",
				},
				"maxResultSizeChars": map[string]any{
					"type":        "number",
					"description": "Maximum total size in characters for all results (default: 1MB). Useful for preventing huge outputs.",
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

// Call executes the tool
func (g *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	toolCtx := input.ToolContextValue()
	// Extract pattern
	pattern, ok := input.Parsed["pattern"].(string)
	if !ok || pattern == "" {
		return tool.NewErrorResult(fmt.Errorf("pattern is required and must be a string")), nil
	}

	// Extract path (optional)
	searchDir, err := g.resolvePath("", toolCtx)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	if path, ok := input.Parsed["path"].(string); ok && path != "" {
		searchDir, err = g.resolvePath(path, toolCtx)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// Validate search directory exists
	if _, err = os.Stat(searchDir); err != nil {
		// Format helpful error with suggestion
		formattedErr := formatNotFoundError(searchDir, g.workingDir)
		return tool.NewErrorResult(formattedErr), nil
	}

	// Security validation
	if err := validatePathForSecurity(searchDir); err != nil {
		return tool.NewErrorResult(err), nil
	}

	// Policy + permission check
	sandboxCtx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if ws := toolCtx.Workspace; ws != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(ws.Root)
	}
	policyDecision, policyErr := g.filesystemPolicy.EvaluatePath(sandboxCtx, searchDir, access.AccessSearch)
	if policyErr != nil {
		return tool.NewErrorResult(policyErr), nil
	}
	if err := access.ErrorForDecision(policyDecision.DecisionResult); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if permissionCheck != nil {
		effectiveDir := strings.TrimSpace(toolCtx.WorkingDirectory)
		if effectiveDir == "" {
			effectiveDir = strings.TrimSpace(g.workingDir)
		}
		req := access.PermissionRequest{
			ToolName:    ToolName,
			Environment: sandbox.EnvironmentLocal,
			Access:      access.AccessSearch,
			Paths:       []string{searchDir},
			Scope:       access.ApprovalScopeToolCall,
		}
		res, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput:              map[string]any{"pattern": pattern, "path": searchDir},
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
		if err := access.ErrorForPermissionResult(res, "glob search requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// Start timer
	startTime := time.Now()

	// Execute glob search
	matches, err := g.doGlob(ctx, searchDir, pattern)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("glob search failed: %w", err)), nil
	}

	// Calculate duration
	duration := time.Since(startTime)

	// Extract offset parameter
	offset := 0
	if off, ok := input.Parsed["offset"].(float64); ok {
		offset = int(off)
	}

	// Extract maxResultSizeChars parameter
	maxSize := MaxResultSizeBytes
	if size, ok := input.Parsed["maxResultSizeChars"].(float64); ok && size > 0 {
		maxSize = int(size)
	}

	// Store applied values for output
	var appliedOffset *int
	var appliedMaxSize *int

	// Apply offset
	if offset > 0 && offset < len(matches) {
		matches = matches[offset:]
		appliedOffset = &offset
	}

	// Truncate results if necessary
	truncated := false
	if len(matches) > MaxResults {
		matches = matches[:MaxResults]
		truncated = true
	}

	// Calculate total size and truncate if needed
	totalSize := 0
	for i, match := range matches {
		totalSize += len(match)
		if totalSize > maxSize {
			matches = matches[:i]
			truncated = true
			appliedMaxSize = &maxSize
			break
		}
	}

	// Prepare output with rich formatting
	output := map[string]any{
		"filenames":  matches,
		"numFiles":   len(matches), // int is fine here, JSON encoder will handle it
		"durationMs": duration.Milliseconds(),
		"truncated":  truncated,
	}

	// Add applied constraints if they were used
	if appliedOffset != nil {
		output["appliedOffset"] = *appliedOffset
	}
	if appliedMaxSize != nil {
		output["appliedMaxResultSizeChars"] = *appliedMaxSize
	}

	// Create human-readable message
	message := formatGlobResult(matches, len(matches), duration.Milliseconds(), truncated)

	// Return result with both JSON data and human-readable message
	result := tool.NewJSONResult(output)
	result.Content = message

	return result, nil
}

// Description returns the tool description
func (g *Tool) Description(ctx context.Context) (string, error) {
	return ToolDescription, nil
}

// ValidateInput validates and normalizes glob input.
func (g *Tool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	_ = ctx
	pattern, ok := input["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is required and must be a string")
	}
	return input, nil
}

// CheckPermissions performs glob-specific permission checks before the global pipeline.
func (g *Tool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	_ = ctx
	searchDir, err := g.resolvePath("", toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	if path, ok := input["path"].(string); ok && path != "" {
		searchDir, err = g.resolvePath(path, toolCtx)
		if err != nil {
			return types.Deny(err.Error())
		}
	}
	if err := validatePathForSecurity(searchDir); err != nil {
		return types.Deny(err.Error())
	}
	return types.Passthrough(input)
}

func (g *Tool) resolvePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := g.effectiveWorkingDir(toolCtx)
	if strings.TrimSpace(path) == "" {
		return workingDir, nil
	}
	if filepath.IsAbs(path) || strings.TrimSpace(workingDir) == "" {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

func (g *Tool) effectiveWorkingDir(toolCtx tool.ToolUseContext) string {
	if strings.TrimSpace(toolCtx.WorkingDirectory) != "" {
		return toolCtx.WorkingDirectory
	}
	if strings.TrimSpace(g.workingDir) != "" {
		return g.workingDir
	}
	return "."
}

// IsConcurrencySafe reports that glob searches can run concurrently.
func (g *Tool) IsConcurrencySafe(input map[string]any) bool {
	_ = input
	return true
}

// IsReadOnly reports that glob does not modify state.
func (g *Tool) IsReadOnly(input map[string]any) bool {
	_ = input
	return true
}

// IsEnabled returns whether this tool is currently active.
func (g *Tool) IsEnabled() bool { return true }

// FormatResult serialises the tool output into the tool_result content string.
func (g *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput enriches a shallow clone of the parsed input with derived fields.
func (g *Tool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

// doGlob performs the actual glob search using ripgrep
func (g *Tool) doGlob(ctx context.Context, searchDir string, pattern string) ([]string, error) {
	// Check for context cancellation before starting
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Handle absolute patterns by extracting base directory
	searchPath := searchDir
	searchPattern := pattern

	if filepath.IsAbs(pattern) {
		baseDir, relativePattern, err := extractGlobBaseDirectory(pattern)
		if err != nil {
			return nil, fmt.Errorf("failed to extract glob base directory: %w", err)
		}
		if baseDir != "" {
			searchPath = baseDir
			searchPattern = relativePattern
		}
	}

	// Build ripgrep arguments. We list candidate files from the search root and
	// apply Seshat's documented glob semantics ourselves so `*` does not cross path
	// separators while `**` remains recursive.
	args := g.buildRipgrepArgs()

	// Execute ripgrep
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = searchPath

	output, err := cmd.Output()
	if err != nil {
		// If ripgrep not found, return error
		if strings.Contains(err.Error(), "executable file not found") {
			return nil, shared.RipgrepNotFoundError()
		}
		// If no matches found, that's ok - return empty results
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			// No files found
			return []string{}, nil
		}
		return nil, fmt.Errorf("ripgrep execution failed: %w", err)
	}

	// Parse output - one file per line
	files := strings.Split(strings.TrimSpace(string(output)), "\n")

	// Filter empty lines
	var matches []string
	for _, file := range files {
		if file != "" {
			matches = append(matches, file)
		}
	}

	// Convert to relative paths from search directory and apply the documented
	// glob semantics on the relative path.
	var relativeMatches []string
	for _, match := range matches {
		// Check for cancellation during result processing
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		normalizedMatch := filepath.ToSlash(match)
		if !matchRelativeGlobPath(searchPattern, normalizedMatch) {
			continue
		}

		// ripgrep returns paths relative to cmd.Dir, so they're already relative
		relativeMatches = append(relativeMatches, match)
	}

	return relativeMatches, nil
}

// buildRipgrepArgs builds command line arguments for ripgrep in file-listing mode.
// User glob semantics are enforced in Go after candidate enumeration.
func (g *Tool) buildRipgrepArgs() []string {
	args := []string{
		"--files",         // List files instead of searching content
		"--sort=modified", // Sort by modification time (oldest first)
		"--hidden",        // Include hidden files
		"--no-ignore",     // Don't respect .gitignore (default behavior)
	}

	// Add ignore patterns from shared utilities
	ignorePatterns := shared.GetFileReadIgnorePatterns(context.Background())
	for _, ignorePattern := range ignorePatterns {
		args = append(args, "--glob", "!"+ignorePattern)
	}

	// Exclude VCS directories
	for _, vcsDir := range shared.GetVCSDirectoriesToExclude() {
		args = append(args, "--glob", "!"+vcsDir)
	}

	return args
}

func matchRelativeGlobPath(pattern string, relativePath string) bool {
	normalizedPattern := filepath.ToSlash(pattern)
	normalizedPath := filepath.ToSlash(relativePath)

	patternSegments := splitGlobSegments(normalizedPattern)
	pathSegments := splitGlobSegments(normalizedPath)
	return matchGlobSegments(patternSegments, pathSegments)
}

func splitGlobSegments(value string) []string {
	if value == "" || value == "." {
		return nil
	}
	return strings.Split(value, "/")
}

func matchGlobSegments(patternSegments []string, pathSegments []string) bool {
	if len(patternSegments) == 0 {
		return len(pathSegments) == 0
	}

	if patternSegments[0] == "**" {
		next := patternSegments[1:]
		if len(next) == 0 {
			return true
		}
		for i := 0; i <= len(pathSegments); i++ {
			if matchGlobSegments(next, pathSegments[i:]) {
				return true
			}
		}
		return false
	}

	if len(pathSegments) == 0 {
		return false
	}

	matched, err := path.Match(patternSegments[0], pathSegments[0])
	if err != nil || !matched {
		return false
	}

	return matchGlobSegments(patternSegments[1:], pathSegments[1:])
}

// extractGlobBaseDirectory extracts the static base directory from a glob pattern
// Returns (baseDir, relativePattern, error)
//
// Examples:
// - "/home/user/src/**/*.go" → ("/home/user/src", "**/*.go", nil)
// - "*.txt" → ("", "*.txt", nil)
// - "/home/user/file.txt" → ("/home/user", "file.txt", nil)
func extractGlobBaseDirectory(pattern string) (string, string, error) {
	// Find the first glob special character: *, ?, [, {
	globCharsIndex := -len(pattern)
	for i, c := range pattern {
		if c == '*' || c == '?' || c == '[' || c == '{' {
			globCharsIndex = i
			break
		}
	}

	// If no glob characters, this is a literal path
	if globCharsIndex < 0 {
		dir := filepath.Dir(pattern)
		file := filepath.Base(pattern)
		return dir, file, nil
	}

	// Get everything before the first glob character
	staticPrefix := pattern[:globCharsIndex]

	// Find the last path separator in the static prefix
	lastSepIndex := strings.LastIndexAny(staticPrefix, "/\\")

	if lastSepIndex == -1 {
		// No path separator before glob - pattern is relative to cwd
		return "", pattern, nil
	}

	baseDir := staticPrefix[:lastSepIndex]
	relativePattern := pattern[lastSepIndex+1:]

	// Handle root directory patterns (e.g., /*.txt on Unix)
	if baseDir == "" && lastSepIndex == 0 {
		baseDir = "/"
	}

	// Handle Windows drive root paths (e.g., C:/*.txt)
	// On Windows, "C:" means "current directory on drive C" (relative)
	// We need "C:/" or "C:\" for the actual drive root
	if len(baseDir) == 2 && strings.HasSuffix(baseDir, ":") {
		// This is a Windows drive letter without root
		baseDir = baseDir + string(filepath.Separator)
	}

	return baseDir, relativePattern, nil
}

// SetWorkingDir sets the working directory for the tool
func (g *Tool) SetWorkingDir(dir string) {
	g.workingDir = dir
}

// GetWorkingDir returns the current working directory
func (g *Tool) GetWorkingDir() string {
	return g.workingDir
}

// ── Tool metadata ──────────────────────────────────────────────────────────────

const (
	// ToolName is the name of the glob tool
	ToolName = "glob"

	// SearchHint is a hint for tool search functionality.
	SearchHint = "find files using glob patterns"

	// ToolDescription is the human-readable description of what the glob tool does
	ToolDescription = `Find files in the workspace by name or extension using glob patterns.

Use this tool to discover which files exist matching a pattern before reading or editing them. Prefer it over Bash find or ls for file discovery.

## When to use

- Finding all files of a given type: "*.go", "**/*.ts", "**/*.test.js"
- Locating a file when you know part of its name or path
- Checking whether a specific file or directory exists
- Building a list of files to process or read

## Do NOT use

- To search file contents — use Grep for that
- When you already know the full file path — read it directly

## Parameters

- pattern (required): Glob pattern to match against file paths
  - "*" matches any file in the current directory
  - "**/*.go" matches all Go files recursively
  - "src/**/*.{ts,tsx}" matches TypeScript files under src/
- path (optional): Directory to search from. Defaults to current working directory.

## Output

- filenames: Matching file paths (relative to search directory)
- numFiles: Total matches found
- durationMs: Search duration in milliseconds
- truncated: True when more than 100 files matched (results are capped)

## Examples

- All Go files: pattern="**/*.go"
- All Markdown files: pattern="**/*.md"
- Test files only: pattern="**/*_test.go"
- Files in a specific dir: pattern="internal/tools/**/*.go", path="."

## Notes

- Case-sensitive on Linux, case-insensitive on macOS/Windows
- Results are capped at 100 — narrow the pattern if truncated`
)
