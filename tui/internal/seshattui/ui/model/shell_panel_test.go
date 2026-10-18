package model

import (
	"fmt"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"image"
	"strings"
	"testing"
)

func numberedRun(id string, n int) *shellRun {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return &shellRun{id: id, command: "seq " + id, output: b.String()}
}

func TestShellPanelKeepsTheLastRunsAndSelectsTheNewest(t *testing.T) {
	var p shellPanel
	for i := range shellPanelMaxRuns + 5 {
		p.add(&shellRun{id: fmt.Sprint(i)})
	}
	if len(p.runs) != shellPanelMaxRuns {
		t.Fatalf("kept %d runs, want %d", len(p.runs), shellPanelMaxRuns)
	}
	if got := p.selected().id; got != fmt.Sprint(shellPanelMaxRuns+4) {
		t.Fatalf("selected %q, want the newest", got)
	}
	if !p.visible {
		t.Fatal("running a command must show the panel")
	}
	if p.find("0") != nil {
		t.Fatal("the oldest runs must be forgotten")
	}
}

func TestShellPanelScrollStaysInsideTheOutput(t *testing.T) {
	var p shellPanel
	p.add(numberedRun("a", 100))
	const body = 10
	p.scrollBy(-1000, body)
	if p.scroll != 0 || p.follow {
		t.Fatalf("scroll=%d follow=%v, want the top and no following", p.scroll, p.follow)
	}
	p.scrollBy(1000, body)
	if p.scroll != 90 || !p.follow {
		t.Fatalf("scroll=%d follow=%v, want the last page and following", p.scroll, p.follow)
	}
	p.scrollBy(-5, body)
	if p.scroll != 85 || p.follow {
		t.Fatalf("scroll=%d follow=%v, want to have left the end", p.scroll, p.follow)
	}
}

func TestShellPanelPickMovesBetweenRunsWithoutLeavingThem(t *testing.T) {
	var p shellPanel
	p.add(numberedRun("a", 3))
	p.add(numberedRun("b", 3))
	p.pick(-1)
	if p.selected().id != "a" {
		t.Fatalf("selected %q, want a", p.selected().id)
	}
	p.pick(-1)
	if p.selected().id != "a" {
		t.Fatal("pick must stop at the first run")
	}
	p.pick(5)
	if p.selected().id != "b" {
		t.Fatal("pick must stop at the last run")
	}
}

func TestShellPanelLayoutGoesRightOnAWideScreenAndUnderOnANarrowOne(t *testing.T) {
	wide := &UI{state: uiChat}
	wide.shellPanel.add(numberedRun("a", 3))
	l := uiLayout{main: image.Rect(0, 1, 160, 50)}
	wide.applyShellPanelLayout(&l)
	if l.shellPanel.Empty() || l.shellPanel.Min.Y != 1 || l.shellPanel.Max.X != 160 || l.main.Max.X >= l.shellPanel.Min.X {
		t.Fatalf("wide: panel %v main %v, want the panel at the right with the chat narrower", l.shellPanel, l.main)
	}

	narrow := &UI{state: uiChat}
	narrow.shellPanel.add(numberedRun("a", 3))
	l = uiLayout{main: image.Rect(0, 1, 80, 40)}
	narrow.applyShellPanelLayout(&l)
	if l.shellPanel.Empty() || l.shellPanel.Max.Y != 40 || l.main.Max.Y >= l.shellPanel.Min.Y || l.main.Max.X != 80 {
		t.Fatalf("narrow: panel %v main %v, want the panel under a full-width chat", l.shellPanel, l.main)
	}

	tiny := &UI{state: uiChat}
	tiny.shellPanel.add(numberedRun("a", 3))
	l = uiLayout{main: image.Rect(0, 1, 60, 12)}
	tiny.applyShellPanelLayout(&l)
	if !l.shellPanel.Empty() || l.main != image.Rect(0, 1, 60, 12) {
		t.Fatalf("tiny: panel %v main %v, want the layout left alone", l.shellPanel, l.main)
	}
}

func TestShellPanelLayoutIsUntouchedWhenHiddenOrOutsideChat(t *testing.T) {
	hidden := &UI{state: uiChat}
	hidden.shellPanel.add(numberedRun("a", 3))
	hidden.shellPanel.visible = false
	l := uiLayout{main: image.Rect(0, 1, 160, 50)}
	hidden.applyShellPanelLayout(&l)
	if !l.shellPanel.Empty() || l.main != image.Rect(0, 1, 160, 50) {
		t.Fatalf("hidden: panel %v main %v", l.shellPanel, l.main)
	}
	landing := &UI{state: uiLanding}
	landing.shellPanel.add(numberedRun("a", 3))
	landing.applyShellPanelLayout(&l)
	if !l.shellPanel.Empty() {
		t.Fatal("the panel is drawn in the chat only")
	}
}

func TestShellPanelWheelOnlyActsOverThePanel(t *testing.T) {
	m := &UI{state: uiChat}
	m.shellPanel.add(numberedRun("a", 100))
	m.layout = uiLayout{shellPanel: image.Rect(100, 1, 160, 30)}
	m.shellPanel.scroll, m.shellPanel.follow = 50, false
	if m.shellPanelWheel(10, 10, 1) {
		t.Fatal("a wheel over the chat must not be taken")
	}
	if !m.shellPanelWheel(120, 10, 1) || m.shellPanel.scroll != 50+shellPanelWheelStep {
		t.Fatalf("a wheel over the panel must scroll it, scroll=%d", m.shellPanel.scroll)
	}
}

func TestShellPanelViewHasTheSizeAskedAndFollowsTheEnd(t *testing.T) {
	sty := styles.SeshatDark()
	m := &UI{state: uiChat, com: &common.Common{Styles: &sty}}
	run := numberedRun("a", 100)
	run.output += strings.Repeat("x", 300) + "\n"
	m.shellPanel.add(run)

	view := m.shellPanelView(50, 14)
	rows := strings.Split(view, "\n")
	if len(rows) != 14 {
		t.Fatalf("view has %d rows, want 14", len(rows))
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != 50 {
			t.Fatalf("row %d is %d columns wide, want 50: %q", i, w, row)
		}
	}
	if !strings.Contains(view, "xxx") || strings.Contains(view, "line 1\n") {
		t.Fatalf("a panel that follows must show the end of the output (the long last line), not the start")
	}
	if !strings.Contains(view, "seq a") {
		t.Fatal("the command must be shown")
	}
}

func TestShellPanelViewShowsExitStatusAndSharing(t *testing.T) {
	sty := styles.SeshatDark()
	m := &UI{state: uiChat, com: &common.Common{Styles: &sty}}
	m.shellPanel.add(&shellRun{id: "a", command: "false", exitCode: 1, shared: true})
	view := m.shellPanelView(60, 10)
	if !strings.Contains(view, "exit status 1") || !strings.Contains(view, "shared with the agent") {
		t.Fatalf("view misses the status or the sharing note:\n%s", view)
	}
}
