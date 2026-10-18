package builtin

import (
	"context"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

// wantRegistered lists a representative tool from each family wired in
// RegisterBuiltinToolsWithConfig: catching an accidental removal or a typo'd
// tool name here is cheap, versus discovering it only when a host runtime
// tries to resolve the tool at request time.
var wantRegistered = []string{
	"bash",
	"read_file",
	"convert_document",
	"read_document_url",
	"render_document_page",
	"glob",
	"grep",
	"generate_image",
	"text_to_speech",
	"speech_to_text",
	"calculator",
	"unit_convert",
	"statistics",
	"financial_calc",
	"rag_search",
	"web_search",
	"web_fetch",
}

func TestRegisterBuiltinTools_NilConfig(t *testing.T) {
	reg := registry.NewRegistry()
	if err := RegisterBuiltinTools(reg); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}
	for _, name := range wantRegistered {
		if !reg.HasTool(name) {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestNewBuiltinRegistry_NoDuplicateRegistrations(t *testing.T) {
	reg, err := NewBuiltinRegistry()
	if err != nil {
		t.Fatalf("NewBuiltinRegistry: %v", err)
	}
	if reg.Count() == 0 {
		t.Fatal("expected at least one registered tool")
	}
}

func TestNewBuiltinRegistryWithConfig_EmptyConfig(t *testing.T) {
	reg, err := NewBuiltinRegistryWithConfig(&Config{})
	if err != nil {
		t.Fatalf("NewBuiltinRegistryWithConfig: %v", err)
	}
	// render_document_page must always register - it degrades to a
	// "not configured" text result at Call() time rather than being omitted,
	// same as convert_document/read_document_url.
	tl, ok := reg.Get("render_document_page")
	if !ok {
		t.Fatal("expected render_document_page to be registered even without DocumentPageRenderer configured")
	}
	if !tl.IsEnabled() {
		t.Error("expected render_document_page to report enabled regardless of renderer configuration")
	}
}

func TestNewBuiltinRegistryWithConfig_EnableToolSearchReachesRegistry(t *testing.T) {
	reg, err := NewBuiltinRegistryWithConfig(&Config{EnableToolSearch: true})
	if err != nil {
		t.Fatalf("NewBuiltinRegistryWithConfig: %v", err)
	}
	if !reg.HasTool("skill") {
		t.Fatal("expected the skill tool to still be registered (EnableToolSearch hides it from the initial surface, it does not unregister it)")
	}

	surface, err := registry.NewSurfaceBuilder(reg).Build(context.Background(), registry.SurfaceBuildRequest{
		IncludeReadOnly:    true,
		IncludeDestructive: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, def := range surface.Tools {
		if def.Name == "skill" {
			t.Fatalf("expected Config.EnableToolSearch to keep the deferred skill tool out of the initial surface, but it is there: %#v", def)
		}
	}
}

// TestToolSearchToolIsOfferedWhenTheClientDefersTools: EnableToolSearch hides the deferred tools, so the model must be given
// tool_search to find them. It used to follow only the ENABLE_TOOL_SEARCH variable, which left a host that set the option
// with hidden tools and no way to reach them.
func TestToolSearchToolIsOfferedWhenTheClientDefersTools(t *testing.T) {
	t.Setenv("ENABLE_TOOL_SEARCH", "")
	surfaceNames := func(enable bool) map[string]bool {
		t.Helper()
		reg, err := NewBuiltinRegistryWithConfig(&Config{EnableToolSearch: enable})
		if err != nil {
			t.Fatalf("NewBuiltinRegistryWithConfig: %v", err)
		}
		surface, err := registry.NewSurfaceBuilder(reg).Build(context.Background(), registry.SurfaceBuildRequest{IncludeReadOnly: true, IncludeDestructive: true})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		names := map[string]bool{}
		for _, def := range surface.Tools {
			names[def.Name] = true
		}
		return names
	}

	if on := surfaceNames(true); !on["tool_search"] || on["skill"] {
		t.Fatalf("with EnableToolSearch the surface must hold tool_search and not the deferred skill tool: tool_search=%v skill=%v", on["tool_search"], on["skill"])
	}
	if off := surfaceNames(false); off["tool_search"] || !off["skill"] {
		t.Fatalf("without it nothing is deferred and tool_search stays off: tool_search=%v skill=%v", off["tool_search"], off["skill"])
	}
}
