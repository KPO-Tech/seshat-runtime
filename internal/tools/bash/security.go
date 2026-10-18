package bash

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/textsafe"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/files/shared"
	"github.com/KPO-Tech/seshat-runtime/internal/workspace"
)

// dangerousPatterns are compiled once at package init — never per-call.
// Ordered from most specific to most general.
var dangerousPatterns = []*regexp.Regexp{
	// Filesystem nuking
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bdd\b[^|&;]*\bif=/dev/(zero|random|urandom|sd[a-z]|hd[a-z])`),

	// Raw device writes
	regexp.MustCompile(`\b(dd|tee)\b[^|&;]*\bof=/dev/(sd[a-z]|hd[a-z]|nvme\d)`),

	// Fork bombs (multiple variants)
	regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|`),
	regexp.MustCompile(`\bforkbomb\b`),

	// Download-and-execute pipelines
	regexp.MustCompile(`\b(curl|wget)\b[^|&;]*\|[^|&;]*(bash|sh|zsh|python|perl|ruby)\b`),

	// Dangerous eval patterns
	regexp.MustCompile(`\beval\b[^|&;]*\$\(`),
	regexp.MustCompile(`\beval\b[^|&;]*` + "`"),

	// History wiping
	regexp.MustCompile(`\bhistory\s+-c\b`),
	regexp.MustCompile(`/dev/null\s*>[^|&;]*bash_history`),

	// Sudo + destructive (flag abuse)
	regexp.MustCompile(`\bsudo\b[^|&;]*\bmkfs\b`),
	regexp.MustCompile(`\bsudo\b[^|&;]*\bdd\b[^|&;]*\bof=/dev`),
}

// protectedWritePaths are directories that shell write-commands must never target.
var protectedWritePaths = []string{
	"/bin",
	"/boot",
	"/dev",
	"/etc",
	"/lib",
	"/lib64",
	"/proc",
	"/root",
	"/run",
	"/sbin",
	"/sys",
	"/usr",
	"/var",
}

// SecurityValidator provides Bash-specific security validation.
type SecurityValidator struct{}

// NewSecurityValidator creates a new Bash security validator.
func NewSecurityValidator() *SecurityValidator { return &SecurityValidator{} }

// ValidateCommand checks a command against hardcoded dangerous patterns.
// Returns a SecurityViolation (never nil when dangerous) or nil when safe.
func (v *SecurityValidator) ValidateCommand(command string) *SecurityViolation {
	// A command whose visible text differs from its real text cannot be approved by reading it.
	if r, found := textsafe.DeceptiveRune(command); found {
		return &SecurityViolation{
			Command:   command,
			Violation: "hidden-character",
			Severity:  SeverityCritical,
			Reason:    fmt.Sprintf("the command contains an invisible or direction-changing character (U+%04X) that can make it read differently from what it does", r),
		}
	}
	parsed := NewParser().ParseCommand(command)
	for _, seg := range parsed.Segments {
		if viol := validateSegmentCommand(seg.Text); viol != nil {
			viol.Command = command
			return viol
		}
	}
	return nil
}

// ValidateWorkspace ensures that write commands cannot escape the workspace via
// relative traversal and that no segment writes to a protected system path.
// Absolute write targets outside the workspace are allowed to continue into the
// normal permission flow so the user can explicitly approve paths like /tmp.
func (v *SecurityValidator) ValidateWorkspace(command string, workspaceRoot string) *SecurityViolation {
	ws, err := workspace.New(workspaceRoot)
	if err != nil {
		return &SecurityViolation{
			Command:   command,
			Violation: "workspace",
			Severity:  SeverityHigh,
			Reason:    fmt.Sprintf("invalid workspace: %v", err),
		}
	}
	parsed := NewParser().ParseCommand(command)
	for _, seg := range parsed.Segments {
		if viol := validateSegmentWorkspace(seg.Text, ws); viol != nil {
			viol.Command = command
			return viol
		}
	}
	return nil
}

func validateSegmentWorkspace(segment string, ws *workspace.Context) *SecurityViolation {
	_, actual := parseEnvVars(segment)
	fields := strings.Fields(actual)
	if len(fields) == 0 {
		return nil
	}

	cmdIndex := 0
	cmd := strings.Trim(fields[0], "\"'")
	if filepath.Base(cmd) == "sudo" && len(fields) > 1 {
		cmdIndex = 1
		cmd = strings.Trim(fields[1], "\"'")
	}

	baseCmd := filepath.Base(cmd)
	removalCmd := baseCmd == "rm" || baseCmd == "rmdir"
	afterDoubleDash := false

	for i := cmdIndex + 1; i < len(fields); i++ {
		token := cleanPathToken(fields[i])
		if token == "" {
			continue
		}
		if token == "--" {
			afterDoubleDash = true
			continue
		}
		if !afterDoubleDash && strings.HasPrefix(token, "-") {
			continue
		}

		if strings.HasPrefix(token, "~") {
			return &SecurityViolation{
				Violation: token,
				Severity:  SeverityHigh,
				Reason:    "bash path escapes workspace: home-directory expansion is not allowed",
			}
		}

		if filepath.IsAbs(token) {
			if isProtectedAbsolutePath(token) {
				return &SecurityViolation{
					Violation: token,
					Severity:  SeverityCritical,
					Reason:    "bash absolute path targets protected system path",
				}
			}
			continue
		}

		if removalCmd {
			if _, err := ws.Resolve(token); err != nil {
				return &SecurityViolation{
					Violation: token,
					Severity:  SeverityHigh,
					Reason:    "bash relative removal path escapes workspace",
				}
			}
		}
	}
	return nil
}

func validateSegmentCommand(segment string) *SecurityViolation {
	for _, re := range dangerousPatterns {
		if re.MatchString(segment) {
			return &SecurityViolation{
				Violation: re.String(),
				Severity:  SeverityCritical,
				Reason:    fmt.Sprintf("matches dangerous pattern: %s", re.String()),
			}
		}
	}
	return validateRemovalSegment(segment)
}

func validateRemovalSegment(segment string) *SecurityViolation {
	_, actual := parseEnvVars(segment)
	fields := strings.Fields(actual)
	if len(fields) == 0 {
		return nil
	}

	cmdIndex := 0
	cmd := filepath.Base(strings.Trim(fields[0], "\"'"))
	if cmd == "sudo" && len(fields) > 1 {
		cmdIndex = 1
		cmd = filepath.Base(strings.Trim(fields[1], "\"'"))
	}
	if cmd != "rm" && cmd != "rmdir" {
		return nil
	}

	afterDoubleDash := false
	for i := cmdIndex + 1; i < len(fields); i++ {
		token := cleanPathToken(fields[i])
		if token == "" {
			continue
		}
		if token == "--" {
			afterDoubleDash = true
			continue
		}
		if !afterDoubleDash && strings.HasPrefix(token, "-") {
			continue
		}
		if !shouldValidateRemovalTarget(token) {
			continue
		}
		if err := shared.CheckDangerousRemovalPath(token, string(filepath.Separator)); err != nil {
			return &SecurityViolation{
				Violation: token,
				Severity:  SeverityCritical,
				Reason:    err.Error(),
			}
		}
	}
	return nil
}

func shouldValidateRemovalTarget(token string) bool {
	clean := filepath.Clean(token)
	if strings.HasPrefix(token, "~") || filepath.IsAbs(token) {
		return true
	}
	if strings.ContainsAny(token, "*?[") {
		return true
	}
	if clean == "." || clean == ".." {
		return true
	}
	return strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// ValidateGitCommand performs deep analysis of git subcommands to block
// dangerous options that bypass workspace restrictions or execute arbitrary code.
// Returns a SecurityViolation when the git invocation is unsafe, nil otherwise.
func (v *SecurityValidator) ValidateGitCommand(command string) *SecurityViolation {
	_, actual := parseEnvVars(command)
	fields := strings.Fields(actual)
	if len(fields) == 0 || fields[0] != "git" {
		return nil
	}

	// Dangerous global git options that can redirect the git dir or execute hooks
	dangerousGlobalOpts := []string{
		"-C", "-c", "--git-dir", "--work-tree", "--namespace",
		"--super-prefix", "--exec-path",
	}
	for i, f := range fields[1:] {
		for _, bad := range dangerousGlobalOpts {
			if f == bad || strings.HasPrefix(f, bad+"=") {
				return &SecurityViolation{
					Command:   command,
					Violation: f,
					Severity:  SeverityCritical,
					Reason:    fmt.Sprintf("git global option '%s' can redirect repository or execute code", bad),
				}
			}
		}
		// subcommand starts here; stop scanning globals
		if !strings.HasPrefix(f, "-") {
			_ = i
			break
		}
	}

	// Find the subcommand
	subCmd := ""
	subCmdIdx := -1
	for i, f := range fields[1:] {
		if !strings.HasPrefix(f, "-") {
			subCmd = f
			subCmdIdx = i + 1
			break
		}
	}
	if subCmd == "" {
		return nil
	}

	// Allowed read-only subcommands (no side effects)
	safeSubCmds := map[string]bool{
		"status": true, "log": true, "diff": true, "show": true,
		"branch": true, "tag": true, "stash": true, "describe": true,
		"shortlog": true, "rev-parse": true, "rev-list": true,
		"ls-files": true, "ls-tree": true, "cat-file": true,
		"blame": true, "grep": true, "bisect": true,
	}

	// Dangerous subcommands always require approval
	dangerousSubCmds := map[string]bool{
		"push": true, "fetch": true, "pull": true, "clone": true,
		"remote": true, "submodule": true, "filter-branch": true,
		"gc": true, "fsck": true, "clean": true, "reset": true,
		"rebase": true, "merge": true, "cherry-pick": true,
	}

	if dangerousSubCmds[subCmd] {
		return &SecurityViolation{
			Command:   command,
			Violation: subCmd,
			Severity:  SeverityHigh,
			Reason:    fmt.Sprintf("git subcommand '%s' modifies repository state or network", subCmd),
		}
	}

	if !safeSubCmds[subCmd] {
		return nil // unknown subcommand — let permission layer decide
	}

	// For safe subcommands, check for options that execute code
	dangerousSubOpts := []string{
		"--output", "--exec", "--ext-diff", "--textconv",
		"--upload-pack", "--receive-pack",
	}
	args := fields[subCmdIdx+1:]
	for _, arg := range args {
		for _, bad := range dangerousSubOpts {
			if arg == bad || strings.HasPrefix(arg, bad+"=") {
				return &SecurityViolation{
					Command:   command,
					Violation: arg,
					Severity:  SeverityHigh,
					Reason:    fmt.Sprintf("git option '%s' can execute arbitrary code", bad),
				}
			}
		}
	}

	return nil
}

// GetCommandClassification returns the classification of a command.
// Delegates to the shared classifyCommandName so there is a single lookup table.
func (v *SecurityValidator) GetCommandClassification(command string) CommandClassification {
	_, actual := parseEnvVars(command)
	fields := strings.Fields(actual)
	if len(fields) == 0 {
		return ClassificationUnknown
	}
	// Strip sudo prefix for classification purposes.
	cmd := fields[0]
	if cmd == "sudo" && len(fields) > 1 {
		cmd = fields[1]
	}

	switch classifyCommandName(cmd) {
	case CommandTypeRead:
		return ClassificationReadOnly
	case CommandTypeSearch:
		return ClassificationSearch
	case CommandTypeWrite:
		return ClassificationWrite
	case CommandTypeStateChange:
		return ClassificationStateChange
	case CommandTypeVersionControl:
		return ClassificationVersionControl
	}
	return ClassificationUnknown
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func cleanPathToken(raw string) string {
	token := strings.Trim(raw, "\"'`(),")
	for _, prefix := range []string{"2>>", "2>", "&>>", "&>", ">>", ">", "<"} {
		if strings.HasPrefix(token, prefix) {
			token = strings.TrimPrefix(token, prefix)
			break
		}
	}
	return strings.Trim(token, "\"'")
}

func isProtectedWritePath(path string) bool {
	clean := filepath.Clean(path)
	for _, root := range protectedWritePaths {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isProtectedAbsolutePath(path string) bool {
	return isProtectedWritePath(path)
}

// ── Types ──────────────────────────────────────────────────────────────────────

// Severity represents the severity of a security violation.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// CommandClassification represents the classification of a command.
type CommandClassification string

const (
	ClassificationUnknown        CommandClassification = "unknown"
	ClassificationReadOnly       CommandClassification = "readonly"
	ClassificationSearch         CommandClassification = "search"
	ClassificationWrite          CommandClassification = "write"
	ClassificationStateChange    CommandClassification = "state_change"
	ClassificationVersionControl CommandClassification = "version_control"
	ClassificationShell          CommandClassification = "shell"
)

// SecurityViolation represents a security violation.
type SecurityViolation struct {
	Command   string
	Violation string
	Severity  Severity
	Reason    string
}

func (v *SecurityViolation) Error() string {
	return v.Reason + ": " + v.Command
}
