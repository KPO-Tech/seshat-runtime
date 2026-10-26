// Package collectionplan proposes how to group the files of an archive into collections, from where they are and what they are, without
// reading any of them. It is the quick plan of the automatic collections: a folder of the archive becomes a collection, the small
// folders are gathered, a very big folder is split along its subfolders, and what cannot be read, what is a duplicate and what is not
// worth indexing is listed with its reason instead of being lost. The plan is a proposal: the person edits it before anything is created.
//
// The package does no input or output beyond ScanDir: the same Plan serves a folder of the computer and the listing of a drive,
// whatever lists the entries.
package collectionplan

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Entry is one file of the archive, as listed by whatever scanned the source. Nothing in it needs the content of the file.
type Entry struct {
	// Path is slash-separated and relative to the root of the archive.
	Path    string
	Size    int64
	ModTime time.Time
	// Hash is the hash of the content when the scanner computed one; two entries with the same non-empty hash are the same file.
	Hash string
	// Link is set for a symbolic link: it is not followed, and the plan leaves it out.
	Link bool
	// Problem is set when the scanner could not read the entry (a folder or a file it has no right to): it is listed, not lost.
	Problem string
}

// Options tunes the plan. The zero value is usable.
type Options struct {
	// Readable says whether a file extension (lower case, with its dot) is one the reader can turn into text. Nil means DefaultReadable.
	Readable func(ext string) bool
	// MinFiles is how many readable files a folder needs to be a collection of its own; smaller folders are gathered into one collection
	// named MiscName. Default 2: only a folder of a single file is gathered. A higher value gathers real groups: on the test bench
	// (scripts/collections-eval), with half of the folders right, gathering the folders of up to 1, 2 and 4 files leaves 52 %, 54 % and
	// 58 % of the files to fix afterwards, against 51 % when nothing is gathered.
	MinFiles int
	// MaxFiles is the size above which a collection is split along its subfolders, when it has subfolders big enough to stand alone.
	// Default 400.
	MaxFiles int
	// MaxPlanFiles refuses a listing longer than this (ErrTooManyFiles). Default 100000.
	MaxPlanFiles int
	// MiscName is the name of the collection that gathers the small folders and the files at the root. Default "Miscellaneous".
	MiscName string
}

// DefaultReadable is the set of formats the reader turns into text: PDF, the Office formats, HTML and plain text.
func DefaultReadable(ext string) bool { return readable[ext] }

var readable = map[string]bool{
	".pdf": true, ".docx": true, ".pptx": true, ".xlsx": true, ".html": true, ".htm": true, ".xhtml": true,
	".txt": true, ".md": true, ".markdown": true, ".csv": true, ".tsv": true, ".json": true, ".xml": true, ".log": true, ".rst": true,
}

// Collection is a group of files proposed as one collection.
type Collection struct {
	Name  string
	Files []string
	Bytes int64
	// Folders are the folders of the archive the files come from, relative to its root ("" is the root itself).
	Folders []string
}

// Excluded is a file the plan leaves out, with why.
type Excluded struct {
	Path   string
	Reason string
}

// Duplicate is a file with the same content as an earlier one (by path); only the first is in a collection.
type Duplicate struct {
	Path string
	Of   string
}

// Plan is the proposal.
type Plan struct {
	Collections []Collection
	Excluded    []Excluded
	Duplicates  []Duplicate
}

// Assignments maps each file of the plan to the name of its collection.
func (p Plan) Assignments() map[string]string {
	out := make(map[string]string)
	for _, c := range p.Collections {
		for _, f := range c.Files {
			out[f] = c.Name
		}
	}
	return out
}

// ErrTooManyFiles is returned for a listing longer than Options.MaxPlanFiles.
var ErrTooManyFiles = errors.New("collectionplan: the archive has more files than a plan can hold")

func (o Options) withDefaults() Options {
	if o.Readable == nil {
		o.Readable = DefaultReadable
	}
	if o.MinFiles <= 0 {
		o.MinFiles = 2
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 400
	}
	if o.MaxPlanFiles <= 0 {
		o.MaxPlanFiles = 100000
	}
	if o.MiscName == "" {
		o.MiscName = "Miscellaneous"
	}
	return o
}

// Make proposes the collections of an archive. The same entries, in any order, give the same plan.
func Make(entries []Entry, opts Options) (Plan, error) {
	opts = opts.withDefaults()
	if len(entries) > opts.MaxPlanFiles {
		return Plan{}, fmt.Errorf("%w: %d files, the limit is %d", ErrTooManyFiles, len(entries), opts.MaxPlanFiles)
	}
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var plan Plan
	var kept []Entry
	firstOf := map[string]string{}
	for _, e := range sorted {
		p := path.Clean(strings.ReplaceAll(e.Path, "\\", "/"))
		if e.Path == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			plan.Excluded = append(plan.Excluded, Excluded{Path: e.Path, Reason: "path outside the archive"})
			continue
		}
		e.Path = p
		if reason := exclusion(e, opts); reason != "" {
			plan.Excluded = append(plan.Excluded, Excluded{Path: e.Path, Reason: reason})
			continue
		}
		if e.Hash != "" {
			if first, seen := firstOf[e.Hash]; seen {
				plan.Duplicates = append(plan.Duplicates, Duplicate{Path: e.Path, Of: first})
				continue
			}
			firstOf[e.Hash] = e.Path
		}
		kept = append(kept, e)
	}
	plan.Collections = group(kept, opts)
	return plan, nil
}

// exclusion says why a file is not worth a place in a collection, or "" when it is.
func exclusion(e Entry, opts Options) string {
	if e.Problem != "" {
		return "unreadable: " + e.Problem
	}
	if e.Link {
		return "symbolic link"
	}
	segments := strings.Split(e.Path, "/")
	for _, s := range segments {
		if strings.HasPrefix(s, ".") {
			return "hidden"
		}
	}
	name := strings.ToLower(segments[len(segments)-1])
	switch {
	case name == "thumbs.db" || name == "desktop.ini" || strings.HasPrefix(name, "~$") || strings.HasSuffix(name, "~"):
		return "system or temporary file"
	case e.Size == 0:
		return "empty file"
	case !opts.Readable(path.Ext(name)):
		return "format the reader does not handle"
	}
	return ""
}

// folderOf is the folder of a file: its path without its name ("" for a file at the root).
func folderOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// group turns the kept files into collections: one per top-level folder, with the small ones gathered and the big ones split.
func group(files []Entry, opts Options) []Collection {
	byTop := map[string][]Entry{}
	for _, f := range files {
		top := ""
		if i := strings.Index(f.Path, "/"); i >= 0 {
			top = f.Path[:i]
		}
		byTop[top] = append(byTop[top], f)
	}
	var out []Collection
	var misc []Entry
	for top, fs := range byTop {
		if top == "" || len(fs) < opts.MinFiles {
			misc = append(misc, fs...)
			continue
		}
		out = append(out, split(top, top, fs, opts, 1)...)
	}
	if len(misc) > 0 {
		out = append(out, build(opts.MiscName, misc))
	}
	return uniqueNames(out)
}

// split keeps a folder as one collection unless it is too big and has subfolders that are big enough to stand alone.
func split(name, dir string, fs []Entry, opts Options, depth int) []Collection {
	if len(fs) <= opts.MaxFiles || depth >= 4 {
		return []Collection{build(name, fs)}
	}
	bySub := map[string][]Entry{}
	var direct []Entry
	for _, f := range fs {
		rest := strings.TrimPrefix(f.Path, dir+"/")
		if i := strings.Index(rest, "/"); i >= 0 {
			bySub[rest[:i]] = append(bySub[rest[:i]], f)
		} else {
			direct = append(direct, f)
		}
	}
	var out []Collection
	for sub, sf := range bySub {
		if len(sf) < opts.MinFiles {
			direct = append(direct, sf...)
			continue
		}
		out = append(out, split(name+" / "+sub, dir+"/"+sub, sf, opts, depth+1)...)
	}
	if len(out) == 0 {
		return []Collection{build(name, fs)} // no subfolder is big enough: splitting would only scatter the files
	}
	if len(direct) > 0 {
		out = append(out, build(name, direct))
	}
	return out
}

func build(name string, fs []Entry) Collection {
	c := Collection{Name: name}
	folders := map[string]bool{}
	for _, f := range fs {
		c.Files = append(c.Files, f.Path)
		c.Bytes += f.Size
		folders[folderOf(f.Path)] = true
	}
	sort.Strings(c.Files)
	for d := range folders {
		c.Folders = append(c.Folders, d)
	}
	sort.Strings(c.Folders)
	return c
}

// uniqueNames sorts the collections by name and tells apart two that would have the same one (compared without case).
func uniqueNames(cs []Collection) []Collection {
	sort.Slice(cs, func(i, j int) bool {
		if strings.EqualFold(cs[i].Name, cs[j].Name) {
			return cs[i].Folders[0] < cs[j].Folders[0]
		}
		return strings.ToLower(cs[i].Name) < strings.ToLower(cs[j].Name)
	})
	seen := map[string]int{}
	for i := range cs {
		key := strings.ToLower(cs[i].Name)
		seen[key]++
		if seen[key] > 1 {
			cs[i].Name = fmt.Sprintf("%s (%d)", cs[i].Name, seen[key])
		}
	}
	return cs
}
