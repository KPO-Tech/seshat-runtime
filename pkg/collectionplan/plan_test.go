package collectionplan

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// files makes n readable entries in a folder.
func files(dir string, n int) []Entry {
	var out []Entry
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("file%03d.pdf", i)
		if dir != "" {
			p = dir + "/" + p
		}
		out = append(out, Entry{Path: p, Size: 100})
	}
	return out
}

func names(p Plan) []string {
	var out []string
	for _, c := range p.Collections {
		out = append(out, fmt.Sprintf("%s:%d", c.Name, len(c.Files)))
	}
	return out
}

func mustMake(t *testing.T, entries []Entry, opts Options) Plan {
	t.Helper()
	plan, err := Make(entries, opts)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestAFolderBecomesACollection(t *testing.T) {
	entries := append(files("Contracts", 6), files("Invoices", 8)...)
	plan := mustMake(t, entries, Options{})
	if got, want := names(plan), []string{"Contracts:6", "Invoices:8"}; !reflect.DeepEqual(got, want) {
		t.Errorf("collections = %v, want %v", got, want)
	}
	if got := plan.Assignments()["Invoices/file003.pdf"]; got != "Invoices" {
		t.Errorf("Invoices/file003.pdf is in %q", got)
	}
}

func TestSmallFoldersAndRootFilesAreGathered(t *testing.T) {
	entries := append(files("Contracts", 6), files("Notes", 2)...)
	entries = append(entries, files("Misc2", 3)...)
	entries = append(entries, files("", 2)...)
	plan := mustMake(t, entries, Options{MiscName: "Other", MinFiles: 5})
	if got, want := names(plan), []string{"Contracts:6", "Other:7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("collections = %v, want %v", got, want)
	}
	misc := plan.Collections[1]
	if want := []string{"", "Misc2", "Notes"}; !reflect.DeepEqual(misc.Folders, want) {
		t.Errorf("the gathered folders = %q, want %q", misc.Folders, want)
	}
}

func TestABigFolderIsSplitAlongItsBigSubfolders(t *testing.T) {
	var entries []Entry
	entries = append(entries, files("Archive/2021", 6)...)
	entries = append(entries, files("Archive/2022", 7)...)
	entries = append(entries, files("Archive/tiny", 2)...)
	entries = append(entries, files("Archive", 3)...)
	plan := mustMake(t, entries, Options{MaxFiles: 10, MinFiles: 3})
	want := []string{"Archive:5", "Archive / 2021:6", "Archive / 2022:7"} // the tiny subfolder stays with the files of its parent
	if got := names(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("collections = %v, want %v", got, want)
	}
}

func TestABigFolderWithoutBigSubfoldersIsNotScattered(t *testing.T) {
	entries := append(files("Big", 12), files("Big/a", 1)...)
	entries = append(entries, files("Big/b", 2)...)
	plan := mustMake(t, entries, Options{MaxFiles: 10, MinFiles: 3})
	if got, want := names(plan), []string{"Big:15"}; !reflect.DeepEqual(got, want) {
		t.Errorf("collections = %v, want %v", got, want)
	}
}

func TestOnlyAFolderOfASingleFileIsGatheredByDefault(t *testing.T) {
	entries := append(files("Pair", 2), files("Single", 1)...)
	entries = append(entries, files("Contracts", 6)...)
	plan := mustMake(t, entries, Options{})
	if got, want := names(plan), []string{"Contracts:6", "Miscellaneous:1", "Pair:2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("collections = %v, want %v", got, want)
	}
}

func TestWhatIsNotWorthIndexingIsListedWithItsReason(t *testing.T) {
	entries := append(files("Docs", 5), []Entry{
		{Path: "Docs/.hidden/secret.pdf", Size: 10},
		{Path: "Docs/Thumbs.db", Size: 10},
		{Path: "Docs/~$draft.docx", Size: 10},
		{Path: "Docs/empty.pdf", Size: 0},
		{Path: "Docs/movie.mp4", Size: 10},
		{Path: "Docs/shortcut.pdf", Link: true},
		{Path: "Docs/locked", Problem: "permission denied"},
		{Path: "../outside.pdf", Size: 10},
	}...)
	plan := mustMake(t, entries, Options{})
	reasons := map[string]string{}
	for _, e := range plan.Excluded {
		reasons[e.Path] = e.Reason
	}
	want := map[string]string{
		"Docs/.hidden/secret.pdf": "hidden",
		"Docs/Thumbs.db":          "system or temporary file",
		"Docs/~$draft.docx":       "system or temporary file",
		"Docs/empty.pdf":          "empty file",
		"Docs/movie.mp4":          "format the reader does not handle",
		"Docs/shortcut.pdf":       "symbolic link",
		"Docs/locked":             "unreadable: permission denied",
		"../outside.pdf":          "path outside the archive",
	}
	if !reflect.DeepEqual(reasons, want) {
		t.Errorf("excluded = %v\nwant     %v", reasons, want)
	}
	if got := names(plan); !reflect.DeepEqual(got, []string{"Docs:5"}) {
		t.Errorf("collections = %v", got)
	}
}

func TestADuplicateKeepsTheFirstByPath(t *testing.T) {
	entries := append(files("Docs", 5), Entry{Path: "Docs/copy.pdf", Size: 100, Hash: "h"}, Entry{Path: "Docs/a-original.pdf", Size: 100, Hash: "h"})
	plan := mustMake(t, entries, Options{})
	if want := []Duplicate{{Path: "Docs/copy.pdf", Of: "Docs/a-original.pdf"}}; !reflect.DeepEqual(plan.Duplicates, want) {
		t.Errorf("duplicates = %v, want %v", plan.Duplicates, want)
	}
	if got := names(plan); !reflect.DeepEqual(got, []string{"Docs:6"}) {
		t.Errorf("collections = %v", got)
	}
}

func TestTwoFoldersWithTheSameNameAreToldApart(t *testing.T) {
	entries := append(files("Reports", 5), files("reports", 5)...)
	plan := mustMake(t, entries, Options{})
	got := names(plan)
	if len(got) != 2 || got[0] == got[1] {
		t.Errorf("collections = %v, want two different names", got)
	}
}

func TestTheSameFilesInAnyOrderGiveTheSamePlan(t *testing.T) {
	entries := append(files("A", 7), files("B", 9)...)
	entries = append(entries, files("C", 2)...)
	first := mustMake(t, entries, Options{})
	rand.New(rand.NewSource(7)).Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	if second := mustMake(t, entries, Options{}); !reflect.DeepEqual(first, second) {
		t.Errorf("the plan depends on the order of the entries")
	}
}

func TestAListingThatIsTooLongIsRefused(t *testing.T) {
	_, err := Make(files("A", 12), Options{MaxPlanFiles: 10})
	if !errors.Is(err, ErrTooManyFiles) {
		t.Errorf("err = %v, want ErrTooManyFiles", err)
	}
}

func TestEveryFileIsInTheCollectionsOrListed(t *testing.T) {
	entries := append(files("A", 6), files("B", 3)...)
	entries = append(entries, Entry{Path: "A/x.bin", Size: 5}, Entry{Path: "A/dup.pdf", Size: 100, Hash: "h"}, Entry{Path: "A/dup2.pdf", Size: 100, Hash: "h"})
	plan := mustMake(t, entries, Options{})
	seen := map[string]int{}
	for _, c := range plan.Collections {
		for _, f := range c.Files {
			seen[f]++
		}
	}
	for _, e := range plan.Excluded {
		seen[e.Path]++
	}
	for _, d := range plan.Duplicates {
		seen[d.Path]++
	}
	for _, e := range entries {
		if seen[e.Path] != 1 {
			t.Errorf("%s accounted for %d times, want exactly once", e.Path, seen[e.Path])
		}
	}
}

func TestScanDirListsTheFilesWithoutReadingThemUnlessAskedToHash(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/one.pdf", "same")
	write("a/two.pdf", "same")
	write("b/three.txt", "other")

	plain, err := ScanDir(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 3 {
		t.Fatalf("%d entries, want 3: %v", len(plain), plain)
	}
	for _, e := range plain {
		if e.Hash != "" || e.Size == 0 {
			t.Errorf("%s: hash %q size %d (no hash was asked for)", e.Path, e.Hash, e.Size)
		}
	}
	hashed, err := ScanDir(root, true)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]string{}
	for _, e := range hashed {
		byPath[e.Path] = e.Hash
	}
	if byPath["a/one.pdf"] == "" || byPath["a/one.pdf"] != byPath["a/two.pdf"] || byPath["a/one.pdf"] == byPath["b/three.txt"] {
		t.Errorf("hashes = %v", byPath)
	}
}

func TestScanDirRefusesAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanDir(p, false); err == nil {
		t.Error("a file is not an archive")
	}
}
