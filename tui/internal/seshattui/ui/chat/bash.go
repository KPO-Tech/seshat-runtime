package chat

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/internal/textsafe"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/agent/tools"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/message"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// -----------------------------------------------------------------------------
// Bash Tool
// -----------------------------------------------------------------------------

// BashToolMessageItem is a message item that represents a bash tool call.
type BashToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*BashToolMessageItem)(nil)

// NewBashToolMessageItem creates a new [BashToolMessageItem].
func NewBashToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &BashToolRenderContext{}, canceled)
}

// BashToolRenderContext renders bash tool messages.
type BashToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (b *BashToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := width
	if opts.IsPending() {
		return pendingTool(sty, "Bash", opts.Anim, opts.Compact)
	}

	var params tools.BashParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		params.Command = "failed to parse command"
	}

	// Check if this is a background job.
	var meta tools.BashResponseMetadata
	if opts.HasResult() {
		_ = json.Unmarshal([]byte(opts.Result.Metadata), &meta)
	}

	if meta.Background {
		description := cmp.Or(meta.Description, params.Command)
		content := "Command: " + params.Command + "\n" + opts.Result.Content
		return renderJobTool(sty, opts, cappedWidth, "Start", meta.ShellID, description, content)
	}

	// Build header: show first line of command only; suffix "(+N lines)" for scripts.
	toolParams := []string{bashHeaderCmd(params.Command)}
	if params.RunInBackground {
		toolParams = append(toolParams, "background", "true")
	}

	header := toolHeader(sty, opts.Status, "Bash", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() {
		return header
	}

	output := meta.Output
	if output == "" && opts.Result.Content != tools.BashNoOutput {
		output = opts.Result.Content
	}
	// What a process printed may hold escape sequences that rewrite the screen or the clipboard.
	output = textsafe.SanitizeOutput(output)

	if output == "" {
		noOut := sty.Tool.StateCancelled.Render("(no output)")
		return joinToolParts(header, sty.Tool.Body.Render(noOut))
	}

	body := toolOutputCodeContent(sty, bashOutputLang(output), output, 0, cappedWidth, opts.ExpandedContent)
	return joinToolParts(header, body)
}

// bashHeaderCmd formats a bash command for the header line.
// Multi-line scripts show only the first non-empty line and a "(+N lines)" count.
func bashHeaderCmd(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	lines := strings.Split(cmd, "\n")
	if len(lines) <= 1 {
		return strings.ReplaceAll(cmd, "\t", "    ")
	}
	first := strings.TrimSpace(lines[0])
	first = strings.ReplaceAll(first, "\t", "    ")
	return fmt.Sprintf("%s (+%d lines)", first, len(lines)-1)
}

// bashOutputLang returns the chroma filename hint for the command output.
// Detects JSON blobs; falls back to plain text for everything else.
func bashOutputLang(output string) string {
	t := strings.TrimSpace(output)
	if len(t) > 0 && (t[0] == '{' || t[0] == '[') {
		return "output.json"
	}
	return "output.txt"
}

// -----------------------------------------------------------------------------
// Job Output Tool
// -----------------------------------------------------------------------------

// JobOutputToolMessageItem is a message item for job_output tool calls.
type JobOutputToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*JobOutputToolMessageItem)(nil)

// NewJobOutputToolMessageItem creates a new [JobOutputToolMessageItem].
func NewJobOutputToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &JobOutputToolRenderContext{}, canceled)
}

// JobOutputToolRenderContext renders job_output tool messages.
type JobOutputToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (j *JobOutputToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := width
	if opts.IsPending() {
		return pendingTool(sty, "Job Output", opts.Anim, opts.Compact)
	}

	var params tools.JobOutputParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return invalidInputContent(sty, opts, "Job Output", cappedWidth)
	}

	var description string
	if opts.HasResult() && opts.Result.Metadata != "" {
		var meta tools.JobOutputResponseMetadata
		if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil {
			description = cmp.Or(meta.Description, meta.Command)
		}
	}

	content := ""
	if opts.HasResult() {
		content = opts.Result.Content
	}
	return renderJobTool(sty, opts, cappedWidth, "Output", params.ShellID, description, content)
}

// -----------------------------------------------------------------------------
// Job Kill Tool
// -----------------------------------------------------------------------------

// JobKillToolMessageItem is a message item for job_kill tool calls.
type JobKillToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*JobKillToolMessageItem)(nil)

// NewJobKillToolMessageItem creates a new [JobKillToolMessageItem].
func NewJobKillToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &JobKillToolRenderContext{}, canceled)
}

// JobKillToolRenderContext renders job_kill tool messages.
type JobKillToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (j *JobKillToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := width
	if opts.IsPending() {
		return pendingTool(sty, "Job Kill", opts.Anim, opts.Compact)
	}

	var params tools.JobKillParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return invalidInputContent(sty, opts, "Job Kill", cappedWidth)
	}

	var description string
	if opts.HasResult() && opts.Result.Metadata != "" {
		var meta tools.JobKillResponseMetadata
		if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil {
			description = cmp.Or(meta.Description, meta.Command)
		}
	}

	header := jobHeader(sty, opts.Status, "Kill", params.ShellID, description, cappedWidth)
	if opts.Compact {
		return header
	}

	// Errors surface a body; success is silent — the ✓ icon communicates it.
	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}
	return header
}

// renderJobTool renders a job-related tool with the common pattern:
// header → early state → body (line numbers + JSON detection).
func renderJobTool(sty *styles.Styles, opts *ToolRenderOpts, width int, action, shellID, description, content string) string {
	header := jobHeader(sty, opts.Status, action, shellID, description, width)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	content = textsafe.SanitizeOutput(content)
	if content == "" {
		noOut := sty.Tool.StateCancelled.Render("(no output)")
		return joinToolParts(header, sty.Tool.Body.Render(noOut))
	}

	body := toolOutputCodeContent(sty, bashOutputLang(content), content, 0, width, opts.ExpandedContent)
	return joinToolParts(header, body)
}

// jobHeader builds a header for job-related tools.
// Format: "● Job (Action) PID shellID description..."
func jobHeader(sty *styles.Styles, status ToolStatus, action, shellID, description string, width int) string {
	icon := toolIcon(sty, status)
	jobPart := sty.Tool.JobToolName.Render("Job")
	actionPart := sty.Tool.JobAction.Render("(" + action + ")")
	pidPart := sty.Tool.JobPID.Render("PID " + shellID)

	prefix := fmt.Sprintf("%s %s %s %s", icon, jobPart, actionPart, pidPart)

	if description == "" {
		return prefix
	}

	prefixWidth := lipgloss.Width(prefix)
	availableWidth := width - prefixWidth - 1
	if availableWidth < 10 {
		return prefix
	}

	truncatedDesc := ansi.Truncate(description, availableWidth, "…")
	return prefix + " " + sty.Tool.JobDescription.Render(truncatedDesc)
}

// joinToolParts joins header and body with a blank line separator.
func joinToolParts(header, body string) string {
	return strings.Join([]string{header, "", body}, "\n")
}
