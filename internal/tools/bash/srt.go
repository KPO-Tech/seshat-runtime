package bash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/textsafe"
)

// The srt backend confines a command with Anthropic's sandbox-runtime (https://github.com/anthropic-experimental/sandbox-runtime,
// Apache-2.0): Seatbelt on macOS, bubblewrap on Linux, a dedicated low-privilege user with ACLs and a network filter on
// Windows (alpha, needs a one-time `srt windows-install`). Seshat does not bundle it: it is used when the `srt` command
// is installed, and only on a host where Landlock is not available. Nothing changes when it is absent.
const (
	// EnvSrt names the srt executable to use, or turns the backend off with "off", "0" or "false".
	EnvSrt = "SESHAT_SRT"
	// EnvSrtDomains adds comma-separated domains the confined command may reach, on top of defaultSrtDomains.
	EnvSrtDomains = "SESHAT_SRT_ALLOWED_DOMAINS"
	// EnvSrtWrite adds comma-separated paths the confined command may write, on top of the workspace and the temp folder.
	EnvSrtWrite = "SESHAT_SRT_ALLOW_WRITE"
)

// defaultSrtDomains are the hosts a coding agent needs for ordinary work: source hosting, package registries and
// documentation. srt denies every other destination, since it does not accept a bare "*".
var defaultSrtDomains = []string{
	"github.com", "*.github.com", "*.githubusercontent.com", "gitlab.com", "*.gitlab.com", "bitbucket.org",
	"registry.npmjs.org", "*.npmjs.org", "yarnpkg.com", "registry.yarnpkg.com",
	"pypi.org", "*.pypi.org", "files.pythonhosted.org",
	"proxy.golang.org", "sum.golang.org", "go.dev", "pkg.go.dev",
	"crates.io", "*.crates.io", "static.crates.io", "index.crates.io",
	"rubygems.org", "*.rubygems.org",
	"repo.maven.apache.org", "repo1.maven.org", "plugins.gradle.org",
	"*.docker.io", "*.docker.com",
}

// secretFolders are read-denied inside the confinement: they hold keys that no build or test needs.
var secretFolders = []string{"~/.ssh", "~/.aws", "~/.gnupg", "~/.config/gcloud", "~/.azure", "~/.kube"}

type srtSettings struct {
	Filesystem srtFilesystem `json:"filesystem"`
	Network    srtNetwork    `json:"network"`
}

type srtFilesystem struct {
	DenyRead   []string `json:"denyRead"`
	AllowWrite []string `json:"allowWrite"`
	DenyWrite  []string `json:"denyWrite"`
}

type srtNetwork struct {
	AllowedDomains []string `json:"allowedDomains"`
	DeniedDomains  []string `json:"deniedDomains"`
}

var srtLookup = struct {
	once sync.Once
	path string
}{}

// srtPath returns the srt executable, or "" when there is none or the backend is turned off.
func srtPath() string {
	srtLookup.once.Do(func() {
		setting := strings.TrimSpace(os.Getenv(EnvSrt))
		switch strings.ToLower(setting) {
		case "off", "0", "false", "no":
			return
		}
		name := "srt"
		if setting != "" {
			name = setting
		}
		p, err := exec.LookPath(name)
		if err != nil {
			return
		}
		// srt can be installed and still unable to confine (on Windows it needs a one-time setup; on Linux it needs
		// bubblewrap). A command wrapped by such an srt would fail every time, so it is tried once first.
		if problem := probeSrt(p); problem != "" {
			slog.Warn("bash tool: srt is installed but cannot confine commands here; they run as if it were absent", "problem", problem)
			return
		}
		srtLookup.path = p
	})
	return srtLookup.path
}

// probeSrt runs a trivial command through srt and returns what srt said when it could not run it, or "" when it did.
func probeSrt(srt string) string {
	file, err := srtSettingsFile(buildSrtSettings(os.TempDir()))
	if err != nil {
		return err.Error()
	}
	shell, args := "sh", []string{"-c", "exit 0"}
	if runtime.GOOS == "windows" {
		shell, args = "cmd", []string{"/C", "exit 0"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, srt, append([]string{"--settings", file, "--", shell}, args...)...).CombinedOutput() // #nosec G702 -- srt is the executable the user installed or named in SESHAT_SRT; the arguments are fixed
	if err == nil {
		return ""
	}
	return strings.TrimSpace(textsafe.SanitizeOutput(string(out)))
}

func srtAvailable() bool { return srtPath() != "" }

// splitList reads a comma-separated environment value.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// buildSrtSettings returns the confinement for a command run in workspaceRoot: write the workspace and the temp
// folder, read everything except the secret folders, reach only the allowed domains.
func buildSrtSettings(workspaceRoot string) srtSettings {
	return srtSettings{
		Filesystem: srtFilesystem{
			DenyRead:   secretFolders,
			AllowWrite: append([]string{workspaceRoot, os.TempDir()}, splitList(os.Getenv(EnvSrtWrite))...),
			DenyWrite:  []string{},
		},
		Network: srtNetwork{
			AllowedDomains: append(append([]string{}, defaultSrtDomains...), splitList(os.Getenv(EnvSrtDomains))...),
			DeniedDomains:  []string{},
		},
	}
}

// srtSettingsFile writes the settings to a file named after their content and returns its path. The same settings
// reuse the same file, so no file piles up per command.
func srtSettingsFile(settings srtSettings) (string, error) {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	dir := filepath.Join(os.TempDir(), "seshat-srt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".json")
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return path, nil
	}
	return path, os.WriteFile(path, data, 0o600)
}

// srtCommand wraps shell and args so that srt runs them confined. ok is false when srt is not installed, there is no
// workspace to confine to, or the settings could not be written; the caller then runs the command as it would have.
func srtCommand(shell string, args []string, workspaceRoot string) (path string, wrapped []string, ok bool) {
	srt := srtPath()
	if srt == "" || workspaceRoot == "" {
		return shell, args, false
	}
	file, err := srtSettingsFile(buildSrtSettings(workspaceRoot))
	if err != nil {
		slog.Warn("bash tool: could not write the srt settings; the command runs without srt", "error", err)
		return shell, args, false
	}
	wrapped = append([]string{"--settings", file, "--", shell}, args...)
	return srt, wrapped, true
}
