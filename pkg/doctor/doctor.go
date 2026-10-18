package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	dbpkg "github.com/KPO-Tech/seshat-runtime/internal/db"
	"github.com/KPO-Tech/seshat-runtime/pkg/config"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// Status is the outcome of a check: ok, warn, fail or skipped.
type Status string

const (
	StatusOK      Status = "ok"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

// Check is one line of the doctor's report: what was checked, its Status, a Detail that says what was found, the Advice to fix it, and the Section it belongs to.
type Check struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Advice  string `json:"advice,omitempty"`
	Section string `json:"section,omitempty"`
}

// Report is the result of a doctor run: the version, the platform, the runtime root and configuration path, and every Check.
type Report struct {
	Version     string  `json:"version"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	RuntimeRoot string  `json:"runtime_root"`
	ConfigPath  string  `json:"config_path,omitempty"`
	Checks      []Check `json:"checks"`
}

// HasFailures reports whether any check of the report has failed. Warnings do not count.
func (r Report) HasFailures() bool {
	for _, check := range r.Checks {
		if check.Status == StatusFail {
			return true
		}
	}
	return false
}

// PrintText writes the report as plain text: a header with the version, the platform and the runtime root, then the checks grouped by section with their status label, detail and advice.
func PrintText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Seshat doctor")
	fmt.Fprintln(out, "-------------")
	fmt.Fprintf(out, "Version      : %s\n", emptyAs(report.Version, "dev"))
	fmt.Fprintf(out, "Platform     : %s/%s\n", report.OS, report.Arch)
	fmt.Fprintf(out, "Runtime root : %s\n", report.RuntimeRoot)
	fmt.Fprintln(out, "")

	currentSection := ""
	for _, check := range report.Checks {
		if check.Section != currentSection {
			if currentSection != "" {
				fmt.Fprintln(out, "")
			}
			currentSection = check.Section
			fmt.Fprintf(out, "%s\n", titleCase(currentSection))
		}
		fmt.Fprintf(out, "  %s %-18s %s\n", StatusLabel(check.Status), check.Name, check.Detail)
		if check.Advice != "" {
			fmt.Fprintf(out, "      %s\n", check.Advice)
		}
	}
}

// StatusLabel returns the fixed-width label printed before a check in the text report: [ok], [warn], [fail], or [skip] for any other status.
func StatusLabel(status Status) string {
	switch status {
	case StatusOK:
		return "[ok]  "
	case StatusWarn:
		return "[warn]"
	case StatusFail:
		return "[fail]"
	default:
		return "[skip]"
	}
}

// Options configures a doctor run: the version to show and the configuration to check.
type Options struct {
	Version string
	Config  config.Config
}

// Run performs the checks (the runtime directories and the configuration, the model and the credential of the provider, and the tools the runtime relies on, git and uv) and returns the report.
func Run(ctx context.Context, opts Options) Report {
	cfg := opts.Config
	report := Report{
		Version:     opts.Version,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		RuntimeRoot: config.EffectiveRuntimeRoot(cfg),
		ConfigPath:  filepath.Join(config.EffectiveRuntimeRoot(cfg), "config.yaml"),
	}

	add := func(section, name string, status Status, detail, advice string) {
		report.Checks = append(report.Checks, Check{
			Section: section,
			Name:    name,
			Status:  status,
			Detail:  detail,
			Advice:  advice,
		})
	}

	checkDir(add, "runtime", "runtime root", report.RuntimeRoot, true)
	checkFileOptional(add, "runtime", "config file", report.ConfigPath)

	workingDir := strings.TrimSpace(cfg.Cwd)
	if workingDir == "" || workingDir == "." {
		if cwd, err := os.Getwd(); err == nil {
			workingDir = cwd
		}
	}
	checkDir(add, "runtime", "working directory", runtimepath.ExpandTilde(workingDir), false)

	sessionDB := config.EffectiveSessionDBPath(cfg)
	checkSQLite(ctx, add, "storage", "session database", sessionDB)

	model := resolveModel(cfg)
	if model.Model == "" {
		add("provider", "model", StatusWarn, "no model configured", "Run `seshat config --model provider:model` or set SESHAT_MODEL.")
	} else {
		add("provider", "model", StatusOK, fmt.Sprintf("%s:%s", model.Provider, model.Model), "")
	}
	if model.Provider == "" {
		add("provider", "api key", StatusSkipped, "no provider resolved", "Configure a model first.")
	} else if key := config.ResolveAPIKey(cfg, model.Provider); strings.TrimSpace(key) != "" {
		add("provider", "api key", StatusOK, fmt.Sprintf("%s credential is configured", model.Provider), "")
	} else if model.Provider == sdk.APIProviderOllama {
		add("provider", "api key", StatusSkipped, "ollama does not require an API key", "")
	} else {
		add("provider", "api key", StatusWarn, fmt.Sprintf("no credential found for %s", model.Provider), "Run `seshat config --provider ... --api-key ...` or set the provider environment variable.")
	}

	checkCommand(add, "tools", "git", "git", "--version")
	checkCommand(add, "tools", "uv", "uv", "--version")
	checkDocling(add, cfg)
	checkSandbox(add)

	return report
}

func checkDir(add func(string, string, Status, string, string), section, name, path string, createExpected bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		add(section, name, StatusWarn, "not configured", "")
		return
	}
	path = runtimepath.ExpandTilde(path)
	info, err := os.Stat(path)
	if err != nil {
		status := StatusFail
		advice := "Create the directory or update the config path."
		if createExpected {
			status = StatusWarn
			advice = "This is created automatically on first run."
		}
		add(section, name, status, fmt.Sprintf("%s (%v)", path, err), advice)
		return
	}
	if !info.IsDir() {
		add(section, name, StatusFail, path+" is not a directory", "Point this setting at a directory.")
		return
	}
	add(section, name, StatusOK, path, "")
}

func checkFileOptional(add func(string, string, Status, string, string), section, name, path string) {
	if _, err := os.Stat(path); err == nil {
		add(section, name, StatusOK, path, "")
	} else {
		add(section, name, StatusSkipped, "not found; defaults/env will be used", "")
	}
}

func checkSQLite(ctx context.Context, add func(string, string, Status, string, string), section, name, path string) {
	if strings.TrimSpace(path) == "" {
		add(section, name, StatusWarn, "no SQLite path configured", "Set SESHAT_SESSION_DB_PATH or let Seshat use its default runtime path.")
		return
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); err != nil {
		add(section, name, StatusWarn, fmt.Sprintf("%s parent is not available (%v)", path, err), "It will be created on first run, or set SESHAT_SESSION_DB_PATH to a writable location.")
		return
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		add(section, name, StatusFail, fmt.Sprintf("%s (%v)", path, err), "Fix permissions or choose another session DB path.")
		return
	}
	db, err := dbpkg.Open(ctx, dbpkg.DefaultSQLiteConfig(path))
	if err != nil {
		add(section, name, StatusFail, fmt.Sprintf("%s (%v)", path, err), "Check file permissions and SQLite compatibility.")
		return
	}
	defer db.Close()
	if err := db.SQL().PingContext(ctx); err != nil {
		add(section, name, StatusFail, fmt.Sprintf("%s (%v)", path, err), "Check file permissions and SQLite compatibility.")
		return
	}
	add(section, name, StatusOK, path, "")
}

func checkCommand(add func(string, string, Status, string, string), section, name, command string, args ...string) {
	path, err := exec.LookPath(command)
	if err != nil {
		add(section, name, StatusWarn, "not found on PATH", fmt.Sprintf("Install `%s` or add it to PATH.", command))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		add(section, name, StatusWarn, path, strings.TrimSpace(string(out)))
		return
	}
	add(section, name, StatusOK, fmt.Sprintf("%s (%s)", strings.TrimSpace(string(out)), path), "")
}

// checkSandbox says what confines the commands of the bash tool. "none" is a warning, not a pass: such commands run
// with the user's full rights.
func checkSandbox(add func(string, string, Status, string, string)) {
	switch backend := sdk.SandboxBackend(); backend {
	case "landlock":
		add("tools", "sandbox", StatusOK, "commands are confined by Landlock (write only in the working folder)", "")
	case "srt":
		add("tools", "sandbox", StatusOK, "commands are confined by srt (write only in the working folder and the temp folder, limited network)", "")
	default:
		add("tools", "sandbox", StatusWarn, "commands are NOT confined: they run with your full rights",
			"Install srt (npm i -g @anthropic-ai/sandbox-runtime; Windows support is alpha) or use Docker. Linux with Landlock is confined automatically.")
	}
}

func checkDocling(add func(string, string, Status, string, string), cfg config.Config) {
	if strings.TrimSpace(cfg.DocumentReaderURL) != "" {
		add("tools", "document_reader", StatusOK, "external URL configured: "+cfg.DocumentReaderURL, "")
		return
	}
	add("tools", "document_reader", StatusOK, "built-in reader (DOCX, PPTX, XLSX, PDF with a text layer)", "Set DOCUMENT_READER_URL to a document-reader service (seshat-intelligence) for scans and complex layouts.")
}

func resolveModel(cfg config.Config) sdk.ModelIdentifier {
	raw := strings.TrimSpace(cfg.Model)
	model := config.ParseModelIdentifier(raw)
	if config.HasExplicitProviderPrefix(raw) {
		return model
	}
	provider := config.DetectProviderFromModel(raw)
	if provider == "" {
		_, provider = config.EffectiveAPIKeyAndProvider(cfg)
	}
	model.Provider = provider
	return model
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func titleCase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// checkCommandTimeout bounds the `<tool> --version` style probes: a tool that hangs must not hang the diagnosis.
const checkCommandTimeout = 10 * time.Second
