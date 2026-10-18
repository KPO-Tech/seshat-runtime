package model

import (
	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

// sandboxLabel says what confines the shell commands of this session, and whether anything does. A host that shows a
// lock for unconfined commands misleads, so "none" is said as plainly as the others.
func sandboxLabel(backend string) (label string, confined bool) {
	switch backend {
	case bash.BackendLandlock:
		return "Confined by Landlock", true
	case bash.BackendSrt:
		return "Confined by srt", true
	}
	return "Commands not confined", false
}

// sandboxInfo renders the one-line confinement status under the working directory.
func (m *UI) sandboxInfo(width int) string {
	t := m.com.Styles
	label, confined := sandboxLabel(bash.SandboxBackend())
	icon := t.Resource.OnlineIcon.String()
	text := t.Resource.AdditionalText.Render(label)
	if !confined {
		icon = t.Resource.ErrorIcon.String()
		text = t.Resource.Name.Render(label)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(icon + " " + text)
}

// sandboxChip is the short form shown in the one-line headers: the backend name in the muted colour when commands are
// confined, "not confined" in the error colour when nothing confines them. Said every time, since the landing and chat
// pages have no sidebar.
func (m *UI) sandboxChip() string {
	return sandboxChipFor(m.com.Styles, bash.SandboxBackend())
}

func sandboxChipFor(t *styles.Styles, backend string) string {
	if _, confined := sandboxLabel(backend); !confined {
		return t.LSP.ErrorDiagnostic.Render("not confined")
	}
	return t.Sidebar.WorkingDir.Render(backend)
}
