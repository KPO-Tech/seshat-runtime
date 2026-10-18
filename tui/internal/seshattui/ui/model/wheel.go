package model

import (
	tea "charm.land/bubbletea/v2"
)

// handleWheel scrolls what is under the mouse: the command panel when it is there, the chat otherwise.
func (m *UI) handleWheel(msg tea.MouseWheelMsg) tea.Cmd {
	direction := 1
	if msg.Button == tea.MouseWheelUp {
		direction = -1
	}
	if m.shellPanelWheel(msg.X, msg.Y, direction) {
		return nil
	}
	var cmds []tea.Cmd
	switch m.state {
	case uiChat:
		switch msg.Button {
		case tea.MouseWheelUp:
			if cmd := m.chat.ScrollByAndAnimate(-MouseScrollThreshold); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if !m.chat.SelectedItemInView() {
				m.chat.SelectPrev()
				if cmd := m.chat.ScrollToSelectedAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case tea.MouseWheelDown:
			if cmd := m.chat.ScrollByAndAnimate(MouseScrollThreshold); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if !m.chat.SelectedItemInView() {
				if m.chat.AtBottom() {
					m.chat.SelectLast()
				} else {
					m.chat.SelectNext()
				}
				if cmd := m.chat.ScrollToSelectedAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	}
	return tea.Batch(cmds...)
}
