package model

import (
	"cmp"
	"fmt"
	"image"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/session"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/logo"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
)

// modelInfo renders the current model information including reasoning
// settings and context usage/cost for the sidebar.
func (m *UI) modelInfo(width int) string {
	model := m.selectedLargeModel()
	reasoningInfo := ""
	providerName := ""

	if model != nil {
		providerConfig, ok := m.com.Config().Providers.Get(model.ModelCfg.Provider)
		if ok {
			providerName = providerConfig.Name
			if model.CatwalkCfg.CanReason {
				if len(model.CatwalkCfg.ReasoningLevels) == 0 {
					if model.ModelCfg.Think {
						reasoningInfo = "Thinking On"
					} else {
						reasoningInfo = "Thinking Off"
					}
				} else {
					reasoningEffort := cmp.Or(model.ModelCfg.ReasoningEffort, model.CatwalkCfg.DefaultReasoningEffort)
					reasoningInfo = fmt.Sprintf("Reasoning %s", common.FormatReasoningEffort(reasoningEffort))
				}
			}
		}
	}

	var modelContext *common.ModelContextInfo
	if model != nil && m.session != nil {
		modelContext = &common.ModelContextInfo{
			ContextUsed:    m.session.CompletionTokens + m.session.PromptTokens,
			Cost:           m.session.Cost,
			ModelContext:   model.CatwalkCfg.ContextWindow,
			EstimatedUsage: m.session.EstimatedUsage,
		}
	}
	var modelName string
	if model != nil {
		modelName = model.CatwalkCfg.Name
	}
	return common.ModelInfo(m.com.Styles, modelName, providerName, reasoningInfo, modelContext, width, m.hyperCredits)
}

func getDynamicHeightLimits(availableHeight, fileCount, lspCount, mcpCount, skillCount, taskCount int) (maxFiles, maxLSPs, maxMCPs, maxSkills, maxTasks int) {
	const (
		minItemsPerSection      = 2
		defaultMaxFilesShown    = 1000
		defaultMaxLSPsShown     = 1000
		defaultMaxMCPsShown     = 1000
		defaultMaxSkillsShown   = 1000
		minAvailableHeightLimit = 10
	)

	if availableHeight < minAvailableHeightLimit {
		return minItemsPerSection, minItemsPerSection, minItemsPerSection, minItemsPerSection, minItemsPerSection
	}

	maxFiles = minItemsPerSection
	maxLSPs = minItemsPerSection
	maxMCPs = minItemsPerSection
	maxSkills = minItemsPerSection
	maxTasks = minItemsPerSection

	remainingHeight := max(0, availableHeight-(minItemsPerSection*5))

	sectionValues := []*int{&maxFiles, &maxLSPs, &maxMCPs, &maxSkills, &maxTasks}
	sectionCaps := []int{defaultMaxFilesShown, defaultMaxLSPsShown, defaultMaxMCPsShown, defaultMaxSkillsShown, defaultMaxSkillsShown}
	sectionNeeds := []int{max(0, fileCount-maxFiles), max(0, lspCount-maxLSPs), max(0, mcpCount-maxMCPs), max(0, skillCount-maxSkills), max(0, taskCount-maxTasks)}

	for remainingHeight > 0 {
		allocated := false
		for i, section := range sectionValues {
			if remainingHeight == 0 {
				break
			}
			if sectionNeeds[i] == 0 || *section >= sectionCaps[i] {
				continue
			}
			*section = *section + 1
			sectionNeeds[i]--
			remainingHeight--
			allocated = true
		}
		if !allocated {
			break
		}
	}

	for remainingHeight > 0 {
		allocated := false
		for i, section := range sectionValues {
			if remainingHeight == 0 {
				break
			}
			if *section >= sectionCaps[i] {
				continue
			}
			*section = *section + 1
			remainingHeight--
			allocated = true
		}
		if !allocated {
			break
		}
	}

	return maxFiles, maxLSPs, maxMCPs, maxSkills, maxTasks
}

type sidebarTaskHitZone struct {
	TaskID string
	Rect   image.Rectangle
}

type tasksSectionRender struct {
	Section string
	Zones   []sidebarTaskHitZone
}

const sidebarTaskDetailsCollapsedLines = 6

func sortSidebarTodos(todos []session.Todo) []session.Todo {
	sorted := slices.Clone(todos)
	slices.SortStableFunc(sorted, func(a, b session.Todo) int {
		return sidebarTaskStatusOrder(a.Status) - sidebarTaskStatusOrder(b.Status)
	})
	return sorted
}

func sidebarTaskStatusOrder(status session.TodoStatus) int {
	switch status {
	case session.TodoStatusInProgress:
		return 0
	case session.TodoStatusPending:
		return 1
	default:
		return 2
	}
}

func findSidebarTask(todos []session.Todo, id string) *session.Todo {
	for i := range todos {
		if todos[i].ID == id {
			return &todos[i]
		}
	}
	return nil
}

func (m *UI) ensureSidebarTaskSelection() {
	if m.session == nil || len(m.session.Todos) == 0 {
		m.selectedSidebarTaskID = ""
		m.sidebarTaskHitZones = nil
		m.sidebarTaskExpanded = false
		if m.focus == uiFocusSidebar {
			m.focus = uiFocusMain
		}
		return
	}
	if findSidebarTask(m.session.Todos, m.selectedSidebarTaskID) != nil {
		return
	}
	for _, todo := range m.session.Todos {
		if todo.Status == session.TodoStatusInProgress {
			m.selectedSidebarTaskID = todo.ID
			return
		}
	}
	for _, todo := range m.session.Todos {
		if todo.Status == session.TodoStatusPending {
			m.selectedSidebarTaskID = todo.ID
			return
		}
	}
	m.selectedSidebarTaskID = m.session.Todos[0].ID
}

func tasksSummary(todos []session.Todo) string {
	if len(todos) == 0 {
		return "empty"
	}
	completed := 0
	for _, todo := range todos {
		if todo.Status == session.TodoStatusCompleted {
			completed++
		}
	}
	return fmt.Sprintf("%d/%d done", completed, len(todos))
}

func renderSidebarTaskRow(sty *styles.Styles, todo session.Todo, spinnerView string, width int, selected bool) string {
	prefix := sty.Tool.TodoPendingIcon.Render(styles.TodoPendingIcon + " ")
	switch todo.Status {
	case session.TodoStatusCompleted:
		prefix = sty.Tool.TodoCompletedIcon.Render(styles.TodoCompletedIcon + " ")
	case session.TodoStatusInProgress:
		prefix = sty.Tool.TodoInProgressIcon.Render(spinnerView + " ")
	}
	line := prefix + sty.Tool.TodoItem.Render(todo.Content)
	if selected {
		return sty.TextSelection.Width(width).Padding(0, 1).Render(line)
	}
	return lipgloss.NewStyle().Width(width).Render(line)
}

func tasksInfo(todos []session.Todo, selectedTaskID, spinnerView string, sty *styles.Styles, origin image.Point, width, maxItems int, isSection bool) tasksSectionRender {
	title := sty.Files.SectionTitle.Render("Tasks")
	if isSection {
		title = common.Section(sty, "Tasks", width, tasksSummary(todos))
	}
	if len(todos) == 0 {
		return tasksSectionRender{Section: lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, sty.Files.EmptyMessage.Render("None")))}
	}
	sorted := sortSidebarTodos(todos)
	visible := sorted
	if maxItems > 0 && len(visible) > maxItems {
		visible = visible[:maxItems]
	}
	lines := []string{title, ""}
	zones := make([]sidebarTaskHitZone, 0, len(visible))
	baseY := origin.Y + 2
	for i, todo := range visible {
		lines = append(lines, renderSidebarTaskRow(sty, todo, spinnerView, width, todo.ID == selectedTaskID))
		zones = append(zones, sidebarTaskHitZone{TaskID: todo.ID, Rect: image.Rect(origin.X, baseY+i, origin.X+width, baseY+i+1)})
	}
	if maxItems > 0 && len(sorted) > maxItems {
		lines = append(lines, sty.Files.TruncationHint.Render(fmt.Sprintf("…and %d more", len(sorted)-maxItems)))
	}
	return tasksSectionRender{Section: lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n")), Zones: zones}
}

func taskDetailsInfo(todos []session.Todo, selectedTaskID string, expanded bool, sty *styles.Styles, width int, isSection bool) string {
	selected := findSidebarTask(todos, selectedTaskID)
	statusInfo := ""
	if selected != nil {
		statusInfo = string(selected.Status)
	}
	title := sty.Files.SectionTitle.Render("Task Details")
	if isSection {
		title = common.Section(sty, "Task Details", width, statusInfo)
	}
	if selected == nil {
		return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, sty.Files.EmptyMessage.Render("No task selected")))
	}
	description := selected.Description
	if description == "" {
		description = "No description"
	}
	statusLine := "Status: " + string(selected.Status)
	if selected.Status == session.TodoStatusInProgress && selected.ActiveForm != "" {
		statusLine += " · " + selected.ActiveForm
	}
	parts := []string{
		sty.TextSelection.Width(width).Padding(0, 1).Render(selected.Content),
		sty.Section.Title.Render(statusLine),
	}
	if selected.Owner != "" {
		parts = append(parts, sty.Section.Title.Render("Owner: "+selected.Owner))
	}
	parts = append(parts, sty.Tool.Body.Render(renderSidebarTaskDescription(sty, description, width, expanded)))
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, strings.Join(parts, "\n")))
}

func renderSidebarTaskDescription(sty *styles.Styles, description string, width int, expanded bool) string {
	lines := strings.Split(strings.ReplaceAll(description, "\r\n", "\n"), "\n")
	if expanded || len(lines) <= sidebarTaskDetailsCollapsedLines {
		return lipgloss.NewStyle().Width(width).Render(description)
	}
	visible := strings.Join(lines[:sidebarTaskDetailsCollapsedLines], "\n")
	hint := sty.Files.TruncationHint.Render(fmt.Sprintf("… (%d lines hidden) [space to expand]", len(lines)-sidebarTaskDetailsCollapsedLines))
	return lipgloss.NewStyle().Width(width).Render(visible) + "\n" + hint
}

func (m *UI) hasSidebarTasks() bool {
	return m.session != nil && len(m.session.Todos) > 0
}

func (m *UI) moveSidebarTaskSelection(delta int) bool {
	if !m.hasSidebarTasks() || delta == 0 {
		return false
	}
	sorted := sortSidebarTodos(m.session.Todos)
	idx := 0
	for i, todo := range sorted {
		if todo.ID == m.selectedSidebarTaskID {
			idx = i
			break
		}
	}
	next := max(0, min(len(sorted)-1, idx+delta))
	if sorted[next].ID == m.selectedSidebarTaskID {
		return false
	}
	m.selectedSidebarTaskID = sorted[next].ID
	return true
}

func (m *UI) selectSidebarTaskBoundary(first bool) bool {
	if !m.hasSidebarTasks() {
		return false
	}
	sorted := sortSidebarTodos(m.session.Todos)
	target := sorted[len(sorted)-1].ID
	if first {
		target = sorted[0].ID
	}
	if target == m.selectedSidebarTaskID {
		return false
	}
	m.selectedSidebarTaskID = target
	return true
}

func (m *UI) drawSidebar(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	const logoHeightBreakpoint = 30

	t := m.com.Styles
	width := area.Dx()
	height := area.Dy()

	title := t.Sidebar.SessionTitle.Width(width).MaxHeight(2).Render(m.session.Title)
	cwd := common.PrettyPath(t, m.com.Workspace.WorkingDir(), width)
	sidebarLogo := m.sidebarLogo
	if height < logoHeightBreakpoint {
		sidebarLogo = logo.SmallRender(m.com.Styles, width, logo.Opts{Hyper: m.com.IsHyper()})
	}
	blocks := []string{sidebarLogo, title, "", cwd, m.sandboxInfo(width), "", m.modelInfo(width), ""}
	sidebarHeader := lipgloss.JoinVertical(lipgloss.Left, blocks...)

	var remainingHeightArea image.Rectangle
	layout.Vertical(layout.Len(lipgloss.Height(sidebarHeader)), layout.Fill(1)).Split(m.layout.sidebar).Assign(new(image.Rectangle), &remainingHeightArea)
	remainingHeight := remainingHeightArea.Dy() - 6
	filesCount := 0
	for _, f := range m.sessionFiles {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		filesCount++
	}
	lspsCount := len(m.lspStates)
	mcpsCount := 0
	for _, mcpCfg := range m.com.Config().MCP.Sorted() {
		if _, ok := m.mcpStates[mcpCfg.Name]; ok {
			mcpsCount++
		}
	}
	skillsCount := len(m.skillStatusItems())
	tasksCount := len(m.session.Todos)
	maxFiles, maxLSPs, maxMCPs, maxSkills, maxTasks := getDynamicHeightLimits(remainingHeight, filesCount, lspsCount, mcpsCount, skillsCount, tasksCount)

	lspSection := m.lspInfo(width, maxLSPs, true)
	mcpSection := m.mcpInfo(width, maxMCPs, true)
	skillsSection := m.skillsInfo(width, maxSkills, true)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), width, maxFiles, true)
	inProgressIcon := t.Tool.TodoInProgressIcon.Render(styles.SpinnerIcon)
	if m.todoIsSpinning {
		inProgressIcon = m.todoSpinner.View()
	}
	preTaskBlocks := []string{sidebarHeader, filesSection, "", lspSection, "", mcpSection, "", skillsSection, ""}
	tasksOrigin := image.Pt(area.Min.X, area.Min.Y+lipgloss.Height(strings.Join(preTaskBlocks, "\n")))
	tasksRender := tasksInfo(m.session.Todos, m.selectedSidebarTaskID, inProgressIcon, t, tasksOrigin, width, maxTasks, true)
	m.sidebarTaskHitZones = tasksRender.Zones
	taskDetailsSection := taskDetailsInfo(m.session.Todos, m.selectedSidebarTaskID, m.sidebarTaskExpanded, t, width, true)

	uv.NewStyledString(
		lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(
			lipgloss.JoinVertical(
				lipgloss.Left,
				sidebarHeader,
				filesSection,
				"",
				lspSection,
				"",
				mcpSection,
				"",
				skillsSection,
				"",
				tasksRender.Section,
				"",
				taskDetailsSection,
			),
		),
	).Draw(scr, area)
}
