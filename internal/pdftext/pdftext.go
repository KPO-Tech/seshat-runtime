// Package pdftext reads the text layer of a PDF page as markdown (headings, paragraphs, lists, code, formulas and
// tables), without any external service: most PDFs - reports, exports, invoices - are text-native. See
// PageMarkdown, and PageMarkdownWith for a host that has a layout model to find the tables with no ruling lines.
package pdftext

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// MinCharsPerPage is the threshold below which extracted text is treated as
// "not really there" - a scanned/image-only PDF will still yield a handful
// of stray characters from stamps or embedded metadata, but nowhere near
// this per page on average. Below it, callers should prefer OCR (the
// document reader) or the vision fallback instead of trusting this near-empty text.
const MinCharsPerPage = 20

// Result holds the outcome of a native PDF text extraction attempt.
type Result struct {
	Text      string
	PageCount int
	// Sparse is true when the extracted text is too little relative to the
	// page count to be a real text layer - most likely a scanned PDF.
	Sparse bool
}

// Extract reads a whole PDF's text layer as markdown, page after page. It does not attempt OCR: a scanned PDF
// with no text layer returns a Sparse result with little or no text, not an error - callers decide what to do next.
// A host that wants the pages one at a time, with the tables its layout model finds, uses PageMarkdownWith.
func Extract(data []byte) (*Result, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse PDF: %w", err)
	}

	pageCount := r.NumPage()
	var sb strings.Builder
	for i := 1; i <= pageCount; i++ {
		text, err := PageMarkdown(r.Page(i))
		if err != nil {
			// A single malformed page shouldn't sink extraction for the
			// whole document - skip it and keep going.
			continue
		}
		sb.WriteString(text)
		sb.WriteString("\n\n")
	}

	text := strings.TrimSpace(sb.String())
	return &Result{
		Text:      text,
		PageCount: pageCount,
		Sparse:    pageCount == 0 || len(text) < MinCharsPerPage*pageCount,
	}, nil
}
