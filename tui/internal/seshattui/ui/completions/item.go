package completions

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/sahilm/fuzzy"
)

// FileCompletionValue represents a file path completion value.
type FileCompletionValue struct {
	Path string
}

// ResourceCompletionValue represents a MCP resource completion value.
type ResourceCompletionValue struct {
	MCPName  string
	URI      string
	Title    string
	MIMEType string
}

// SkillCompletionValue represents a skill completion value.
type SkillCompletionValue struct {
	Name        string
	Description string
}

// CommandCompletionValue is a command of the palette offered by the popup that opens with a backslash. Payload is what the
// host runs when it is chosen.
type CommandCompletionValue struct {
	Title    string
	Shortcut string
	Payload  any
}

// CompletionItem represents an item in the completions list.
type CompletionItem struct {
	*list.Versioned

	text         string
	desc         string // optional, rendered dim in an aligned column when there's room
	value        any
	match        fuzzy.Match
	focused      bool
	nameColWidth int // shared column width so descriptions line up across rows
	cache        map[int]string

	// Styles
	normalStyle  lipgloss.Style
	focusedStyle lipgloss.Style
	matchStyle   lipgloss.Style
	descStyle    lipgloss.Style
	iconStyle    lipgloss.Style
	barStyle     lipgloss.Style
}

// NewCompletionItem creates a new completion item. desc may be empty.
func NewCompletionItem(text, desc string, value any, styles ItemStyles) *CompletionItem {
	return &CompletionItem{
		Versioned:    list.NewVersioned(),
		text:         text,
		desc:         desc,
		value:        value,
		normalStyle:  styles.Normal,
		focusedStyle: styles.Focused,
		matchStyle:   styles.Match,
		descStyle:    styles.Desc,
		iconStyle:    styles.Icon,
		barStyle:     styles.Bar,
	}
}

// ItemStyles bundles the styles a [CompletionItem] needs to render.
type ItemStyles struct {
	Normal, Focused, Match, Desc, Icon, Bar lipgloss.Style
}

// SetNameColumnWidth sets the shared name-column width used to align
// descriptions across all rows in the popup.
func (c *CompletionItem) SetNameColumnWidth(w int) {
	if c.nameColWidth == w {
		return
	}
	c.cache = nil
	c.nameColWidth = w
}

// Finished implements list.Item. Completion items render purely from
// (text, match, focus); any mutation (SetMatch / SetFocused) bumps
// Version() so the frozen cache entry invalidates on the next
// render. Marking them finished lets the F6 list memo skip the
// per-line work for the steady completions popup.
func (c *CompletionItem) Finished() bool {
	return true
}

// Text returns the display text of the item.
func (c *CompletionItem) Text() string {
	return c.text
}

// Value returns the value of the item.
func (c *CompletionItem) Value() any {
	return c.value
}

// Desc returns the item's secondary description, if any.
func (c *CompletionItem) Desc() string {
	return c.desc
}

// Filter implements [list.FilterableItem].
func (c *CompletionItem) Filter() string {
	return c.text
}

// SetMatch implements [list.MatchSettable].
func (c *CompletionItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(c.match, m) {
		return
	}
	c.cache = nil
	c.match = m
	c.Bump()
}

// sameFuzzyMatch reports whether two fuzzy.Match values are
// observably equal. Because Match contains a slice (MatchedIndexes)
// it is not directly comparable with ==; we compare the scalar
// fields and then walk the indexes. SetMatch uses this to skip
// gratuitous version bumps when the same match is reapplied.
func sameFuzzyMatch(a, b fuzzy.Match) bool {
	return a.Str == b.Str &&
		a.Index == b.Index &&
		a.Score == b.Score &&
		slices.Equal(a.MatchedIndexes, b.MatchedIndexes)
}

// SetFocused implements [list.Focusable].
func (c *CompletionItem) SetFocused(focused bool) {
	if c.focused == focused {
		return
	}
	c.cache = nil
	c.focused = focused
	c.Bump()
}

// completionIcon marks every row in the popup; selection is conveyed by the
// left accent bar and the name's color/weight, not by a full-row fill.
const completionIcon = "●"

// Render implements [list.Item].
func (c *CompletionItem) Render(width int) string {
	return renderItem(
		c.normalStyle, c.focusedStyle, c.matchStyle, c.descStyle, c.iconStyle, c.barStyle,
		c.text, c.desc,
		c.focused,
		width,
		c.nameColWidth,
		c.cache,
		&c.match,
	)
}

// renderItem renders one popup row: a left accent bar (focused rows only),
// an icon, the name, and — if there's room — a description aligned to
// nameColWidth so descriptions line up across rows regardless of name
// length.
func renderItem(
	normalStyle, focusedStyle, matchStyle, descStyle, iconStyle, barStyle lipgloss.Style,
	text, desc string,
	focused bool,
	width, nameColWidth int,
	cache map[int]string,
	match *fuzzy.Match,
) string {
	if cache == nil {
		cache = make(map[int]string)
	}

	cached, ok := cache[width]
	if ok {
		return cached
	}

	const prefixWidth = 3 // bar + icon + gap
	innerWidth := width - 2 - prefixWidth
	style := normalStyle
	if focused {
		style = focusedStyle
	}
	matchStyle = matchStyle.Background(style.GetBackground())
	descStyle = descStyle.Background(style.GetBackground())
	iconStyle = iconStyle.Background(style.GetBackground())
	barStyle = barStyle.Background(style.GetBackground())

	nameWidth := max(ansi.StringWidth(text), min(nameColWidth, innerWidth))
	const minGap = 2
	const minDescWidth = 4

	name := text
	if ansi.StringWidth(name) > innerWidth {
		name = ansi.Truncate(name, innerWidth, "…")
		nameWidth = ansi.StringWidth(name)
	}
	paddedName := name + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name)))

	combined := paddedName
	var descShown string
	if avail := innerWidth - nameWidth - minGap; desc != "" && avail >= minDescWidth {
		descShown = desc
		if ansi.StringWidth(descShown) > avail {
			descShown = ansi.Truncate(descShown, avail, "…")
		}
		gap := max(minGap, innerWidth-nameWidth-ansi.StringWidth(descShown))
		combined = paddedName + strings.Repeat(" ", gap) + descShown
	}

	if ansi.StringWidth(combined) > innerWidth {
		combined = ansi.Truncate(combined, innerWidth, "…")
		if ansi.StringWidth(combined) <= nameWidth {
			descShown = ""
		}
	}

	bar := " "
	if focused {
		bar = "▏"
	}
	full := bar + completionIcon + " " + combined

	content := style.Padding(0, 1).Width(width).Render(full)

	var ranges []lipgloss.Range
	if focused {
		ranges = append(ranges, lipgloss.NewRange(1, 2, barStyle))
	}
	ranges = append(ranges, lipgloss.NewRange(2, 3, iconStyle))
	if descShown != "" {
		descStart := prefixWidth + ansi.StringWidth(combined) - ansi.StringWidth(descShown)
		ranges = append(ranges, lipgloss.NewRange(descStart+1, prefixWidth+ansi.StringWidth(combined)+1, descStyle))
	}
	if len(match.MatchedIndexes) > 0 {
		for _, rng := range matchedRanges(match.MatchedIndexes) {
			start, stop := bytePosToVisibleCharPos(text, rng)
			ranges = append(ranges, lipgloss.NewRange(start+prefixWidth+1, stop+prefixWidth+2, matchStyle))
		}
	}
	// StyleRanges walks ranges in order and expects them sorted by Start;
	// an out-of-order range regresses its internal cursor and corrupts the
	// output (the tail gets duplicated).
	slices.SortFunc(ranges, func(a, b lipgloss.Range) int { return a.Start - b.Start })
	content = lipgloss.StyleRanges(content, ranges...)

	cache[width] = content
	return content
}

// matchedRanges converts a list of match indexes into contiguous ranges.
func matchedRanges(in []int) [][2]int {
	if len(in) == 0 {
		return [][2]int{}
	}
	current := [2]int{in[0], in[0]}
	if len(in) == 1 {
		return [][2]int{current}
	}
	var out [][2]int
	for i := 1; i < len(in); i++ {
		if in[i] == current[1]+1 {
			current[1] = in[i]
		} else {
			out = append(out, current)
			current = [2]int{in[i], in[i]}
		}
	}
	out = append(out, current)
	return out
}

// bytePosToVisibleCharPos converts byte positions to visible character positions.
func bytePosToVisibleCharPos(str string, rng [2]int) (int, int) {
	bytePos, byteStart, byteStop := 0, rng[0], rng[1]
	pos, start, stop := 0, 0, 0
	gr := uniseg.NewGraphemes(str)
	for byteStart > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	start = pos
	for byteStop > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	stop = pos
	return start, stop
}

// Ensure CompletionItem implements the required interfaces.
var (
	_ list.Item           = (*CompletionItem)(nil)
	_ list.FilterableItem = (*CompletionItem)(nil)
	_ list.MatchSettable  = (*CompletionItem)(nil)
	_ list.Focusable      = (*CompletionItem)(nil)
)
