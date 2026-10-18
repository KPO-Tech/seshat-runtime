package chat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/list"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

// shellOutputMaxLines is how many lines of a command's output the chat shows; earlier ones are summarised.
const shellOutputMaxLines = 60

// ShellOutputItem shows a command the user ran from the prompt with "!", outside the conversation with the agent. It
// exists only in the chat view: the agent learns of it through the context block of the next message (see
// ShellContextBlock), not through this item.
type ShellOutputItem struct {
	*list.Versioned
	*cachedMessageItem

	id       string
	command  string
	output   string
	exitCode int
	running  bool
	shared   bool
	sty      *styles.Styles
}

// NewShellOutputItem creates the item for a command that is starting. shared says whether the agent will be told about
// the command (the "!" form) or not (the "!!" form).
func NewShellOutputItem(sty *styles.Styles, id, command string, shared bool) *ShellOutputItem {
	return &ShellOutputItem{
		Versioned:         list.NewVersioned(),
		cachedMessageItem: &cachedMessageItem{},
		id:                id,
		command:           command,
		running:           true,
		shared:            shared,
		sty:               sty,
	}
}

// ID implements MessageItem.
func (s *ShellOutputItem) ID() string { return s.id }

// Finished implements list.Item: a running command may still change.
func (s *ShellOutputItem) Finished() bool { return !s.running }

// SetResult records the end of the command.
func (s *ShellOutputItem) SetResult(output string, exitCode int) {
	s.output = output
	s.exitCode = exitCode
	s.running = false
	s.clearCache()
	s.Bump()
}

// RawRender implements MessageItem.
func (s *ShellOutputItem) RawRender(width int) string {
	if content, _, ok := s.getCachedRender(width); ok {
		return content
	}
	content := s.renderContent(width)
	s.setCachedRender(content, width, lipgloss.Height(content))
	return content
}

// Render implements MessageItem.
func (s *ShellOutputItem) Render(width int) string {
	if cached, ok := s.getCachedPrefixedRender(width, 0); ok {
		return cached
	}
	out := s.RawRender(width)
	s.setCachedPrefixedRender(out, width, 0)
	return out
}

func (s *ShellOutputItem) renderContent(width int) string {
	muted := s.sty.Messages.MsgTimestamp
	head := muted.Render("$ ") + s.command
	var b strings.Builder
	b.WriteString(head)
	switch {
	case s.running:
		b.WriteString("\n" + muted.Render("running…"))
	default:
		if body := strings.TrimRight(s.output, "\r\n"); body != "" {
			b.WriteString("\n" + lastLines(body, shellOutputMaxLines))
		}
		if s.exitCode != 0 {
			b.WriteString("\n" + s.sty.Messages.ErrorDetails.Render(fmt.Sprintf("exit status %d", s.exitCode)))
		}
		if !s.shared {
			b.WriteString("\n" + muted.Render("not shared with the agent"))
		}
	}
	return lipgloss.NewStyle().Width(max(width, 10)).Render(b.String())
}

// lastLines keeps the last n lines of s, with a line saying how many were left out.
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("… %d earlier lines\n", len(lines)-n) + strings.Join(lines[len(lines)-n:], "\n")
}

const (
	shellContextOpen  = "<user_shell_commands>"
	shellContextClose = "</user_shell_commands>"
	// shellContextMaxOutput bounds the output of one command in the context block: its start and its end are kept.
	shellContextMaxOutput = 6000
	shellContextHead      = 1500
)

// ShellRun is a command the user ran with "!" and the agent should know about.
type ShellRun struct {
	Command  string
	Output   string
	ExitCode int
}

// ShellContextBlock renders the commands the user ran since the last message as a block placed before that message, so
// the agent can use their output without the user copying it. It returns "" when there is nothing to tell.
func ShellContextBlock(runs []ShellRun) string {
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(shellContextOpen + "\nThe user ran these commands in their own terminal, outside this conversation. Their output is below for reference; do not run them again unless asked.\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "\n$ %s\n", r.Command)
		b.WriteString(clipMiddle(strings.TrimRight(r.Output, "\r\n"), shellContextMaxOutput, shellContextHead))
		if r.ExitCode != 0 {
			fmt.Fprintf(&b, "\n[exit status %d]", r.ExitCode)
		}
		b.WriteString("\n")
	}
	b.WriteString(shellContextClose)
	return b.String()
}

// StripShellContext splits a user message into the shell context block it starts with, if any, and the text the user
// wrote. The chat shows only that text.
func StripShellContext(text string) (block, rest string) {
	trimmed := strings.TrimLeft(text, "\r\n ")
	if !strings.HasPrefix(trimmed, shellContextOpen) {
		return "", text
	}
	end := strings.Index(trimmed, shellContextClose)
	if end < 0 {
		return "", text
	}
	end += len(shellContextClose)
	return trimmed[:end], strings.TrimLeft(trimmed[end:], "\r\n ")
}

// clipMiddle keeps the first head bytes and the last max-head of s when it is longer than max, with a marker between.
func clipMiddle(s string, max, head int) string {
	if len(s) <= max {
		return s
	}
	tail := max - head
	return s[:head] + fmt.Sprintf("\n[… %d bytes left out …]\n", len(s)-max) + s[len(s)-tail:]
}
