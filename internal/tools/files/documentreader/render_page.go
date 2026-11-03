package documentreader

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/pdfsmart"
)

// maxInlinedImageBytes caps how large a rendered page image can be before it
// is dropped from the follow-up message. A giant page render would otherwise
// balloon token usage (base64 is ~33% larger than the raw bytes) or risk
// being rejected outright by providers that cap request/image size. 8 MiB of
// raw PNG is already far beyond what any current provider can usefully
// consume as a single image.
const maxInlinedImageBytes = 8 * 1024 * 1024

const (
	RenderPageToolName = "render_document_page"

	RenderPageDisplayName = "Render Document Page"

	RenderPageSearchHint = "render one PDF page as an image for visual inspection of diagrams, charts, tables, or scanned content"

	RenderPageDescription = "Render one page of a local document to a PNG image for visual inspection, then attach it to the conversation as an image (in the message that follows this tool's result) so you can actually see it.\n\n" +
		"Use this after reading a document when markdown extraction may miss important visual details: diagrams, charts, screenshots, equations, scanned pages, or visually structured tables.\n\n" +
		"Current local support is PDF pages through the configured native document renderer. The page number is 1-indexed."
)

type RenderPageTool struct {
	workingDir       string
	renderer         pdfsmart.PageRenderer
	filesystemPolicy *access.FilesystemPolicy
}

func NewRenderPageTool(cfg Config, workingDir string) *RenderPageTool {
	return &RenderPageTool{
		workingDir:       workingDir,
		renderer:         cfg.pageRenderer(),
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
}

func (t *RenderPageTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        RenderPageToolName,
		DisplayName: RenderPageDisplayName,
		SearchHint:  RenderPageSearchHint,
		Description: RenderPageDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local PDF file to inspect",
				},
				"page": map[string]any{
					"type":        "integer",
					"description": "1-indexed page number to render",
					"minimum":     1,
				},
			},
			"required": []string{"path", "page"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

func (t *RenderPageTool) Call(ctx context.Context, input tool.CallInput, permissionCheck types.CanUseToolFn) (tool.CallResult, error) {
	filePath, ok := input.Parsed["path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return tool.NewErrorResult(fmt.Errorf("path is required and must be a string")), nil
	}
	if err := shared.ValidateFilePath(filePath, "reading"); err != nil {
		return tool.NewErrorResult(err), nil
	}
	page, err := parsePositivePage(input.Parsed["page"])
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

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
	if strings.ToLower(filepath.Ext(absolutePath)) != ".pdf" {
		return tool.NewErrorResult(fmt.Errorf("render_document_page currently supports PDF files only: %s", filePath)), nil
	}

	if permissionCheck != nil {
		req := access.PermissionRequest{
			ToolName:      RenderPageToolName,
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessRead,
			Paths:         []string{absolutePath},
			Justification: "Render document page for visual inspection",
			Scope:         access.ApprovalScopeToolCall,
		}
		permResult, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput: map[string]any{
				"path": absolutePath,
				"page": page,
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
		if err := access.ErrorForPermissionResult(permResult, "document page rendering requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	if t.renderer == nil {
		return tool.NewTextResult(fmt.Sprintf(
			"File: %s\nPage: %d\n\nrender_document_page requires a configured native document page renderer.",
			filePath, page,
		)), nil
	}

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to read %s: %w", filePath, err)), nil
	}
	png, err := t.renderer.RenderPage(ctx, data, page)
	if err != nil {
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("render_document_page cancelled")), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to render page %d of %s: %w", page, filePath, err)), nil
	}
	if len(png) > maxInlinedImageBytes {
		return tool.NewTextResult(fmt.Sprintf(
			"Page %d of %s rendered to %d bytes, which exceeds the %d byte limit for inline image delivery. "+
				"Try a lower-resolution renderer configuration, or inspect the page another way.",
			page, filePath, len(png), maxInlinedImageBytes,
		)), nil
	}

	// The rendered page is delivered as a genuine image content block in a
	// follow-up message (result.NewMessages), not embedded as base64 text in
	// this tool result - see types.ImageContent / types.UserMessageWithImage,
	// the same mechanism internal/pdfsmart/vision uses to hand a page image to
	// a vision-capable model. A base64 data URI inlined into ToolResultContent
	// (a plain string, see internal/types/message.go) is never reconstructed
	// into a real image block anywhere downstream - the model would just see
	// an opaque, multi-KB text blob instead of the page. Keeping this tool's
	// own Content short also keeps it well clear of MicroCompactor's default
	// tool-result trim threshold (internal/runtime/memory/micro.go), which
	// trims by character count and would otherwise corrupt an inlined base64
	// blob if the session later hits auto-compaction.
	image := types.ImageContent{}
	image.Source.Type = "base64"
	image.Source.MediaType = "image/png"
	image.Source.Data = base64.StdEncoding.EncodeToString(png)

	result := tool.NewTextResult(formatRenderPageSummary(filePath, page, len(png)))
	result.NewMessages = []types.Message{
		types.UserMessageWithImage(
			fmt.Sprintf("render-page-%s", toolCtx.ToolUseID),
			fmt.Sprintf("Rendered page %d of %s:", page, filepath.Base(filePath)),
			image,
		),
	}
	return result, nil
}

func formatRenderPageSummary(sourcePath string, page int, pngBytes int) string {
	return fmt.Sprintf(
		"Rendered page %d of %s as a %d KB PNG image, attached as an image in the message that follows this result.",
		page, sourcePath, (pngBytes+1023)/1024,
	)
}

func parsePositivePage(raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		if v >= 1 {
			return v, nil
		}
	case int64:
		if v >= 1 {
			return int(v), nil
		}
	case float64:
		if v >= 1 && v == float64(int(v)) {
			return int(v), nil
		}
	}
	return 0, fmt.Errorf("page is required and must be a positive integer")
}

func (t *RenderPageTool) Description(_ context.Context) (string, error) {
	return RenderPageDescription, nil
}

func (t *RenderPageTool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	filePath, ok := input["path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("path is required and must be a string")
	}
	if _, err := parsePositivePage(input["page"]); err != nil {
		return nil, err
	}
	return input, nil
}

func (t *RenderPageTool) CheckPermissions(_ context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	filePath, _ := input["path"].(string)
	if strings.TrimSpace(filePath) == "" {
		return types.Deny("path is required and must be a string")
	}
	if _, err := parsePositivePage(input["page"]); err != nil {
		return types.Deny(err.Error())
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

func (t *RenderPageTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *RenderPageTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *RenderPageTool) IsEnabled() bool                         { return true }
func (t *RenderPageTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *RenderPageTool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}

func (t *RenderPageTool) validateReadPath(toolCtx tool.ToolUseContext, path string) error {
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

func (t *RenderPageTool) resolvePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := t.effectiveWorkingDir(toolCtx)
	if filepath.IsAbs(path) || strings.TrimSpace(workingDir) == "" {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

func (t *RenderPageTool) effectiveWorkingDir(toolCtx tool.ToolUseContext) string {
	if strings.TrimSpace(toolCtx.WorkingDirectory) != "" {
		return toolCtx.WorkingDirectory
	}
	if strings.TrimSpace(t.workingDir) != "" {
		return t.workingDir
	}
	return "."
}
