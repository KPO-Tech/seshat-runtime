package documentreader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	reader "github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	fileReadTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/read"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	// ConvertToolName is the name of the convert_document tool.
	ConvertToolName = "convert_document"

	// ConvertDisplayName is the display name of the convert_document tool.
	ConvertDisplayName = "Convert Document"

	// ConvertSearchHint is a hint for tool search functionality.
	ConvertSearchHint = "OCR a scanned PDF, extract a complex slide deck, or transcribe audio through a document reader"

	// ConvertDescription is the description of the convert_document tool.
	ConvertDescription = "Convert a local file to markdown using the configured document reader: OCR, layout analysis, table structure, and audio transcription.\n\n" +
		"## When to use\n\n" +
		"- A scanned/image-only PDF that read_file reports as having little or no extractable text\n" +
		"- A PPTX slide deck — most slide decks are mostly visual (screenshots, diagrams, charts) with only a title or two of real text, so read_file's native extraction routinely \"succeeds\" while missing almost everything that actually matters. read_file already auto-falls-back to the document reader for a PPTX whose extracted text is sparse relative to its slide count, but if you're unsure or read_file returned something that looks thin, don't hesitate to call this directly rather than trying to work around it with other tools (terminal, ad-hoc scripts, per-slide image extraction, etc.) — none of that gets better OCR than a whole-document reader does in one shot\n" +
		"- Audio files (WAV, MP3) that need transcription\n" +
		"- A DOCX/PPTX/XLSX that read_file's native extraction failed on\n\n" +
		"## When NOT to use\n\n" +
		"- Plain text-native PDFs, or text-heavy DOCX/XLSX — use read_file instead, it extracts them natively without the network round-trip to an external document-reader service\n" +
		"- read_file already tries the document reader automatically as a last resort for unreadable or sparse files, so you usually don't need this tool for a first read. Reach for it when you want deliberate control: forcing a reconversion, or converting a file read_file didn't route to documentreader.\n\n" +
		"## Rules\n\n" +
		"- Requires a document reader to be configured (document_reader_url) or injected by the host.\n" +
		"- Pass the whole file in one call (`path` is the deck/document itself, e.g. \"deck.pptx\") — the document reader converts every slide/page in a single request. Never split a deck into individual slide images and OCR them one at a time: it's slower, it's more tool calls, and it throws away the layout/reading-order/table-structure analysis a whole-document reader does for a whole document at once.\n"
)

// ConvertTool converts a local file to markdown via a document-conversion backend.
type ConvertTool struct {
	workingDir       string
	documentReader   reader.Converter
	filesystemPolicy *access.FilesystemPolicy
}

// NewConvertTool creates a new convert_document tool.
func NewConvertTool(cfg Config, workingDir string) *ConvertTool {
	return &ConvertTool{
		workingDir:       workingDir,
		documentReader:   cfg.converter(),
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

func (t *ConvertTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ConvertToolName,
		DisplayName: ConvertDisplayName,
		SearchHint:  ConvertSearchHint,
		Description: ConvertDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local file to convert (PDF, DOCX, PPTX, XLSX, image, WAV, or MP3)",
				},
				"save_path": map[string]any{
					"type":        "string",
					"description": "Optional path to write the extracted markdown. Example: \"docs/converted.md\"",
				},
			},
			"required": []string{"path"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

func (t *ConvertTool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	filePath, ok := input.Parsed["path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return tool.NewErrorResult(fmt.Errorf("path is required and must be a string")), nil
	}
	if err := shared.ValidateFilePath(filePath, "reading"); err != nil {
		return tool.NewErrorResult(err), nil
	}

	savePath, _ := input.Parsed["save_path"].(string)
	savePath = strings.TrimSpace(savePath)

	toolCtx := input.ToolContextValue()
	absolutePath, err := t.resolvePath(filePath, toolCtx)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	if err := shared.ValidateUNCPathSecurity(absolutePath); err != nil {
		return tool.NewErrorResult(err), nil
	}
	if err := t.validateReadPath(toolCtx, absolutePath); err != nil {
		return tool.NewErrorResult(fmt.Errorf("path validation failed: %w", err)), nil
	}

	info, statErr := os.Stat(absolutePath)
	if statErr != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to access %s: %w", filePath, statErr)), nil
	}
	if info.IsDir() {
		return tool.NewErrorResult(fmt.Errorf("path is a directory, not a file: %s", filePath)), nil
	}

	if permissionCheck != nil {
		req := access.PermissionRequest{
			ToolName:      ConvertToolName,
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessRead,
			Paths:         []string{absolutePath},
			Justification: "Convert document via configured document reader",
			Scope:         access.ApprovalScopeToolCall,
		}
		permResult, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput: map[string]any{
				"path": absolutePath,
			},
			ToolUseID:              toolCtx.ToolUseID,
			SessionID:              toolCtx.SessionID,
			TurnID:                 toolCtx.TurnID,
			PermissionMode:         toolCtx.PermissionMode,
			WorkingDirectory:       t.effectiveWorkingDir(toolCtx),
			IsToolRunningInSandbox: toolCtx.EnableSandbox,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := access.ErrorForPermissionResult(permResult, "document conversion requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	if t.documentReader == nil || !t.documentReader.IsAvailable(ctx) {
		return tool.NewTextResult(fmt.Sprintf(
			"File: %s\n\nconvert_document requires a configured document reader.",
			filePath,
		)), nil
	}

	ext := strings.ToLower(filepath.Ext(absolutePath))
	conversion, err := t.documentReader.ConvertFile(ctx, absolutePath)
	if err != nil {
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("convert_document cancelled")), nil
		}
		if ext == ".wav" || ext == ".mp3" {
			// The bundled docling-serve ASR pipeline needs openai-whisper,
			// which is not part of its default install.
			return tool.NewErrorResult(fmt.Errorf(
				"document conversion failed for %s: %w\n\nAudio transcription requires docling-serve's ASR extra (openai-whisper), which is not installed by default. Reinstall it with DOCLING_EXTRAS=asr, e.g.: DOCLING_EXTRAS=asr ./scripts/install-python-env.sh",
				filePath, err,
			)), nil
		}
		return tool.NewErrorResult(fmt.Errorf("document conversion failed for %s: %w", filePath, err)), nil
	}

	savedAt := ""
	if savePath != "" {
		if absSave, err := t.resolvePath(savePath, toolCtx); err == nil {
			if err := os.MkdirAll(filepath.Dir(absSave), 0o755); err == nil {
				if err := os.WriteFile(absSave, []byte(conversion.Markdown), 0o644); err == nil {
					savedAt = absSave
				}
			}
		}
	}

	fileReadTool.RecordExternalRead(absolutePath, info.ModTime(), conversion.Markdown, true)

	return tool.NewTextResult(formatConvertResult(filePath, conversion, savedAt)), nil
}

func formatConvertResult(sourcePath string, r *reader.ConversionResult, savedAt string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("File: %s\n", sourcePath))
	if r.PageCount > 0 {
		b.WriteString(fmt.Sprintf("Pages: %d\n", r.PageCount))
	}
	if len(r.Images) > 0 {
		b.WriteString(fmt.Sprintf("Extracted images: %d\n", len(r.Images)))
		for _, img := range r.Images {
			b.WriteString(fmt.Sprintf("  - %s (%s) [data:%s;base64,%s]\n",
				img.Filename, img.MimeType, img.MimeType, img.Base64))
		}
	}
	if savedAt != "" {
		b.WriteString(fmt.Sprintf("Saved to: %s\n", savedAt))
	}
	b.WriteString("\n")
	b.WriteString(r.Markdown)
	return b.String()
}

// ── Tool interface plumbing ────────────────────────────────────────────────

func (t *ConvertTool) Description(_ context.Context) (string, error) {
	return ConvertDescription, nil
}

func (t *ConvertTool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	filePath, ok := input["path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("path is required and must be a string")
	}
	return input, nil
}

func (t *ConvertTool) CheckPermissions(_ context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	filePath, _ := input["path"].(string)
	if strings.TrimSpace(filePath) == "" {
		return types.Deny("path is required and must be a string")
	}
	absolutePath, err := t.resolvePath(filePath, toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	if err := shared.ValidateUNCPathSecurity(absolutePath); err != nil {
		return types.Deny(err.Error())
	}
	if err := t.validateReadPath(toolCtx, absolutePath); err != nil {
		return types.Deny(err.Error())
	}
	return types.Passthrough(input)
}

func (t *ConvertTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *ConvertTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *ConvertTool) IsEnabled() bool                         { return true }
func (t *ConvertTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *ConvertTool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}

func (t *ConvertTool) validateReadPath(toolCtx tool.ToolUseContext, path string) error {
	sandboxCtx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if toolCtx.Workspace != nil {
		sandboxCtx.WorkspaceRoot = strings.TrimSpace(toolCtx.Workspace.Root)
	}
	decision, err := t.filesystemPolicy.EvaluatePath(sandboxCtx, path, access.AccessRead)
	if err != nil {
		return err
	}
	return access.ErrorForDecision(decision.DecisionResult)
}

func (t *ConvertTool) resolvePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := t.effectiveWorkingDir(toolCtx)
	if filepath.IsAbs(path) || strings.TrimSpace(workingDir) == "" {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

func (t *ConvertTool) effectiveWorkingDir(toolCtx tool.ToolUseContext) string {
	if strings.TrimSpace(toolCtx.WorkingDirectory) != "" {
		return toolCtx.WorkingDirectory
	}
	if strings.TrimSpace(t.workingDir) != "" {
		return t.workingDir
	}
	return "."
}
