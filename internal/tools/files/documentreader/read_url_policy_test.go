package documentreader

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

// recordingConverter is a document reader that remembers what it was asked to fetch.
type recordingConverter struct {
	urls []string
	text string
}

func (c *recordingConverter) IsAvailable(context.Context) bool { return true }
func (c *recordingConverter) ConvertFile(context.Context, string) (*documentreader.ConversionResult, error) {
	return &documentreader.ConversionResult{}, nil
}
func (c *recordingConverter) ConvertBytes(context.Context, []byte, string) (*documentreader.ConversionResult, error) {
	return &documentreader.ConversionResult{}, nil
}
func (c *recordingConverter) ConvertURL(_ context.Context, docURL string) (*documentreader.ConversionResult, error) {
	c.urls = append(c.urls, docURL)
	return &documentreader.ConversionResult{Markdown: c.text}, nil
}

// fixedResolver answers every name with the same addresses.
type fixedResolver struct{ addrs []netip.Addr }

func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addrs, nil
}

func newURLTool(conv *recordingConverter, addrs ...string) *ReadURLTool {
	var parsed []netip.Addr
	for _, a := range addrs {
		parsed = append(parsed, netip.MustParseAddr(a))
	}
	return &ReadURLTool{documentReader: conv, resolver: fixedResolver{addrs: parsed}}
}

func callURL(t *testing.T, tl *ReadURLTool, workDir string, parsed map[string]any) tool.CallResult {
	t.Helper()
	result, err := tl.Call(context.Background(), tool.CallInput{
		Parsed:      parsed,
		ToolContext: &tool.ToolUseContext{WorkingDirectory: workDir},
	}, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return result
}

// The document reader fetches the address the model chose, from the machine it runs on. An address on the local network or a metadata
// service must be refused before the reader is asked.
func TestReadURLRefusesTheLocalNetwork(t *testing.T) {
	for _, address := range []string{
		"http://127.0.0.1:8080/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://localhost/secret.pdf",
		"http://[::1]/x.pdf",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://10.1.2.3/report.pdf",
	} {
		conv := &recordingConverter{text: "secret"}
		result := callURL(t, newURLTool(conv, "93.184.216.34"), t.TempDir(), map[string]any{"url": address})
		if result.Error == nil {
			t.Errorf("%s: expected the address to be refused, got: %s", address, result.Content)
		}
		if len(conv.urls) != 0 {
			t.Errorf("%s: the document reader was asked to fetch it: %v", address, conv.urls)
		}
	}
}

// A public name that resolves to a private address is the same request in disguise.
func TestReadURLRefusesANameThatResolvesToAPrivateAddress(t *testing.T) {
	conv := &recordingConverter{text: "secret"}
	result := callURL(t, newURLTool(conv, "10.0.0.5"), t.TempDir(), map[string]any{"url": "https://docs.example.com/paper.pdf"})
	if result.Error == nil {
		t.Fatalf("expected a refusal, got: %s", result.Content)
	}
	if len(conv.urls) != 0 {
		t.Fatalf("the document reader was asked to fetch it: %v", conv.urls)
	}
}

func TestReadURLStillFetchesAPublicDocument(t *testing.T) {
	conv := &recordingConverter{text: "# Paper"}
	result := callURL(t, newURLTool(conv, "93.184.216.34"), t.TempDir(), map[string]any{"url": "https://docs.example.com/paper.pdf"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(conv.urls) != 1 {
		t.Fatalf("expected one fetch, got %v", conv.urls)
	}
}

func TestReadURLValidateInputRefusesALocalAddress(t *testing.T) {
	tl := NewReadURLTool(Config{})
	if _, err := tl.ValidateInput(context.Background(), map[string]any{"url": "http://169.254.169.254/latest/"}); err == nil {
		t.Fatal("expected the metadata address to be refused")
	}
}

func TestReadURLSavePath(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.md")

	t.Run("a path outside the workspace is refused before the download", func(t *testing.T) {
		conv := &recordingConverter{text: "page"}
		work := t.TempDir()
		for _, savePath := range []string{outside, filepath.Join("..", "escape.md"), filepath.Join("..", "..", "escape.md")} {
			result := callURL(t, newURLTool(conv, "93.184.216.34"), work, map[string]any{"url": "https://docs.example.com/a.pdf", "save_path": savePath})
			if result.Error == nil {
				t.Errorf("%s: expected a refusal, got: %s", savePath, result.Content)
			}
		}
		if len(conv.urls) != 0 {
			t.Errorf("the document was fetched although the save path was refused: %v", conv.urls)
		}
		if _, err := os.Stat(outside); err == nil {
			t.Error("a file was written outside the workspace")
		}
	})

	t.Run("a file the platform protects is refused", func(t *testing.T) {
		conv := &recordingConverter{text: "alias evil=1"}
		work := t.TempDir()
		result := callURL(t, newURLTool(conv, "93.184.216.34"), work, map[string]any{"url": "https://docs.example.com/a.pdf", "save_path": ".bashrc"})
		if result.Error == nil {
			t.Fatalf("expected a refusal, got: %s", result.Content)
		}
		if _, err := os.Stat(filepath.Join(work, ".bashrc")); err == nil {
			t.Error(".bashrc was written")
		}
	})

	t.Run("a file that exists is not replaced", func(t *testing.T) {
		conv := &recordingConverter{text: "from the page"}
		work := t.TempDir()
		existing := filepath.Join(work, "main.go")
		if err := os.WriteFile(existing, []byte("package main"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := callURL(t, newURLTool(conv, "93.184.216.34"), work, map[string]any{"url": "https://docs.example.com/a.pdf", "save_path": "main.go"})
		if result.Error == nil {
			t.Fatalf("expected a refusal, got: %s", result.Content)
		}
		got, _ := os.ReadFile(existing)
		if string(got) != "package main" {
			t.Errorf("the existing file was changed: %q", got)
		}
	})

	t.Run("a new file inside the workspace is written, and the answer says where", func(t *testing.T) {
		conv := &recordingConverter{text: "# Paper"}
		work := t.TempDir()
		result := callURL(t, newURLTool(conv, "93.184.216.34"), work, map[string]any{"url": "https://docs.example.com/a.pdf", "save_path": filepath.Join("docs", "paper.md")})
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}
		saved := filepath.Join(work, "docs", "paper.md")
		got, err := os.ReadFile(saved)
		if err != nil || string(got) != "# Paper" {
			t.Fatalf("the file was not written: %v %q", err, got)
		}
		if !strings.Contains(result.Content, "Saved to:") {
			t.Errorf("the answer does not say where the text went:\n%s", result.Content)
		}
	})
}

func TestReadURLIsReadOnlyOnlyWhenNothingIsSaved(t *testing.T) {
	tl := NewReadURLTool(Config{})
	if !tl.IsReadOnly(map[string]any{"url": "https://docs.example.com/a.pdf"}) {
		t.Error("without save_path the tool only reads")
	}
	if tl.IsReadOnly(map[string]any{"url": "https://docs.example.com/a.pdf", "save_path": "x.md"}) {
		t.Error("with save_path the tool writes a file and must not be called read only")
	}
}
