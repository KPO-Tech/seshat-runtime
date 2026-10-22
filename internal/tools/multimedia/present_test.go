package multimedia

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

func presentCall(t *testing.T, workDir string, input map[string]any) tool.CallResult {
	t.Helper()
	result, err := NewPresentTool().Call(context.Background(), tool.CallInput{
		Parsed:      input,
		ToolContext: &tool.ToolUseContext{WorkingDirectory: workDir},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPresentArtifactDescribesAFileInsideTheWorkingFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "clip.MP4"), []byte("not really a video"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := presentCall(t, dir, map[string]any{"path": "out/clip.MP4", "summary": "a test clip"})
	if result.IsError() {
		t.Fatalf("a finished file inside the folder is presented, got %v", result.Error)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatal(err)
	}
	if got["kind"] != "video" || got["path"] != "out/clip.MP4" || got["title"] != "clip.MP4" || got["summary"] != "a test clip" {
		t.Fatalf("unexpected description: %v", got)
	}
	if strings.Contains(result.Content, dir) {
		t.Fatal("the answer carries the path relative to the folder, not the machine's own")
	}

	absolute := presentCall(t, dir, map[string]any{"path": filepath.Join(dir, "out", "clip.MP4"), "kind": "audio"})
	if absolute.IsError() || !strings.Contains(absolute.Content, `"kind":"audio"`) {
		t.Fatalf("an absolute path inside the folder works and a stated kind wins, got %v %s", absolute.Error, absolute.Content)
	}
}

func TestPresentArtifactRefusesWhatIsNotAFileOfTheFolder(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.png"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"outside":  outside,
		"parent":   "../secret.png",
		"missing":  "nothing.png",
		"empty":    "empty.png",
		"folder":   ".",
		"no path":  "",
		"dot dots": "out/../../x.png",
	} {
		if result := presentCall(t, dir, map[string]any{"path": path}); !result.IsError() {
			t.Errorf("%s: %q must be refused, got %s", name, path, result.Content)
		}
	}
}
