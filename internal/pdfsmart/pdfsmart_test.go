package pdfsmart

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	"github.com/KPO-Tech/seshat-runtime/internal/pdftext"
)

// Fixtures are copied from internal/pdftext's own testdata (already
// validated there): text_layer.pdf is a real multi-element text-layer PDF
// with no embedded images; scanned.pdf is a real pdfcpu-built PDF whose
// single page is nothing but an embedded screenshot image - exactly the
// "has an embedded image XObject" case this package routes to documentreader.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "pdftext", "testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return data
}

func TestConvert_TextOnlyPDFExtractsNativelyWithNilDoclingClient(t *testing.T) {
	t.Parallel()
	result, ok, err := Convert(context.Background(), readTestdata(t, "text_layer.pdf"), nil, VisionFallback{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !ok {
		t.Fatalf("expected a text-only PDF (no embedded images) to succeed with no docling client, got pages: %+v", result.Pages)
	}
	if !strings.Contains(result.Markdown, "Sample Report") {
		t.Fatalf("expected extracted content, got:\n%s", result.Markdown)
	}
	for _, p := range result.Pages {
		if p.Source != PageSourceNative {
			t.Errorf("expected every page to be extracted natively, page %d got source %q", p.Page, p.Source)
		}
	}
	if pagesBy(result, PageSourceDocling) != 0 {
		t.Errorf("expected 0 pages routed to docling, got %d", pagesBy(result, PageSourceDocling))
	}
}

func TestConvert_ImagePDFFailsCleanlyWithNilDoclingClient(t *testing.T) {
	t.Parallel()
	_, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), nil, VisionFallback{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	// The page has an embedded image, so it needs docling - with no
	// client available, ok must be false (the safety contract: never
	// return a result missing a page docling would have covered).
	if ok {
		t.Fatal("expected ok=false when a page needing docling has no docling client to use")
	}
}

func TestConvert_ImagePDFUsesDoclingWhenAvailable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/convert/file":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success","document":{"md_content":"# This came from docling for this page","pages":[1]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := documentreader.NewDoclingClient(server.URL)
	result, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), client, VisionFallback{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !ok {
		t.Fatalf("expected success once docling is available for the image page, got pages: %+v", result.Pages)
	}
	if !strings.Contains(result.Markdown, "This came from docling for this page") {
		t.Fatalf("expected docling's markdown in the result, got:\n%s", result.Markdown)
	}
	if pagesBy(result, PageSourceDocling) == 0 {
		t.Error("expected at least one page to be routed to docling for an image-only PDF")
	}
}

func TestConvert_GarbledDoclingOutputForAnImagePageFailsCleanly(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/convert/file":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success","document":{"md_content":"(cid:12)(cid:47)(cid:8)(cid:91)","pages":[1]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := documentreader.NewDoclingClient(server.URL)
	_, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), client, VisionFallback{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if ok {
		t.Fatal("expected garbled docling output on the only page to be rejected, not accepted as success")
	}
}

// fakeRenderer returns a fixed, non-empty byte slice for any page - Convert
// never inspects the bytes themselves, only passes them to the transcriber.
type fakeRenderer struct{ err error }

func (r fakeRenderer) RenderPage(_ context.Context, _ []byte, pageNum int) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []byte(fmt.Sprintf("fake-png-page-%d", pageNum)), nil
}

// fakeTranscriber returns a fixed transcription, or simulates being
// unavailable/erroring, depending on its fields.
type fakeTranscriber struct {
	available    bool
	transcribed  string
	err          error
	transcribeFn func(pngImage []byte) (string, error)
}

func (t fakeTranscriber) IsAvailable(context.Context) bool { return t.available }

func (t fakeTranscriber) TranscribePage(_ context.Context, pngImage []byte) (string, error) {
	if t.transcribeFn != nil {
		return t.transcribeFn(pngImage)
	}
	if t.err != nil {
		return "", t.err
	}
	return t.transcribed, nil
}

func TestConvert_VisionFallbackUsedWhenDoclingUnavailable(t *testing.T) {
	t.Parallel()
	vision := VisionFallback{
		Renderer:    fakeRenderer{},
		Transcriber: fakeTranscriber{available: true, transcribed: "# Transcribed by vision"},
	}
	result, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), nil, vision)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !ok {
		t.Fatalf("expected success via the vision fallback, got pages: %+v", result.Pages)
	}
	if !strings.Contains(result.Markdown, "Transcribed by vision") {
		t.Fatalf("expected vision's transcription in the result, got:\n%s", result.Markdown)
	}
	if pagesBy(result, PageSourceVision) == 0 {
		t.Error("expected at least one page to be routed to vision for an image-only PDF with no docling client")
	}
	for _, p := range result.Pages {
		if p.Source != PageSourceVision {
			t.Errorf("expected page %d to be sourced from vision, got %q", p.Page, p.Source)
		}
	}
}

func TestConvert_VisionFallbackTriedAfterDoclingFails(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/convert/file":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := documentreader.NewDoclingClient(server.URL)
	vision := VisionFallback{
		Renderer:    fakeRenderer{},
		Transcriber: fakeTranscriber{available: true, transcribed: "recovered via vision"},
	}
	result, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), client, vision)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !ok {
		t.Fatalf("expected the vision fallback to recover the page after docling failed, got pages: %+v", result.Pages)
	}
	if !strings.Contains(result.Markdown, "recovered via vision") {
		t.Fatalf("expected vision's transcription in the result, got:\n%s", result.Markdown)
	}
}

func TestConvert_VisionFallbackSkippedWhenTranscriberUnavailable(t *testing.T) {
	t.Parallel()
	vision := VisionFallback{
		Renderer:    fakeRenderer{},
		Transcriber: fakeTranscriber{available: false, transcribed: "should never be used"},
	}
	_, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), nil, vision)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when the vision transcriber reports itself unavailable (e.g. a text-only model)")
	}
}

func TestConvert_GarbledVisionOutputFailsCleanly(t *testing.T) {
	t.Parallel()
	vision := VisionFallback{
		Renderer:    fakeRenderer{},
		Transcriber: fakeTranscriber{available: true, transcribed: "(cid:12)(cid:47)(cid:8)(cid:91)"},
	}
	_, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), nil, vision)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if ok {
		t.Fatal("expected garbled vision output on the only page to be rejected, not accepted as success")
	}
}

func TestConvert_VisionRendererErrorFailsCleanly(t *testing.T) {
	t.Parallel()
	vision := VisionFallback{
		Renderer:    fakeRenderer{err: fmt.Errorf("simulated render failure")},
		Transcriber: fakeTranscriber{available: true, transcribed: "should never be reached"},
	}
	_, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), nil, vision)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when the page renderer errors")
	}
}

// pdfcpu fills a package-level default configuration lazily and without a lock, so the first calls
// must not run concurrently. Doing one here, before any parallel test starts, is what the production
// code does with its own sync.Once.
func init() { _ = model.NewDefaultConfiguration() }

// -- Analyze and ReadPages ---------------------------------------------------------------------

func threePages(t *testing.T) []byte { return readTestdata(t, "three_pages.pdf") }

// textThenScan is a two-page PDF: a page with a real text layer, then a page that is only an image.
func textThenScan(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	inputs := []io.ReadSeeker{bytes.NewReader(readTestdata(t, "text_layer.pdf")), bytes.NewReader(readTestdata(t, "scanned.pdf"))}
	if err := api.MergeRaw(context.Background(), inputs, &out, false, nil); err != nil {
		t.Fatalf("merge fixtures: %v", err)
	}
	return out.Bytes()
}

type countingConverter struct {
	markdown string
	calls    []string
}

func (c *countingConverter) IsAvailable(context.Context) bool { return true }
func (c *countingConverter) ConvertFile(context.Context, string) (*documentreader.ConversionResult, error) {
	return &documentreader.ConversionResult{Markdown: c.markdown}, nil
}
func (c *countingConverter) ConvertBytes(_ context.Context, _ []byte, filename string) (*documentreader.ConversionResult, error) {
	c.calls = append(c.calls, filename)
	return &documentreader.ConversionResult{Markdown: c.markdown}, nil
}
func (c *countingConverter) ConvertURL(context.Context, string) (*documentreader.ConversionResult, error) {
	return nil, fmt.Errorf("not supported")
}

func TestPageCount(t *testing.T) {
	t.Parallel()
	if n, err := PageCount(context.Background(), threePages(t)); err != nil || n != 3 {
		t.Fatalf("PageCount = %d, %v, want 3", n, err)
	}
	if _, err := PageCount(context.Background(), []byte("not a pdf")); err == nil {
		t.Fatal("a file that is not a PDF should be an error")
	}
}

func TestReadPages_PagesPastTheEngineBudgetAreDeferredNotFailed(t *testing.T) {
	t.Parallel()
	scan := readTestdata(t, "scanned.pdf")
	var out bytes.Buffer
	if err := api.MergeRaw(context.Background(), []io.ReadSeeker{bytes.NewReader(scan), bytes.NewReader(scan), bytes.NewReader(scan)}, &out, false, nil); err != nil {
		t.Fatal(err)
	}
	engine := &countingConverter{markdown: "Engine text."}
	result, ok, err := ReadPages(context.Background(), out.Bytes(), nil, Options{MaxEnginePages: 2}, engine, VisionFallback{})
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(engine.calls) != 2 {
		t.Fatalf("want 2 engine calls and ok=false, got %d calls ok=%v", len(engine.calls), ok)
	}
	if result.Pages[0].Deferred || result.Pages[1].Deferred || !result.Pages[2].Deferred {
		t.Fatalf("only page 3 should be deferred: %+v", result.Pages)
	}
	if result.Pages[2].Text != "" {
		t.Fatalf("a deferred page carries no text: %+v", result.Pages[2])
	}
}

func TestReadPages_ReadsOnlyTheRequestedPages(t *testing.T) {
	t.Parallel()
	result, ok, err := ReadPages(context.Background(), threePages(t), []int{3, 1}, Options{}, nil, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ReadPages: ok=%v err=%v", ok, err)
	}
	if len(result.Pages) != 2 || result.Pages[0].Page != 1 || result.Pages[1].Page != 3 {
		t.Fatalf("want pages 1 and 3 in order, got %+v", result.Pages)
	}
	if !strings.Contains(result.Pages[0].Text, "Alpha") || !strings.Contains(result.Pages[1].Text, "Charlie") || strings.Contains(result.Markdown, "Bravo") {
		t.Fatalf("wrong pages read:\n%s", result.Markdown)
	}
}

func TestReadPages_RejectsAPageOutsideTheDocument(t *testing.T) {
	t.Parallel()
	if _, _, err := ReadPages(context.Background(), threePages(t), []int{4}, Options{}, nil, VisionFallback{}); err == nil {
		t.Fatal("page 4 of a 3-page PDF should be an error")
	}
	if _, _, err := ReadPages(context.Background(), threePages(t), []int{0}, Options{}, nil, VisionFallback{}); err == nil {
		t.Fatal("page 0 should be an error")
	}
}

func TestReadPages_OnlyAPageWithoutTextGoesToTheEngine(t *testing.T) {
	t.Parallel()
	engine := &countingConverter{markdown: "Scanned page text read by the engine."}
	result, ok, err := ReadPages(context.Background(), textThenScan(t), nil, Options{}, engine, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ReadPages: ok=%v err=%v", ok, err)
	}
	if len(engine.calls) != 1 || engine.calls[0] != "page-2.pdf" {
		t.Fatalf("only page 2 should reach the engine, got %v", engine.calls)
	}
	if result.Pages[0].Source != PageSourceNative || result.Pages[1].Source != PageSourceDocling {
		t.Fatalf("unexpected sources: %+v", result.Pages)
	}
}

func TestReadPages_APageWithTextIsNotSentToTheEngine(t *testing.T) {
	t.Parallel()
	engine := &countingConverter{markdown: "Engine text."}
	if _, _, err := ReadPages(context.Background(), textThenScan(t), []int{1}, Options{ImagePagesNeedEngine: true}, engine, VisionFallback{}); err != nil {
		t.Fatal(err)
	}
	if len(engine.calls) != 0 {
		t.Fatalf("page 1 has text and no image, the engine must not be called, got %v", engine.calls)
	}
}

// tableReader is a document reader that also knows where the tables with no ruling lines are, as the native one
// does with its models.
type tableReader struct {
	countingConverter
	asked  []int
	tables []pdftext.TableStructure
	err    error
}

func (r *tableReader) FindTables(_ context.Context, _ []byte, pageIndex int) ([]pdftext.TableStructure, error) {
	r.asked = append(r.asked, pageIndex)
	return r.tables, r.err
}

func TestReadPages_AReaderThatFindsTablesFillsThemFromThePageText(t *testing.T) {
	t.Parallel()
	data := readTestdata(t, "borderless.pdf")
	reader := &tableReader{tables: []pdftext.TableStructure{{
		X0: 55, Top: 140, X1: 510, Bottom: 268,
		Columns: [][4]float64{{55, 140, 230, 268}, {243, 140, 300, 268}, {312, 140, 372, 268}, {383, 140, 442, 268}, {453, 140, 510, 268}},
		Rows:    [][4]float64{{55, 140, 510, 155}, {55, 162, 510, 177}, {55, 182, 510, 197}, {55, 202, 510, 230}, {55, 233, 510, 248}, {55, 253, 510, 268}},
		Headers: [][4]float64{{55, 140, 510, 155}},
	}}}

	result, ok, err := ReadPages(context.Background(), data, nil, Options{}, reader, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(result.Markdown, "| Product line | Q1 | Q2 | Q3 | Q4 |") || !strings.Contains(result.Markdown, "| Support | 30 | 31 | 33 | 34 |") {
		t.Fatalf("the table was not read:\n%s", result.Markdown)
	}
	// Only the page whose text is columnar is put to the models, and the page index counts from zero.
	if len(reader.asked) != 1 || reader.asked[0] != 0 {
		t.Fatalf("the reader was asked about pages %v, want [0]", reader.asked)
	}
	if len(reader.calls) != 0 {
		t.Errorf("a page with a text layer must not be sent to the engine: %v", reader.calls)
	}
}

func TestReadPages_AReaderWhoseTableSearchFailsStillReadsThePage(t *testing.T) {
	t.Parallel()
	data := readTestdata(t, "borderless.pdf")
	reader := &tableReader{err: fmt.Errorf("models are not installed")}
	result, ok, err := ReadPages(context.Background(), data, []int{1}, Options{}, reader, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(result.Markdown, "Hardware 120 135 128 141") {
		t.Fatalf("the page text is missing:\n%s", result.Markdown)
	}
}

func TestOnlyAPictureBigEnoughForATableIsLarge(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "pdftext", "testdata", "table_image.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	pages, large, _, _, err := pagesWithEmbeddedImages(context.Background(), data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !pages[1] || !large[1] {
		t.Errorf("the pasted table is a large picture: pages=%v large=%v", pages, large)
	}
	if largeEnough(model.Image{Width: 64, Height: 64}) {
		t.Error("an icon is not large")
	}
	if !largeEnough(model.Image{Width: 800, Height: 400}) {
		t.Error("a screenshot is large")
	}
}

func TestOnlyAMillionPixelPictureIsBig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "pdftext", "testdata", "table_image.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	imageSets, err := api.ExtractImagesRaw(context.Background(), bytes.NewReader(data), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pixels int
	for _, set := range imageSets {
		for _, img := range set {
			if w, h, known := pictureSize(img); known && w*h > pixels {
				pixels = w * h
			}
		}
	}
	_, _, big, _, err := pagesWithEmbeddedImages(context.Background(), data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := pixels >= bigPictureMinPixels; big[1] != want {
		t.Errorf("a picture of %d pixels: big=%v, want %v", pixels, big[1], want)
	}
	if pixels >= bigPictureMinPixels {
		t.Skipf("the pasted table is %d pixels, so only the big side is covered here", pixels)
	}
}

func TestTheModelsStopBeingAskedOnceTheBudgetIsSpent(t *testing.T) {
	t.Parallel()
	asked := 0
	slow := func() ([]pdftext.TableStructure, error) {
		asked++
		time.Sleep(15 * time.Millisecond)
		return nil, nil
	}
	budget := &modelBudget{limit: 10 * time.Millisecond}
	source := budget.wrap(slow)
	for i := 0; i < 4; i++ {
		if _, err := source(); err != nil {
			t.Fatal(err)
		}
	}
	if asked != 1 || budget.skipped != 3 {
		t.Fatalf("asked=%d skipped=%d, want the first page asked and the next three skipped", asked, budget.skipped)
	}

	unbounded := &modelBudget{limit: -1}
	asked = 0
	source = unbounded.wrap(slow)
	for i := 0; i < 3; i++ {
		_, _ = source()
	}
	if asked != 3 || unbounded.skipped != 0 {
		t.Fatalf("a negative budget is no bound: asked=%d skipped=%d", asked, unbounded.skipped)
	}
	if (&modelBudget{limit: time.Second}).wrap(nil) != nil {
		t.Error("no source stays no source")
	}
}

func TestReadPagesReportsNothingSkippedWhenTheBudgetIsEnough(t *testing.T) {
	t.Parallel()
	data := readTestdata(t, "borderless.pdf")
	reader := &tableReader{}
	result, ok, err := ReadPages(context.Background(), data, nil, Options{}, reader, VisionFallback{})
	if err != nil || !ok || result.ModelPagesSkipped != 0 {
		t.Fatalf("ok=%v err=%v skipped=%d", ok, err, result.ModelPagesSkipped)
	}
}

// pagesBy counts the pages of a result that were read by one source.
func pagesBy(result Result, source PageSource) int {
	n := 0
	for _, p := range result.Pages {
		if p.Source == source {
			n++
		}
	}
	return n
}

// A document read through the engine comes back into ReadPages for each page it hands over; the time spent on the
// layout models is one budget for the whole document, carried by the context.
func TestAnExhaustedModelBudgetInTheContextIsRespected(t *testing.T) {
	t.Parallel()
	data := readTestdata(t, "borderless.pdf")
	reader := &tableReader{}
	ctx := context.WithValue(context.Background(), modelBudgetKey{}, &modelBudget{limit: time.Millisecond, spent: time.Second})
	result, ok, err := ReadPages(ctx, data, []int{1}, Options{}, reader, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(reader.asked) != 0 || result.ModelPagesSkipped != 1 {
		t.Fatalf("asked=%v skipped=%d, want no question to the models and one page skipped", reader.asked, result.ModelPagesSkipped)
	}
	if !strings.Contains(result.Markdown, "Hardware 120 135 128 141") {
		t.Fatalf("the page is still read:\n%s", result.Markdown)
	}
}
