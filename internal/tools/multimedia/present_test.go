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

func TestPresentArtifactShowsAYouTubeVideoByItsLink(t *testing.T) {
	for link, want := range map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s": "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc":               "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":         "dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ":         "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":        "dQw4w9WgXcQ",
	} {
		result := presentCall(t, t.TempDir(), map[string]any{"url": link})
		if result.IsError() {
			t.Errorf("%s must be accepted, got %v", link, result.Error)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
			t.Fatal(err)
		}
		if got["kind"] != "youtube" || got["video_id"] != want || got["url"] != "https://www.youtube.com/watch?v="+want {
			t.Errorf("%s: unexpected description %v", link, got)
		}
	}
}

func TestPresentArtifactRefusesALinkThatIsNotAYouTubeVideo(t *testing.T) {
	for _, link := range []string{
		"https://evil.example.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com.evil.example.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ%22%3E%3Cscript%3E",
		"https://www.youtube.com/results?search_query=cats",
		"javascript:alert(1)",
		"ftp://youtu.be/dQw4w9WgXcQ",
	} {
		if result := presentCall(t, t.TempDir(), map[string]any{"url": link}); !result.IsError() {
			t.Errorf("%q must be refused, got %s", link, result.Content)
		}
	}
}
