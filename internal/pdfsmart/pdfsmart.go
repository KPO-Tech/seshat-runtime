// Package pdfsmart extracts a PDF's text page by page, sending only the
// pages that actually need it through docling instead of the whole
// document - most PDFs are mostly prose, and paying docling's per-page
// layout-detection pass (see its standard PDF pipeline) for every page of
// a large document is wasted work when only a handful of pages have an
// embedded image or an unreadable native text layer.
//
// A page is routed to docling when:
//   - it has at least one embedded raster image (a PDF XObject with
//     /Subtype /Image, detected via pdfcpu's own resource inspection -
//     a deterministic read of the file's structure, not a heuristic or
//     an ML classification), or
//   - its native text layer is too sparse (see pdftext.MinCharsPerPage),
//     or looks like broken font-encoding extraction (see
//     textquality.IsGarbledText).
//
// This deliberately does NOT attempt to detect borderless tables or
// vector-drawn diagrams/charts - those aren't reliably detectable without
// a trained layout model (this is exactly why docling itself runs layout
// detection on every page unconditionally, rather than trying to guess
// which pages need it). A page containing one is only caught here if its
// native text also happens to be sparse or garbled; otherwise it's
// extracted natively, and the caller gets prose-quality text for a page
// that visually contained a table. Callers that can't accept ever missing
// a table this way (e.g. financial documents, invoices) should keep
// sending those documents through docling wholesale instead of this
// package - see Convert's own doc comment for the exact safety contract.
package pdfsmart

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpumodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	"github.com/KPO-Tech/seshat-runtime/internal/pdftext"
	"github.com/KPO-Tech/seshat-runtime/internal/textquality"
)

// pdfcpuConfigWarmup works around a real data race inside pdfcpu itself:
// model.NewDefaultConfiguration() (called internally by api.ExtractImagesRaw
// and api.Trim whenever conf is nil, as it is everywhere in this file)
// lazily populates an unsynchronized package-level cache
// (model.loadedDefaultConfig) on first use - concurrent first calls from
// multiple goroutines race on that write. Once initialized, later calls
// only read the cached value, which is safe to do concurrently. Calling
// NewDefaultConfiguration once, non-concurrently, before any pdfcpu call
// this package makes establishes that happens-before via sync.Once,
// making every subsequent call - however concurrent - safe.
var pdfcpuConfigWarmup sync.Once

func warmUpPDFCPUConfig() {
	pdfcpuConfigWarmup.Do(func() {
		_ = pdfcpumodel.NewDefaultConfiguration()
	})
}

// PageSource identifies how a page's text was obtained.
type PageSource string

const (
	PageSourceNative  PageSource = "native"
	PageSourceDocling PageSource = "docling"
	// PageSourceVision marks a page that needed the vision-LLM fallback -
	// native text extraction was too sparse/absent AND docling was either
	// unavailable or also couldn't produce usable text for it. See
	// VisionFallback's doc comment for how this stage is configured.
	PageSourceVision PageSource = "vision"
)

// PageRenderer renders a single PDF page to a raster image, feeding the
// vision-LLM fallback (see Convert's own doc comment for where this slots
// into the page-processing pipeline). Implementations reuse Phase 1's
// internal/nativedoc/pdfium CGO rendering (internal/nativedoc/parser's
// Converter implements this directly), wired in as an interface exactly
// like documentreader.Converter is, so this package itself never
// depends on CGO or the nativedoc build tag.
type PageRenderer interface {
	// RenderPage renders 1-indexed page pageNum of the PDF in data,
	// returning PNG-encoded image bytes.
	RenderPage(ctx context.Context, data []byte, pageNum int) ([]byte, error)
}

// VisionTranscriber sends a rendered page image to a vision-capable LLM and
// returns its transcription. Implementations must report IsAvailable=false
// (rather than attempting the call and failing) when the configured model
// isn't multimodal - see internal/model.Registry.VisionCapable, the
// capability check used elsewhere in this codebase for the same purpose.
type VisionTranscriber interface {
	IsAvailable(ctx context.Context) bool
	// TranscribePage returns the page's transcribed text/markdown given its
	// PNG-encoded image bytes.
	TranscribePage(ctx context.Context, pngImage []byte) (string, error)
}

// VisionFallback bundles the two collaborators the vision-LLM fallback
// stage needs. Both Renderer and Transcriber must be set (and Transcriber
// must report IsAvailable) for the fallback to actually run on a page - the
// zero value (or either field left nil) simply skips this stage, the same
// as a nil/unavailable docling client skips the docling stage. Passed as
// its own value rather than two more positional Convert parameters so a
// future fallback stage doesn't need another breaking signature change.
type VisionFallback struct {
	Renderer    PageRenderer
	Transcriber VisionTranscriber
}

func (v VisionFallback) usable(ctx context.Context) bool {
	return v.Renderer != nil && v.Transcriber != nil && v.Transcriber.IsAvailable(ctx)
}

// PageResult is one page's contribution to a Result.
type PageResult struct {
	Page             int
	Text             string
	Source           PageSource
	HasEmbeddedImage bool
	// HasBigPicture says the page holds a picture of a million pixels or more (or one whose size cannot be read): what a
	// scanned page is made of. A page with such a picture and little text is a scan with a stamp, not a page of text.
	HasBigPicture bool
	// Pictures are the pictures of the page the text does not say anything about, numbered from 1 on the page. A page read
	// natively has one marker in Text for each of them (see ImageMarker); a page read by an engine has none, since the
	// engine read the pictures.
	Pictures []Picture
	// Deferred is set on a page that needed the engine but was left unread because Options.MaxEnginePages
	// had been used up. It is not a failure: reading the page again later will read it.
	Deferred bool
}

// Result is the outcome of a page-aware PDF-to-markdown conversion.
type Result struct {
	Markdown string
	Pages    []PageResult
	// ModelPagesSkipped counts the pages whose tables the layout models were not asked about because the time
	// budget (Options.ModelBudget) was spent. Their text is read as usual, and so are the tables the rulings show.
	ModelPagesSkipped int
}

// Convert extracts data's text page by page (see package doc for the
// native-vs-docling routing rule per page). A page that still has no usable
// text after docling gets one more attempt via vision, when configured (see
// VisionFallback) - render the page and transcribe it with a vision-capable
// LLM, gated on that model actually reporting vision support (see
// VisionTranscriber.IsAvailable) so this is never attempted against a
// text-only model.
//
// ok is true only when EVERY page produced usable text, whether native, via
// docling, or via the vision fallback - if even one page that needed one of
// those couldn't get it, ok is false and Result should be discarded. This
// is a deliberate safety contract, not an oversight: a partial result
// missing one page's content is exactly the silent, permanent data loss
// this package exists to avoid on the pages it CAN cheaply skip the
// expensive paths for - it must never produce that same failure mode on a
// page it couldn't. Callers should fall back to sending the whole document
// through docling when ok is false, the same as if this package didn't
// exist.
func Convert(ctx context.Context, data []byte, documentReader documentreader.Converter, vision VisionFallback) (Result, bool, error) {
	return ReadPages(ctx, data, nil, Options{ImagePagesNeedEngine: true}, documentReader, vision)
}

// Options tunes how ReadPages routes a page.
type Options struct {
	// ImagePagesNeedEngine sends a page with an embedded image to the engine even when its text layer
	// is fine, which is what Convert does. A reader that lets the caller look at the page itself
	// leaves it false: the text is read natively and the image is only reported.
	ImagePagesNeedEngine bool

	// MaxEnginePages bounds how many pages one call sends to the document reader (zero means no
	// bound). Pages past it are returned with Deferred set. Each engine call is slow, so a caller
	// that wants an answer in bounded time sets it and reads the rest in a later call.
	MaxEnginePages int

	// Password opens a PDF that is protected by a user password. A PDF whose user password is empty needs none.
	Password string

	// ModelBudget bounds the time one call spends asking the layout models about tables. A model looks at one page
	// in seconds on a CPU, and many pages of a long document look as if they might hold a table, so without a bound
	// a book takes minutes for the sake of the few pages that have one. Zero means DefaultModelBudget; a negative
	// value means no bound. Once it is spent the remaining pages are read without the models.
	ModelBudget time.Duration
}

// DefaultModelBudget is the time a call may spend on the layout models when Options.ModelBudget is zero.
const DefaultModelBudget = 20 * time.Second

// TableFinder is a document reader that can also say where the tables with no ruling lines are on a page, and how
// they are laid out, from a layout model. A reader that has this (the native one, with its models) is asked for a page
// whose text looks columnar; the page's own text fills the cells the model finds. pageIndex counts from zero. An
// error, or no tables, leaves the page as it would have been without the model.
type TableFinder interface {
	FindTables(ctx context.Context, pdfData []byte, pageIndex int) ([]pdftext.TableStructure, error)
}

// PageCount is the number of pages of a PDF, without reading any of them.
func PageCount(ctx context.Context, data []byte) (int, error) {
	data, err := Unlock(ctx, data, "")
	if err != nil {
		return 0, err
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("pdfsmart: parse pdf: %w", err)
	}
	if reader.NumPage() == 0 {
		return 0, fmt.Errorf("pdfsmart: pdf has no pages")
	}
	return reader.NumPage(), nil
}

// ReadPages is Convert for a chosen set of 1-indexed pages (nil means every page), so a caller
// that wants three pages of a long document does not pay for the rest. Result.Pages holds the
// requested pages in order; ok is true only when every one of them produced usable text, with the
// same contract as Convert.
func ReadPages(ctx context.Context, data []byte, pages []int, opts Options, documentReader documentreader.Converter, vision VisionFallback) (Result, bool, error) {
	warmUpPDFCPUConfig()

	// An encrypted PDF is read from a decrypted copy, so everything below (the page reader, the image check, the
	// engine, the layout models) sees the same plain file.
	data, err := Unlock(ctx, data, opts.Password)
	if err != nil {
		return Result{}, false, err
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, false, fmt.Errorf("pdfsmart: parse pdf: %w", err)
	}
	pageCount := reader.NumPage()
	if pageCount == 0 {
		return Result{}, false, fmt.Errorf("pdfsmart: pdf has no pages")
	}
	wanted, err := wantedPages(pages, pageCount)
	if err != nil {
		return Result{}, false, err
	}

	var selection []string
	if pages != nil {
		for _, n := range wanted {
			selection = append(selection, strconv.Itoa(n))
		}
	}
	pagesWithImages, largeImages, bigPictures, pagePictures, err := pagesWithEmbeddedImages(ctx, data, selection)
	imageDetectionOK := err == nil
	if err != nil {
		// Can't tell which pages have images - conservatively treat every
		// page as needing docling rather than silently extracting
		// natively past an image detection failed to find.
		pagesWithImages, largeImages, bigPictures, pagePictures = allPages(pageCount), map[int]bool{}, map[int]bool{}, map[int]int{}
	}

	result := Result{Pages: make([]PageResult, 0, len(wanted))}
	// The budget travels with the context: a page sent to the engine is read again by the engine, which may come
	// back here, and the time is one document's.
	budget, inherited := ctx.Value(modelBudgetKey{}).(*modelBudget)
	if !inherited {
		budget = &modelBudget{limit: opts.ModelBudget}
		if budget.limit == 0 {
			budget.limit = DefaultModelBudget
		}
		ctx = context.WithValue(ctx, modelBudgetKey{}, budget)
	}
	skippedBefore := budget.skipped
	var sb strings.Builder
	allOK := true
	enginePages := 0
	// Nothing is configured that could read a page the text layer does not give: no engine and no vision model. A
	// page that holds no text is then kept as it is, with a marker for each of its pictures, instead of failing the
	// document (a cover, a blank page, a full-page picture). An engine that is configured but does not answer is not
	// this case: the document stays unread, to be read again once the engine is back.
	noReader := documentReader == nil && (vision.Renderer == nil || vision.Transcriber == nil)
	pagesRead, pagesLeftToPictures := 0, 0

	for _, i := range wanted {
		needsDocling := opts.ImagePagesNeedEngine && pagesWithImages[i]
		var text, native string
		var pictures []Picture

		// nativeText is what the page's own text layer gives, and whether it is good enough to be the page.
		nativeText := func() (string, bool) {
			page := reader.Page(i)
			if page.V.IsNull() {
				return "", false
			}
			md, extractErr := pdftext.PageMarkdownWith(page, pdftext.PageOptions{Tables: budget.wrap(tableSource(ctx, documentReader, data, i)), LargeImage: largeImages[i]})
			if extractErr != nil || len(strings.TrimSpace(md)) < pdftext.MinCharsPerPage || textquality.IsGarbledText(md) {
				return "", false
			}
			return md, true
		}

		if !needsDocling {
			if md, ok := nativeText(); ok {
				text, native = md, md
			} else {
				needsDocling = true
			}
		}

		source := PageSourceNative
		if needsDocling {
			if documentReader != nil && opts.MaxEnginePages > 0 && enginePages >= opts.MaxEnginePages {
				result.Pages = append(result.Pages, PageResult{Page: i, Source: PageSourceDocling, HasEmbeddedImage: imageDetectionOK && pagesWithImages[i], Deferred: true})
				allOK = false
				continue
			}
			if documentReader != nil {
				enginePages++
			}
			source = PageSourceDocling
			text = ""
			if documentReader != nil && documentReader.IsAvailable(ctx) {
				if pageData, extractErr := extractSinglePage(ctx, data, i); extractErr == nil {
					if conversion, convErr := documentReader.ConvertBytes(ctx, pageData, fmt.Sprintf("page-%d.pdf", i)); convErr == nil &&
						strings.TrimSpace(conversion.Markdown) != "" && !textquality.IsGarbledText(conversion.Markdown) {
						text = conversion.Markdown
					}
				}
			}
			if strings.TrimSpace(text) == "" && vision.usable(ctx) {
				if img, renderErr := vision.Renderer.RenderPage(ctx, data, i); renderErr == nil {
					if transcribed, transErr := vision.Transcriber.TranscribePage(ctx, img); transErr == nil &&
						strings.TrimSpace(transcribed) != "" && !textquality.IsGarbledText(transcribed) {
						text = transcribed
						source = PageSourceVision
					}
				}
			}
			// Nothing read the pictures of this page (no engine, or it could not), but the page has text of its own: keep
			// the text and say where the pictures are, rather than lose the page for the sake of them. A page with no
			// text at all (a scan) is still a failure below.
			if strings.TrimSpace(text) == "" && pagesWithImages[i] {
				if md, ok := nativeText(); ok {
					text, native, source = md, md, PageSourceNative
				}
			}
			if strings.TrimSpace(text) == "" && noReader {
				pagesLeftToPictures++
				source = PageSourceNative
				text, pictures = withImageMarkers("", i, pagePictures[i])
				text = strings.TrimSpace(text)
			} else if strings.TrimSpace(text) == "" {
				allOK = false
			}
		}
		if source == PageSourceNative && native != "" {
			text, pictures = withImageMarkers(native, i, pagePictures[i])
		}
		if native != "" || (strings.TrimSpace(text) != "" && source != PageSourceNative) {
			pagesRead++
		}

		result.Pages = append(result.Pages, PageResult{
			Page:             i,
			Text:             text,
			Source:           source,
			HasEmbeddedImage: imageDetectionOK && pagesWithImages[i],
			HasBigPicture:    bigPictures[i], // empty when the detection failed (see above)
			Pictures:         pictures,
		})
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(text)
		}
	}

	// Pages left to their pictures are not a document: when no page gave any text it is a scan, which needs an engine.
	if pagesLeftToPictures > 0 && pagesRead == 0 {
		allOK = false
	}

	result.Markdown = sb.String()
	result.ModelPagesSkipped = budget.skipped - skippedBefore
	return result, allOK, nil
}

type modelBudgetKey struct{}

// modelBudget spends the time a call may give to the layout models. Pages are read one after the other, so it needs
// no lock.
type modelBudget struct {
	limit   time.Duration
	spent   time.Duration
	skipped int
}

// wrap makes a table source stop answering once the budget is spent.
func (b *modelBudget) wrap(source pdftext.TableSource) pdftext.TableSource {
	if source == nil || b.limit < 0 {
		return source
	}
	return func() ([]pdftext.TableStructure, error) {
		if b.spent >= b.limit {
			b.skipped++
			return nil, nil
		}
		start := time.Now()
		defer func() { b.spent += time.Since(start) }()
		return source()
	}
}

// tableSource gives a page what it needs to ask the reader about its tables, or nothing when the reader cannot say.
func tableSource(ctx context.Context, reader documentreader.Converter, data []byte, page int) pdftext.TableSource {
	finder, ok := reader.(TableFinder)
	if !ok || finder == nil {
		return nil
	}
	return func() ([]pdftext.TableStructure, error) { return finder.FindTables(ctx, data, page-1) }
}

// wantedPages returns the requested pages sorted and de-duplicated, or every page when none are given.
func wantedPages(pages []int, pageCount int) ([]int, error) {
	if pages == nil {
		return allPageList(pageCount), nil
	}
	seen := make(map[int]bool, len(pages))
	wanted := make([]int, 0, len(pages))
	for _, n := range pages {
		if n < 1 || n > pageCount {
			return nil, fmt.Errorf("pdfsmart: page %d is out of range (the PDF has %d pages)", n, pageCount)
		}
		if !seen[n] {
			seen[n] = true
			wanted = append(wanted, n)
		}
	}
	sort.Ints(wanted)
	return wanted, nil
}

func allPageList(n int) []int {
	list := make([]int, n)
	for i := range list {
		list[i] = i + 1
	}
	return list
}

func allPages(n int) map[int]bool {
	m := make(map[int]bool, n)
	for i := 1; i <= n; i++ {
		m[i] = true
	}
	return m
}

// pagesWithEmbeddedImages reports which 1-indexed pages have at least one
// embedded raster image (a PDF XObject with /Subtype /Image), via
// pdfcpu's own resource inspection - a deterministic read of the file's
// structure, not a heuristic or an ML classification. large says which of them hold one big enough to be a table, and
// pictures counts, for each page, the pictures worth a marker in its text (see withImageMarkers).
func pagesWithEmbeddedImages(ctx context.Context, data []byte, selectedPages []string) (pages, large, big map[int]bool, pictures map[int]int, err error) {
	imageSets, err := api.ExtractImagesRaw(ctx, bytes.NewReader(data), selectedPages, nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	pages, large, big, pictures = make(map[int]bool), make(map[int]bool), make(map[int]bool), make(map[int]int)
	objects := make(map[int]map[int]int) // object number -> page -> how many times the page uses it
	for _, set := range imageSets {
		for _, img := range set {
			if img.PageNr <= 0 {
				continue
			}
			width, height, known := pictureSize(img)
			pages[img.PageNr] = true
			if !known || (width >= tableImageMinWidth && height >= tableImageMinHeight) {
				large[img.PageNr] = true
			}
			if !known || width*height >= bigPictureMinPixels {
				big[img.PageNr] = true
			}
			if known && (width < markerImageMinSide || height < markerImageMinSide) {
				continue
			}
			if objects[img.ObjNr] == nil {
				objects[img.ObjNr] = make(map[int]int)
			}
			objects[img.ObjNr][img.PageNr]++
		}
	}
	for _, onPages := range objects {
		// The same picture on three pages or more is a logo, a letterhead or a background, not a figure.
		if len(onPages) >= repeatedPictureMinPages {
			continue
		}
		for page, n := range onPages {
			pictures[page] += n
		}
	}
	return pages, large, big, pictures, nil
}

// largeEnough says whether a picture is big enough to hold a table. pdfcpu does not fill in the size of the pictures it
// extracts raw, so it is read from the picture's own header; one whose format cannot be read (a scan's CCITT or JBIG2
// image) counts as large, because the cost of a wrong yes is only a look by the models.
func largeEnough(img pdfcpumodel.Image) bool {
	width, height, known := pictureSize(img)
	return !known || (width >= tableImageMinWidth && height >= tableImageMinHeight)
}

// pictureSize is the size of a picture in pixels, and false when it cannot be read.
func pictureSize(img pdfcpumodel.Image) (width, height int, known bool) {
	width, height = img.Width, img.Height
	if width != 0 && height != 0 {
		return width, height, true
	}
	if img.Reader == nil {
		return 0, 0, false
	}
	config, _, err := image.DecodeConfig(img.Reader)
	if err != nil {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

// Pictures smaller than this, in pixels, are a logo, an icon or a rule; a table in a picture is bigger. The second
// result of pagesWithEmbeddedImages says which pages hold one at least this big.
const (
	tableImageMinWidth  = 240
	tableImageMinHeight = 80
)

// bigPictureMinPixels is the size, in pixels, from which a picture can be a whole scanned page rather than a figure in one.
const bigPictureMinPixels = 1_000_000

// extractSinglePage produces a standalone one-page PDF containing just
// page n, so documentReader only has to process that one page instead of
// the whole document.
func extractSinglePage(ctx context.Context, data []byte, n int) ([]byte, error) {
	var buf bytes.Buffer
	if err := api.Trim(ctx, bytes.NewReader(data), &buf, []string{strconv.Itoa(n)}, nil); err != nil {
		return nil, fmt.Errorf("pdfsmart: extract page %d: %w", n, err)
	}
	return buf.Bytes(), nil
}
