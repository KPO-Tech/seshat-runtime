//go:build ignore

// plan_folder runs the quick planner (pkg/collectionplan) on a folder and writes the plan in the format evaluate.py reads.
// Usage: go run scripts/collections-eval/plan_folder.go [-hash] [-min N] [-max N] <archive folder> > plan.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/KPO-Tech/seshat-runtime/pkg/collectionplan"
)

func main() {
	hash := flag.Bool("hash", false, "read each file once, on this computer, to find duplicates")
	minFiles := flag.Int("min", 0, "files a folder needs to be a collection of its own (default 5)")
	maxFiles := flag.Int("max", 0, "files above which a folder is split along its subfolders (default 400)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: plan_folder [-hash] [-min N] [-max N] <archive folder>")
		os.Exit(2)
	}
	entries, err := collectionplan.ScanDir(flag.Arg(0), *hash)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	plan, err := collectionplan.Make(entries, collectionplan.Options{MinFiles: *minFiles, MaxFiles: *maxFiles})
	if err != nil {
		fmt.Fprintln(os.Stderr, "plan:", err)
		os.Exit(1)
	}
	out := map[string]any{"collections": map[string][]string{}}
	collections := out["collections"].(map[string][]string)
	for _, c := range plan.Collections {
		collections[c.Name] = c.Files
	}
	fmt.Fprintf(os.Stderr, "%d files listed, %d collections, %d excluded, %d duplicates\n", len(entries), len(plan.Collections), len(plan.Excluded), len(plan.Duplicates))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}
