package model

import (
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/app"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// showInfoMsg puts a message in the status bar and schedules its removal.
func (m *UI) showInfoMsg(msg util.InfoMsg) tea.Cmd {
	if msg.Type == util.InfoTypeError {
		slog.Error("Error reported", "error", msg.Msg)
	}
	m.status.SetInfoMsg(msg)
	ttl := msg.TTL
	if ttl <= 0 {
		ttl = DefaultStatusTTL
	}
	return clearInfoMsgCmd(ttl)
}

// showUpdateAvailable tells the user a newer version exists.
func (m *UI) showUpdateAvailable(msg app.UpdateAvailableMsg) tea.Cmd {
	text := fmt.Sprintf("Seshat update available: v%s → v%s.", msg.CurrentVersion, msg.LatestVersion)
	if msg.IsDevelopment {
		text = fmt.Sprintf("This is a development version of Seshat. The latest version is v%s.", msg.LatestVersion)
	}
	ttl := 10 * time.Second
	m.status.SetInfoMsg(util.InfoMsg{
		Type: util.InfoTypeUpdate,
		Msg:  text,
		TTL:  ttl,
	})
	return clearInfoMsgCmd(ttl)
}
