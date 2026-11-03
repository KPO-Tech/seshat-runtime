package edit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	fileReadTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/read"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	// ToolName is the name of the edit tool
	ToolName = "edit_file"

	// ToolDescription is the description of the edit tool
	ToolDescription = "Edit an existing file by replacing an exact string with a new one. The canonical tool for all partial modifications to existing files.\n\n" +
		"## When to use\n\n" +
		"- Modifying any part of an existing file: function bodies, config values, imports, constants\n" +
		"- Renaming a symbol across a file (use replace_all)\n" +
		"- Any edit where you are changing less than the whole file\n\n" +
		"## When NOT to use\n\n" +
		"- Complete file rewrites — use write_file instead\n" +
		"- Creating a new file — use write_file instead\n\n" +
		"## Rules\n\n" +
		"- You must read the file with read_file before editing. This tool errors if you have not read the file first.\n" +
		"- Match indentation exactly as it appears in the file (tabs vs. spaces). Never include line-number prefixes from Read output in old_string or new_string.\n" +
		"- old_string must be unique in the file. If it appears more than once, provide more surrounding lines for context, or use replace_all.\n" +
		"- Use replace_all to rename a variable or string across the entire file.\n" +
		"- Use the smallest old_string that is clearly unique — 2–4 adjacent lines is usually enough.\n"

	leftSingleCurlyQuote  = '‘'
	rightSingleCurlyQuote = '’'
	leftDoubleCurlyQuote  = '“'
	rightDoubleCurlyQuote = '”'
	patchContextLines     = 3
)

// StructuredPatchHunk represents a single structured diff hunk.
type StructuredPatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// GitDiff is the shared git diff summary, re-aliased here for backward compat.
type GitDiff = shared.GitDiff

// Tool implements the edit file tool
type Tool struct {
	workingDir       string
	filesystemPolicy *access.FilesystemPolicy
}

// NewEditTool creates a new edit file tool
func NewEditTool(workingDir string) *Tool {
	return &Tool{
		workingDir:       workingDir,
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

// Definition returns the tool definition
func (e *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Edit File",
		SearchHint:  SearchHint,
		Description: ToolDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "The path to the file to edit",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The text to replace",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The text to replace it with (must be different from old_string)",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace all occurrences of old_string (default false)",
				},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		IsDestructive:      true,
		RequiresPermission: true,
	}
}

// Call executes the tool
func (e *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	filePath, ok := input.Parsed["file_path"].(string)
	if !ok || filePath == "" {
		return tool.NewErrorResult(fmt.Errorf("file_path is required and must be a string")), nil
	}
	if err := shared.ValidateFilePath(filePath, "editing"); err != nil {
		return tool.NewErrorResult(err), nil
	}

	oldString, ok := input.Parsed["old_string"].(string)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("old_string is required and must be a string")), nil
	}

	newString, ok := input.Parsed["new_string"].(string)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("new_string is required and must be a string")), nil
	}

	if oldString == newString {
		return tool.NewErrorResult(fmt.Errorf("old_string and new_string must be different")), nil
	}

	replaceAll := false
	if replaceAllVal, ok := input.Parsed["replace_all"].(bool); ok {
		replaceAll = replaceAllVal
	}

	if err := shared.ValidateFilePath(filePath, "editing"); err != nil {
		return tool.NewErrorResult(err), nil
	}

	absolutePath, err := e.resolvePath(filePath, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	if err := shared.ValidateUNCPathSecurity(absolutePath); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if strings.EqualFold(filepath.Ext(absolutePath), ".ipynb") {
		return tool.NewErrorResult(fmt.Errorf("file is a Jupyter Notebook. Use a notebook editing tool instead: %s", filePath)), nil
	}

	if err := shared.ValidateSensitivePath(absolutePath, "editTool", newString); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if info, statErr := os.Stat(absolutePath); statErr == nil && info.IsDir() {
		return tool.NewErrorResult(fmt.Errorf("path is a directory, not a file: %s", filePath)), nil
	}

	// Pre-permission guard: check read state from cache (no I/O).
	// For existing files with old_string, the read state must already be present.
	readState, hasReadState := fileReadTool.GetLastReadState(absolutePath)
	normalizedOldString := normalizeLineEndings(oldString)

	if normalizedOldString != "" {
		// Editing existing content: file must have been read.
		if _, statErr := os.Stat(absolutePath); statErr == nil {
			// File exists.
			if !hasReadState {
				return tool.NewErrorResult(fmt.Errorf("file has not been read yet. Read it first before editing it: %s", filePath)), nil
			}
			if readState.IsPartialView {
				return tool.NewErrorResult(fmt.Errorf("file has not been fully read yet. Read it first before editing it: %s", filePath)), nil
			}
		}
		// File does not exist and old_string is non-empty → will be caught below after content read.
	}

	toolCtx := input.ToolContextValue()
	sandboxCtx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if ws := toolCtx.Workspace; ws != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(ws.Root)
	}
	policyDecision, policyErr := e.filesystemPolicy.EvaluatePath(sandboxCtx, absolutePath, access.AccessWrite)
	if policyErr != nil {
		return tool.NewErrorResult(policyErr), nil
	}
	if err := access.ErrorForDecision(policyDecision.DecisionResult); err != nil {
		return tool.NewErrorResult(err), nil
	}

	if permissionCheck != nil {
		effectiveDir := strings.TrimSpace(toolCtx.WorkingDirectory)
		if effectiveDir == "" {
			effectiveDir = strings.TrimSpace(e.workingDir)
		}
		req := access.PermissionRequest{
			ToolName:      ToolName,
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessWrite,
			Paths:         []string{absolutePath},
			Justification: "Edit file content",
			Scope:         access.ApprovalScopeToolCall,
		}
		res, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput: map[string]any{
				"file_path":   absolutePath,
				"old_string":  oldString,
				"new_string":  newString,
				"replace_all": replaceAll,
			},
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
		if err := access.ErrorForPermissionResult(res, "file editing requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// Read file content AFTER permission is granted.
	rawOriginalContent, fileExists, err := readFileIfExists(absolutePath)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	normalizedNewString := normalizeLineEndings(newString)

	if fileExists {
		if normalizedOldString == "" {
			if strings.TrimSpace(normalizeLineEndings(rawOriginalContent)) != "" {
				return tool.NewErrorResult(fmt.Errorf("cannot create new file - file already exists: %s", filePath)), nil
			}
		} else if hasReadState {
			// Post-permission staleness check.
			if currentInfo, statErr := os.Stat(absolutePath); statErr == nil {
				readModTime := time.Unix(readState.Timestamp, 0)
				if currentInfo.ModTime().After(readModTime) {
					if normalizeLineEndings(rawOriginalContent) != normalizeLineEndings(readState.Content) {
						return tool.NewErrorResult(fmt.Errorf("file has been modified since read (read at %s, file modified at %s). Read it again first: %s",
							readModTime.Format(time.RFC3339),
							currentInfo.ModTime().Format(time.RFC3339),
							filePath)), nil
					}
				}
			}
		}
	} else if normalizedOldString != "" {
		return tool.NewErrorResult(fmt.Errorf("file not found: %s", filePath)), nil
	}

	normalizedOriginalContent := normalizeLineEndings(rawOriginalContent)
	preferredLineEnding := detectPreferredLineEnding(rawOriginalContent)
	if preferredLineEnding == "" {
		preferredLineEnding = "\n"
	}

	actualOldString := ""
	actualNewString := normalizedNewString
	replacementsMade := 0
	var updatedNormalizedContent string

	if normalizedOldString == "" {
		updatedNormalizedContent = actualNewString
		replacementsMade = 1
	} else {
		actualOldString = FindActualString(normalizedOriginalContent, normalizedOldString)
		if actualOldString == "" {
			if match, ok := FindFuzzyMatch(normalizedOriginalContent, normalizedOldString); ok {
				return tool.NewErrorResult(fmt.Errorf(
					"old_string not found in file: %s\n\nClosest match in the file (%.0f%% similar):\n%s\n\nDiff vs your old_string ({-in your old_string, not in the file-} / {+in the file, not in your old_string+}):\n%s",
					filePath, match.Similarity*100, match.Text, CharDiff(normalizedOldString, match.Text),
				)), nil
			}
			return tool.NewErrorResult(fmt.Errorf("old_string not found in file: %s", filePath)), nil
		}

		occurrences := strings.Count(normalizedOriginalContent, actualOldString)
		if occurrences > 1 && !replaceAll {
			return tool.NewErrorResult(fmt.Errorf("found %d matches of old_string in file. Set replace_all=true or provide more context to make old_string unique: %s", occurrences, filePath)), nil
		}

		actualNewString = preserveQuoteStyle(normalizedOldString, actualOldString, normalizedNewString)
		if replaceAll {
			updatedNormalizedContent = strings.ReplaceAll(normalizedOriginalContent, actualOldString, actualNewString)
			replacementsMade = occurrences
		} else {
			updatedNormalizedContent = strings.Replace(normalizedOriginalContent, actualOldString, actualNewString, 1)
			replacementsMade = 1
		}
	}

	oldLines := countLines(normalizedOriginalContent)
	newLines := countLines(updatedNormalizedContent)
	linesAdded := 0
	linesRemoved := 0
	if newLines > oldLines {
		linesAdded = newLines - oldLines
	} else {
		linesRemoved = oldLines - newLines
	}

	structuredPatch := buildStructuredPatch(normalizedOriginalContent, updatedNormalizedContent)

	if err := os.MkdirAll(filepath.Dir(absolutePath), 0755); err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to create parent directories: %w", err)), nil
	}

	contentToWrite := applyLineEnding(updatedNormalizedContent, preferredLineEnding)
	if err := os.WriteFile(absolutePath, []byte(contentToWrite), 0644); err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to write file: %w", err)), nil
	}

	if info, statErr := os.Stat(absolutePath); statErr == nil {
		fileReadTool.RecordExternalRead(absolutePath, info.ModTime(), updatedNormalizedContent, true)
	}

	output := map[string]any{
		"file_path":         filePath,
		"old_string":        actualOldString,
		"new_string":        actualNewString,
		"replace_all":       replaceAll,
		"replacements_made": replacementsMade,
		"lines_added":       linesAdded,
		"lines_removed":     linesRemoved,
		"success":           true,
		"original_file":     normalizedOriginalContent,
		"structured_patch":  structuredPatch,
		"user_modified":     false,
	}
	if gitDiff, ok := shared.ComputeGitDiff(ctx, absolutePath); ok {
		output["git_diff"] = gitDiff
	}

	res := tool.NewJSONResult(output)
	res.Metadata = &tool.ResultMetadata{Additional: output}
	return res, nil
}

// Description returns the tool description
func (e *Tool) Description(ctx context.Context) (string, error) {
	return ToolDescription, nil
}

// ValidateInput validates and normalizes edit input.
func (e *Tool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	_ = ctx
	filePath, ok := input["file_path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("file_path is required and must be a string")
	}
	oldString, ok := input["old_string"].(string)
	if !ok {
		return nil, fmt.Errorf("old_string is required and must be a string")
	}
	newString, ok := input["new_string"].(string)
	if !ok {
		return nil, fmt.Errorf("new_string is required and must be a string")
	}
	if oldString == newString {
		return nil, fmt.Errorf("old_string and new_string must be different")
	}
	return input, nil
}

// CheckPermissions performs edit-specific permission checks before the global pipeline.
func (e *Tool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	_ = ctx
	filePath, _ := input["file_path"].(string)
	absolutePath, err := e.resolvePath(filePath, toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	if err := shared.ValidateFilePath(filePath, "editing"); err != nil {
		return types.Deny(err.Error())
	}
	if err := shared.ValidateFilePath(absolutePath, "editing"); err != nil {
		return types.Deny(err.Error())
	}
	if err := shared.ValidateUNCPathSecurity(absolutePath); err != nil {
		return types.Deny(err.Error())
	}
	newString, _ := input["new_string"].(string)
	if err := shared.ValidateSensitivePath(absolutePath, "editTool", newString); err != nil {
		return types.Deny(err.Error())
	}

	// Plan mode is a read-only exploration/design phase (see write.go's
	// matching comment for the full reasoning). Deny rather than the
	// AcceptEdits-style auto-allow this used to fall into.
	if toolCtx.ExecutionMode == "plan" {
		return types.DenyWithDecisionReason(
			fmt.Sprintf("file edits are not allowed in plan mode: %s", absolutePath),
			&types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonMode,
				Source: "plan",
				Reason: "plan mode is read-only - get the plan approved with submit_plan before editing",
			},
		)
	}

	return types.Passthrough(input)
}

// IsConcurrencySafe reports that edits are not safe to run concurrently.
func (e *Tool) IsConcurrencySafe(input map[string]any) bool {
	_ = input
	return false
}

// IsReadOnly reports that edits modify state.
func (e *Tool) IsReadOnly(input map[string]any) bool {
	_ = input
	return false
}

// IsEnabled returns whether this tool is currently active.
func (e *Tool) IsEnabled() bool { return true }

// FormatResult serialises the tool output into the tool_result content string.
func (e *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

func (e *Tool) resolvePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := e.effectiveWorkingDir(toolCtx)
	if filepath.IsAbs(path) || strings.TrimSpace(workingDir) == "" {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

func (e *Tool) effectiveWorkingDir(toolCtx tool.ToolUseContext) string {
	if strings.TrimSpace(toolCtx.WorkingDirectory) != "" {
		return toolCtx.WorkingDirectory
	}
	if strings.TrimSpace(e.workingDir) != "" {
		return e.workingDir
	}
	return "."
}

// BackfillInput enriches a shallow clone of the parsed input with derived fields.
func (e *Tool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

// SetWorkingDir sets the working directory for the tool
func (e *Tool) SetWorkingDir(dir string) {
	e.workingDir = dir
}

// GetWorkingDir returns the current working directory
func (e *Tool) GetWorkingDir() string {
	return e.workingDir
}

func readFileIfExists(filePath string) (string, bool, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to read file: %w", err)
	}
	return string(content), true, nil
}

func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func detectPreferredLineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(s, "\n") {
		return "\n"
	}
	return ""
}

func applyLineEnding(s string, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

func normalizeQuoteRune(r rune) rune {
	switch r {
	case leftSingleCurlyQuote, rightSingleCurlyQuote:
		return '\''
	case leftDoubleCurlyQuote, rightDoubleCurlyQuote:
		return '"'
	default:
		return r
	}
}

// FindActualString finds searchString in fileContent, tolerating curly vs.
// straight quote differences (a model very commonly "corrects" one style to
// the other when recalling text). Returns the exact substring as it appears
// in fileContent (which may differ from searchString only in quote style),
// or "" if no match - including a quote-normalized one - exists.
func FindActualString(fileContent string, searchString string) string {
	if strings.Contains(fileContent, searchString) {
		return searchString
	}

	fileRunes := []rune(fileContent)
	searchRunes := []rune(searchString)
	if len(searchRunes) == 0 || len(searchRunes) > len(fileRunes) {
		return ""
	}

	for start := 0; start <= len(fileRunes)-len(searchRunes); start++ {
		matched := true
		for i := range searchRunes {
			if normalizeQuoteRune(fileRunes[start+i]) != normalizeQuoteRune(searchRunes[i]) {
				matched = false
				break
			}
		}
		if matched {
			return string(fileRunes[start : start+len(searchRunes)])
		}
	}

	return ""
}

func preserveQuoteStyle(oldString string, actualOldString string, newString string) string {
	if oldString == actualOldString {
		return newString
	}

	hasDoubleQuotes := strings.ContainsRune(actualOldString, leftDoubleCurlyQuote) || strings.ContainsRune(actualOldString, rightDoubleCurlyQuote)
	hasSingleQuotes := strings.ContainsRune(actualOldString, leftSingleCurlyQuote) || strings.ContainsRune(actualOldString, rightSingleCurlyQuote)
	if !hasDoubleQuotes && !hasSingleQuotes {
		return newString
	}

	result := newString
	if hasDoubleQuotes {
		result = applyCurlyDoubleQuotes(result)
	}
	if hasSingleQuotes {
		result = applyCurlySingleQuotes(result)
	}
	return result
}

func isOpeningContext(runes []rune, index int) bool {
	if index == 0 {
		return true
	}
	prev := runes[index-1]
	return unicode.IsSpace(prev) || strings.ContainsRune("([{—–", prev)
}

func applyCurlyDoubleQuotes(s string) string {
	runes := []rune(s)
	result := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r == '"' {
			if isOpeningContext(runes, i) {
				result = append(result, leftDoubleCurlyQuote)
			} else {
				result = append(result, rightDoubleCurlyQuote)
			}
			continue
		}
		result = append(result, r)
	}
	return string(result)
}

func applyCurlySingleQuotes(s string) string {
	runes := []rune(s)
	result := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r == '\'' {
			var prev rune
			var next rune
			prevIsLetter := false
			nextIsLetter := false
			if i > 0 {
				prev = runes[i-1]
				prevIsLetter = unicode.IsLetter(prev)
			}
			if i < len(runes)-1 {
				next = runes[i+1]
				nextIsLetter = unicode.IsLetter(next)
			}
			if prevIsLetter && nextIsLetter {
				result = append(result, rightSingleCurlyQuote)
			} else if isOpeningContext(runes, i) {
				result = append(result, leftSingleCurlyQuote)
			} else {
				result = append(result, rightSingleCurlyQuote)
			}
			continue
		}
		result = append(result, r)
	}
	return string(result)
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func buildStructuredPatch(oldContent, newContent string) []StructuredPatchHunk {
	if oldContent == newContent {
		return []StructuredPatchHunk{}
	}

	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	minLen := len(oldLines)
	if len(newLines) < minLen {
		minLen = len(newLines)
	}

	prefix := 0
	for prefix < minLen && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < minLen-prefix && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	beforeStart := maxInt(0, prefix-patchContextLines)
	oldChangeEnd := len(oldLines) - suffix
	newChangeEnd := len(newLines) - suffix
	afterOldEnd := minInt(len(oldLines), oldChangeEnd+patchContextLines)
	afterNewEnd := minInt(len(newLines), newChangeEnd+patchContextLines)

	lines := make([]string, 0)
	for _, line := range oldLines[beforeStart:prefix] {
		lines = append(lines, " "+line)
	}
	for _, line := range oldLines[prefix:oldChangeEnd] {
		lines = append(lines, "-"+line)
	}
	for _, line := range newLines[prefix:newChangeEnd] {
		lines = append(lines, "+"+line)
	}
	for _, line := range newLines[newChangeEnd:afterNewEnd] {
		lines = append(lines, " "+line)
	}

	return []StructuredPatchHunk{{
		OldStart: beforeStart + 1,
		OldLines: afterOldEnd - beforeStart,
		NewStart: beforeStart + 1,
		NewLines: afterNewEnd - beforeStart,
		Lines:    lines,
	}}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// countLines counts the number of lines in a string
func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := 0
	for _, c := range s {
		if c == '\n' {
			count++
		}
	}
	if !strings.HasSuffix(s, "\n") && len(s) > 0 {
		count++
	}
	return count
}

// ── Tool metadata ──────────────────────────────────────────────────────────────

// SearchHint is a hint for tool search functionality.
const SearchHint = "edit text in a file by finding and replacing strings"
