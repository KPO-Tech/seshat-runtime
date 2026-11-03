package grep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	// DefaultHeadLimit is the default limit on results
	DefaultHeadLimit = 250

	// MaxHeadLimit is the maximum limit to prevent abuse
	MaxHeadLimit = 1000
)

// OutputMode represents the grep output mode
type OutputMode string

const (
	OutputModeContent          OutputMode = "content"
	OutputModeFilesWithMatches OutputMode = "files_with_matches"
	OutputModeCount            OutputMode = "count"
)

// Tool implements the grep tool for searching file contents
type Tool struct {
	workingDir       string
	filesystemPolicy *access.FilesystemPolicy
}

// NewGrepTool creates a new grep tool
func NewGrepTool(workingDir string) *Tool {
	return &Tool{
		workingDir:       workingDir,
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

// Definition returns the tool definition
func (g *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Grep",
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
					"enum":        []string{"content", "files_with_matches", "count"},
					"description": "Output mode: content shows matching lines, files_with_matches shows file paths, count shows match counts",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Glob pattern to filter files (e.g. '*.js', '*.{ts,tsx}')",
				},
				"-i": map[string]any{
					"type":        "boolean",
					"description": "Case insensitive search",
				},
				"-n": map[string]any{
					"type":        "boolean",
					"description": "Show line numbers in output (only for content mode)",
				},
				"-C": map[string]any{
					"type":        "number",
					"description": "Number of lines before and after each match",
				},
				"-A": map[string]any{
					"type":        "number",
					"description": "Number of lines after each match",
				},
				"-B": map[string]any{
					"type":        "number",
					"description": "Number of lines before each match",
				},
				"head_limit": map[string]any{
					"type":        "number",
					"description": "Limit output to first N lines/entries (default: 250, 0 = unlimited)",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "Skip first N lines/entries before applying head_limit (default: 0)",
				},
				"type": map[string]any{
					"type":        "string",
					"description": "File type to search (rg --type). Common types: js, py, rust, go, java, ts, etc. More efficient than glob for standard file types.",
				},
				"multiline": map[string]any{
					"type":        "boolean",
					"description": "Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false.",
				},
				"context": map[string]any{
					"type":        "number",
					"description": "Number of lines to show before and after each match (rg -C). Alias for -C.",
				},
				"sort_by_mtime": map[string]any{
					"type":        "boolean",
					"description": "Sort results by modification time (oldest first). Useful for seeing changes over time.",
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
	workingDir := g.effectiveWorkingDir(toolCtx)
	// Extract pattern
	pattern, ok := input.Parsed["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return tool.NewErrorResult(fmt.Errorf("pattern is required and must be a string")), nil
	}

	// Extract path (optional)
	searchPath, err := g.resolvePath("", toolCtx)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	if path, ok := input.Parsed["path"].(string); ok && path != "" {
		searchPath, err = g.resolvePath(path, toolCtx)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// UNC path security check
	if err := shared.ValidateUNCPathSecurity(searchPath); err != nil {
		return tool.NewErrorResult(err), nil
	}

	// Validate path exists
	if _, err = os.Stat(searchPath); err != nil {
		// Format helpful error with suggestion
		formattedErr := shared.FormatNotFoundError(searchPath, g.workingDir)
		return tool.NewErrorResult(formattedErr), nil
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
	policyDecision, policyErr := g.filesystemPolicy.EvaluatePath(sandboxCtx, searchPath, access.AccessSearch)
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
			Paths:       []string{searchPath},
			Scope:       access.ApprovalScopeToolCall,
		}
		res, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput:              map[string]any{"pattern": pattern, "path": searchPath},
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
		if err := access.ErrorForPermissionResult(res, "grep search requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// Extract output mode
	outputMode := OutputModeFilesWithMatches
	if modeStr, ok := input.Parsed["output_mode"].(string); ok {
		outputMode = OutputMode(modeStr)
	}

	// Validate output mode
	if outputMode != OutputModeContent && outputMode != OutputModeFilesWithMatches && outputMode != OutputModeCount {
		return tool.NewErrorResult(fmt.Errorf("invalid output_mode: %s", outputMode)), nil
	}

	// Start timer
	startTime := time.Now()

	// Execute grep search
	result, err := g.doGrep(ctx, searchPath, pattern, input.Parsed, outputMode, workingDir)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("grep search failed: %w", err)), nil
	}

	// Calculate duration
	duration := time.Since(startTime)

	// Add duration to result
	result["durationMs"] = duration.Milliseconds()

	return tool.NewJSONResult(result), nil
}

// Description returns the tool description
func (g *Tool) Description(ctx context.Context) (string, error) {
	return ToolDescription, nil
}

// ValidateInput validates and normalizes grep input.
func (g *Tool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	_ = ctx
	pattern, ok := input["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is required and must be a string")
	}
	return input, nil
}

// CheckPermissions performs grep-specific permission checks before the global pipeline.
func (g *Tool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	_ = ctx
	searchPath, err := g.resolvePath("", toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	if path, ok := input["path"].(string); ok && path != "" {
		searchPath, err = g.resolvePath(path, toolCtx)
		if err != nil {
			return types.Deny(err.Error())
		}
	}
	if err := shared.ValidateUNCPathSecurity(searchPath); err != nil {
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

// IsConcurrencySafe reports that grep searches can run concurrently.
func (g *Tool) IsConcurrencySafe(input map[string]any) bool {
	_ = input
	return true
}

// IsReadOnly reports that grep does not modify state.
func (g *Tool) IsReadOnly(input map[string]any) bool {
	_ = input
	return true
}

// IsEnabled returns whether this tool is currently active.
func (g *Tool) IsEnabled() bool { return true }

// FormatResult serialises the tool output into the tool_result content string.
// For content mode, returns the matching lines directly so both the LLM and
// the TUI receive readable grep output rather than a raw Go map string.
func (g *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	if m, ok := data.(map[string]any); ok {
		if content, ok := m["content"].(string); ok && content != "" {
			return content
		}
		if b, err := json.Marshal(m); err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput enriches a shallow clone of the parsed input with derived fields.
func (g *Tool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

// doGrep performs the actual grep search using ripgrep
func (g *Tool) doGrep(
	ctx context.Context,
	searchPath string,
	pattern string,
	params map[string]any,
	outputMode OutputMode,
	workingDir string,
) (map[string]any, error) {
	// Build ripgrep command
	args := g.buildRipgrepArgs(pattern, searchPath, params, outputMode)

	// Execute ripgrep
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = workingDir

	output, err := cmd.Output()
	if err != nil {
		// If ripgrep not found, return error
		if strings.Contains(err.Error(), "executable file not found") {
			return nil, shared.RipgrepNotFoundError()
		}
		// If no matches found, that's ok - return empty results
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			// No matches found
			return g.formatEmptyResults(outputMode), nil
		}
		return nil, fmt.Errorf("ripgrep execution failed: %w", err)
	}

	// Parse output based on mode
	result := make(map[string]any)
	result["mode"] = string(outputMode)

	switch outputMode {
	case OutputModeContent:
		return g.parseContentOutput(string(output), params)
	case OutputModeFilesWithMatches:
		return g.parseFilesOutput(string(output), params)
	case OutputModeCount:
		return g.parseCountOutput(string(output), params)
	}

	return result, nil
}

// buildRipgrepArgs builds command line arguments for ripgrep.
// Thin wrapper over the package-level BuildRipgrepArgs, which doesn't
// depend on any Tool state - factored out so other search tools (e.g.
// search_start's streaming/cancellable search) can build an identical rg
// invocation without duplicating this logic or depending on this Tool type.
func (g *Tool) buildRipgrepArgs(
	pattern string,
	searchPath string,
	params map[string]any,
	outputMode OutputMode,
) []string {
	return BuildRipgrepArgs(pattern, searchPath, params, outputMode)
}

// BuildRipgrepArgs builds command line arguments for ripgrep from the same
// param shape the grep tool accepts (pattern, path, glob, -i, -n, -A/-B/-C,
// context, type, multiline, sort_by_mtime) plus the desired output mode.
func BuildRipgrepArgs(
	pattern string,
	searchPath string,
	params map[string]any,
	outputMode OutputMode,
) []string {
	args := []string{"--hidden"} // Start with --hidden to show hidden files

	// Sort by modification time if requested
	if sortByMtime, ok := params["sort_by_mtime"].(bool); ok && sortByMtime {
		args = append(args, "--sort=modified")
	}

	// Exclude VCS directories to avoid noise from version control metadata
	for _, vcsDir := range shared.GetVCSDirectoriesToExclude() {
		args = append(args, "--glob", "!"+vcsDir)
	}

	// Limit line length to prevent base64/minified content from cluttering output
	args = append(args, "--max-columns", "500")

	// Add multiline flags if specified
	if multiline, ok := params["multiline"].(bool); ok && multiline {
		args = append(args, "-U", "--multiline-dotall")
	}

	// Add case insensitive flag
	if ignoreCase, ok := params["-i"].(bool); ok && ignoreCase {
		args = append(args, "-i")
	}

	// Add line numbers for content mode
	if outputMode == OutputModeContent {
		showLineNumbers := true
		if showNum, ok := params["-n"].(bool); ok {
			showLineNumbers = showNum
		}
		if showLineNumbers {
			args = append(args, "-n")
		}

		// Add context flags (context takes precedence over -C/-A/-B)
		if context, ok := params["context"].(float64); ok && context > 0 {
			args = append(args, "-C", fmt.Sprintf("%.0f", context))
		} else {
			if contextC, ok := params["-C"].(float64); ok && contextC > 0 {
				args = append(args, "-C", fmt.Sprintf("%.0f", contextC))
			}
			if afterContext, ok := params["-A"].(float64); ok && afterContext > 0 {
				args = append(args, "-A", fmt.Sprintf("%.0f", afterContext))
			}
			if beforeContext, ok := params["-B"].(float64); ok && beforeContext > 0 {
				args = append(args, "-B", fmt.Sprintf("%.0f", beforeContext))
			}
		}
	}

	// Add type filter if specified
	if fileType, ok := params["type"].(string); ok && fileType != "" {
		args = append(args, "--type", fileType)
	}

	// Add glob filter
	if glob, ok := params["glob"].(string); ok && glob != "" {
		args = append(args, "-g", glob)
	}

	// Add mode-specific flags
	switch outputMode {
	case OutputModeFilesWithMatches:
		args = append(args, "-l") // Only filenames
	case OutputModeCount:
		args = append(args, "-c") // Count matches
	case OutputModeContent:
		args = append(args, "--no-heading") // Consistent format
	}

	// If pattern starts with dash, use -e flag to specify it as a pattern
	// This prevents ripgrep from interpreting it as a command-line option
	if strings.HasPrefix(pattern, "-") {
		args = append(args, "-e", pattern)
	} else {
		args = append(args, pattern)
	}

	// Add search path
	args = append(args, searchPath)

	return args
}

// parseContentOutput parses ripgrep output in content mode
func (g *Tool) parseContentOutput(output string, params map[string]any) (map[string]any, error) {
	lines := strings.Split(output, "\n")

	// Get head_limit and offset
	headLimit := DefaultHeadLimit
	if limit, ok := params["head_limit"].(float64); ok {
		headLimit = int(limit)
		if headLimit > MaxHeadLimit {
			headLimit = MaxHeadLimit
		}
	}

	offset := 0
	if off, ok := params["offset"].(float64); ok {
		offset = int(off)
	}

	// Store appliedLimit and appliedOffset for output formatting
	var appliedLimit *int
	var appliedOffset *int

	// Apply offset
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
		appliedOffset = &offset
	}

	// Apply head limit
	truncated := false
	if headLimit > 0 && len(lines) > headLimit {
		lines = lines[:headLimit]
		truncated = true
		appliedLimit = &headLimit
	}

	// Parse unique files
	fileSet := make(map[string]bool)
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Parse format: "filename:line:content" or "filename:line:content:"
		parts := strings.SplitN(line, ":", 3)
		if len(parts) >= 1 {
			filename := parts[0]
			fileSet[filename] = true
		}
	}

	// Convert to sorted list
	filenames := make([]string, 0, len(fileSet))
	for file := range fileSet {
		filenames = append(filenames, file)
	}

	result := map[string]any{
		"mode":      string(OutputModeContent),
		"content":   strings.Join(lines, "\n"),
		"numLines":  len(lines),
		"numFiles":  len(filenames),
		"filenames": filenames,
		"truncated": truncated,
	}

	// Add appliedLimit and appliedOffset if they were set
	if appliedLimit != nil {
		result["appliedLimit"] = *appliedLimit
	}
	if appliedOffset != nil {
		result["appliedOffset"] = *appliedOffset
	}

	return result, nil
}

// parseFilesOutput parses ripgrep output in files_with_matches mode
func (g *Tool) parseFilesOutput(output string, params map[string]any) (map[string]any, error) {
	lines := strings.Split(output, "\n")

	// Filter empty lines
	var files []string
	for _, line := range lines {
		if line != "" {
			files = append(files, line)
		}
	}

	// Get head_limit and offset
	headLimit := DefaultHeadLimit
	if limit, ok := params["head_limit"].(float64); ok {
		headLimit = int(limit)
		if headLimit > MaxHeadLimit {
			headLimit = MaxHeadLimit
		}
	}

	offset := 0
	if off, ok := params["offset"].(float64); ok {
		offset = int(off)
	}

	// Store appliedLimit and appliedOffset for output formatting
	var appliedLimit *int
	var appliedOffset *int

	// Apply offset
	if offset > 0 && offset < len(files) {
		files = files[offset:]
		appliedOffset = &offset
	}

	// Apply head limit
	truncated := false
	if headLimit > 0 && len(files) > headLimit {
		files = files[:headLimit]
		truncated = true
		appliedLimit = &headLimit
	}

	result := map[string]any{
		"mode":      string(OutputModeFilesWithMatches),
		"filenames": files,
		"numFiles":  len(files),
		"truncated": truncated,
	}

	// Add appliedLimit and appliedOffset if they were set
	if appliedLimit != nil {
		result["appliedLimit"] = *appliedLimit
	}
	if appliedOffset != nil {
		result["appliedOffset"] = *appliedOffset
	}

	return result, nil
}

// parseCountOutput parses ripgrep output in count mode
func (g *Tool) parseCountOutput(output string, params map[string]any) (map[string]any, error) {
	lines := strings.Split(output, "\n")

	// Parse counts
	counts := make(map[string]int)
	var files []string

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Parse format: "filename:count"
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			filename := parts[0]
			countStr := strings.TrimSpace(parts[1])
			count, err := strconv.Atoi(countStr)
			if err == nil {
				counts[filename] = count
				files = append(files, filename)
			}
		}
	}

	// Get head_limit and offset
	headLimit := DefaultHeadLimit
	if limit, ok := params["head_limit"].(float64); ok {
		headLimit = int(limit)
		if headLimit > MaxHeadLimit {
			headLimit = MaxHeadLimit
		}
	}

	offset := 0
	if off, ok := params["offset"].(float64); ok {
		offset = int(off)
	}

	// Store appliedLimit and appliedOffset for output formatting
	var appliedLimit *int
	var appliedOffset *int

	// Apply offset
	if offset > 0 && offset < len(files) {
		files = files[offset:]
		appliedOffset = &offset
	}

	// Apply head limit
	truncated := false
	if headLimit > 0 && len(files) > headLimit {
		files = files[:headLimit]
		truncated = true
		appliedLimit = &headLimit
	}

	// Build counts map for truncated files
	filteredCounts := make(map[string]int)
	totalMatches := 0
	for _, file := range files {
		filteredCounts[file] = counts[file]
		totalMatches += counts[file]
	}

	result := map[string]any{
		"mode":       string(OutputModeCount),
		"counts":     filteredCounts,
		"filenames":  files,
		"numFiles":   len(files),
		"numMatches": totalMatches,
		"truncated":  truncated,
	}

	// Add appliedLimit and appliedOffset if they were set
	if appliedLimit != nil {
		result["appliedLimit"] = *appliedLimit
	}
	if appliedOffset != nil {
		result["appliedOffset"] = *appliedOffset
	}

	return result, nil
}

// formatEmptyResults returns empty results for when no matches are found
func (g *Tool) formatEmptyResults(outputMode OutputMode) map[string]any {
	result := map[string]any{
		"mode":      string(outputMode),
		"filenames": []string{},
		"numFiles":  0,
		"truncated": false,
	}

	switch outputMode {
	case OutputModeContent:
		result["content"] = ""
		result["numLines"] = 0
	case OutputModeCount:
		result["counts"] = map[string]int{}
	}

	return result
}

// SetWorkingDir sets the working directory for the tool
func (g *Tool) SetWorkingDir(dir string) {
	g.workingDir = dir
}

// GetWorkingDir returns the current working directory
func (g *Tool) GetWorkingDir() string {
	return g.workingDir
}

// ValidatePattern checks if the regex pattern is valid
func ValidatePattern(pattern string) error {
	// Check for empty pattern
	if pattern == "" {
		return fmt.Errorf("pattern cannot be empty")
	}

	_, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid regex pattern: %w", err)
	}
	return nil
}

// parseFileLine parses a ripgrep content line into filename, line number, and content
func parseFileLine(line string) (filename string, lineNum int, content string, err error) {
	// Format: "filename:line:content" or "filename:line:content:"
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return "", 0, "", fmt.Errorf("invalid format")
	}

	filename = parts[0]
	lineNumStr := parts[1]
	content = parts[2]

	lineNum, err = strconv.Atoi(lineNumStr)
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid line number: %w", err)
	}

	return filename, lineNum, content, nil
}

// ── Tool metadata ──────────────────────────────────────────────────────────────

const (
	// ToolName is the name of the grep tool
	ToolName = "grep"

	// SearchHint is a hint for tool search functionality.
	SearchHint = "search for text patterns in files using regular expressions"

	// ToolDescription is the human-readable description of what the grep tool does
	ToolDescription = `Search for text patterns in files using regular expressions.

Powered by ripgrep. Use this tool to locate symbols, references, patterns, or any text across the codebase. Prefer it over Bash grep or rg for all content searches.

## When to use

- Finding where a function, type, or variable is defined or referenced
- Locating all usages of a constant, error message, or config key
- Scanning logs or output files for error patterns
- Narrowing down files before reading them

## Do NOT use

- To find files by name or extension — use Glob instead
- When you already know the exact file — read it directly

## Parameters

- pattern (required): Regular expression to search for
- path (optional): File or directory to search in. Defaults to current working directory.
- output_mode (optional): Controls what is returned
  - "content" — matching lines with surrounding context (default when you need to read the match)
  - "files_with_matches" — only file paths (default; use to discover which files match)
  - "count" — match counts per file
- glob (optional): Filter files by extension or name pattern (e.g. "*.go", "*.{ts,tsx}")
- -i (optional): Case-insensitive search
- -n (optional): Include line numbers in content mode (default: true)
- -C / -A / -B (optional): Context lines around each match (before+after / after / before)
- head_limit (optional): Max lines/entries to return (default: 250, 0 = unlimited)
- offset (optional): Skip first N entries before applying head_limit

## Output

- filenames: Files that contain at least one match
- content: Matching lines with context (content mode only)
- counts: Per-file match counts (count mode only)
- numFiles, numLines, durationMs, truncated

## Examples

- Find a function definition: pattern="func NewSession", glob="*.go"
- Find TODO comments with context: pattern="TODO", output_mode="content", -C=2
- Count errors by file: pattern="ERROR", output_mode="count", path="logs/"
- Search only TypeScript: pattern="interface User", glob="*.ts"

## Notes

- Automatically excludes .git, node_modules, vendor, and other noise dirs
- Supports full extended regex (ERE)
- Results are capped to prevent excessive output — use head_limit=0 to override`
)
