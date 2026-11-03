package access

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// shellWrappers lists prefix patterns that indicate a shell wrapper command.
// When the command starts with one of these, the inner argument is extracted
// and each sub-command is evaluated individually — mirroring Codex's
// parse_shell_lc_plain_commands approach.
var shellWrappers = [][]string{
	{"bash", "-c"},
	{"bash", "-lc"},
	{"sh", "-c"},
	{"sh", "-lc"},
	{"zsh", "-c"},
	{"zsh", "-lc"},
	{"/bin/bash", "-c"},
	{"/bin/bash", "-lc"},
	{"/bin/sh", "-c"},
	{"/bin/sh", "-lc"},
	{"/bin/zsh", "-c"},
	{"/bin/zsh", "-lc"},
}

// CommandPolicy is the single authoritative command safety classifier.
// It replaces the scattered PermissionValidator in the bash package.
//
// Two-tier model (mirrors Codex):
//   - IsKnownSafe: explicit allowlist → bypass approval entirely
//   - Evaluate:    deny/ask/allow based on danger fragments and command type
type CommandPolicy struct {
	denyFragments []string
	denyPatterns  []*regexp.Regexp
	askCommands   map[string]bool
}

func NewDefaultCommandPolicy() *CommandPolicy {
	return &CommandPolicy{
		// Hard-deny fragments: commands that must never run regardless of mode.
		denyFragments: []string{
			"dd if=/dev/zero",
			"mkfs",
		},
		denyPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\brm\b[^|&;]*\s+-[^\s]*[rf][^\s]*\s+/(\s|$)`),
			regexp.MustCompile(`(?i)\brm\b[^|&;]*\s+-[^\s]*[rf][^\s]*\s+/\*(\s|$)`),
		},
		// Ask commands: the bare command name requires explicit approval.
		askCommands: map[string]bool{
			"rm":     true,
			"dd":     true,
			"mkfs":   true,
			"fdisk":  true,
			"format": true,
			"chmod":  true,
			"chown":  true,
		},
	}
}

// ─── Public API ───────────────────────────────────────────────────────────────

// DenyFragments returns the substrings that cause a command to be hard-denied
// regardless of mode, for introspection (e.g. the get_config tool).
func (p *CommandPolicy) DenyFragments() []string {
	if p == nil {
		p = NewDefaultCommandPolicy()
	}
	return append([]string(nil), p.denyFragments...)
}

// DenyPatterns returns the regex source strings that cause a command to be
// hard-denied regardless of mode, for introspection (e.g. the get_config
// tool).
func (p *CommandPolicy) DenyPatterns() []string {
	if p == nil {
		p = NewDefaultCommandPolicy()
	}
	patterns := make([]string, 0, len(p.denyPatterns))
	for _, re := range p.denyPatterns {
		patterns = append(patterns, re.String())
	}
	return patterns
}

// AskCommands returns the bare command names that always require explicit
// approval, for introspection (e.g. the get_config tool).
func (p *CommandPolicy) AskCommands() []string {
	if p == nil {
		p = NewDefaultCommandPolicy()
	}
	cmds := make([]string, 0, len(p.askCommands))
	for cmd := range p.askCommands {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds)
	return cmds
}

// IsKnownSafe returns true when every sub-command in the expression is on the
// explicit safe allowlist, meaning the entire command can be executed without
// any approval prompt.
//
// Mirrors Codex's is_known_safe_command. If the command is a shell wrapper
// (bash -c "…") the inner script is split into segments and each is checked.
func (p *CommandPolicy) IsKnownSafe(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if inner := extractShellWrapperInner(command); inner != "" {
		return p.isKnownSafeComposed(inner)
	}
	return isKnownSafeSegment(command)
}

// Evaluate returns the policy decision for a command.
//
// Order of evaluation:
//  1. Known-safe allowlist → Allow (no approval prompt needed)
//  2. Shell wrapper → evaluate composed inner
//  3. Deny fragment match → Deny
//  4. Ask-command list → Ask
//  5. Command type classification: write/vcs/unknown → Ask, read/search/state → Allow
func (p *CommandPolicy) Evaluate(command string) DecisionResult {
	if p == nil {
		p = NewDefaultCommandPolicy()
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return DecisionResult{Decision: DecisionDeny, Reason: "command is required"}
	}
	if p.IsKnownSafe(command) {
		return DecisionResult{Decision: DecisionAllow, Reason: "command is on the safe allowlist"}
	}
	if inner := extractShellWrapperInner(command); inner != "" {
		return p.evaluateComposed(inner)
	}
	return p.evaluateSingle(command)
}

// ─── Internal evaluation ──────────────────────────────────────────────────────

// evaluateComposed splits a composed command string on shell operators and
// returns the most restrictive decision across all segments.
func (p *CommandPolicy) evaluateComposed(composed string) DecisionResult {
	segments := splitShellSegments(composed)
	worst := DecisionResult{Decision: DecisionAllow, Reason: "composed command allowed"}
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		r := p.Evaluate(seg) // recurse: inner segment may also be a wrapper
		switch r.Decision {
		case DecisionDeny:
			return r
		case DecisionAsk:
			worst = r
		}
	}
	return worst
}

// evaluateSingle checks a single (non-composed, non-wrapper) command.
func (p *CommandPolicy) evaluateSingle(command string) DecisionResult {
	// 1. Deny fragments/patterns — hardcoded catastrophic patterns
	for _, pattern := range p.denyPatterns {
		if pattern.MatchString(command) {
			return DecisionResult{
				Decision: DecisionDeny,
				Reason:   "command matches deny rule: " + pattern.String(),
			}
		}
	}
	for _, fragment := range p.denyFragments {
		if strings.Contains(command, fragment) {
			return DecisionResult{
				Decision: DecisionDeny,
				Reason:   "command matches deny rule: " + fragment,
			}
		}
	}

	// 2. Ask-command list — commands that always need a human decision
	cmd := firstCommandToken(command)
	if cmd != "" && p.askCommands[cmd] {
		return DecisionResult{
			Decision: DecisionAsk,
			Reason:   "command requires approval: " + cmd,
		}
	}

	// 3. Command-type classification.
	//    Read/State → allow (pure read-only, no side effects).
	//    Write/VCS/Search/Unknown → ask.
	//    Note: safe search commands (grep, find without -exec, rg without --pre…)
	//    are already handled by IsKnownSafe above and never reach this branch.
	//    Reaching here with a search command means options weren't vetted → ask.
	switch classifyCommandToken(cmd) {
	case cmdTypeRead, cmdTypeStateChange:
		return DecisionResult{Decision: DecisionAllow, Reason: "command allowed by policy"}
	}
	return DecisionResult{Decision: DecisionAsk, Reason: "command type requires approval: " + cmd}
}

// ─── Known-safe classification (mirrors Codex + Seshat combined) ──────────────

// isKnownSafeComposed checks that every segment of a composed command is safe.
// Redirections (>, >>, <, |) make a command not auto-approvable.
func (p *CommandPolicy) isKnownSafeComposed(composed string) bool {
	// Redirections to files are never auto-approved (Codex: "ls > out.txt" is unsafe)
	if containsFileRedirection(composed) {
		return false
	}
	segments := splitShellSegments(composed)
	if len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if !isKnownSafeSegment(seg) {
			return false
		}
	}
	return true
}

// isKnownSafeSegment returns true when a single command segment is on the
// explicit allowlist.  Takes the raw segment string (may have env-var prefixes).
func isKnownSafeSegment(segment string) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return true
	}
	// Strip leading VAR=value tokens to reach the actual binary name
	_, actual := parseEnvTokens(segment)
	fields := strings.Fields(actual)
	if len(fields) == 0 {
		return true
	}
	cmd := strings.ToLower(filepath.Base(fields[0]))

	switch cmd {
	// ── Always-safe read-only commands ─────────────────────────────────────
	case "cat", "cd", "cut", "echo", "expr", "false", "grep", "egrep", "fgrep",
		"head", "id", "ls", "dir", "nl", "paste", "pwd", "rev", "seq",
		"sort", "stat", "tail", "tr", "true", "uname", "uniq", "wc",
		"which", "whereis", "whoami", "date", "cal", "uptime", "env",
		"printenv", "type", "command", "hostname",
		"od", "xxd", "hexdump", "file", "du", "df",
		"diff", "diff3", "comm", "cmp",
		"col", "expand", "unexpand", "fold", "fmt", "pr", "strings",
		"bc", "numfmt",
		"pgrep", "ps", "lsof", "ss", "netstat":
		return true

	// ── Commands that are safe only with certain options ───────────────────
	case "base64":
		return isSafeBase64(fields[1:])
	case "find":
		return isSafeFind(fields[1:])
	case "rg":
		return isSafeRg(fields[1:])
	case "sed":
		return isSafeSed(fields[1:])
	case "git":
		return isSafeGit(fields)
	case "tee":
		// tee always writes to a file — never auto-approve
		return false
	case "tar", "zip", "unzip":
		// tar/zip can extract/create files — never auto-approve
		return false
	case "top", "htop":
		// interactive; safe only in non-batch mode (no -b flag)
		for _, arg := range fields[1:] {
			if arg == "-b" || arg == "--batch" {
				return false
			}
		}
		return true
	}
	return false
}

// ─── Per-command option-level safety checks ───────────────────────────────────
// Mirrors Codex's per-command option checking in is_safe_command.rs

func isSafeBase64(args []string) bool {
	for _, arg := range args {
		if arg == "-o" || arg == "--output" {
			return false
		}
		if strings.HasPrefix(arg, "--output=") {
			return false
		}
		// -o<value> inline form, e.g. -ob64.txt
		if strings.HasPrefix(arg, "-o") && arg != "-o" {
			return false
		}
	}
	return true
}

func isSafeFind(args []string) bool {
	// Options that execute commands, delete files, or write to files.
	unsafe := []string{"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fls", "-fprint", "-fprint0", "-fprintf"}
	for _, arg := range args {
		for _, bad := range unsafe {
			if arg == bad {
				return false
			}
		}
	}
	return true
}

func isSafeRg(args []string) bool {
	// Options with args that can execute external commands.
	unsafeWithArg := []string{"--pre", "--hostname-bin"}
	// Options without args that are unsafe.
	unsafeFlag := []string{"--search-zip", "-z"}
	for _, arg := range args {
		for _, bad := range unsafeFlag {
			if arg == bad {
				return false
			}
		}
		for _, bad := range unsafeWithArg {
			if arg == bad || strings.HasPrefix(arg, bad+"=") {
				return false
			}
		}
	}
	return true
}

// isSafeSed returns true only for `sed -n {N|M,N}p` (pure read, no in-place).
// Mirrors Codex's is_valid_sed_n_arg + Seshat's isSafeSed.
func isSafeSed(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, a := range args {
		if a == "-i" || strings.HasPrefix(a, "-i") {
			return false // in-place edit
		}
	}
	// Allow only if -n is present and the expression is NNp / M,Np
	hasN := false
	hasPrint := false
	for _, a := range args {
		if a == "-n" {
			hasN = true
		}
		if sedPrintPattern.MatchString(a) {
			hasPrint = true
		}
	}
	return hasN && hasPrint
}

var sedPrintPattern = regexp.MustCompile(`^\d+(,\d+)?p$`)

// isSafeGit checks git commands for unsafe global options and dangerous
// subcommands. Merges Seshat's ValidateGitCommand and Codex's is_safe_git_command.
func isSafeGit(fields []string) bool {
	if len(fields) == 0 || strings.ToLower(filepath.Base(fields[0])) != "git" {
		return false
	}

	// Safe read-only subcommands — only these are auto-approvable.
	safeSubCmds := map[string]bool{
		"status": true, "log": true, "diff": true, "show": true,
		"branch": true, "describe": true, "rev-parse": true,
		"rev-list": true, "ls-files": true, "ls-tree": true,
		"cat-file": true, "blame": true, "shortlog": true,
	}

	// Scan tokens after "git", skipping global options with values.
	subCmd := ""
	subCmdIdx := -1
	skipNext := false
	for i, arg := range fields[1:] {
		if skipNext {
			skipNext = false
			continue
		}
		// Unsafe global options — any of these disqualifies the command.
		if isUnsafeGitGlobalOption(arg) {
			return false
		}
		// Global options that consume the next token as their value.
		if isGitGlobalOptionWithSeparateValue(arg) {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// First non-option token is the subcommand.
		subCmd = arg
		subCmdIdx = i + 1
		break
	}

	if subCmd == "" || !safeSubCmds[subCmd] {
		return false
	}

	subArgs := fields[subCmdIdx+1:]

	// git branch is safe only for listing queries, not mutations.
	if subCmd == "branch" {
		return isGitBranchReadOnly(subArgs)
	}

	// For other safe subcommands, check for options that can execute code.
	unsafeSubOpts := []string{"--output", "--ext-diff", "--textconv", "--exec"}
	for _, arg := range subArgs {
		for _, bad := range unsafeSubOpts {
			if arg == bad || strings.HasPrefix(arg, bad+"=") {
				return false
			}
		}
	}
	return true
}

// isGitBranchReadOnly returns true only when git branch arguments clearly
// indicate a read-only listing query. Mirrors Codex's git_branch_is_read_only.
func isGitBranchReadOnly(args []string) bool {
	if len(args) == 0 {
		return true // bare `git branch` lists branches
	}
	for _, arg := range args {
		switch arg {
		case "--list", "-l", "--show-current", "-a", "--all",
			"-r", "--remotes", "-v", "-vv", "--verbose":
		default:
			if strings.HasPrefix(arg, "--format=") {
				continue
			}
			// Any other flag or positional arg may mutate branches.
			return false
		}
	}
	return true
}

// isUnsafeGitGlobalOption returns true when arg is a git global option that
// can redirect the git dir, override config, or execute hooks.
func isUnsafeGitGlobalOption(arg string) bool {
	// Inline-value forms: -C<path>, -c<key>=<val>, --git-dir=<path>, …
	for _, prefix := range []string{
		"--config-env=", "--exec-path=", "--git-dir=",
		"--namespace=", "--super-prefix=", "--work-tree=",
	} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	// Inline short forms: -C<path>, -c<key>
	if (strings.HasPrefix(arg, "-C") || strings.HasPrefix(arg, "-c")) && len(arg) > 2 {
		return true
	}
	// Paging flags that redirect output to a pager (not pure read-only)
	switch arg {
	case "-C", "-c", "--config-env", "--exec-path", "--git-dir",
		"--namespace", "--super-prefix", "--work-tree",
		"-p", "--paginate":
		return true
	}
	return false
}

// isGitGlobalOptionWithSeparateValue returns true when arg is a git global
// option that consumes the next token as its value.
func isGitGlobalOptionWithSeparateValue(arg string) bool {
	switch arg {
	case "-C", "-c", "--config-env", "--exec-path", "--git-dir",
		"--namespace", "--super-prefix", "--work-tree":
		return true
	}
	return false
}

// ─── Shell parsing helpers ────────────────────────────────────────────────────

// splitShellSegments splits a command string on &&, ||, ;, | operators.
// Intentionally conservative: processes longer operators first.
func splitShellSegments(composed string) []string {
	// Process operators longest-first to avoid splitting || as two | chars.
	result := []string{composed}
	for _, op := range []string{"&&", "||", ";", "|"} {
		var next []string
		for _, part := range result {
			next = append(next, strings.Split(part, op)...)
		}
		result = next
	}
	return result
}

// extractShellWrapperInner returns the inner command string when the given
// command matches a known shell wrapper prefix (e.g. "bash -c '…'").
func extractShellWrapperInner(command string) string {
	fields := strings.Fields(command)
	for _, wrapper := range shellWrappers {
		if len(fields) < len(wrapper)+1 {
			continue
		}
		match := true
		for i, w := range wrapper {
			if !strings.EqualFold(fields[i], w) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		inner := strings.Join(fields[len(wrapper):], " ")
		return stripOuterQuotes(inner)
	}
	return ""
}

// containsFileRedirection returns true when composed contains a > or >> that
// redirects output to a file (as opposed to being part of a here-doc or
// string literal). Conservative: rejects any unquoted >.
// Mirrors Codex's rejection of "ls > out.txt" as not auto-approvable.
func containsFileRedirection(s string) bool {
	inSingle := false
	runes := []rune(s)
	for i, r := range runes {
		if r == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		if r == '>' {
			// Allow >&1 (fd redirect), reject everything else
			if i+1 < len(runes) && runes[i+1] == '&' {
				continue
			}
			return true
		}
	}
	return false
}

// stripOuterQuotes removes a single layer of matching outer quotes from s.
func stripOuterQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') ||
			(s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// firstCommandToken returns the first non-env-var token of a command string.
func firstCommandToken(command string) string {
	fields := strings.Fields(command)
	for _, field := range fields {
		if strings.Contains(field, "=") && !strings.HasPrefix(field, "/") && !strings.HasPrefix(field, "./") {
			continue
		}
		return filepath.Base(field)
	}
	return ""
}

// parseEnvTokens strips leading VAR=value tokens and returns the remainder.
func parseEnvTokens(command string) (env map[string]string, remainder string) {
	env = make(map[string]string)
	fields := strings.Fields(command)
	for i, field := range fields {
		if idx := strings.IndexByte(field, '='); idx > 0 && !strings.HasPrefix(field, "-") {
			key := field[:idx]
			if isShellIdentifier(key) {
				env[key] = field[idx+1:]
				continue
			}
		}
		return env, strings.Join(fields[i:], " ")
	}
	return env, command
}

func isShellIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, r := range s {
		if i == 0 && r >= '0' && r <= '9' {
			return false
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// ─── Command type classification ──────────────────────────────────────────────

type cmdType int

const (
	cmdTypeRead           cmdType = iota
	cmdTypeSearch         cmdType = iota
	cmdTypeStateChange    cmdType = iota
	cmdTypeWrite          cmdType = iota
	cmdTypeVersionControl cmdType = iota
	cmdTypeUnknown        cmdType = iota
)

func classifyCommandToken(cmd string) cmdType {
	switch cmd {
	case "cat", "head", "tail", "less", "more",
		"ls", "tree", "du", "stat", "file", "wc",
		"which", "whereis", "locate", "diff", "echo",
		"printf", "pwd", "env", "printenv", "type", "command":
		return cmdTypeRead
	case "grep", "rg", "ag", "ack", "find", "fd":
		return cmdTypeSearch
	case "mv", "cp", "mkdir", "touch", "rm", "chmod",
		"chown", "chgrp", "ln", "tee", "dd", "truncate",
		"install", "rename", "rmdir":
		return cmdTypeWrite
	case "cd", "export", "unset", "source", ".":
		return cmdTypeStateChange
	case "git", "svn", "hg", "fossil":
		return cmdTypeVersionControl
	}
	return cmdTypeUnknown
}
