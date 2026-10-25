//go:build cgo && nativedoc

// Package parser assembles seshat's native document primitives - pdf_oxide
// (text extraction), pdfium (page rendering, see nativedoc/pdfium's own doc
// comment for why rendering is split from pdf_oxide), nativedoc
// (OCR/layout/table-structure ONNX models), and internal/officetext (DOCX,
// XLSX) - into a real documentreader.Converter implementation (see
// Converter in converter.go). This is what makes seshat's document-reading
// tools work with no external service (docling-serve, seshat-intelligence)
// configured, not just a validation harness.
//
// This is original orchestration code, not ported from RAGFlow - RAGFlow's
// own equivalent (internal/deepdoc/parser/) does considerably more
// (layout-aware reading order via DLA, table extraction via TSR, image
// extraction) than the deliberately-scoped v1 here. See
// docs/issues/document-intelligence-roadmap.md for what's intentionally
// not built yet.
package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"

	pdfoxide "github.com/yfedoseev/pdf_oxide/go"

	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	"github.com/KPO-Tech/seshat-runtime/internal/nativedoc"
	"github.com/KPO-Tech/seshat-runtime/internal/nativedoc/pdfium"
	"github.com/KPO-Tech/seshat-runtime/internal/pdfsmart"
	"github.com/KPO-Tech/seshat-runtime/internal/pdftext"
)

// convertPDF converts a PDF. Per page: native text extraction (pdf_oxide)
// is used when the page has a real text layer; otherwise the page is
// rendered (pdfium) and read via OCR (nativedoc's det/rec models). Reading
// order for OCR'd pages is a simple top-to-bottom, left-to-right sort of
// detected text boxes - correct for single-column pages, not yet
// layout-aware for multi-column ones (that needs DLA-based column/region
// detection, not built here yet).
func (c *Converter) convertPDF(ctx context.Context, data []byte, filename string) (*documentreader.ConversionResult, error) {
	doc, err := pdfoxide.OpenFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("nativedoc/parser: open %s: %w", filename, err)
	}
	defer doc.Close()

	count, err := doc.PageCount()
	if err != nil {
		return nil, fmt.Errorf("nativedoc/parser: page count for %s: %w", filename, err)
	}

	// A page with a text layer is written the way the Read tool writes it: markdown, with its ruled tables, the ones
	// the models find, and the ones that are pictures. The reader is given finderOnly, which is not asked to convert
	// a page the reader cannot read: that is left to the OCR below.
	read, _, readErr := pdfsmart.ReadPages(ctx, data, nil, pdfsmart.Options{}, finderOnly{c}, pdfsmart.VisionFallback{})
	native := map[int]string{}
	bigPicture := map[int]bool{}
	coverage := map[int]float64{}
	if readErr == nil {
		for _, p := range read.Pages {
			if p.Source == pdfsmart.PageSourceNative && strings.TrimSpace(p.Text) != "" {
				native[p.Page-1] = strings.TrimSpace(p.Text)
			}
			bigPicture[p.Page-1] = p.HasBigPicture
			coverage[p.Page-1] = p.ImageCoverage
		}
	}

	pages := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, textErr := doc.ExtractText(i)
		chars := len(strings.TrimSpace(text))
		// A real text layer, by the plain text's own measure (a page with a few words over a picture is not one), and not
		// a scan that carries a stamp or a header: its content is in the picture, which the text layer does not read.
		if textErr == nil && chars >= c.minCharsPerPage() && !isScannedPage(chars, bigPicture[i], coverage[i]) {
			if md, ok := native[i]; ok {
				text = md
			}
			pages = append(pages, strings.TrimSpace(text))
			continue
		}
		ocrText, ocrErr := c.ocrPDFPage(ctx, data, i)
		if ocrErr != nil {
			return nil, fmt.Errorf("nativedoc/parser: page %d of %s has no usable native text and OCR failed: %w", i, filename, ocrErr)
		}
		if textErr == nil && chars > 0 {
			// What the text layer says is exact: keep the lines the OCR did not give.
			layer := text
			if md, ok := native[i]; ok {
				layer = md
			}
			ocrText = withNativeLines(ocrText, layer)
		}
		pages = append(pages, ocrText)
	}

	return &documentreader.ConversionResult{
		Markdown:  strings.Join(pages, "\n\n"),
		PageCount: count,
	}, nil
}

func (c *Converter) ocrPDFPage(ctx context.Context, pdfData []byte, pageIdx int) (string, error) {
	rgba, err := pdfium.RenderPage(pdfData, pageIdx, c.renderDPI())
	if err != nil {
		return "", fmt.Errorf("render page: %w", err)
	}
	img, err := nativedoc.FromImage(rgba)
	if err != nil {
		return "", fmt.Errorf("convert rendered page: %w", err)
	}

	det, err := nativedoc.RunDet(ctx, c.ModelDir, img)
	if err != nil {
		return "", fmt.Errorf("detect text: %w", err)
	}
	boxes := append([]nativedoc.DetBox(nil), det.Boxes...)
	sortReadingOrder(boxes)

	recs := c.recognizeBoxes(ctx, img, boxes)
	var items []pageItem
	for i, box := range boxes {
		if strings.TrimSpace(recs[i].Text) == "" {
			continue
		}
		x0, y0, x1, y1 := boxBounds(box)
		items = append(items, pageItem{x0: x0, y0: y0, x1: x1, y1: y1, text: recs[i].Text})
	}
	items = c.withTables(ctx, pdfData, pageIdx, img, items)

	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, it.text)
	}
	return strings.Join(lines, "\n"), nil
}

// pageItem is a line read from a page image, or a table (already written as markdown) that replaces the lines it
// covers, with its box in pixels.
type pageItem struct {
	x0, y0, x1, y1 float32
	text           string
}

func boxBounds(box nativedoc.DetBox) (x0, y0, x1, y1 float32) {
	x0, y0, x1, y1 = box.Pts[0][0], box.Pts[0][1], box.Pts[0][0], box.Pts[0][1]
	for _, p := range box.Pts {
		x0, x1 = min(x0, p[0]), max(x1, p[0])
		y0, y1 = min(y0, p[1]), max(y1, p[1])
	}
	return x0, y0, x1, y1
}

// withTables finds the tables on a page image and replaces the lines of text inside each with one markdown table,
// built from those lines (the picture has already been read: no second OCR) and the table's structure. Lines that
// are not in a table stay as they are, in the same order; the table takes the place of its first line. Without the
// layout models, or when they fail, the lines come back unchanged.
func (c *Converter) withTables(ctx context.Context, pdfData []byte, pageIdx int, img *nativedoc.Image, items []pageItem) []pageItem {
	if len(items) < 4 {
		return items
	}
	layouts, err := nativedoc.FindTables(ctx, c.ModelDir, img)
	if err != nil || len(layouts) == 0 {
		return items
	}
	width, height, err := pdfium.PageSize(pdfData, pageIdx)
	if err != nil || width <= 0 {
		return items
	}
	scale := float64(img.W) / width
	pts := func(b [4]float32) [4]float64 {
		return [4]float64{float64(b[0]) / scale, float64(b[1]) / scale, float64(b[2]) / scale, float64(b[3]) / scale}
	}

	replaced := map[int]string{} // index of a table's first line -> the table
	covered := map[int]bool{}
	for _, l := range layouts {
		whole := pts([4]float32{l.X0, l.Y0, l.X1, l.Y1})
		s := pdftext.TableStructure{X0: whole[0], Top: whole[1], X1: whole[2], Bottom: whole[3]}
		for _, b := range l.Columns {
			s.Columns = append(s.Columns, pts(b))
		}
		for _, b := range l.Rows {
			s.Rows = append(s.Rows, pts(b))
		}
		for _, b := range l.Headers {
			s.Headers = append(s.Headers, pts(b))
		}
		var inside []int
		var words []pdftext.TableWord
		for i, it := range items {
			cx, cy := (it.x0+it.x1)/2, (it.y0+it.y1)/2
			if covered[i] || cx < l.X0 || cx > l.X1 || cy < l.Y0 || cy > l.Y1 {
				continue
			}
			inside = append(inside, i)
			b := pts([4]float32{it.x0, it.y0, it.x1, it.y1})
			words = append(words, pdftext.TableWord{X0: b[0], Top: b[1], X1: b[2], Bottom: b[3], Text: it.text})
		}
		if len(inside) < 4 {
			continue
		}
		md, ok := pdftext.TableMarkdown(s, words, height)
		if !ok {
			continue
		}
		for _, i := range inside {
			covered[i] = true
		}
		replaced[inside[0]] = md
	}
	if len(replaced) == 0 {
		return items
	}
	out := make([]pageItem, 0, len(items))
	for i, it := range items {
		if md, ok := replaced[i]; ok {
			out = append(out, pageItem{text: "\n" + md + "\n"})
		}
		if !covered[i] {
			out = append(out, it)
		}
	}
	return out
}

// sortReadingOrder orders boxes top-to-bottom then left-to-right, using
// each box's top edge and left edge (Pts[0] is top-left, clockwise - see
// DetBox's own doc comment). Correct for single-column text; a multi-column
// layout needs DLA-based column detection to interleave correctly, which
// this package doesn't do yet.
func sortReadingOrder(boxes []nativedoc.DetBox) {
	sort.Slice(boxes, func(i, j int) bool {
		yi, yj := boxes[i].Pts[0][1], boxes[j].Pts[0][1]
		if yi != yj {
			return yi < yj
		}
		return boxes[i].Pts[0][0] < boxes[j].Pts[0][0]
	})
}

// cropBox extracts the axis-aligned bounding rectangle of a detected box
// from the source image. DetBox quads from this document type are
// typically already axis-aligned (horizontal text); a tilted quad (a page
// scanned crooked) is straightened by warpBox instead.
func cropBox(img *nativedoc.Image, box nativedoc.DetBox) *nativedoc.Image {
	if isTilted(box) {
		if warped, ok := warpBox(img, box); ok {
			return warped
		}
	}
	minX, minY := box.Pts[0][0], box.Pts[0][1]
	maxX, maxY := box.Pts[0][0], box.Pts[0][1]
	for _, p := range box.Pts {
		if p[0] < minX {
			minX = p[0]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	x0, y0 := int(minX), int(minY)
	x1, y1 := int(maxX), int(maxY)
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > img.W {
		x1 = img.W
	}
	if y1 > img.H {
		y1 = img.H
	}
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return &nativedoc.Image{W: 1, H: 1, Pix: make([]byte, 3)}
	}
	pix := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		srcOff := ((y0+y)*img.W + x0) * 3
		dstOff := y * w * 3
		copy(pix[dstOff:dstOff+w*3], img.Pix[srcOff:srcOff+w*3])
	}
	return &nativedoc.Image{W: w, H: h, Pix: pix}
}

// finderOnly is the converter as the page reader sees it when it is the one calling: it can find tables but is not
// available to convert a page, so the page reader does not call back into the conversion it is part of.
type finderOnly struct{ c *Converter }

func (f finderOnly) IsAvailable(context.Context) bool { return false }

func (f finderOnly) ConvertBytes(context.Context, []byte, string) (*documentreader.ConversionResult, error) {
	return nil, fmt.Errorf("nativedoc/parser: not a converter")
}

func (f finderOnly) ConvertFile(context.Context, string) (*documentreader.ConversionResult, error) {
	return nil, fmt.Errorf("nativedoc/parser: not a converter")
}

func (f finderOnly) ConvertURL(context.Context, string) (*documentreader.ConversionResult, error) {
	return nil, fmt.Errorf("nativedoc/parser: not a converter")
}

func (f finderOnly) FindTables(ctx context.Context, data []byte, page int) ([]pdftext.TableStructure, error) {
	return f.c.FindTables(ctx, data, page)
}

// recBatchSize is how many lines the recognizer reads in one model run.
const recBatchSize = 6

// recognizeBoxes reads the line of every box, several lines per model run. The lines are grouped by width so that a batch is not
// padded out to its widest line (the recognizer sizes a batch to its widest member); a batch that fails is read line by line.
// The result is in the order of boxes.
func (c *Converter) recognizeBoxes(ctx context.Context, img *nativedoc.Image, boxes []nativedoc.DetBox) []nativedoc.OCRRecResult {
	crops := make([]*nativedoc.Image, len(boxes))
	order := make([]int, len(boxes))
	for i, box := range boxes {
		crops[i] = cropBox(img, box)
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ca, cb := crops[order[a]], crops[order[b]]
		return float64(ca.W)*float64(cb.H) < float64(cb.W)*float64(ca.H)
	})
	results := make([]nativedoc.OCRRecResult, len(boxes))
	for start := 0; start < len(order); start += recBatchSize {
		group := order[start:min(start+recBatchSize, len(order))]
		batch := make([]*nativedoc.Image, len(group))
		for i, idx := range group {
			batch[i] = crops[idx]
		}
		recs, err := nativedoc.RunOCRRecBatchReal(ctx, c.ModelDir, batch)
		if err != nil || len(recs) != len(group) {
			for _, idx := range group {
				if rec, recErr := nativedoc.RunOCRRec(ctx, c.ModelDir, crops[idx]); recErr == nil {
					results[idx] = rec
				}
			}
			continue
		}
		for i, idx := range group {
			results[idx] = recs[i]
		}
	}
	return results
}
