package access

import (
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"
)

// FilesystemPolicy centralizes common filesystem access checks.
type FilesystemPolicy struct {
	readDeniedPrefixes  []string
	writeDeniedPrefixes []string
}

func NewDefaultFilesystemPolicy() *FilesystemPolicy {
	return &FilesystemPolicy{
		// Linux kernel/system internals only — no narrow Windows or macOS
		// equivalent exists, and read access is rarely dangerous, so this
		// list stays deliberately narrower than writeDeniedPrefixes below.
		readDeniedPrefixes: []string{
			"/boot",
			"/sys",
			"/proc/sys",
		},
		writeDeniedPrefixes: []string{
			// Linux.
			"/boot",
			"/sys",
			"/proc/sys",
			"/etc",
			"/usr/bin",
			"/usr/sbin",
			"/bin",
			"/sbin",
			// macOS system directories — macOS is Unix-like but keeps its own
			// /System and /Library trees distinct from the Linux paths above.
			"/System",
			"/Library",
			// Windows system and program directories. Matched
			// case-insensitively on Windows (see hasPathPrefix) since NTFS
			// paths are case-preserving but not case-sensitive.
			`C:\Windows`,
			`C:\Program Files`,
			`C:\Program Files (x86)`,
		},
	}
}

// ReadDeniedPrefixes returns the path prefixes read access is denied under,
// for introspection (e.g. the get_config tool) - not a mutable handle.
func (p *FilesystemPolicy) ReadDeniedPrefixes() []string {
	if p == nil {
		p = NewDefaultFilesystemPolicy()
	}
	return append([]string(nil), p.readDeniedPrefixes...)
}

// WriteDeniedPrefixes returns the path prefixes write access is denied
// under, for introspection (e.g. the get_config tool) - not a mutable handle.
func (p *FilesystemPolicy) WriteDeniedPrefixes() []string {
	if p == nil {
		p = NewDefaultFilesystemPolicy()
	}
	return append([]string(nil), p.writeDeniedPrefixes...)
}

func (p *FilesystemPolicy) EvaluatePath(ctx Context, path string, access AccessKind) (PathDecision, error) {
	if p == nil {
		p = NewDefaultFilesystemPolicy()
	}

	rawProtectedPrefix := ""
	resolvedPath, err := ctx.ResolvePath(path)
	if err != nil {
		return PathDecision{}, err
	}

	switch access {
	case AccessRead:
		rawProtectedPrefix = matchingSlashPrefix(path, p.readDeniedPrefixes)
		if rawProtectedPrefix == "" {
			rawProtectedPrefix = matchingPrefix(resolvedPath, p.readDeniedPrefixes)
		}
		if rawProtectedPrefix != "" {
			prefix := rawProtectedPrefix
			return denyPath(resolvedPath, fmt.Sprintf("read access denied for protected path prefix %q", prefix)), nil
		}
		if err := requireExistingPath(resolvedPath); err != nil {
			return PathDecision{}, err
		}
	case AccessSearch:
		info, err := os.Stat(resolvedPath)
		if err != nil {
			return PathDecision{}, err
		}
		if !info.IsDir() {
			return PathDecision{}, fmt.Errorf("not a directory: %s", resolvedPath)
		}
		rawProtectedPrefix = matchingSlashPrefix(path, p.readDeniedPrefixes)
		if rawProtectedPrefix == "" {
			rawProtectedPrefix = matchingPrefix(resolvedPath, p.readDeniedPrefixes)
		}
		if prefix := rawProtectedPrefix; prefix != "" {
			return denyPath(resolvedPath, fmt.Sprintf("search access denied for protected path prefix %q", prefix)), nil
		}
	case AccessWrite, AccessCreate, AccessDelete:
		rawProtectedPrefix = matchingSlashPrefix(path, p.writeDeniedPrefixes)
		if rawProtectedPrefix == "" {
			rawProtectedPrefix = matchingPrefix(resolvedPath, p.writeDeniedPrefixes)
		}
		if prefix := rawProtectedPrefix; prefix != "" {
			return denyPath(resolvedPath, fmt.Sprintf("write access denied for protected path prefix %q", prefix)), nil
		}
	default:
		return PathDecision{}, fmt.Errorf("unsupported filesystem access kind: %s", access)
	}

	return PathDecision{
		DecisionResult: DecisionResult{
			Decision: DecisionAllow,
			Reason:   "path allowed by filesystem policy",
		},
		ResolvedPath: resolvedPath,
	}, nil
}

func requireExistingPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return nil
}

func denyPath(path string, reason string) PathDecision {
	return PathDecision{
		DecisionResult: DecisionResult{
			Decision: DecisionDeny,
			Reason:   reason,
		},
		ResolvedPath: path,
	}
}

func matchingPrefix(path string, prefixes []string) string {
	for _, prefix := range prefixes {
		if hasPathPrefix(path, prefix) {
			return prefix
		}
	}
	return ""
}

func matchingSlashPrefix(path string, prefixes []string) string {
	cleanPath := pathpkg.Clean(strings.ReplaceAll(path, "\\", "/"))
	for _, prefix := range prefixes {
		cleanPrefix := pathpkg.Clean(strings.ReplaceAll(prefix, "\\", "/"))
		if cleanPath == cleanPrefix || strings.HasPrefix(cleanPath, cleanPrefix+"/") {
			return prefix
		}
	}
	return ""
}

func hasPathPrefix(path string, prefix string) bool {
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if runtime.GOOS == "windows" {
		// NTFS paths are case-preserving but not case-sensitive — compare
		// case-insensitively so "c:\windows\..." still matches "C:\Windows".
		path = strings.ToLower(path)
		prefix = strings.ToLower(prefix)
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+string(filepath.Separator))
}
