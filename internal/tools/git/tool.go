// Package git provides structured tools for git operations.
//
// Why structured tools instead of bash?
// The Bash tool can run git commands, but returns raw text. These tools
// return typed JSON — the agent can reason about branches, diffs, and
// commit history without parsing shell output, and the engine can apply
// permission guards to destructive operations (commit, push, reset).
//
// Implementation notes for contributors:
//   - All tools use os/exec to invoke the local git binary — no external API,
//     no credentials needed for most read operations.
//   - git_commit and git_push set RequiresPermission: true so the user is
//     prompted before the engine touches the repo history.
//   - Keep each tool focused: git_status only reports state, git_commit only
//     creates commits — do not combine operations in one tool call.
//
// See GitHub issue for full implementation spec.
package git

import (
	"context"
	"errors"
	"fmt"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ErrNotImplemented is returned by all git tools until os/exec wrappers are complete.
var ErrNotImplemented = errors.New("git tools: not implemented — see GitHub issue")

// ─── git_status ──────────────────────────────────────────────────────────────

const statusDesc = `Show the working tree status of a git repository.

Returns a JSON object with:
- "branch":    current branch name (or HEAD SHA if detached)
- "staged":    list of files staged for the next commit
- "unstaged":  list of modified files not yet staged
- "untracked": list of new files not tracked by git
- "ahead":     number of commits ahead of the remote tracking branch
- "behind":    number of commits behind the remote tracking branch
- "clean":     true when the working tree has no changes

Parameters:
- path: absolute or relative path to the git repository root (default: working directory)`

type StatusTool struct{}

func NewStatusTool() *StatusTool { return &StatusTool{} }

func (t *StatusTool) Definition() tool.Definition {
	return tool.Definition{
		Name: "git_status", SearchHint: "git status changed staged untracked files in the repository", DisplayName: "Git Status", Description: statusDesc,
		Category: "vcs",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path to the repository root."},
			},
		}),
	}
}
func (t *StatusTool) Call(_ context.Context, _ tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewErrorResult(fmt.Errorf("%w", ErrNotImplemented)), nil
}
func (t *StatusTool) Description(_ context.Context) (string, error) { return statusDesc, nil }
func (t *StatusTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *StatusTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *StatusTool) IsConcurrencySafe(_ map[string]any) bool                           { return true }
func (t *StatusTool) IsReadOnly(_ map[string]any) bool                                  { return true }
func (t *StatusTool) IsEnabled() bool                                                   { return false }
func (t *StatusTool) FormatResult(data any) string                                      { return fmt.Sprintf("%v", data) }
func (t *StatusTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }

// ─── git_log ─────────────────────────────────────────────────────────────────

const logDesc = `Show the commit history of a git repository.

Returns a list of commit objects, each with:
- "sha":     full commit hash
- "short":   7-character abbreviated hash
- "author":  author name and email
- "date":    ISO-8601 commit date
- "subject": first line of the commit message
- "body":    rest of the commit message (may be empty)

Parameters:
- path:    repository root (default: working directory)
- branch:  branch or ref to walk (default: HEAD)
- limit:   maximum number of commits to return (default: 20, max: 200)
- since:   only commits after this date (e.g. "2024-01-01" or "2 weeks ago")
- until:   only commits before this date
- author:  filter by author name or email (substring match)
- grep:    filter commit messages by pattern`

type LogTool struct{}

func NewLogTool() *LogTool { return &LogTool{} }

func (t *LogTool) Definition() tool.Definition {
	return tool.Definition{
		Name: "git_log", SearchHint: "git log commit history of the repository", DisplayName: "Git Log", Description: logDesc,
		Category: "vcs",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"branch": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer", "default": 20, "maximum": 200},
				"since":  map[string]any{"type": "string"},
				"until":  map[string]any{"type": "string"},
				"author": map[string]any{"type": "string"},
				"grep":   map[string]any{"type": "string"},
			},
		}),
	}
}
func (t *LogTool) Call(_ context.Context, _ tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewErrorResult(fmt.Errorf("%w", ErrNotImplemented)), nil
}
func (t *LogTool) Description(_ context.Context) (string, error) { return logDesc, nil }
func (t *LogTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *LogTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *LogTool) IsConcurrencySafe(_ map[string]any) bool                           { return true }
func (t *LogTool) IsReadOnly(_ map[string]any) bool                                  { return true }
func (t *LogTool) IsEnabled() bool                                                   { return false }
func (t *LogTool) FormatResult(data any) string                                      { return fmt.Sprintf("%v", data) }
func (t *LogTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }

// ─── git_diff ────────────────────────────────────────────────────────────────

const diffDesc = `Show changes between commits, working tree, or staged index.

Returns a JSON object with:
- "files": list of changed files, each with:
    - "path":      file path relative to repo root
    - "status":    "added" | "modified" | "deleted" | "renamed" | "copied"
    - "additions": number of added lines
    - "deletions": number of deleted lines
    - "patch":     unified diff for this file (may be omitted for binary files)
- "total_additions": total lines added across all files
- "total_deletions": total lines deleted across all files

Parameters:
- path:   repository root (default: working directory)
- from:   base ref (commit SHA, branch, tag). If omitted, compares working tree.
- to:     target ref. If omitted and from is set, compares from..HEAD.
- staged: if true, show staged (index) changes instead of working tree (default: false)
- files:  restrict diff to these file paths (list of strings)`

type DiffTool struct{}

func NewDiffTool() *DiffTool { return &DiffTool{} }

func (t *DiffTool) Definition() tool.Definition {
	return tool.Definition{
		Name: "git_diff", SearchHint: "git diff changes between commits or working tree", DisplayName: "Git Diff", Description: diffDesc,
		Category: "vcs",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"from":   map[string]any{"type": "string"},
				"to":     map[string]any{"type": "string"},
				"staged": map[string]any{"type": "boolean", "default": false},
				"files":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		}),
	}
}
func (t *DiffTool) Call(_ context.Context, _ tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewErrorResult(fmt.Errorf("%w", ErrNotImplemented)), nil
}
func (t *DiffTool) Description(_ context.Context) (string, error) { return diffDesc, nil }
func (t *DiffTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *DiffTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *DiffTool) IsConcurrencySafe(_ map[string]any) bool                           { return true }
func (t *DiffTool) IsReadOnly(_ map[string]any) bool                                  { return true }
func (t *DiffTool) IsEnabled() bool                                                   { return false }
func (t *DiffTool) FormatResult(data any) string                                      { return fmt.Sprintf("%v", data) }
func (t *DiffTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }

// ─── git_commit ───────────────────────────────────────────────────────────────

const commitDesc = `Stage files and create a git commit.

The tool runs: git add <files> && git commit -m <message>

Returns:
- "sha":     the new commit hash
- "branch":  branch the commit was made on
- "message": the commit message used
- "files":   list of files that were staged and committed

Parameters:
- path:    repository root (default: working directory)
- message: commit message — must follow Conventional Commits if the repo enforces it (required)
- files:   list of file paths to stage before committing; use ["."] for all changes
- amend:   if true, amend the last commit instead of creating a new one (default: false)

IMPORTANT: This tool modifies git history. Prefer creating new commits over amending.
Never amend commits that have already been pushed to a shared remote.`

type CommitTool struct{}

func NewCommitTool() *CommitTool { return &CommitTool{} }

func (t *CommitTool) Definition() tool.Definition {
	return tool.Definition{
		Name: "git_commit", SearchHint: "git commit stage and commit changes with a message", DisplayName: "Git Commit", Description: commitDesc,
		Category:           "vcs",
		IsDestructive:      false,
		RequiresPermission: true,
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"message": map[string]any{"type": "string", "description": "Commit message (required)."},
				"files":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"amend":   map[string]any{"type": "boolean", "default": false},
			},
			"required": []string{"message"},
		}),
	}
}
func (t *CommitTool) Call(_ context.Context, _ tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewErrorResult(fmt.Errorf("%w", ErrNotImplemented)), nil
}
func (t *CommitTool) Description(_ context.Context) (string, error) { return commitDesc, nil }
func (t *CommitTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *CommitTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *CommitTool) IsConcurrencySafe(_ map[string]any) bool                           { return false }
func (t *CommitTool) IsReadOnly(_ map[string]any) bool                                  { return false }
func (t *CommitTool) IsEnabled() bool                                                   { return false }
func (t *CommitTool) FormatResult(data any) string                                      { return fmt.Sprintf("%v", data) }
func (t *CommitTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }

// ─── git_branch ───────────────────────────────────────────────────────────────

const branchDesc = `List, create, switch, or delete git branches.

Actions:
- "list"   (default): return all local and remote branches with their last commit info
- "create": create a new branch from an optional base ref (does not switch)
- "switch": switch the working tree to an existing branch (like git checkout)
- "delete": delete a local branch (fails if not merged unless force=true)

Returns (for list):
- "branches": list of objects with "name", "current", "remote", "sha", "subject", "date"

Returns (for create/switch/delete):
- "success": true
- "branch":  the branch that was created, switched to, or deleted

Parameters:
- path:   repository root (default: working directory)
- action: "list" | "create" | "switch" | "delete" (default: "list")
- name:   branch name (required for create, switch, delete)
- base:   base ref for create (default: HEAD)
- force:  force delete even if not merged (default: false)`

type BranchTool struct{}

func NewBranchTool() *BranchTool { return &BranchTool{} }

func (t *BranchTool) Definition() tool.Definition {
	return tool.Definition{
		Name: "git_branch", SearchHint: "git branch list create switch or delete branches", DisplayName: "Git Branch", Description: branchDesc,
		Category: "vcs",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"action": map[string]any{"type": "string", "enum": []string{"list", "create", "switch", "delete"}, "default": "list"},
				"name":   map[string]any{"type": "string"},
				"base":   map[string]any{"type": "string"},
				"force":  map[string]any{"type": "boolean", "default": false},
			},
		}),
	}
}
func (t *BranchTool) Call(_ context.Context, _ tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewErrorResult(fmt.Errorf("%w", ErrNotImplemented)), nil
}
func (t *BranchTool) Description(_ context.Context) (string, error) { return branchDesc, nil }
func (t *BranchTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *BranchTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *BranchTool) IsConcurrencySafe(_ map[string]any) bool                           { return true }
func (t *BranchTool) IsReadOnly(_ map[string]any) bool                                  { return false }
func (t *BranchTool) IsEnabled() bool                                                   { return false }
func (t *BranchTool) FormatResult(data any) string                                      { return fmt.Sprintf("%v", data) }
func (t *BranchTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }
