package model

import (
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

func TestSandboxLabelNeverShowsAnUnconfinedHostAsConfined(t *testing.T) {
	for backend, wantConfined := range map[string]bool{
		bash.BackendLandlock: true,
		bash.BackendSrt:      true,
		bash.BackendNone:     false,
		"":                   false,
		"something-new":      false,
	} {
		label, confined := sandboxLabel(backend)
		if confined != wantConfined || label == "" {
			t.Errorf("%q: got (%q, %v), want confined=%v", backend, label, confined, wantConfined)
		}
	}
}

func TestSandboxChipSaysNotConfinedAndNamesTheBackend(t *testing.T) {
	sty := &styles.Styles{}
	if got := sandboxChipFor(sty, bash.BackendNone); !strings.Contains(got, "not confined") {
		t.Errorf("no backend must say so, got %q", got)
	}
	for _, backend := range []string{bash.BackendLandlock, bash.BackendSrt} {
		got := sandboxChipFor(sty, backend)
		if !strings.Contains(got, backend) || strings.Contains(got, "not confined") {
			t.Errorf("%s: got %q", backend, got)
		}
	}
}
