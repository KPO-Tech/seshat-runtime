package dialog

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/csync"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

// Codex is a ChatGPT account: choosing it opens the device sign-in, not an API key field.
func TestCodexOpensTheDeviceSignInNotAnAPIKeyField(t *testing.T) {
	st := styles.SeshatDark()
	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig](), Models: map[config.SelectedModelType]config.SelectedModel{}}
	com := &common.Common{Workspace: panelStubWorkspace{cfg: cfg}, Styles: &st}

	dlg, cmd := NewOAuthCodex(com, false, catwalk.Provider{ID: "codex", Name: "Codex"}, config.SelectedModel{}, config.SelectedModelTypeLarge)
	if dlg == nil || cmd == nil {
		t.Fatal("the dialog and its first command (the request of the code) must be returned")
	}
	if dlg.ID() != OAuthID {
		t.Fatalf("dialog id = %q, want the OAuth dialog", dlg.ID())
	}
	if got := dlg.oAuthProvider.name(); got != "Codex (ChatGPT)" {
		t.Fatalf("provider name = %q", got)
	}
}
