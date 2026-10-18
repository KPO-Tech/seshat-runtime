package completions

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/agent/tools/mcp"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/fsext"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/ordered"
)

const (
	minHeight = 1
	maxHeight = 5
	minWidth  = 10
	maxWidth  = 100

	tierExactName = iota
	tierPrefixName
	tierPathSegment
	tierFallback
)

// SelectionMsg is sent when a completion is selected.
type SelectionMsg[T any] struct {
	Value    T
	KeepOpen bool // If true, insert without closing.
}

// ClosedMsg is sent when the completions are closed.
type ClosedMsg struct{}

// CompletionItemsLoadedMsg is sent when files have been loaded for completions.
type CompletionItemsLoadedMsg struct {
	Files     []FileCompletionValue
	Resources []ResourceCompletionValue
}

// Styles bundles everything needed to render the popup and its items.
type Styles struct {
	ItemStyles
	Border lipgloss.Style
}

// Completions represents the completions popup component.
type Completions struct {
	// Popup dimensions (content only; Render() adds the border on top)
	width  int
	height int

	// State
	open  bool
	query string

	// Key bindings
	keyMap KeyMap

	// List component
	list *list.FilterableList

	styles Styles

	allItems []list.FilterableItem
	filtered []list.FilterableItem
}

type namePriorityRule struct {
	tier  int
	match func(pathLower, baseLower, stemLower, queryLower string) bool
}

var namePriorityRules = []namePriorityRule{
	{
		tier: tierExactName,
		match: func(_ string, baseLower, stemLower, queryLower string) bool {
			return baseLower == queryLower || stemLower == queryLower
		},
	},
	{
		tier: tierPrefixName,
		match: func(_ string, baseLower, _ string, queryLower string) bool {
			return strings.HasPrefix(baseLower, queryLower)
		},
	},
	{
		tier: tierPathSegment,
		match: func(pathLower, _ string, _ string, queryLower string) bool {
			return hasPathSegment(pathLower, queryLower)
		},
	},
}

// New creates a new completions component.
func New(styles Styles) *Completions {
	l := list.NewFilterableList()
	l.SetGap(0)
	l.SetReverse(true)

	return &Completions{
		keyMap: DefaultKeyMap(),
		list:   l,
		styles: styles,
	}
}

// SetStyles updates the styles used when rendering completion items.
// Existing items are not restyled; subsequent SetItems calls pick up the
// new styles.
func (c *Completions) SetStyles(styles Styles) {
	c.styles = styles
}

// IsOpen returns whether the completions popup is open.
func (c *Completions) IsOpen() bool {
	return c.open
}

// Query returns the current filter query.
func (c *Completions) Query() string {
	return c.query
}

// Size returns the full on-screen size of the popup, including the border
// and the scrollbar column (when shown).
func (c *Completions) Size() (width, height int) {
	visible := min(len(c.filtered), c.height)
	w := c.width
	if c.hasScrollbar() {
		w++
	}
	return w + 2, visible + 2 // +2 each axis for the border
}

// hasScrollbar reports whether there are more filtered items than fit in
// the visible height.
func (c *Completions) hasScrollbar() bool {
	return len(c.filtered) > c.height
}

// KeyMap returns the key bindings.
func (c *Completions) KeyMap() KeyMap {
	return c.keyMap
}

// Open opens the completions with file items from the filesystem.
func (c *Completions) Open(depth, limit int) tea.Cmd {
	return func() tea.Msg {
		var msg CompletionItemsLoadedMsg
		var wg sync.WaitGroup
		wg.Go(func() {
			msg.Files = loadFiles(depth, limit)
		})
		wg.Go(func() {
			msg.Resources = loadMCPResources()
		})
		wg.Wait()
		return msg
	}
}

// SetItems sets the files and MCP resources and rebuilds the merged list.
func (c *Completions) SetItems(files []FileCompletionValue, resources []ResourceCompletionValue) {
	items := make([]list.FilterableItem, 0, len(files)+len(resources))

	// Add files first.
	for _, file := range files {
		items = append(items, NewCompletionItem(file.Path, "", file, c.styles.ItemStyles))
	}

	// Add MCP resources.
	for _, resource := range resources {
		name := resource.MCPName + "/" + cmp.Or(resource.Title, resource.URI)
		items = append(items, NewCompletionItem(name, "", resource, c.styles.ItemStyles))
	}

	c.openWith(items)
}

// SetSkillItems sets the skill items and opens the popup. Unlike SetItems,
// the data is already resident in memory (loaded once at startup), so no
// filesystem scan or [tea.Cmd] is needed.
func (c *Completions) SetSkillItems(skillItems []SkillCompletionValue) {
	items := make([]list.FilterableItem, 0, len(skillItems))
	for _, sk := range skillItems {
		items = append(items, NewCompletionItem("/"+sk.Name, sk.Description, sk, c.styles.ItemStyles))
	}
	c.openWith(items)
}

// SetCommandItems sets the palette commands and opens the popup.
func (c *Completions) SetCommandItems(values []CommandCompletionValue) {
	items := make([]list.FilterableItem, 0, len(values))
	for _, v := range values {
		items = append(items, NewCompletionItem("\\"+v.Title, v.Shortcut, v, c.styles.ItemStyles))
	}
	c.openWith(items)
}

// openWith populates the list with items and opens the popup.
func (c *Completions) openWith(items []list.FilterableItem) {
	c.open = true
	c.query = ""
	c.allItems = items
	c.filtered = append([]list.FilterableItem(nil), items...)
	c.list.SetItems(c.filtered...)
	c.list.SetFilter("")
	c.list.Focus()

	c.width = maxWidth
	c.height = ordered.Clamp(len(items), int(minHeight), int(maxHeight))
	c.list.SetSize(c.width, c.height)
	c.list.SelectFirst()
	c.list.ScrollToSelected()

	c.updateSize()
}

// Close closes the completions popup.
func (c *Completions) Close() {
	c.open = false
}

// Filter filters the completions with the given query.
func (c *Completions) Filter(query string) {
	if !c.open {
		return
	}

	if query == c.query {
		return
	}

	c.query = query
	c.applyNamePriorityFilter(query)

	c.updateSize()
}

func (c *Completions) applyNamePriorityFilter(query string) {
	if query == "" {
		c.filtered = append([]list.FilterableItem(nil), c.allItems...)
		c.list.SetItems(c.filtered...)
		return
	}

	c.list.SetItems(c.allItems...)
	c.list.SetFilter(query)
	raw := c.list.FilteredItems()
	filtered := make([]list.FilterableItem, 0, len(raw))
	for _, item := range raw {
		filterable, ok := item.(list.FilterableItem)
		if !ok {
			continue
		}
		filtered = append(filtered, filterable)
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	slices.SortStableFunc(filtered, func(a, b list.FilterableItem) int {
		return namePriorityTier(a.Filter(), queryLower) - namePriorityTier(b.Filter(), queryLower)
	})
	c.filtered = filtered
	c.list.SetItems(c.filtered...)
}

func namePriorityTier(path, queryLower string) int {
	if queryLower == "" {
		return tierFallback
	}

	pathLower := strings.ToLower(path)
	baseLower := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, `/`)))
	stemLower := strings.TrimSuffix(baseLower, filepath.Ext(baseLower))
	for _, rule := range namePriorityRules {
		if rule.match(pathLower, baseLower, stemLower, queryLower) {
			return rule.tier
		}
	}
	return tierFallback
}

func hasPathSegment(pathLower, queryLower string) bool {
	return slices.Contains(strings.FieldsFunc(pathLower, func(r rune) bool {
		return r == '/' || r == '\\'
	}), queryLower)
}

// prefixWidth must match the bar+icon+gap prefix renderItem draws before
// the name column.
const prefixWidth = 3

func (c *Completions) updateSize() {
	items := c.filtered
	start, end := c.list.VisibleItemIndices()
	const descGap = 2

	maxNameW, maxDescW := 0, 0
	for i := start; i <= end; i++ {
		item := c.list.ItemAt(i)
		if item == nil {
			continue
		}
		maxNameW = max(maxNameW, ansi.StringWidth(item.(interface{ Text() string }).Text()))
		if d, ok := item.(interface{ Desc() string }); ok {
			maxDescW = max(maxDescW, ansi.StringWidth(d.Desc()))
		}
	}

	width := 0
	if maxNameW > 0 {
		width = prefixWidth + maxNameW
		if maxDescW > 0 {
			width += descGap + maxDescW
		}
	}
	c.width = ordered.Clamp(width+2, int(minWidth), int(maxWidth))
	c.height = ordered.Clamp(len(items), int(minHeight), int(maxHeight))
	c.list.SetSize(c.width, c.height)

	// Align descriptions into a shared column across all visible rows.
	for i := start; i <= end; i++ {
		if item, ok := c.list.ItemAt(i).(*CompletionItem); ok {
			item.SetNameColumnWidth(maxNameW)
		}
	}

	c.list.SelectFirst()
	c.list.ScrollToSelected()
}

// HasItems returns whether there are visible items.
func (c *Completions) HasItems() bool {
	return len(c.filtered) > 0
}

// Update handles key events for the completions.
func (c *Completions) Update(msg tea.KeyPressMsg) (tea.Msg, bool) {
	if !c.open {
		return nil, false
	}

	switch {
	case key.Matches(msg, c.keyMap.Up):
		c.selectPrev()
		return nil, true

	case key.Matches(msg, c.keyMap.Down):
		c.selectNext()
		return nil, true

	case key.Matches(msg, c.keyMap.UpInsert):
		c.selectPrev()
		return c.selectCurrent(true), true

	case key.Matches(msg, c.keyMap.DownInsert):
		c.selectNext()
		return c.selectCurrent(true), true

	case key.Matches(msg, c.keyMap.Select):
		return c.selectCurrent(false), true

	case key.Matches(msg, c.keyMap.Cancel):
		c.Close()
		return ClosedMsg{}, true
	}

	return nil, false
}

// selectPrev selects the previous item with circular navigation.
func (c *Completions) selectPrev() {
	items := c.filtered
	if len(items) == 0 {
		return
	}
	if !c.list.SelectPrev() {
		c.list.WrapToEnd()
	}
	c.list.ScrollToSelected()
}

// selectNext selects the next item with circular navigation.
func (c *Completions) selectNext() {
	items := c.filtered
	if len(items) == 0 {
		return
	}
	if !c.list.SelectNext() {
		c.list.WrapToStart()
	}
	c.list.ScrollToSelected()
}

// selectCurrent returns a command with the currently selected item.
func (c *Completions) selectCurrent(keepOpen bool) tea.Msg {
	items := c.filtered
	if len(items) == 0 {
		return nil
	}

	selected := c.list.Selected()
	if selected < 0 || selected >= len(items) {
		return nil
	}

	item, ok := items[selected].(*CompletionItem)
	if !ok {
		return nil
	}

	if !keepOpen {
		c.open = false
	}

	switch item := item.Value().(type) {
	case ResourceCompletionValue:
		return SelectionMsg[ResourceCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	case FileCompletionValue:
		return SelectionMsg[FileCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	case SkillCompletionValue:
		return SelectionMsg[SkillCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	case CommandCompletionValue:
		return SelectionMsg[CommandCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	default:
		return nil
	}
}

// Render renders the completions popup: a bordered box around the item
// list, with a scrollbar in the right margin when there are more items
// than fit in the visible height.
func (c *Completions) Render() string {
	if !c.open || len(c.filtered) == 0 {
		return ""
	}

	content := c.list.List.Render()
	if sb := c.scrollbar(); sb != "" {
		content = lipgloss.JoinHorizontal(lipgloss.Top, content, sb)
	}
	return c.styles.Border.Render(content)
}

// scrollbar renders a thin vertical scrollbar reflecting the current
// scroll position, or "" when everything fits in view.
func (c *Completions) scrollbar() string {
	contentSize := len(c.filtered)
	if c.height <= 0 || contentSize <= c.height {
		return ""
	}

	start, _ := c.list.VisibleItemIndices()
	thumbSize := max(1, c.height*c.height/contentSize)
	maxOffset := contentSize - c.height
	trackSpace := c.height - thumbSize
	thumbPos := 0
	if trackSpace > 0 && maxOffset > 0 {
		thumbPos = min(trackSpace, start*trackSpace/maxOffset)
	}

	lines := make([]string, c.height)
	for i := range c.height {
		if i >= thumbPos && i < thumbPos+thumbSize {
			lines[i] = c.styles.Bar.Render("┃")
		} else {
			lines[i] = c.styles.Desc.Render("│")
		}
	}
	return strings.Join(lines, "\n")
}

func loadFiles(depth, limit int) []FileCompletionValue {
	files, _, _ := fsext.ListDirectory(".", nil, depth, limit)
	slices.Sort(files)
	result := make([]FileCompletionValue, 0, len(files))
	for _, file := range files {
		result = append(result, FileCompletionValue{
			Path: strings.TrimPrefix(file, "./"),
		})
	}
	return result
}

func loadMCPResources() []ResourceCompletionValue {
	var resources []ResourceCompletionValue
	for mcpName, mcpResources := range mcp.Resources() {
		for _, r := range mcpResources {
			resources = append(resources, ResourceCompletionValue{
				MCPName:  mcpName,
				URI:      r.URI,
				Title:    r.Name,
				MIMEType: r.MIMEType,
			})
		}
	}
	return resources
}
