package chat

import (
	"strings"
	"testing"
)

func TestShellContextBlockRoundTripsThroughStrip(t *testing.T) {
	block := ShellContextBlock([]ShellRun{
		{Command: "pip install rich", Output: "Successfully installed rich\n"},
		{Command: "pytest", Output: "1 failed\n", ExitCode: 1},
	})
	if !strings.Contains(block, "$ pip install rich") || !strings.Contains(block, "[exit status 1]") {
		t.Fatalf("block misses a command or a status:\n%s", block)
	}
	gotBlock, rest := StripShellContext(block + "\n\nwhy did the tests fail?")
	if gotBlock != block {
		t.Fatalf("stripped block differs from the one built")
	}
	if rest != "why did the tests fail?" {
		t.Fatalf("rest = %q, want the user's text only", rest)
	}
}

func TestShellContextBlockIsEmptyWithoutCommands(t *testing.T) {
	if got := ShellContextBlock(nil); got != "" {
		t.Fatalf("got %q, want nothing to tell", got)
	}
}

func TestStripShellContextLeavesOrdinaryMessagesAlone(t *testing.T) {
	for _, text := range []string{"hello", "<user_shell_commands> never closed", ""} {
		block, rest := StripShellContext(text)
		if block != "" || rest != text {
			t.Errorf("StripShellContext(%q) = (%q, %q), want the text untouched", text, block, rest)
		}
	}
}

func TestShellContextBlockClipsLongOutputKeepingBothEnds(t *testing.T) {
	long := "START" + strings.Repeat("m", 20000) + "END"
	block := ShellContextBlock([]ShellRun{{Command: "build", Output: long}})
	if !strings.Contains(block, "START") || !strings.Contains(block, "END") || !strings.Contains(block, "left out") {
		t.Fatalf("a long output must keep its start and end and say what was left out")
	}
	if len(block) > shellContextMaxOutput+600 {
		t.Fatalf("block has %d bytes, want it bounded", len(block))
	}
}
