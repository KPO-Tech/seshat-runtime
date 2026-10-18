package model

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/dialog"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

func TestQuitFromThePaletteAsksForConfirmation(t *testing.T) {
	sty := styles.SeshatDark()
	m := &UI{com: &common.Common{Styles: &sty}, dialog: dialog.NewOverlay()}

	m.applyDialogAction(dialog.ActionOpenDialog{DialogID: dialog.QuitID})

	if !m.dialog.ContainsDialog(dialog.QuitID) {
		t.Fatal("quitting from the palette must open the confirmation dialog, not quit at once")
	}
}
