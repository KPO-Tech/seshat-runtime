package multimedia

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	presentToolName = "present_artifact"
	presentDesc     = `Show a file you made to the person, in the conversation: an image, an audio clip, a video, a PDF, a document, a spreadsheet, a slide deck, a web page.
Call it once when the file is finished (after the code, the render or the conversion that made it), with the path of the file. The person plays or opens it there; do not describe how to open it. The file must be inside the working folder.`

	// maxPresentedBytes is the largest file the tool agrees to present.
	maxPresentedBytes = 500 << 20
)

// PresentTool implements the present_artifact built-in tool: it checks a file the agent made and tells the interface to show it.
type PresentTool struct{}

// NewPresentTool creates a present_artifact Tool.
func NewPresentTool() *PresentTool { return &PresentTool{} }

func (t *PresentTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        presentToolName,
		SearchHint:  "show play display open the file you made video audio image pdf document to the user",
		DisplayName: "Present File",
		Description: presentDesc,
		Category:    "creative",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path of the finished file, relative to the working folder or absolute inside it."},
				"title":   map[string]any{"type": "string", "description": "A short title for the person (optional)."},
				"summary": map[string]any{"type": "string", "description": "One sentence on what the file is (optional)."},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"image", "audio", "video", "pdf", "docx", "xlsx", "pptx", "html", "markdown", "other"},
					"description": "Leave out to take it from the file extension.",
				},
			},
			"required": []string{"path"},
		}),
		Metadata: map[string]any{"surface_profiles": []string{"mono_run"}},
	}
}

// artifactKinds maps a file extension to the kind the interface knows how to show.
var artifactKinds = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".svg": "image", ".bmp": "image",
	".mp3": "audio", ".wav": "audio", ".ogg": "audio", ".m4a": "audio", ".flac": "audio", ".aac": "audio", ".opus": "audio",
	".mp4": "video", ".webm": "video", ".mov": "video", ".mkv": "video", ".m4v": "video",
	".pdf": "pdf", ".docx": "docx", ".xlsx": "xlsx", ".pptx": "pptx",
	".html": "html", ".htm": "html", ".md": "markdown", ".markdown": "markdown",
}

func (t *PresentTool) Call(_ context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	raw, _ := input.Parsed["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: path is required")), nil
	}
	workDir := ""
	if input.ToolContext != nil {
		workDir = input.ToolContext.WorkingDirectory
	}
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return tool.NewErrorResult(fmt.Errorf("present_artifact: no working folder: %w", err)), nil
		}
		workDir = wd
	}
	full := raw
	if !filepath.IsAbs(full) {
		full = filepath.Join(workDir, full)
	}
	full = filepath.Clean(full)
	rel, ok := relativeInside(workDir, full)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: %s is outside the working folder %s; copy it there first", raw, workDir)), nil
	}
	info, err := os.Stat(full)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: %w", err)), nil
	}
	if !info.Mode().IsRegular() {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: %s is not a file", raw)), nil
	}
	if info.Size() == 0 {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: %s is empty", raw)), nil
	}
	if info.Size() > maxPresentedBytes {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: %s is %d MB, over the %d MB limit", raw, info.Size()>>20, maxPresentedBytes>>20)), nil
	}

	ext := strings.ToLower(filepath.Ext(full))
	kind, _ := input.Parsed["kind"].(string)
	if kind == "" {
		kind = artifactKinds[ext]
	}
	if kind == "" {
		kind = "other"
	}
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	title, _ := input.Parsed["title"].(string)
	if strings.TrimSpace(title) == "" {
		title = filepath.Base(full)
	}
	result := map[string]any{
		"path":      filepath.ToSlash(rel),
		"title":     title,
		"kind":      kind,
		"mime_type": mimeType,
		"size":      info.Size(),
	}
	if summary, _ := input.Parsed["summary"].(string); strings.TrimSpace(summary) != "" {
		result["summary"] = summary
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	return tool.CallResult{Data: result, ContentType: tool.ContentTypeJSON, Content: string(encoded)}, nil
}

// relativeInside returns path relative to dir, and false when path is not inside dir.
func relativeInside(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func (t *PresentTool) Description(_ context.Context) (string, error) { return presentDesc, nil }
func (t *PresentTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *PresentTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.PermissionResult{Behavior: types.PermissionBehaviorAllow}
}
func (t *PresentTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *PresentTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *PresentTool) IsEnabled() bool                         { return true }
func (t *PresentTool) FormatResult(data any) string {
	if m, ok := data.(map[string]any); ok {
		return fmt.Sprintf("Shown to the person: %v (%v)", m["title"], m["kind"])
	}
	return fmt.Sprintf("%v", data)
}
func (t *PresentTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }
