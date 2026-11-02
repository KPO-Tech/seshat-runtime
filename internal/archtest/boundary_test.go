// Package archtest holds the tests that keep the package boundaries of AGENTS.md from eroding.
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePkg = "github.com/KPO-Tech/seshat-runtime/pkg/"

// legacyPkgImports are the imports of pkg/ that internal/ code already had when this test was written. AGENTS.md says internal/ must
// never import pkg/ (the dependency flows entry points, then pkg/sdk, then internal/), so the list only gets shorter: a new import
// fails the test, and an entry that is no longer needed fails it too, so that it is taken out.
//
// pkg/runtimepath is the shared leaf (paths of the runtime root, standard library only) and is the one that is expected to stay.
var legacyPkgImports = map[string][]string{
	"internal/automation/dataflow_adapter.go":            {"pkg/dataflow", "pkg/sdk", "pkg/types", "pkg/workflow"},
	"internal/automation/job.go":                         {"pkg/dataflow"},
	"internal/automation/job_scheduler.go":               {"pkg/dataflow", "pkg/sdk"},
	"internal/automation/runner.go":                      {"pkg/config", "pkg/dataflow", "pkg/sdk"},
	"internal/automation/workflow.go":                    {"pkg/sdk"},
	"internal/db/credentials.go":                         {"pkg/runtimepath"},
	"internal/engine/session_prompt.go":                  {"pkg/repomap"},
	"internal/memory/manager.go":                         {"pkg/runtimepath"},
	"internal/modes/execution/plan.go":                   {"pkg/runtimepath"},
	"internal/permissions/integration.go":                {"pkg/runtimepath"},
	"internal/runtime/tasks/manager.go":                  {"pkg/runtimepath"},
	"internal/storage/storage.go":                        {"pkg/runtimepath"},
	"internal/titlegen/local.go":                         {"pkg/runtimepath"},
	"internal/tools/bash/background.go":                  {"pkg/runtimepath"},
	"internal/tools/bash/monitor.go":                     {"pkg/runtimepath"},
	"internal/tools/builtin/config.go":                   {"pkg/pdfsmart"},
	"internal/tools/files/documentreader/render_page.go": {"pkg/pdfsmart"},
	"internal/tools/files/documentreader/shared.go":      {"pkg/pdfsmart"},
	"internal/tools/files/searchsession/search.go":       {"pkg/officetext"},
	"internal/tools/special/repomap/repomap.go":          {"pkg/repomap"},
	"internal/tools/special/workflow/draft.go":           {"pkg/workflow"},
	"internal/tools/system/mcp/config.go":                {"pkg/runtimepath"},
	"internal/tools/system/skills/loader.go":             {"pkg/runtimepath"},
	"internal/workspace/workspace.go":                    {"pkg/runtimepath"},
}

func TestInternalDoesNotImportPkgBeyondTheKnownList(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		rel, rerr := filepath.Rel(filepath.Join("..", ".."), path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		for _, spec := range file.Imports {
			imported, uerr := strconv.Unquote(spec.Path.Value)
			if uerr == nil && strings.HasPrefix(imported, modulePkg) {
				found[rel] = append(found[rel], "pkg/"+strings.TrimPrefix(imported, modulePkg))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	known := map[string]map[string]bool{}
	for file, imports := range legacyPkgImports {
		known[file] = map[string]bool{}
		for _, imported := range imports {
			known[file][imported] = true
		}
	}
	var problems []string
	for file, imports := range found {
		for _, imported := range imports {
			if !known[file][imported] {
				problems = append(problems, file+" imports "+imported+": internal/ must not import pkg/ (AGENTS.md); depend on an internal package, or pass what is needed in")
			}
		}
	}
	for file, imports := range known {
		for imported := range imports {
			stillThere := false
			for _, got := range found[file] {
				stillThere = stillThere || got == imported
			}
			if !stillThere {
				problems = append(problems, file+" no longer imports "+imported+": remove it from legacyPkgImports")
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
