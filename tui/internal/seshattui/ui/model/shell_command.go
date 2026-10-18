package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/message"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/shell"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/chat"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// shellCommandTimeout bounds a command run from the prompt with "!": its output is captured, so one that waits for input
// would otherwise hang the screen.
const shellCommandTimeout = 5 * time.Minute

// shellMaxCaptured bounds what is kept of a command's output.
const shellMaxCaptured = 1 << 20

// shellKind says how a line typed at the prompt is run.
type shellKind int

const (
	// shellNone: the line is a message for the agent.
	shellNone shellKind = iota
	// shellPrivate: "!cmd" runs the command and shows its output in the command panel. The agent does not see it unless the user
	// shares it.
	shellPrivate
	// shellShared: "!!cmd" does the same and also tells the agent about it with the next message.
	shellShared
	// shellInteractive: ":!cmd" gives the terminal to the command until it ends (a password prompt, a shell, an editor).
	shellInteractive
)

// parseShellInput recognises a line meant for the shell rather than the agent.
func parseShellInput(line string) (shellKind, string) {
	line = strings.TrimSpace(line)
	var kind shellKind
	switch {
	case strings.HasPrefix(line, "!!"):
		kind, line = shellShared, line[2:]
	case strings.HasPrefix(line, ":!"):
		kind, line = shellInteractive, line[2:]
	case strings.HasPrefix(line, "!"):
		kind, line = shellPrivate, line[1:]
	default:
		return shellNone, ""
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return shellNone, ""
	}
	return kind, line
}

// shellDoneMsg reports a command run from the prompt that ended.
type shellDoneMsg struct {
	id       string
	command  string
	output   string
	exitCode int
	shared   bool
}

// shellInteractiveDoneMsg reports a command that had the terminal and ended.
type shellInteractiveDoneMsg struct {
	command string
	err     error
}

// cappedBuffer keeps the first shellMaxCaptured bytes written to it; the writes after that are dropped, so a command that
// prints without end cannot fill the memory.
type cappedBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	dropped bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := shellMaxCaptured - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.dropped = true
		} else {
			c.buf.Write(p)
		}
	} else {
		c.dropped = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped {
		return c.buf.String() + "\n[output cut]"
	}
	return c.buf.String()
}

// runShellInput handles a line typed at the prompt when it is meant for the shell. It reports whether it did.
func (m *UI) runShellInput(line string) (tea.Cmd, bool) {
	kind, command := parseShellInput(line)
	if kind == shellNone {
		return nil, false
	}
	if kind == shellInteractive {
		return m.runInteractive(command), true
	}
	if !m.hasSession() {
		if err := m.startEmptySession(); err != nil {
			return util.ReportError(err), true
		}
	}
	m.shellSeq++
	id := fmt.Sprintf("shell-%d", m.shellSeq)
	shared := kind == shellShared
	m.shellPanel.add(&shellRun{id: id, command: command, running: true, shared: shared})
	m.updateLayoutAndSize()

	cwd := m.com.Workspace.WorkingDir()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), shellCommandTimeout)
		defer cancel()
		var out cappedBuffer
		err := shell.Run(ctx, shell.RunOptions{Command: command, Cwd: cwd, Env: os.Environ(), Stdout: &out, Stderr: &out})
		code := 0
		text := out.String()
		switch {
		case err == nil:
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			code = 124
			text += "\n[stopped after " + shellCommandTimeout.String() + "; use :!" + command + " for a command that waits for input]"
		default:
			code = shell.ExitCode(err)
			if code == 0 {
				code = 1
				text += "\n" + err.Error()
			}
		}
		return shellDoneMsg{id: id, command: command, output: text, exitCode: code, shared: shared}
	}, true
}

// handleShellDone shows the result and, for a shared command, keeps it for the next message to the agent.
func (m *UI) handleShellDone(msg shellDoneMsg) {
	if run := m.shellPanel.find(msg.id); run != nil {
		run.output, run.exitCode, run.running = msg.output, msg.exitCode, false
	}
	if item, ok := m.shellItems[msg.id]; ok {
		item.SetResult(msg.output, msg.exitCode)
		delete(m.shellItems, msg.id)
		// The output made the item taller: without this the end of it stays below the screen until the next message.
		m.chat.ScrollToBottom()
	}
	if msg.shared {
		m.pendingShell = append(m.pendingShell, chat.ShellRun{Command: msg.command, Output: msg.output, ExitCode: msg.exitCode})
	}
}

// takeShellContext returns the block describing the shared commands run since the last message, and forgets them.
func (m *UI) takeShellContext() string {
	block := chat.ShellContextBlock(m.pendingShell)
	m.pendingShell = nil
	return block
}

// runInteractive hands the terminal to a command. The program is suspended meanwhile, so the command reads the keyboard
// itself: a password typed at its prompt never goes through this program.
func (m *UI) runInteractive(command string) tea.Cmd {
	name, args := interactiveShell(command)
	cmd := exec.Command(name, args...)
	cmd.Dir = m.com.Workspace.WorkingDir()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return shellInteractiveDoneMsg{command: command, err: err}
	})
}

// interactiveShell is the program that runs a command line with the real terminal.
func interactiveShell(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, []string{"-c", command}
	}
	return "sh", []string{"-c", command}
}

// startEmptySession opens a session when a command is run before any message, so the output has a chat to appear in.
func (m *UI) startEmptySession() error {
	newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
	if err != nil {
		return err
	}
	if m.forceCompactMode {
		m.isCompact = true
	}
	if newSession.ID != "" {
		m.session = &newSession
	}
	m.setState(uiChat, m.focus)
	return nil
}

// warning is what to tell the user when a command that had the terminal ended with an error, or nil.
func (msg shellInteractiveDoneMsg) warning() tea.Cmd {
	if msg.err == nil {
		return nil
	}
	return util.ReportWarn("Command ended: " + msg.err.Error())
}

// submitPrompt sends what the user typed: to the shell when it starts with "!" or ":!", to the agent otherwise.
func (m *UI) submitPrompt(value string, attachments []message.Attachment) tea.Cmd {
	if cmd, handled := m.runShellInput(value); handled {
		m.historyReset()
		return tea.Batch(cmd, m.loadPromptHistory())
	}
	m.randomizePlaceholders()
	m.historyReset()
	return tea.Batch(m.sendMessage(value, attachments...), m.loadPromptHistory())
}
