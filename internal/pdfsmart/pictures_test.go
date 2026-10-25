package pdfsmart

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
)

// withPicture stamps a picture of the given size on the pages of a PDF, which gives a text page that holds a raster image.
func withPicture(t *testing.T, pdf []byte, pages []string, side int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, side, side))
	for x := 0; x < side; x++ {
		for y := 0; y < side; y++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	file := filepath.Join(t.TempDir(), "picture.png")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	watermark, err := pdfcpu.ParseImageWatermarkDetails(context.Background(), file, "scale:0.3 abs, pos:c", true, types.POINTS, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := api.AddWatermarks(context.Background(), bytes.NewReader(pdf), &out, pages, watermark, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAPageWithTextAndAPictureKeepsItsTextAndSaysWhereThePictureIs(t *testing.T) {
	t.Parallel()
	data := withPicture(t, readTestdata(t, "text_layer.pdf"), []string{"1"}, 300)

	// Convert sends a page with a picture to the engine; with no engine the text is kept.
	result, ok, err := Convert(context.Background(), data, nil, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v: a page with text and a picture is readable without an engine", ok, err)
	}
	if !strings.Contains(result.Markdown, "Sample Report") {
		t.Errorf("the text of the page is missing:\n%s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "[Image 1 on page 1]") {
		t.Errorf("the picture is not marked:\n%s", result.Markdown)
	}
	page := result.Pages[0]
	if page.Source != PageSourceNative || !page.HasEmbeddedImage || len(page.Pictures) != 1 || page.Pictures[0].Number != 1 {
		t.Errorf("page = %+v", page)
	}
}

func TestAPageThatTheEngineReadsHasNoMarker(t *testing.T) {
	t.Parallel()
	data := withPicture(t, readTestdata(t, "text_layer.pdf"), []string{"1"}, 300)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/convert/file" {
			_, _ = w.Write([]byte(`{"status":"success","document":{"md_content":"Text the engine read, with the text of the picture","pages":[1]}}`))
			return
		}
		w.WriteHeader(http.StatusOK) // /health
	}))
	defer server.Close()
	result, ok, err := Convert(context.Background(), data, documentreader.NewDoclingClient(server.URL), VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if strings.Contains(result.Markdown, "[Image") || len(result.Pages[0].Pictures) != 0 {
		t.Errorf("the engine read the picture, nothing is left to mark:\n%s", result.Markdown)
	}
}

func TestAPageWithNoTextIsStillAFailureWithoutAnEngine(t *testing.T) {
	t.Parallel()
	_, ok, err := Convert(context.Background(), readTestdata(t, "scanned.pdf"), nil, VisionFallback{})
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v: a scan has nothing but its picture, and a marker is not its text", ok, err)
	}
}

func TestAPictureOnEveryPageIsALogoNotAFigure(t *testing.T) {
	t.Parallel()
	data := withPicture(t, readTestdata(t, "three_pages.pdf"), nil, 300)
	pages, _, _, pictures, err := pagesWithEmbeddedImages(context.Background(), data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("the picture should be on 3 pages, got %v", pages)
	}
	if len(pictures) != 0 {
		t.Errorf("a picture repeated on every page is not marked, got %v", pictures)
	}
}

func TestAnIconIsNotMarked(t *testing.T) {
	t.Parallel()
	data := withPicture(t, readTestdata(t, "text_layer.pdf"), []string{"1"}, 40)
	result, ok, err := Convert(context.Background(), data, nil, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if strings.Contains(result.Markdown, "[Image") {
		t.Errorf("an icon is not a figure:\n%s", result.Markdown)
	}
}

func TestCaptionsAreAttachedOnlyWhenTheyCanBePaired(t *testing.T) {
	t.Parallel()
	text := "Some prose.\n\n**Figure 2.** Residual learning: a building block.\n\nMore prose.\nTable 1 is not a figure.\nFig. 3: The second one"
	cases := []struct {
		name  string
		count int
		want  []string
	}{
		{"as many pictures as captions", 2, []string{"Figure 2. Residual learning: a building block.", "Fig. 3: The second one"}},
		{"fewer pictures than captions: a guess, so no title", 1, []string{""}},
		{"more pictures than captions: no title", 3, []string{"", "", ""}},
	}
	for _, tc := range cases {
		marked, pictures := withImageMarkers(text, 4, tc.count)
		if len(pictures) != tc.count {
			t.Fatalf("%s: %d pictures", tc.name, len(pictures))
		}
		for i, picture := range pictures {
			if picture.Number != i+1 || picture.Title != tc.want[i] {
				t.Errorf("%s: picture %d = %+v, want title %q", tc.name, i, picture, tc.want[i])
			}
			if !strings.Contains(marked, ImageMarker(4, picture)) {
				t.Errorf("%s: %q is not in the text", tc.name, ImageMarker(4, picture))
			}
		}
	}
	if marked, pictures := withImageMarkers(text, 4, 0); marked != text || pictures != nil {
		t.Error("a page without pictures is left as it is")
	}
}

func TestAMarkerHasNoClosingBracketInItsTitle(t *testing.T) {
	t.Parallel()
	captions := captionsIn("Figure 1. Results [see appendix]")
	if len(captions) != 1 || strings.Count(ImageMarker(1, Picture{Number: 1, Title: captions[0]}), "]") != 1 {
		t.Errorf("captions = %q", captions)
	}
}

// merged joins PDFs one after the other.
func merged(t *testing.T, parts ...[]byte) []byte {
	t.Helper()
	readers := make([]io.ReadSeeker, len(parts))
	for i, part := range parts {
		readers[i] = bytes.NewReader(part)
	}
	var out bytes.Buffer
	if err := api.MergeRaw(context.Background(), readers, &out, false, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAPageThatIsOnlyAPictureDoesNotFailADocumentWithTextWhenNothingCanReadIt(t *testing.T) {
	t.Parallel()
	data := merged(t, readTestdata(t, "text_layer.pdf"), readTestdata(t, "scanned.pdf"))
	result, ok, err := Convert(context.Background(), data, nil, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v: one page that is a picture must not sink the document", ok, err)
	}
	last := result.Pages[len(result.Pages)-1]
	if last.Text != ImageMarker(last.Page, Picture{Number: 1}) || len(last.Pictures) != 1 {
		t.Errorf("the page of the picture should be its marker alone, got %+v", last)
	}
	if !strings.Contains(result.Markdown, "Sample Report") {
		t.Errorf("the text pages are missing:\n%s", result.Markdown)
	}
}

func TestABlankPageDoesNotFailADocumentWithTextWhenNothingCanReadIt(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := api.InsertPages(context.Background(), bytes.NewReader(readTestdata(t, "text_layer.pdf")), &out, []string{"1"}, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	result, ok, err := Convert(context.Background(), out.Bytes(), nil, VisionFallback{})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v: a blank page must not sink the document", ok, err)
	}
	if len(result.Pages) < 2 || strings.TrimSpace(result.Pages[1].Text) != "" {
		t.Errorf("the second page should be blank, got %+v", result.Pages)
	}
}

func TestAPageNothingCouldReadStillFailsTheDocumentWhenAnEngineIsConfigured(t *testing.T) {
	t.Parallel()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	data := merged(t, readTestdata(t, "text_layer.pdf"), readTestdata(t, "scanned.pdf"))
	_, ok, err := Convert(context.Background(), data, documentreader.NewDoclingClient(down.URL), VisionFallback{})
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v: an engine that is configured and does not answer must not leave a silent hole", ok, err)
	}
}

func TestAPageIsReadWhenItHasTextBesidesItsMarkers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text string
		want bool
	}{
		{"", false},
		{"[Image 1 on page 3]", false},
		{"[Image 1 on page 3: Figure 1. A caption]\n\n[Image 2 on page 3]", false},
		{"Some text\n\n[Image 1 on page 3]", true},
		{"[Image of a cat]", true},
	}
	for _, tc := range cases {
		if got := (PageResult{Text: tc.text}).HasText(); got != tc.want {
			t.Errorf("HasText(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
