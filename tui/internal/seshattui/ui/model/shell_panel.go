package model

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	// shellPanelMaxRuns is how many commands the panel remembers.
	shellPanelMaxRuns = 20
	// shellPanelMinWidth is the narrowest the panel is drawn at, and shellPanelSideMinScreen the screen width from which it
	// goes at the right of the chat; on a narrower screen it goes under it.
	shellPanelMinWidth      = 40
	shellPanelSideMinScreen = 100
	shellPanelMinHeight     = 8
	shellPanelWheelStep     = 3
)

// shellRun is one command run from the prompt and what it printed.
type shellRun struct {
	id       string
	command  string
	output   string
	exitCode int
	running  bool
	shared   bool
}

// lines is the output cut into lines, without the final line break.
func (r *shellRun) lines() []string {
	body := strings.TrimRight(r.output, "\r\n")
	if body == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}

// shellPanel keeps the commands run with "!" and shows the selected one next to the chat. What it holds is not part of
// the conversation: it is not stored in the transcript, compacted or read by memory, and the agent sees it only when the
// user shares a run.
type shellPanel struct {
	runs    []*shellRun
	sel     int
	scroll  int // first visible line
	visible bool
	// follow keeps the view on the end of the output of a running command.
	follow bool
}

// add records a command that starts, selects it and shows the panel.
func (p *shellPanel) add(run *shellRun) {
	p.runs = append(p.runs, run)
	if len(p.runs) > shellPanelMaxRuns {
		p.runs = p.runs[len(p.runs)-shellPanelMaxRuns:]
	}
	p.sel = len(p.runs) - 1
	p.scroll = 0
	p.follow = true
	p.visible = true
}

// find returns the run with that ID.
func (p *shellPanel) find(id string) *shellRun {
	for _, r := range p.runs {
		if r.id == id {
			return r
		}
	}
	return nil
}

// selected is the run on show, or nil.
func (p *shellPanel) selected() *shellRun {
	if p.sel < 0 || p.sel >= len(p.runs) {
		return nil
	}
	return p.runs[p.sel]
}

// clear forgets every run and hides the panel.
func (p *shellPanel) clear() {
	*p = shellPanel{}
}

// scrollBy moves the view by n lines, within the output of the selected run, for a body of the given height.
func (p *shellPanel) scrollBy(n, bodyHeight int) {
	run := p.selected()
	if run == nil {
		return
	}
	last := max(0, len(run.lines())-bodyHeight)
	p.scroll = min(max(0, p.scroll+n), last)
	p.follow = p.scroll >= last
}

// pick selects the run offset places away from the current one.
func (p *shellPanel) pick(offset int) {
	if len(p.runs) == 0 {
		return
	}
	p.sel = min(max(0, p.sel+offset), len(p.runs)-1)
	p.scroll = 0
	p.follow = true
}

// bodyHeight is how many lines of output fit in a panel of that height: the box takes its border, a title, the command and
// a status line.
func shellPanelBodyHeight(height int) int {
	return max(1, height-5)
}

// view draws the selected run in a box of the given size.
func (m *UI) shellPanelView(width, height int) string {
	p := &m.shellPanel
	run := p.selected()
	if run == nil || width < 4 || height < 4 {
		return ""
	}
	sty := m.com.Styles
	muted := sty.Messages.MsgTimestamp
	inner := width - 4 // border and one column of padding on each side
	body := shellPanelBodyHeight(height)

	lines := run.lines()
	if p.follow {
		p.scroll = max(0, len(lines)-body)
	}
	p.scroll = min(p.scroll, max(0, len(lines)-body))
	end := min(len(lines), p.scroll+body)
	shown := make([]string, 0, body)
	for _, l := range lines[min(p.scroll, len(lines)):end] {
		shown = append(shown, ansi.Truncate(l, inner, "…"))
	}
	for len(shown) < body {
		shown = append(shown, "")
	}

	title := muted.Render(fmt.Sprintf("Commands %d/%d", p.sel+1, len(p.runs)))
	head := ansi.Truncate("$ "+run.command, inner, "…")
	var status string
	switch {
	case run.running:
		status = "running…"
	case run.exitCode != 0:
		status = sty.Messages.ErrorDetails.Render(fmt.Sprintf("exit status %d", run.exitCode))
	default:
		status = muted.Render(fmt.Sprintf("done · %d lines", len(lines)))
	}
	if run.shared {
		status += muted.Render(" · shared with the agent")
	}
	if len(lines) > body {
		status += muted.Render(fmt.Sprintf(" · %d-%d of %d", p.scroll+1, end, len(lines)))
	}

	content := strings.Join(append([]string{title, head}, append(shown, ansi.Truncate(status, inner, "…"))...), "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted.GetForeground()).
		Padding(0, 1).
		Width(width).
		Height(height).
		Render(content)
}

// applyShellPanelLayout reserves room for the panel when it is shown: at the right of the chat on a wide screen, under it on
// a narrower one. It leaves the layout as it was when the panel is hidden or the screen is too small.
func (m *UI) applyShellPanelLayout(l *uiLayout) {
	l.shellPanel = image.Rectangle{}
	if !m.shellPanel.visible || m.shellPanel.selected() == nil || m.state != uiChat {
		return
	}
	main := l.main
	if main.Dx() >= shellPanelSideMinScreen {
		width := max(shellPanelMinWidth, main.Dx()*2/5)
		l.shellPanel = image.Rect(main.Max.X-width, main.Min.Y, main.Max.X, main.Max.Y)
		l.main.Max.X = main.Max.X - width - 1
		return
	}
	height := max(shellPanelMinHeight, main.Dy()*2/5)
	if main.Dy()-height < shellPanelMinHeight {
		return
	}
	l.shellPanel = image.Rect(main.Min.X, main.Max.Y-height, main.Max.X, main.Max.Y)
	l.main.Max.Y = main.Max.Y - height - 1
}

// drawShellPanel draws the panel in its area, if it has one.
func (m *UI) drawShellPanel(scr uv.Screen, area uv.Rectangle) {
	if area.Empty() {
		return
	}
	if view := m.shellPanelView(area.Dx(), area.Dy()); view != "" {
		uv.NewStyledString(view).Draw(scr, area)
	}
}

// shellPanelWheel scrolls the panel when the mouse is over it. It reports whether it used the event.
func (m *UI) shellPanelWheel(x, y, direction int) bool {
	area := m.layout.shellPanel
	if area.Empty() || !image.Pt(x, y).In(area) {
		return false
	}
	m.shellPanel.scrollBy(direction*shellPanelWheelStep, shellPanelBodyHeight(area.Dy()))
	return true
}
