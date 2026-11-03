package read

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	"github.com/KPO-Tech/seshat-runtime/internal/officetext"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Tool represents the FileRead tool
type Tool struct {
	// config is the tool configuration
	config *ToolConfig

	// filesystemPolicy centralizes common path access checks.
	filesystemPolicy *access.FilesystemPolicy

	// documentReader converts PDFs to structured markdown when set.
	// When nil the tool falls back to base64 pass-through.
	documentReader documentreader.Converter
}

// ToolConfig represents the FileRead tool configuration
type ToolConfig struct {
	// MaxFileSize is the maximum file size to read
	MaxFileSize int64

	// MaxImageSize is the maximum image size to read
	MaxImageSize int64

	// MaxPDFCharsPerRead bounds the text of one PDF read; zero means PDFMaxCharsPerRead.
	MaxPDFCharsPerRead int

	// MaxPDFEnginePagesPerRead bounds the pages with no text layer one PDF read sends to the document
	// reader; zero means PDFMaxEnginePagesPerRead.
	MaxPDFEnginePagesPerRead int

	// DefaultLimit is the default number of lines to read
	DefaultLimit int

	// MaxLimit is the maximum number of lines to read
	MaxLimit int

	// DocumentReaderURL is the base URL of a document-reader service. Used
	// to build the default DocumentConverter when that field is left nil.
	// Example: "http://localhost:5001"
	DocumentReaderURL string

	// DocumentConverter, when set, is used instead of building a
	// service client from DocumentReaderURL. Takes precedence over
	// DocumentReaderURL.
	DocumentConverter documentreader.Converter
}

// DefaultToolConfig returns default tool configuration
func DefaultToolConfig() *ToolConfig {
	return &ToolConfig{
		MaxFileSize:  MaxFileSize,
		MaxImageSize: MaxImageSize,
		DefaultLimit: DefaultLimit,
		MaxLimit:     MaxLimit,
	}
}

// NewTool creates a new FileRead tool
func NewTool(config *ToolConfig) *Tool {
	if config == nil {
		config = DefaultToolConfig()
	}

	t := &Tool{
		config:           config,
		filesystemPolicy: access.NewDefaultFilesystemPolicy(),
	}
	switch {
	case config.DocumentConverter != nil:
		t.documentReader = config.DocumentConverter
	case config.DocumentReaderURL != "":
		t.documentReader = documentreader.NewDoclingClient(config.DocumentReaderURL)
	}
	return t
}

// Definition returns the tool definition
func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "Read File",
		SearchHint:  SearchHint,
		Description: Description,
		Category:    "filesystem",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "The absolute path to the file to read",
				},
				"offset": map[string]any{
					"type":        "number",
					"description": "The line number to start reading from (1-indexed). A negative value counts from the end of the file instead - e.g. offset=-50 starts 50 lines before EOF, useful for tailing logs or large outputs without knowing the total line count. Also applies to a converted document (DOCX, PPTX, XLSX), whose markdown is read in pieces of about 120,000 characters: the result says which offset to continue from. Only provide if the file is too large to read at once.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "The number of lines to read (lines of the markdown, for a converted document). Only provide if the file is too large to read at once.",
				},
				"pages": map[string]any{
					"type":        "string",
					"description": "Page range for PDF files (e.g., \"1-5\", \"3\", \"10-20\", \"5-\" for page 5 to the end). Only applicable to PDF files. A read stops at a size limit and says which page to continue from.",
				},
				"password": map[string]any{
					"type":        "string",
					"description": "Password of a password-protected PDF. Only give it when the user has provided it; a PDF that is encrypted without a password to open it (an owner password only) is read without one.",
				},
			},
			"required": []string{"file_path"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: true,
	}
}

// Call executes the tool
func (t *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	directCall := input.ToolContext == nil
	// Extract file path
	filePath, ok := input.Parsed["file_path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return tool.NewErrorResult(fmt.Errorf("file_path is required and must be a string")), nil
	}
	filePath, err := resolveFilePath(filePath, input.ToolContextValue())
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	// Step 1: Security validation - check if path is blocked
	if t.isBlockedDevicePath(filePath) {
		return tool.NewErrorResult(fmt.Errorf("cannot read device path: %s", filePath)), nil
	}

	// Step 2: Path validation
	err = t.validateReadPath(input.ToolContextValue(), filePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("path validation failed: %w", err)), nil
	}
	if directCall && permissionCheck != nil {
		req := access.PermissionRequest{
			ToolName:      ToolName,
			Environment:   sandbox.EnvironmentLocal,
			Access:        access.AccessRead,
			Paths:         []string{filePath},
			Justification: "Read file contents",
			Scope:         access.ApprovalScopeToolCall,
			Metadata: map[string]any{
				"pages": input.Parsed["pages"],
			},
		}
		toolCtx := input.ToolContextValue()
		permissionResult, err := access.ResolveToolPermission(ctx, permissionCheck, req, access.ToolPermissionOptions{
			ToolInput: map[string]any{
				"file_path": filePath,
				"offset":    input.Parsed["offset"],
				"limit":     input.Parsed["limit"],
				"pages":     input.Parsed["pages"],
			},
			ToolUseID:              toolCtx.ToolUseID,
			SessionID:              toolCtx.SessionID,
			TurnID:                 toolCtx.TurnID,
			PermissionMode:         toolCtx.PermissionMode,
			WorkingDirectory:       strings.TrimSpace(toolCtx.WorkingDirectory),
			IsToolRunningInSandbox: toolCtx.EnableSandbox,
		})
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if err := access.ErrorForPermissionResult(permissionResult, "file read requires approval"); err != nil {
			return tool.NewErrorResult(err), nil
		}
	}

	// Step 3: Validate pages parameter (if provided)
	pagesParam := ""
	if p, ok := input.Parsed["pages"].(string); ok && p != "" {
		pagesParam = p
		// Parse and validate page range
		if _, err := ParsePDFPageRange(pagesParam); err != nil {
			return tool.NewErrorResult(fmt.Errorf("invalid pages parameter: %w", err)), nil
		}
	}

	// Step 5: Check if file exists and get info
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to provide helpful suggestions
			workingDir, _ := os.Getwd()
			return tool.NewErrorResult(FormatNotFoundError(filePath, workingDir)), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to access file: %w", err)), nil
	}

	// Step 6: Detect file type
	fileType, err := DetectFileType(filePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to detect file type: %w", err)), nil
	}

	// Step 6.5: Check deduplication for text files
	if fileType == FileTypeText {
		// Parse offset and limit for cache key
		offset := 0
		limit := 0
		if offsetVal, ok := input.Parsed["offset"].(float64); ok {
			offset = int(offsetVal)
		}
		if limitVal, ok := input.Parsed["limit"].(float64); ok {
			limit = int(limitVal)
		}

		// Check if file has been read before with same parameters
		if _, found := CheckFileUnchanged(filePath, offset, limit); found {
			// File unchanged - return cached result
			result := &FileReadResult{
				Type: FileTypeUnchanged,
				Unchanged: &UnchangedFileResult{
					FilePath: filePath,
					Message:  "File unchanged since last read (using cached content)",
				},
			}
			return tool.NewTextResult(t.formatUnchangedResult(result)), nil
		}
	}

	// Step 7: Read based on file type
	switch fileType {
	case FileTypePDF:
		password, _ := input.Parsed["password"].(string)
		return t.readPDFFile(ctx, filePath, fileInfo, pagesParam, password)
	case FileTypeNotebook:
		return t.readNotebookFile(ctx, filePath, fileInfo)
	case FileTypeText:
		return t.readTextFile(ctx, filePath, fileInfo, input.Parsed)
	case FileTypeImage:
		return t.readImageFile(ctx, filePath, fileInfo)
	case FileTypeDocumentReader:
		return t.readDocumentReaderFile(ctx, filePath, fileInfo, input.Parsed)
	default:
		return t.handleBinaryFile(filePath)
	}
}

// readTextFile reads a text file with optional offset/limit
func (t *Tool) readTextFile(
	ctx context.Context,
	filePath string,
	fileInfo os.FileInfo,
	parsed map[string]any,
) (tool.CallResult, error) {
	// Parse offset and limit
	offset := 0
	limit := t.config.DefaultLimit
	requestedOffset := 0

	if offsetVal, ok := parsed["offset"].(float64); ok {
		requestedOffset = int(offsetVal)
		offset = requestedOffset
	}

	if limitVal, ok := parsed["limit"].(float64); ok {
		limit = int(limitVal)
		if limit < 1 {
			limit = t.config.DefaultLimit
		}
		if limit > t.config.MaxLimit {
			limit = t.config.MaxLimit
		}
	}

	// Check file size
	if fileInfo.Size() > t.config.MaxFileSize {
		return tool.NewErrorResult(fmt.Errorf("file too large (%d bytes, max %d bytes)", fileInfo.Size(), t.config.MaxFileSize)), nil
	}

	if requestedOffset < 0 {
		// Tail mode: resolve "N lines before EOF" to an absolute 1-indexed
		// line number. A file shorter than |offset| just reads from line 1.
		totalLines, err := CountFileLines(ctx, filePath)
		if err != nil {
			if ctx.Err() != nil {
				return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
			}
			return tool.NewErrorResult(fmt.Errorf("failed to count lines for tail offset: %w", err)), nil
		}
		offset = totalLines + requestedOffset + 1
		if offset < 1 {
			offset = 1
		}
	} else if offset < 1 {
		offset = 1
	}

	// Read file with cancellation support
	content, lineCount, totalLines, _, _, _, err := ReadFileInRange(
		ctx,
		filePath,
		offset,
		limit,
		t.config.MaxFileSize,
	)

	if err != nil {
		// Check if it's a cancellation error
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to read file: %w", err)), nil
	}

	// Calculate truncation
	isTruncated := limit > 0 && lineCount == limit && totalLines > offset+lineCount

	// Create result
	result := &FileReadResult{
		Type: FileTypeText,
		Text: &TextFileResult{
			FilePath:   filePath,
			Content:    content,
			NumLines:   lineCount,
			StartLine:  offset,
			TotalLines: totalLines,
			Truncated:  isTruncated,
		},
	}

	// Cache the result for deduplication
	isPartialView := offset != 0 || limit != t.config.DefaultLimit
	CacheFileRead(filePath, content, fileInfo, offset, limit, isPartialView)

	return tool.NewTextResult(t.formatTextResult(result)), nil
}

// readImageFile reads an image file with processing and token validation
func (t *Tool) readImageFile(
	ctx context.Context,
	filePath string,
	fileInfo os.FileInfo,
) (tool.CallResult, error) {
	// Check for cancellation
	select {
	case <-ctx.Done():
		return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
	default:
	}

	// Check file size
	if fileInfo.Size() > t.config.MaxImageSize {
		return tool.NewErrorResult(fmt.Errorf("image too large (%d bytes, max %d bytes)", fileInfo.Size(), t.config.MaxImageSize)), nil
	}

	// Process image with resize/compression and token budget
	processed, err := ReadAndProcessImage(filePath, DefaultMaxImageTokens)
	if err != nil {
		// Check if it's a cancellation error
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to process image: %w", err)), nil
	}

	// Create result
	result := &FileReadResult{
		Type: FileTypeImage,
		Image: &ImageFileResult{
			FilePath:     filePath,
			Base64:       processed.Base64,
			MimeType:     processed.MimeType,
			OriginalSize: processed.OriginalSize,
			Dimensions:   processed.Dimensions,
		},
	}

	return tool.NewTextResult(t.formatImageResult(result)), nil
}

// handleBinaryFile handles binary files that cannot be read
func (t *Tool) handleBinaryFile(filePath string) (tool.CallResult, error) {
	result := &FileReadResult{
		Type: FileTypeBinary,
		Binary: &BinaryFileResult{
			FilePath:   filePath,
			Reason:     "Binary files cannot be displayed as text",
			Suggestion: "Use a specialized tool or download the file to view its contents",
		},
	}

	return tool.NewTextResult(t.formatBinaryResult(result)), nil
}

// markdownSidecarMaxAge caps how much we trust a pre-converted "<name>.md"
// file sitting next to the source: if it's older than the source (edited/
// replaced after conversion) or simply ancient, treat it as stale and
// reconvert rather than serve outdated content.
const markdownSidecarMaxAge = 24 * time.Hour

type documentSidecarMetadata struct {
	Engine    string                    `json:"engine,omitempty"`
	PageCount int                       `json:"page_count,omitempty"`
	Images    []documentSidecarImage    `json:"images,omitempty"`
	Pages     []documentSidecarPageInfo `json:"pages,omitempty"`
}

type documentSidecarImage struct {
	Filename string `json:"filename"`
	MimeType string `json:"mime_type,omitempty"`
}

type documentSidecarPageInfo struct {
	Page     int    `json:"page"`
	Source   string `json:"source,omitempty"`
	HasImage bool   `json:"has_image,omitempty"`
}

func (m documentSidecarMetadata) visualPages() []int {
	pages := make([]int, 0)
	seen := make(map[int]bool)
	for _, page := range m.Pages {
		if page.Page <= 0 || !page.HasImage || seen[page.Page] {
			continue
		}
		seen[page.Page] = true
		pages = append(pages, page.Page)
	}
	return pages
}

// sidecarMarkdown checks for a pre-converted "<name>.md" file next to filePath
// - the exact convention seshat-ai's upload pipeline writes when it eagerly
// DocumentReader-converts an attachment - and returns its content if present and not
// stale. This avoids a second full the configured document reader round-trip (including OCR
// cost for scanned PDFs/images) when the caller already paid for one.
//
// If seshat-ai wrote the companion "<name>.document.json" metadata sidecar,
// callers can also recover page/image hints without reconverting the source.
// The markdown sidecar still carries only text; image bytes themselves remain
// omitted from the read_file text result.
func sidecarMarkdown(filePath string, sourceInfo os.FileInfo) string {
	mdPath := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + ".md"
	mdInfo, err := os.Stat(mdPath)
	if err != nil || mdInfo.IsDir() {
		return ""
	}
	if mdInfo.ModTime().Before(sourceInfo.ModTime()) {
		return ""
	}
	if time.Since(mdInfo.ModTime()) > markdownSidecarMaxAge {
		return ""
	}
	data, err := os.ReadFile(mdPath)
	if err != nil || len(data) == 0 {
		return ""
	}
	return string(data)
}

func sidecarDocumentMetadata(filePath string, sourceInfo os.FileInfo) documentSidecarMetadata {
	metaPath := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + ".document.json"
	metaInfo, err := os.Stat(metaPath)
	if err != nil || metaInfo.IsDir() {
		return documentSidecarMetadata{}
	}
	if metaInfo.ModTime().Before(sourceInfo.ModTime()) {
		return documentSidecarMetadata{}
	}
	if time.Since(metaInfo.ModTime()) > markdownSidecarMaxAge {
		return documentSidecarMetadata{}
	}
	data, err := os.ReadFile(metaPath)
	if err != nil || len(data) == 0 {
		return documentSidecarMetadata{}
	}
	var meta documentSidecarMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return documentSidecarMetadata{}
	}
	return meta
}

// readPDFAsFile hands a PDF over as a file for a model that can read it itself (or, with a page range,
// as the extracted pages). It is the last resort when the PDF cannot be read as text: it has no text
// layer and no reader could supply one, or it could not be parsed at all.
func (t *Tool) readPDFAsFile(
	ctx context.Context,
	filePath string,
	fileInfo os.FileInfo,
	pagesParam string,
) (tool.CallResult, error) {
	if pagesParam != "" {
		parsedRange, err := ParsePDFPageRange(pagesParam)
		if err != nil {
			return tool.NewErrorResult(fmt.Errorf("failed to parse page range: %w", err)), nil
		}
		select {
		case <-ctx.Done():
			return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
		default:
		}
		extractResult, err := ExtractPDFPages(ctx, filePath, parsedRange)
		if err != nil {
			if ctx.Err() != nil {
				return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
			}
			return t.readWholePDFFallback(ctx, filePath, fileInfo, fmt.Sprintf("Could not extract requested PDF pages (%v). Falling back to the full PDF.", err))
		}
		result := &FileReadResult{
			Type: FileTypePDFExtracted,
			PDFExtracted: &PDFExtractedResult{
				FilePath:     filePath,
				OriginalSize: extractResult.OriginalSize,
				Count:        extractResult.Count,
				OutputDir:    extractResult.OutputDir,
			},
		}
		return tool.NewTextResult(t.formatPDFExtractedResult(result)), nil
	}

	pageCount, err := GetPDFPageCount(ctx, filePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to get PDF page count: %w", err)), nil
	}
	if pageCount > PDFATMentionInlineThreshold {
		return tool.NewErrorResult(fmt.Errorf("this PDF has %d pages, which is too many to read at once. Use the pages parameter to read specific page ranges (e.g., pages: \"1-5\"). Maximum %d pages per request.", pageCount, MaxPagesPerRead)), nil
	}

	pdfResult, err := ReadPDF(ctx, filePath)
	if err != nil {
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to read PDF: %w", err)), nil
	}
	result := &FileReadResult{
		Type: FileTypePDF,
		PDF: &PDFFileResult{
			FilePath:     filePath,
			Base64:       pdfResult.Base64,
			OriginalSize: pdfResult.OriginalSize,
			PageCount:    pdfResult.PageCount,
		},
	}
	return tool.NewTextResult(t.formatPDFResult(result)), nil
}

func (t *Tool) readWholePDFFallback(ctx context.Context, filePath string, fileInfo os.FileInfo, warning string) (tool.CallResult, error) {
	select {
	case <-ctx.Done():
		return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
	default:
	}
	pageCount, err := GetPDFPageCount(ctx, filePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to get PDF page count after page extraction failed: %w", err)), nil
	}
	if pageCount > PDFATMentionInlineThreshold {
		return tool.NewErrorResult(fmt.Errorf("%s The PDF has %d pages, which is too many to read at once. Try converting it with the configured document reader or use a smaller page range.", warning, pageCount)), nil
	}
	pdfResult, err := ReadPDF(ctx, filePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to read PDF after page extraction failed: %w", err)), nil
	}
	result := &FileReadResult{
		Type: FileTypePDF,
		PDF: &PDFFileResult{
			FilePath:     filePath,
			Base64:       pdfResult.Base64,
			OriginalSize: fileInfo.Size(),
			PageCount:    pageCount,
		},
	}
	return tool.NewTextResult(warning + "\n\n" + t.formatPDFResult(result)), nil
}

// readDocumentReaderFile converts a DocumentReader-supported binary file (DOCX, PPTX, XLSX, WAV, MP3)
// to markdown via the configured document reader. When DocumentReader is not configured it returns a descriptive
// message so the agent understands why the file can't be read directly.
func (t *Tool) readDocumentReaderFile(
	ctx context.Context,
	filePath string,
	fileInfo os.FileInfo,
	parsed map[string]any,
) (tool.CallResult, error) {
	select {
	case <-ctx.Done():
		return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
	default:
	}

	if fileInfo.Size() > t.config.MaxFileSize {
		return tool.NewErrorResult(fmt.Errorf("file too large (%d bytes, max %d bytes)", fileInfo.Size(), t.config.MaxFileSize)), nil
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	format := strings.TrimPrefix(ext, ".")
	offset, limit := documentRange(parsed)

	finish := func(result *DocumentReaderFileResult, thin bool) (tool.CallResult, error) {
		RecordExternalRead(filePath, fileInfo.ModTime(), result.Markdown, true)
		window := windowDocument(result.Markdown, offset, limit, DocumentMaxCharsPerRead)
		if window.First > window.Total {
			return tool.NewErrorResult(fmt.Errorf("offset %d is past the end of the document, which has %d lines", offset, window.Total)), nil
		}
		result.Markdown = window.Text
		if note := window.continuation(); note != "" {
			result.Notes = append(result.Notes, note)
		}
		if thin {
			result.Notes = append(result.Notes, "This document has little text of its own (it is mostly pictures); what is shown is all that could be read.")
		}
		return tool.NewTextResult(t.formatDocumentReaderResult(&FileReadResult{Type: FileTypeDocumentReader, DocumentReader: result})), nil
	}

	if cached := sidecarMarkdown(filePath, fileInfo); cached != "" {
		meta := sidecarDocumentMetadata(filePath, fileInfo)
		images := make([]PDFImage, 0, len(meta.Images))
		for _, img := range meta.Images {
			images = append(images, PDFImage{Filename: img.Filename, MimeType: img.MimeType})
		}
		return finish(&DocumentReaderFileResult{
			FilePath:     filePath,
			Format:       format,
			Markdown:     cached,
			OriginalSize: fileInfo.Size(),
			PageCount:    meta.PageCount,
			Images:       images,
			VisualPages:  meta.visualPages(),
		}, false)
	}

	// DOCX/PPTX/XLSX are zipped XML, not scanned documents - no ML/OCR is
	// needed to read them, so try the native, dependency-free extractor
	// before ever reaching for the configured document reader. WAV/MP3 (officetext.Extract
	// returns ok=false for those) still need a document reader for transcription.
	thin := ""
	if officetext.SupportedExtensions[ext] {
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return tool.NewErrorResult(fmt.Errorf("failed to read %s: %w", strings.ToUpper(format), readErr)), nil
		}
		markdown, ok, sparse, extractErr := officetext.Extract(filePath, data)
		switch {
		case ok && extractErr == nil && !sparse:
			return finish(&DocumentReaderFileResult{FilePath: filePath, Format: format, Markdown: markdown, OriginalSize: fileInfo.Size()}, false)
		case ok && extractErr == nil:
			// A deck that is mostly pictures, with a title or two of real text: a document reader may get more out
			// of it (OCR of the pictures), and if none does, what was read is still worth giving.
			thin = markdown
		}
	}
	thinResult := func() (tool.CallResult, bool) {
		if thin == "" {
			return tool.CallResult{}, false
		}
		result, _ := finish(&DocumentReaderFileResult{FilePath: filePath, Format: format, Markdown: thin, OriginalSize: fileInfo.Size()}, true)
		return result, true
	}

	if t.documentReader == nil || !t.documentReader.IsAvailable(ctx) {
		if result, ok := thinResult(); ok {
			return result, nil
		}
		return tool.NewTextResult(fmt.Sprintf(
			"File: %s\nFormat: %s | Size: %d bytes\n\nNo text could be read from this file, and no document reader is configured to read it any other way.%s",
			filePath, strings.ToUpper(format), fileInfo.Size(), unreadableFormatHint(ext),
		)), nil
	}

	conversion, err := t.documentReader.ConvertFile(ctx, filePath)
	if err != nil {
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
		}
		if result, ok := thinResult(); ok {
			return result, nil
		}
		return tool.NewErrorResult(fmt.Errorf("document-reader conversion failed for %s: %w%s", strings.ToUpper(format), err, unreadableFormatHint(ext))), nil
	}
	if strings.TrimSpace(conversion.Markdown) == "" {
		if result, ok := thinResult(); ok {
			return result, nil
		}
	}

	images := make([]PDFImage, 0, len(conversion.Images))
	for _, img := range conversion.Images {
		images = append(images, PDFImage{
			Filename: img.Filename,
			MimeType: img.MimeType,
			Base64:   img.Base64,
		})
	}
	return finish(&DocumentReaderFileResult{
		FilePath:     filePath,
		Format:       format,
		Markdown:     conversion.Markdown,
		OriginalSize: fileInfo.Size(),
		PageCount:    conversion.PageCount,
		Images:       images,
	}, false)
}

// documentRange reads the offset and limit of a read of a converted document. Both are lines of its markdown.
func documentRange(parsed map[string]any) (offset, limit int) {
	if v, ok := parsed["offset"].(float64); ok {
		offset = int(v)
	}
	if v, ok := parsed["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	return offset, limit
}

// unreadableFormatHint says why a format could not be read when the reader that was asked has no way to.
func unreadableFormatHint(ext string) string {
	switch ext {
	case ".wav", ".mp3":
		return "\n\nAudio can only be read by a document reader with speech recognition enabled (for docling-serve, its ASR extra: DOCLING_EXTRAS=asr)."
	}
	return ""
}

// readNotebookFile reads a Jupyter notebook file
func (t *Tool) readNotebookFile(
	ctx context.Context,
	filePath string,
	fileInfo os.FileInfo,
) (tool.CallResult, error) {
	// Check for cancellation
	select {
	case <-ctx.Done():
		return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
	default:
	}

	// Check file size
	if fileInfo.Size() > t.config.MaxFileSize {
		return tool.NewErrorResult(fmt.Errorf("notebook too large (%d bytes, max %d bytes)", fileInfo.Size(), t.config.MaxFileSize)), nil
	}

	// Read and parse notebook
	notebook, err := ReadNotebook(filePath)
	if err != nil {
		// Check if it's a cancellation error
		if ctx.Err() != nil {
			return tool.NewErrorResult(fmt.Errorf("file read cancelled")), nil
		}
		return tool.NewErrorResult(fmt.Errorf("failed to read notebook: %w", err)), nil
	}

	// Parse notebook into result
	notebookResult, err := ParseNotebook(notebook, filePath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to parse notebook: %w", err)), nil
	}

	// Create result
	result := &FileReadResult{
		Type:     FileTypeNotebook,
		Notebook: notebookResult,
	}

	return tool.NewTextResult(t.formatNotebookResult(result)), nil
}

// Helper methods

func (t *Tool) isBlockedDevicePath(filePath string) bool {
	// Check exact matches
	if BlockedDevicePaths[filePath] {
		return true
	}

	// Check /proc paths for stdio
	if strings.HasPrefix(filePath, "/proc/") && strings.HasSuffix(filePath, "/fd/0") {
		return true
	}
	if strings.HasPrefix(filePath, "/proc/") && strings.HasSuffix(filePath, "/fd/1") {
		return true
	}
	if strings.HasPrefix(filePath, "/proc/") && strings.HasSuffix(filePath, "/fd/2") {
		return true
	}

	return false
}

func (t *Tool) formatTextResult(result *FileReadResult) string {
	// Use the enhanced formatting with line numbers
	formatted := FormatTextWithLineNumbers(result, false) // false = padded format

	// Add cyber risk reminder if applicable
	if ShouldIncludeCyberRiskReminder() {
		formatted += CyberRiskMitigationReminder
	}

	return formatted
}

func (t *Tool) formatImageResult(result *FileReadResult) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("Image: %s\n", result.Image.FilePath))
	builder.WriteString(fmt.Sprintf("Type: %s\n", result.Image.MimeType))
	builder.WriteString(fmt.Sprintf("Size: %d bytes\n", result.Image.OriginalSize))

	if result.Image.Dimensions != nil {
		builder.WriteString(fmt.Sprintf("Dimensions: %dx%d\n", result.Image.Dimensions.Width, result.Image.Dimensions.Height))
	}

	builder.WriteString(fmt.Sprintf("\n[data:image/%s;base64,%s]", result.Image.MimeType, result.Image.Base64))

	return builder.String()
}

func (t *Tool) formatBinaryResult(result *FileReadResult) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("File: %s\n", result.Binary.FilePath))
	builder.WriteString(fmt.Sprintf("Reason: %s\n", result.Binary.Reason))

	if result.Binary.Suggestion != "" {
		builder.WriteString(fmt.Sprintf("Suggestion: %s\n", result.Binary.Suggestion))
	}

	return builder.String()
}

func (t *Tool) formatPDFResult(result *FileReadResult) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("PDF: %s\n", result.PDF.FilePath))
	builder.WriteString(fmt.Sprintf("Pages: %d\n", result.PDF.PageCount))
	builder.WriteString(fmt.Sprintf("Size: %d bytes\n", result.PDF.OriginalSize))
	builder.WriteString(fmt.Sprintf("\n[data:application/pdf;base64,%s]", result.PDF.Base64))

	return builder.String()
}

func (t *Tool) formatPDFMarkdownResult(result *FileReadResult) string {
	r := result.PDFMarkdown
	var b strings.Builder
	b.WriteString(fmt.Sprintf("PDF: %s\n", r.FilePath))
	b.WriteString(fmt.Sprintf("Pages: %d | Size: %d bytes\n", r.PageCount, r.OriginalSize))
	if len(r.ShownPages) > 0 {
		b.WriteString(fmt.Sprintf("Showing pages: %s of %d\n", formatPageRanges(r.ShownPages), r.PageCount))
	}
	if len(r.Images) > 0 {
		b.WriteString(fmt.Sprintf("Extracted images/figures: %d\n", len(r.Images)))
		for _, img := range r.Images {
			b.WriteString(fmt.Sprintf("  - %s (%s)\n", img.Filename, img.MimeType))
		}
		b.WriteString("Image bytes are intentionally omitted from this text result; use a vision-capable path only when visual inspection is required.\n")
	}
	if len(r.VisualPages) > 0 {
		b.WriteString(fmt.Sprintf("Pages with detected embedded images/visual content: %s (the text does not include what the images show; if %s is available, use it to see them)\n", formatPageRanges(r.VisualPages), renderPageToolName))
	}
	if len(r.NoTextPages) > 0 {
		b.WriteString(fmt.Sprintf("Pages with no readable text (likely scanned): %s (use %s to see them if it is available)\n", formatPageRanges(r.NoTextPages), renderPageToolName))
	}
	if r.ContinueAt > 0 {
		b.WriteString(fmt.Sprintf("Stopped before page %d because this read reached its size limit. Continue with pages=\"%d-\".\n", r.ContinueAt, r.ContinueAt))
	}
	if r.ModelSkipped > 0 {
		b.WriteString(fmt.Sprintf("Tables with no ruling lines were not looked for on %d pages (the time given to the layout models ran out); the text of those pages is complete. Read fewer pages at a time to have them looked at.\n", r.ModelSkipped))
	}
	b.WriteString("\n")
	b.WriteString(r.Markdown)
	return b.String()
}

func (t *Tool) formatDocumentReaderResult(result *FileReadResult) string {
	r := result.DocumentReader
	var b strings.Builder
	b.WriteString(fmt.Sprintf("File: %s\n", r.FilePath))
	b.WriteString(fmt.Sprintf("Format: %s | Size: %d bytes", strings.ToUpper(r.Format), r.OriginalSize))
	if r.PageCount > 0 {
		b.WriteString(fmt.Sprintf(" | Pages: %d", r.PageCount))
	}
	b.WriteString("\n")
	if len(r.Images) > 0 {
		b.WriteString(fmt.Sprintf("Extracted images/figures: %d\n", len(r.Images)))
		for _, img := range r.Images {
			b.WriteString(fmt.Sprintf("  - %s (%s)\n", img.Filename, img.MimeType))
		}
		b.WriteString("Image bytes are intentionally omitted from this text result; use a vision-capable path only when visual inspection is required.\n")
	}
	if len(r.VisualPages) > 0 {
		b.WriteString(fmt.Sprintf("Pages with detected embedded images/visual content: %s\n", formatPageNumbers(r.VisualPages)))
	}
	for _, note := range r.Notes {
		b.WriteString(note)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(r.Markdown)
	return b.String()
}

func formatPageNumbers(pages []int) string {
	parts := make([]string, 0, len(pages))
	for _, page := range pages {
		if page > 0 {
			parts = append(parts, fmt.Sprintf("%d", page))
		}
	}
	return strings.Join(parts, ",")
}

func (t *Tool) formatPDFExtractedResult(result *FileReadResult) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("PDF pages extracted: %d page(s) from %s (%d bytes)\n",
		result.PDFExtracted.Count,
		result.PDFExtracted.FilePath,
		result.PDFExtracted.OriginalSize))

	return builder.String()
}

func (t *Tool) formatNotebookResult(result *FileReadResult) string {
	return FormatNotebookResult(result.Notebook)
}

func (t *Tool) formatUnchangedResult(result *FileReadResult) string {
	return fmt.Sprintf("File: %s\n%s", result.Unchanged.FilePath, result.Unchanged.Message)
}

// Description returns a human-readable description
func (t *Tool) Description(ctx context.Context) (string, error) {
	return "Read file contents with support for text, images, and automatic type detection", nil
}

// ValidateInput validates and normalizes file-read input.
func (t *Tool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	_ = ctx
	filePath, ok := input["file_path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return nil, fmt.Errorf("file_path is required and must be a string")
	}

	normalized := make(map[string]any, len(input))
	for k, v := range input {
		normalized[k] = v
	}
	// A negative offset is meaningful (tail-from-end, see readTextFile) and
	// is passed through as-is; only readTextFile resolves it against the
	// file's actual line count.
	return normalized, nil
}

// CheckPermissions performs file-read-specific permission checks before the global pipeline.
func (t *Tool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	_ = ctx
	filePath, _ := input["file_path"].(string)
	if strings.TrimSpace(filePath) == "" {
		return types.Deny("file_path is required and must be a string")
	}
	resolvedPath, err := resolveFilePath(filePath, toolCtx)
	if err != nil {
		return types.Deny(err.Error())
	}
	filePath = resolvedPath
	if t.isBlockedDevicePath(filePath) {
		return types.Deny(fmt.Sprintf("cannot read device path: %s", filePath))
	}
	if err := t.validateReadPath(toolCtx, filePath); err != nil {
		return types.Deny(fmt.Sprintf("path validation failed: %v", err))
	}
	return types.Passthrough(input)
}

func (t *Tool) validateReadPath(toolCtx tool.ToolUseContext, path string) error {
	ctx := access.Context{
		WorkingDirectory: strings.TrimSpace(toolCtx.WorkingDirectory),
		Environment:      sandbox.EnvironmentLocal,
		SandboxEnabled:   toolCtx.EnableSandbox,
	}
	if toolCtx.Workspace != nil {
		ctx.WorkspaceRoot = toolCtx.Workspace.Root
	}
	for _, dir := range toolCtx.AdditionalWorkingDirectories {
		if strings.TrimSpace(dir) != "" {
			ctx.AdditionalRoots = append(ctx.AdditionalRoots, dir)
		}
	}

	decision, err := t.filesystemPolicy.EvaluatePath(ctx, path, access.AccessRead)
	if err != nil {
		return err
	}
	if decision.Decision == access.DecisionDeny {
		return errors.New(decision.Reason)
	}
	return nil
}

func resolveFilePath(path string, toolCtx tool.ToolUseContext) (string, error) {
	if toolCtx.Workspace != nil {
		return toolCtx.Workspace.Resolve(path)
	}
	workingDir := strings.TrimSpace(toolCtx.WorkingDirectory)
	if filepath.IsAbs(path) || workingDir == "" {
		return path, nil
	}
	return filepath.Join(workingDir, path), nil
}

// IsConcurrencySafe returns whether this tool use can run concurrently.
func (t *Tool) IsConcurrencySafe(input map[string]any) bool {
	_ = input
	return true
}

// IsReadOnly returns whether this tool use is read-only.
func (t *Tool) IsReadOnly(input map[string]any) bool {
	_ = input
	return true
}

// IsEnabled returns whether this tool is currently active.
func (t *Tool) IsEnabled() bool { return true }

// FormatResult serialises the tool output into the tool_result content string.
func (t *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput enriches a shallow clone of the parsed input with derived fields.
func (t *Tool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

// Tool metadata

// ToolName is the name of the file read tool.
const ToolName = "read_file"

// Description is the description of the file read tool.
var Description = fmt.Sprintf(`Read the contents of a file. Supports text files, images, PDFs, Word (DOCX), PowerPoint (PPTX) and Excel (XLSX) documents, and Jupyter notebooks. For large text files, use offset/limit to read specific ranges. A PDF is read page by page with a marker before each page: pages that have a text layer are read directly, and pages that have none (scans) are read with the configured document reader when there is one. A read stops at a size limit and says which page to continue from, so a long PDF is read in several calls with the pages parameter. The result says which pages hold images or have no readable text. WAV and MP3 audio is transcribed only when a document reader is configured.

Reads a file from the local filesystem. You can access any file directly by using this tool. Assume this tool is able to read all files on the machine. If the user provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to %d lines starting from the beginning of the file
- You can optionally specify a line offset and limit (especially handy for long files), but it is recommended to read the whole file by not providing these parameters
- Results are returned using cat -n format, with line numbers starting at 1
- When you already know which part of the file you need, only read that part. This can be important for larger files.
- This tool allows reading images (eg PNG, JPG, etc). When reading an image file the contents are presented visually.
- PDFs: the text layer is read without any service. A configured document reader adds OCR for scanned pages, and keeps tables, headings and layout in the markdown. Without one, a page that has no text layer is reported as having no readable text.
- This tool can read Jupyter notebooks (.ipynb files) and returns all cells with their outputs, combining code, text, and visualizations.
- DOCX, PPTX and XLSX documents are read directly. A configured document reader can add the text of pictures when a document is mostly images, and it is needed to transcribe WAV/MP3 audio files.
- This tool can only read files, not directories. To read a directory, use glob or an ls command via the bash tool.
- You will regularly be asked to read screenshots. If the user provides a path to a screenshot, always use this tool to view the file at the path.
- If you read a file that exists but has empty contents you will receive a warning in place of file contents.`, MaxLinesToRead)

// SearchHint is a hint for tool search functionality.
const SearchHint = "read files from the filesystem"

// MaxLinesToRead is the maximum number of lines to read.
const MaxLinesToRead = 2000
