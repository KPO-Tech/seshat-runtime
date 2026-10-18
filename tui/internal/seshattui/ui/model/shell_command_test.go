package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/shell"
)

func TestParseShellInput(t *testing.T) {
	cases := []struct {
		in      string
		kind    shellKind
		command string
	}{
		{"!pip install rich", shellPrivate, "pip install rich"},
		{"  !ls -la  ", shellPrivate, "ls -la"},
		{"!!cat error.log", shellShared, "cat error.log"},
		{":!sudo su", shellInteractive, "sudo su"},
		{"!", shellNone, ""},
		{"!!", shellNone, ""},
		{":!   ", shellNone, ""},
		{"fix the bug!", shellNone, ""},
		{"hello", shellNone, ""},
		{"", shellNone, ""},
	}
	for _, c := range cases {
		kind, command := parseShellInput(c.in)
		if kind != c.kind || command != c.command {
			t.Errorf("parseShellInput(%q) = (%d, %q), want (%d, %q)", c.in, kind, command, c.kind, c.command)
		}
	}
}

func TestCappedBufferKeepsTheStartOnly(t *testing.T) {
	var b cappedBuffer
	chunk := strings.Repeat("x", 600*1024)
	for range 3 {
		if n, err := b.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v; a capped buffer must still report the full write", n, err)
		}
	}
	out := b.String()
	if !strings.HasSuffix(out, "[output cut]") || len(out) > shellMaxCaptured+32 {
		t.Fatalf("output has %d bytes and suffix %q, want it bounded and marked", len(out), out[max(0, len(out)-20):])
	}
}

func TestShellRunCapturesOutputAndExitStatus(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out cappedBuffer
	if err := shell.Run(ctx, shell.RunOptions{Command: "echo hello", Cwd: cwd, Env: os.Environ(), Stdout: &out, Stderr: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("output = %q, want it to contain hello", out.String())
	}
	var failed cappedBuffer
	err = shell.Run(ctx, shell.RunOptions{Command: "exit 3", Cwd: cwd, Env: os.Environ(), Stdout: &failed, Stderr: &failed})
	if err == nil || shell.ExitCode(err) != 3 {
		t.Fatalf("exit 3 gave err=%v code=%d, want status 3", err, shell.ExitCode(err))
	}
}
