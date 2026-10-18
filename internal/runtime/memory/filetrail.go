package memory

import (
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// A summary written by a model is bad at one thing: remembering which files were touched. Factory's evaluation of
// compaction methods scored every one of them between 2.2 and 2.5 out of 5 on it, and suggests tracking files in the
// harness. The runtime sees every tool call, so it keeps that list itself and appends it to the summary.

// maxTrailPaths is how many paths of each kind a trail keeps; past it the oldest are dropped.
const maxTrailPaths = 40

// fileTrail is the files a conversation touched, oldest first.
type fileTrail struct {
	Modified []string
	Read     []string
}

func (t fileTrail) empty() bool { return len(t.Modified) == 0 && len(t.Read) == 0 }

// pathKeys are the tool input keys that name a file or folder.
var pathKeys = []string{"file_path", "path", "notebook_path", "source", "destination", "target"}

// modifyingTool reports whether a tool, by its name, changes what it names.
func modifyingTool(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"write", "edit", "patch", "create", "delete", "remove", "move", "rename", "mkdir", "replace"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func readingTool(name string) bool { return strings.Contains(strings.ToLower(name), "read") }

// collectFileTrail lists the files the tool calls of messages touched, leaving out calls that failed.
func collectFileTrail(messages []types.Message) fileTrail {
	failed := map[string]bool{}
	for _, m := range messages {
		for _, b := range m.Content {
			if r, ok := b.(types.ToolResultContent); ok && r.IsError {
				failed[r.ToolUseID] = true
			}
		}
	}
	var trail fileTrail
	for _, m := range messages {
		for _, b := range m.Content {
			use, ok := b.(types.ToolUseContent)
			if !ok || failed[use.ID] {
				continue
			}
			paths := pathsOf(use)
			switch {
			case modifyingTool(use.Name):
				trail.Modified = appendUnique(trail.Modified, paths...)
			case readingTool(use.Name):
				trail.Read = appendUnique(trail.Read, paths...)
			}
		}
	}
	return trail
}

// pathsOf returns the paths named by a tool call: the usual input keys, and the file names of a patch.
func pathsOf(use types.ToolUseContent) []string {
	var out []string
	for _, k := range pathKeys {
		if s, ok := use.Input[k].(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	if strings.Contains(strings.ToLower(use.Name), "patch") {
		for _, k := range []string{"patch", "diff", "input"} {
			if s, ok := use.Input[k].(string); ok {
				out = append(out, pathsInPatch(s)...)
			}
		}
	}
	return out
}

// pathsInPatch reads the file names of a unified diff ("+++ b/x") or an apply_patch block ("*** Update File: x").
func pathsInPatch(patch string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			if p != "" && p != "/dev/null" {
				out = append(out, p)
			}
		case strings.HasPrefix(line, "*** Update File:"), strings.HasPrefix(line, "*** Add File:"), strings.HasPrefix(line, "*** Delete File:"):
			out = append(out, strings.TrimSpace(line[strings.Index(line, ":")+1:]))
		}
	}
	return out
}

// appendUnique adds paths at the end, moving one already present to the end (it is the most recent touch), and keeps
// at most maxTrailPaths.
func appendUnique(list []string, paths ...string) []string {
	for _, p := range paths {
		for i, existing := range list {
			if existing == p {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		list = append(list, p)
	}
	if len(list) > maxTrailPaths {
		list = list[len(list)-maxTrailPaths:]
	}
	return list
}

// merge returns the trail of t followed by the trail of later; a file that was only read before and modified since
// is listed as modified only.
func (t fileTrail) merge(later fileTrail) fileTrail {
	out := fileTrail{
		Modified: appendUnique(append([]string(nil), t.Modified...), later.Modified...),
		Read:     appendUnique(append([]string(nil), t.Read...), later.Read...),
	}
	modified := map[string]bool{}
	for _, p := range out.Modified {
		modified[p] = true
	}
	kept := out.Read[:0]
	for _, p := range out.Read {
		if !modified[p] {
			kept = append(kept, p)
		}
	}
	out.Read = kept
	return out
}

const (
	filesMarker    = "Files touched (listed by the runtime from the tool calls, not written by a model):"
	modifiedPrefix = "modified: "
	readPrefix     = "read: "
)

// render writes the trail as the block that ends a summary; parseFileTrail reads it back.
func (t fileTrail) render() string {
	if t.empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString(filesMarker + "\n")
	if len(t.Modified) > 0 {
		b.WriteString(modifiedPrefix + strings.Join(t.Modified, ", ") + "\n")
	}
	if len(t.Read) > 0 {
		b.WriteString(readPrefix + strings.Join(t.Read, ", ") + "\n")
	}
	return b.String()
}

func parseFileTrail(block string) fileTrail {
	var t fileTrail
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, modifiedPrefix):
			t.Modified = splitPaths(strings.TrimPrefix(line, modifiedPrefix))
		case strings.HasPrefix(line, readPrefix):
			t.Read = splitPaths(strings.TrimPrefix(line, readPrefix))
		}
	}
	return t
}

func splitPaths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ", ") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
