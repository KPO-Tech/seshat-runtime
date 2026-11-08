package documentreader

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	reader "github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	webcore "github.com/KPO-Tech/seshat-runtime/internal/web"
)

const (
	// ReadURLToolName is the name of the read_document_url tool.
	ReadURLToolName = "read_document_url"

	// ReadURLDisplayName is the display name of the read_document_url tool.
	ReadURLDisplayName = "Read Document URL"

	// ReadURLSearchHint is a hint for tool search functionality.
	ReadURLSearchHint = "fetch and convert a remote document or web page to markdown"

	// ReadURLDescription is the description of the read_document_url tool.
	ReadURLDescription = "Fetch a document at a URL and convert it to readable markdown. " +
		"Supports PDF, DOCX, PPTX, XLSX, HTML pages, and arXiv papers. " +
		"Requires a document reader to be configured. " +
		"Optionally saves the extracted markdown to a workspace path."
)

// ReadURLTool fetches a remote document and converts it to markdown via a
// document-conversion backend.
type ReadURLTool struct {
	documentReader reader.Converter
	// resolver answers the host lookups of the network policy; nil means the system resolver.
	resolver webcore.HostResolver
}

// NewReadURLTool creates a new read_document_url tool.
func NewReadURLTool(cfg Config) *ReadURLTool {
	return &ReadURLTool{documentReader: cfg.converter()}
}

func (t *ReadURLTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ReadURLToolName,
		DisplayName: ReadURLDisplayName,
		SearchHint:  ReadURLSearchHint,
		Description: ReadURLDescription,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL of the document to fetch and convert (e.g. an arXiv PDF, a DOCX on a server, or any HTML page)",
				},
				"save_path": map[string]any{
					"type":        "string",
					"description": "Optional path (relative to workspace) to write the extracted markdown. Example: \"docs/paper.md\"",
				},
			},
			"required": []string{"url"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

func (t *ReadURLTool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	rawURL, ok := input.Parsed["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return tool.NewErrorResult(fmt.Errorf("url is required")), nil
	}
	rawURL = strings.TrimSpace(rawURL)

	if err := validateURL(rawURL); err != nil {
		return tool.NewErrorResult(err), nil
	}

	savePath, _ := input.Parsed["save_path"].(string)
	savePath = strings.TrimSpace(savePath)

	if t.documentReader == nil || !t.documentReader.IsAvailable(ctx) {
		return tool.NewTextResult(fmt.Sprintf(
			"URL: %s\n\nread_document_url requires a configured document reader.",
			rawURL,
		)), nil
	}

	// The address is chosen by the model, and it is the document reader that fetches it, from wherever the reader runs: the same policy
	// as web_fetch keeps it off the local network and the metadata services. The reader does the download itself, so this is a check
	// of the name and of what it resolves to now, not of the connection.
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("invalid URL: %w", err)), nil
	}
	if err := webcore.ResolveAndRejectLocalNetworkTarget(ctx, parsedURL, t.resolver); err != nil {
		return tool.NewErrorResult(fmt.Errorf("read_document_url: %w", err)), nil
	}

	// Refuse a save path that is not acceptable before the download, not after it.
	saveTo := ""
	if savePath != "" {
		target, err := saveTarget(savePath, input.ToolContextValue())
		if err != nil {
			return tool.NewErrorResult(fmt.Errorf("save_path: %w", err)), nil
		}
		saveTo = target
	}

	conversion, err := t.documentReader.ConvertURL(ctx, rawURL)
	if err != nil {
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("read_document_url cancelled")), nil
		}
		return tool.NewErrorResult(fmt.Errorf("document conversion failed for %s: %w", rawURL, err)), nil
	}

	// Optionally write markdown to workspace path.
	// A failure is told, not hidden: the answer says where the text went or why it did not.
	savedAt := ""
	saveNote := ""
	if saveTo != "" {
		if err := writeNewFile(saveTo, []byte(conversion.Markdown)); err != nil {
			saveNote = fmt.Sprintf("\nNot saved: %v", err)
		} else {
			savedAt = saveTo
		}
	}

	return tool.NewTextResult(formatURLResult(rawURL, conversion, savedAt) + saveNote), nil
}

func (t *ReadURLTool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	rawURL, ok := input["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return nil, fmt.Errorf("url is required")
	}
	if err := validateURL(strings.TrimSpace(rawURL)); err != nil {
		return nil, err
	}
	// The name alone, without a lookup; the lookup is made when the tool runs.
	if parsed, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		if err := webcore.RejectLocalNetworkTarget(parsed); err != nil {
			return nil, fmt.Errorf("read_document_url: %w", err)
		}
	}
	return input, nil
}

func (t *ReadURLTool) CheckPermissions(_ context.Context, input map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(input)
}

func (t *ReadURLTool) IsEnabled() bool { return true }

// IsReadOnly is true only when nothing is saved: with a save_path the tool writes a file.
func (t *ReadURLTool) IsReadOnly(input map[string]any) bool {
	savePath, _ := input["save_path"].(string)
	return strings.TrimSpace(savePath) == ""
}
func (t *ReadURLTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *ReadURLTool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}
func (t *ReadURLTool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}
func (t *ReadURLTool) Description(_ context.Context) (string, error) { return ReadURLDescription, nil }

// ── helpers ──────────────────────────────────────────────────────────────────

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	case "":
		return fmt.Errorf("URL must include a scheme (http:// or https://)")
	default:
		return fmt.Errorf("unsupported URL scheme %q: only http and https are allowed", u.Scheme)
	}
}

// writeNewFile creates the file and its folders; it never replaces a file that appeared since the check.
func writeNewFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the path was checked by saveTarget
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func formatURLResult(sourceURL string, r *reader.ConversionResult, savedAt string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Source: %s\n", sourceURL))
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
