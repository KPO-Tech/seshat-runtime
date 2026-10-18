package bash

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// withSrt makes srtPath report path (or none for "") for the duration of a test.
func withSrt(t *testing.T, path string) {
	t.Helper()
	srtLookup.once.Do(func() {})
	old := srtLookup.path
	srtLookup.path = path
	t.Cleanup(func() { srtLookup.path = old })
}

func TestSrtCommandWrapsTheShell(t *testing.T) {
	withSrt(t, "/usr/bin/srt")
	ws := t.TempDir()

	path, args, ok := srtCommand("/bin/sh", []string{"-c", "ls"}, ws)
	if !ok || path != "/usr/bin/srt" {
		t.Fatalf("want srt to wrap the command, got %q ok=%v", path, ok)
	}
	if args[0] != "--settings" || args[2] != "--" || !slices.Equal(args[3:], []string{"/bin/sh", "-c", "ls"}) {
		t.Fatalf("unexpected arguments %v", args)
	}
	var got srtSettings
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("settings are not valid JSON: %v", err)
	}
	if !slices.Contains(got.Filesystem.AllowWrite, ws) {
		t.Errorf("the workspace must be writable: %v", got.Filesystem.AllowWrite)
	}
	if !slices.Contains(got.Filesystem.DenyRead, "~/.ssh") {
		t.Errorf("secret folders must be unreadable: %v", got.Filesystem.DenyRead)
	}
	if slices.Contains(got.Network.AllowedDomains, "*") {
		t.Error("srt refuses a bare *; never emit it")
	}
}

func TestSrtCommandIsNotUsedWithoutSrtOrWorkspace(t *testing.T) {
	withSrt(t, "")
	if _, _, ok := srtCommand("sh", nil, t.TempDir()); ok {
		t.Error("without srt the command must run as before")
	}
	withSrt(t, "/usr/bin/srt")
	if _, _, ok := srtCommand("sh", nil, ""); ok {
		t.Error("without a workspace there is nothing to confine to")
	}
}

func TestSrtSettingsAddEnvironmentLists(t *testing.T) {
	t.Setenv(EnvSrtDomains, "internal.example.com, *.corp.test")
	t.Setenv(EnvSrtWrite, filepath.Join("a", "b"))
	s := buildSrtSettings("/work")
	if !slices.Contains(s.Network.AllowedDomains, "internal.example.com") || !slices.Contains(s.Network.AllowedDomains, "*.corp.test") {
		t.Errorf("extra domains missing: %v", s.Network.AllowedDomains)
	}
	if !slices.Contains(s.Filesystem.AllowWrite, filepath.Join("a", "b")) {
		t.Errorf("extra write path missing: %v", s.Filesystem.AllowWrite)
	}
}

func TestSandboxBackendPrefersLandlockThenSrt(t *testing.T) {
	withSrt(t, "/usr/bin/srt")
	if landlockAvailable() {
		if SandboxBackend() != BackendLandlock {
			t.Error("Landlock comes first where it exists")
		}
		return
	}
	if SandboxBackend() != BackendSrt {
		t.Errorf("want srt, got %s", SandboxBackend())
	}
	withSrt(t, "")
	if SandboxBackend() != BackendNone {
		t.Errorf("want none, got %s", SandboxBackend())
	}
}
