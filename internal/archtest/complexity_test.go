package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// maxComplexity is the cyclomatic complexity above which a function is listed in knownComplex: a function that long is read, tested and
// changed with difficulty. It is counted as gocyclo does: 1, plus one for each if, for, range, case (default excepted), select case
// and each && or ||, the function literals inside a function included.
const maxComplexity = 30

// knownComplex are the functions that were above maxComplexity when this test was written, with their complexity. A function that is
// not here fails the test as soon as it passes the threshold, and one that is here fails it when it gets more complex, or less complex
// until the number is lowered, so the list only shrinks. Most of them are the message switches of the terminal UI (a case per message
// or key), which are long but flat; the others are the next candidates for being split into steps, as Integrator.ResolverWithContext was.
var knownComplex = map[string]int{
	"internal/agent/runner.go RunAgent":                                                   51,
	"internal/pdfsmart/pdfsmart.go ReadPages":                                             54,
	"internal/pdftext/layout.go plainLine":                                                37,
	"internal/pdftext/rulings.go collectRulings":                                          41,
	"internal/providers/client_codex.go parseCodexSSEStream":                              33,
	"internal/rag/markdown_chunker.go markdownChunker.split":                              38,
	"internal/rag/service.go Service.Ingest":                                              35,
	"tui/internal/seshattui/config/load.go Config.configureProviders":                     59,
	"tui/internal/seshattui/config/load.go configureSelectedModels":                       32,
	"tui/internal/seshattui/shell/jq.go handleJQ":                                         44,
	"tui/internal/seshattui/ui/chat/ask_user.go askUserRenderContext.RenderTool":          50,
	"tui/internal/seshattui/ui/chat/docker_mcp.go DockerMCPToolRenderContext.RenderTool":  34,
	"tui/internal/seshattui/ui/chat/tools.go NewToolMessageItem":                          60,
	"tui/internal/seshattui/ui/chat/tools.go baseToolMessageItem.formatParametersForCopy": 47,
	"tui/internal/seshattui/ui/dialog/models.go Models.setProviderItems":                  40,
	"tui/internal/seshattui/ui/dialog/settings.go Settings.Draw":                          32,
	"tui/internal/seshattui/ui/diffview/diffview.go DiffView.renderSplit":                 40,
	"tui/internal/seshattui/ui/model/ui.go UI.Update":                                     157,
	"tui/internal/seshattui/ui/model/ui.go UI.handleDialogMsg":                            85,
	"tui/internal/seshattui/ui/model/ui.go UI.handleKeyPressMsg":                          148,
	"tui/internal/seshattui/ui/model/ui.go UI.updateSessionMessage":                       39,
	"tui/internal/seshattui/workspace/seshat_workspace.go SeshatWorkspace.SetConfigField": 37,
	"tui/internal/seshattui/workspace/seshat_workspace.go buildToolPermissionParams":      32,
	"internal/tools/agents/agent_tool.go AgentTool.Call":                                  35,
	"internal/tools/files/edit/edit.go Tool.Call":                                         47,
	"internal/tools/files/patch/patch.go Patch.Apply":                                     31,
	"internal/tools/files/write/write.go Tool.Call":                                       34,
	"internal/tools/math/engine/advanced.go AdvancedCalculator.Calculate":                 34,
	"internal/tools/math/engine/expression.go ExpressionCalculator.mathFunctions":         36,
	"internal/tools/notebook/edit.go runEdit":                                             38,
	"internal/tools/task/taskList.go TaskListTool.Call":                                   43,
	"internal/vector/hnsw_index.go loadHNSWIndex":                                         33,
	"pkg/sdk/client.go NewClient":                                                         33,
}

func TestFunctionComplexityDoesNotGrow(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]int{}
	for _, dir := range []string{"cmd", "internal", "pkg", "tui"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if perr != nil {
				return perr
			}
			if isGenerated(file) {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if c := cyclomatic(fn); c > maxComplexity {
					found[rel+" "+funcName(fn)] = c
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if os.Getenv("SESHAT_PRINT_COMPLEXITY") != "" {
		for _, line := range complexityEntries(found) {
			fmt.Println(line)
		}
	}

	keys := map[string]bool{}
	for k := range found {
		keys[k] = true
	}
	for k := range knownComplex {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		have, haveOK := found[k]
		allowed, known := knownComplex[k]
		switch {
		case haveOK && !known:
			t.Errorf("%s has a cyclomatic complexity of %d (the limit is %d): split it into steps", k, have, maxComplexity)
		case haveOK && have > allowed:
			t.Errorf("%s got more complex: %d, it was %d: split it into steps rather than growing it", k, have, allowed)
		case haveOK && have < allowed:
			t.Errorf("%s is less complex (%d, the list says %d): lower knownComplex[%q] to %d", k, have, allowed, k, have)
		case !haveOK && known:
			t.Errorf("%s is no longer above %d: remove it from knownComplex", k, maxComplexity)
		}
	}
}

func complexityEntries(found map[string]int) []string {
	names := make([]string, 0, len(found))
	for k := range found {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, fmt.Sprintf("\t%q: %d,", k, found[k]))
	}
	return out
}

// cyclomatic counts the decision points of a function the way gocyclo does.
func cyclomatic(fn *ast.FuncDecl) int {
	n := 1
	ast.Inspect(fn, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			n++
		case *ast.CaseClause:
			if x.List != nil {
				n++
			}
		case *ast.CommClause:
			if x.Comm != nil {
				n++
			}
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				n++
			}
		}
		return true
	})
	return n
}

// funcName is the name of a function as the report of a linter writes it: Name, or Type.Name for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if idx, ok := typ.(*ast.IndexExpr); ok {
		typ = idx.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
