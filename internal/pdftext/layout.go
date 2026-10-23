package pdftext

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
	"golang.org/x/text/unicode/norm"
)

// This file turns a page's glyphs into markdown that keeps what a reader needs: headings, code with its
// indentation, lists, whole paragraphs, and formulas. PDFs have no structure of their own, so it is read
// from the typesetting: a heading is bigger than the body text, code is set in a monospace font, a
// display formula is indented and set in a math font, a paragraph is a run of lines with normal spacing.
// Each rule is a heuristic, kept conservative: when a rule is unsure the text is left as plain lines,
// which is what the reader got before.

var (
	monoFont   = regexp.MustCompile(`(?i)mono|courier|consolas|typewriter|cmtt|lmmono|menlo|inconsolata|sourcecode|lucidaconsole`)
	mathFont   = regexp.MustCompile(`(?i)math|cmmi|cmsy|cmex|cmbsy|msam|msbm|stix|xits|mtmi|mtsy|mtextra|euclid`)
	boldFont   = regexp.MustCompile(`(?i)bold|black|heavy|semibold|demi`)
	dotLeaders = regexp.MustCompile(`(?:\s?\.){4,}`)
	// sectionNumber starts a heading with its own number ("8.7 Title"): it is a new heading, not the
	// second line of the one above.
	sectionNumber = regexp.MustCompile(`^\d+(?:\.\d+)*\.?\s`)
	hasWordChars  = regexp.MustCompile(`[\p{L}\p{N}]{2,}`)
	listMarker    = regexp.MustCompile(`^(?:[•◦▪●■□‣∙·–—-]|\d{1,3}[.)]|[a-z][.)])\s+`)
)

const (
	headingRatio    = 1.12 // a line at least this much bigger than the body text is a heading
	scriptRatio     = 0.85 // a glyph smaller than this share of its line is a sub or superscript
	scriptShift     = 0.08 // and is moved up or down by more than this share of the line's size (subscripts sit low by about 0.15)
	displayIndent   = 2.5  // a display formula is indented by more than this many body sizes
	paragraphGap    = 1.55 // a vertical gap above this many body sizes between lines is a new paragraph
	fractionShift   = 0.45 // a normal-size glyph this far above or below the baseline is part of a fraction
	maxDisplayWidth = 70   // a display formula line has at most this many glyphs
)

type glyph struct {
	pdf.Text
	mono, bold, math bool

	// table is, for the one glyph that stands in for a table in the page's text, the table's number plus one.
	table int

	// gx and gw are the glyph's place on the page, for geometry (fractions, columns, extents). The
	// library gives one x for a whole drawn string and no usable width for many fonts, so a glyph of a
	// string is placed at its index along the string, at an estimated advance.
	gx, gw float64
}

func newGlyph(t pdf.Text) glyph {
	g := glyph{Text: t}
	g.mono = monoFont.MatchString(t.Font)
	g.bold = boldFont.MatchString(t.Font)
	g.math = mathFont.MatchString(t.Font) || hasMathRune(t.S)
	return g
}

func hasMathRune(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x1D400 && r <= 0x1D7FF, // mathematical alphanumerics (𝑥, 𝒟, 𝜃)
			r >= 0x2200 && r <= 0x22FF, // mathematical operators
			r >= 0x2A00 && r <= 0x2AFF,
			r == 0x2113, r == 0x211B, r == 0x2112, r == 0x211D, r == 0x2124, r == 0x2102, r == 0x2115, r == 0x211A:
			return true
		}
	}
	return false
}

// placeGlyphs sets gx and gw. Glyphs that share an x and a baseline are one drawn string: the first
// starts at x and each next one an advance later. An advance of half the size is the average of a
// proportional Latin font, 0.6 of a monospace one. A glyph with a real width keeps its own x.
func placeGlyphs(gs []glyph) {
	run := 0
	for i := range gs {
		g := &gs[i]
		if i > 0 && math.Abs(g.X-gs[i-1].X) < 0.01 && math.Abs(g.Y-gs[i-1].Y) < 0.01 && g.W == 0 {
			run++
		} else {
			run = 0
		}
		adv := g.W
		if adv <= 0 {
			adv = g.size() * 0.5
			if g.mono {
				adv = g.size() * 0.6
			}
		}
		g.gx = g.X + float64(run)*adv
		if g.W > 0 {
			g.gx = g.X
		}
		g.gw = adv
	}
}

func (g glyph) isSpace() bool { return g.S == " " || g.S == "\t" || g.S == " " }

func (g glyph) isJunk() bool { return g.S == "" || g.S == "\n" || g.S == "\r" || g.S == "�" }

func (g glyph) isMark() bool {
	r, _ := utf8.DecodeRuneInString(g.S)
	return unicode.IsMark(r)
}

func (g glyph) size() float64 {
	if s := math.Abs(g.FontSize); s > 0 {
		return s
	}
	return 10
}

type lineKind int

const (
	kindText lineKind = iota
	kindHeading
	kindCode
	kindMath
	kindTable
)

// line is glyphs that share a baseline, in drawing order.
type line struct {
	glyphs    []glyph
	y, x0, x1 float64
	size      float64 // the most common glyph size
	kind      lineKind
	level     int // heading level
	text      string
}

// PageMarkdown returns a page as markdown: headings, code blocks, lists, paragraphs and formulas.
func PageMarkdown(page pdf.Page) (md string, err error) {
	return PageMarkdownWith(page, PageOptions{})
}

// PageMarkdownWith is PageMarkdown for a caller that has a layout model to ask about tables that have no ruling
// lines. The source is only asked on a page whose text looks columnar and that is not rotated, and what it returns
// only adds tables: what the rulings found stays, and a page is never worse for a model that failed.
func PageMarkdownWith(page pdf.Page, opts PageOptions) (md string, err error) {
	defer func() {
		if r := recover(); r != nil {
			md, err = "", errorFromPanic(r)
		}
	}()
	if page.V.IsNull() {
		return "", nil
	}
	texts := page.Content().Text
	texts = restoreLigatures(texts, ligatureNames(page))
	skip := markerIndexes(page, len(texts)) // by index: the order of the glyphs must not change before this
	rul, _ := pageRulings(page)             // no rulings (or unreadable ones) means no tables, not a failed page
	rotation := pageRotation(page)
	models := modelContext{}
	if opts.Tables != nil && rotation == 0 {
		models = modelContext{opts: opts, box: boxOf(page)}
	}
	md = layoutMarkdownPage(turnTexts(page, texts, rotation), skip, turnRulings(rul.merged(), rotation), models)
	return withoutControls(ligatureLetters.Replace(md)), nil
}

// markerIndexes finds the glyphs the library invents: after every TJ it shows a "\n" through the current
// font's encoding, which comes out as U+FFFD in one font and as a real letter or an omega in another, and
// in a subset font it cannot be told from text by its value. Its place can be told: the library produces
// one glyph per decoded character, so replaying its count over the content stream gives the index of each
// marker. If the replay does not add up to the glyphs it returned, nothing is dropped.
func markerIndexes(page pdf.Page, total int) map[int]bool {
	skip := map[int]bool{}
	ok := true
	func() {
		defer func() {
			if recover() != nil {
				ok = false
			}
		}()
		var encode func(string) string
		index := 0
		count := func(raw string) int {
			if encode == nil {
				return utf8.RuneCountInString(raw)
			}
			return utf8.RuneCountInString(encode(raw))
		}
		pdf.Interpret(page.V.Key("Contents"), func(stk *pdf.Stack, op string) {
			args := make([]pdf.Value, stk.Len())
			for i := len(args) - 1; i >= 0; i-- {
				args[i] = stk.Pop()
			}
			switch op {
			case "Tf":
				if len(args) == 2 {
					if enc := page.Font(args[0].Name()).Encoder(); enc != nil {
						encode = func(raw string) string { return enc.Decode(raw) }
					} else {
						encode = nil
					}
				}
			case "Tj", "'", "\"":
				if len(args) > 0 {
					index += count(args[len(args)-1].RawString())
				}
			case "TJ":
				if len(args) > 0 {
					for i := 0; i < args[0].Len(); i++ {
						if item := args[0].Index(i); item.Kind() == pdf.String {
							index += count(item.RawString())
						}
					}
					for m := count("\n"); m > 0; m-- {
						skip[index] = true
						index++
					}
				}
			}
		})
		if index != total {
			ok = false
		}
	}()
	if !ok {
		return nil
	}
	return skip
}

func layoutMarkdown(texts []pdf.Text) string { return layoutMarkdownWithout(texts, nil) }

// layoutMarkdownWithout is layoutMarkdown without the glyphs at the indexes in skip.
func layoutMarkdownWithout(texts []pdf.Text, skip map[int]bool) string {
	return layoutMarkdownPage(texts, skip, rulings{}, modelContext{})
}

// layoutMarkdownPage is layoutMarkdownWithout for a page that has rulings: the tables they bound are written as
// tables, in place, and their text is not repeated in the running text.
// modelContext is what a page needs to ask a layout model about its tables.
type modelContext struct {
	opts PageOptions
	box  pageBox
}

func layoutMarkdownPage(texts []pdf.Text, skip map[int]bool, rul rulings, models modelContext) string {
	glyphs := make([]glyph, 0, len(texts))
	for i, t := range texts {
		if skip[i] {
			continue
		}
		if g := newGlyph(t); !g.isJunk() {
			glyphs = append(glyphs, g)
		}
	}
	placeGlyphs(glyphs)
	var tables []foundTable
	var used [][]int
	if len(rul.h) >= 1 {
		tables, used = buildTables(rul, glyphs, pageWidth(glyphs))
	}
	if models.opts.Tables != nil {
		taken := make([]bool, len(glyphs))
		for _, idx := range used {
			for _, i := range idx {
				taken[i] = true
			}
		}
		more, moreUsed := modelTables(models.opts, models.box, glyphs, taken, tables, pageWidth(glyphs))
		tables, used = append(tables, more...), append(used, moreUsed...)
	}
	glyphs = standInForTables(glyphs, tables, used)
	lines := buildLines(glyphs)
	if len(lines) == 0 {
		return ""
	}
	var prose []*line
	for _, l := range lines {
		if t := tableOfLine(l); t > 0 {
			l.kind, l.text = kindTable, tables[t-1].markdown
			continue
		}
		prose = append(prose, l)
	}
	if len(prose) == 0 {
		return assemble(lines, 10, columns{})
	}
	body := bodySize(prose)
	cols := pageColumns(prose, body)
	classify(prose, body, cols)
	return assemble(lines, body, cols)
}

// tableOfLine says which table a line stands for (counting from 1), or 0 when it is text. The line of a table is its stand-in glyph, and
// the spaces drawn after it: buildLines adds a space to the line it follows, so a line of one glyph was not the whole rule, and a
// table followed by a space was written as the invisible stand-in character, with all its cells lost.
func tableOfLine(l *line) int {
	table := 0
	for _, g := range l.glyphs {
		switch {
		case g.table > 0 && table == 0:
			table = g.table
		case g.isSpace():
		default:
			return 0
		}
	}
	return table
}

func pageWidth(glyphs []glyph) float64 {
	right := 0.0
	for _, g := range glyphs {
		right = math.Max(right, g.gx+g.gw)
	}
	return right
}

// standInForTables takes the glyphs of each table out of the page's glyphs and puts one glyph in their place,
// where the first of them was drawn, so the table keeps its place in the reading order.
func standInForTables(glyphs []glyph, tables []foundTable, used [][]int) []glyph {
	if len(tables) == 0 {
		return glyphs
	}
	drop := map[int]bool{}
	before := map[int][]int{} // glyph index -> the tables written before it
	var last []int            // tables with nowhere to go but the end
	for t, idx := range used {
		first := -1
		for _, i := range idx {
			drop[i] = true
			if first < 0 || i < first {
				first = i
			}
		}
		if first < 0 {
			first = tables[t].anchor // a table with no glyphs of its own goes before the text just under it
		}
		if first < 0 {
			last = append(last, t)
			continue
		}
		before[first] = append(before[first], t)
	}
	standIn := func(t int) glyph {
		stand := glyph{gx: tables[t].x0, gw: 1, table: t + 1}
		stand.S, stand.X, stand.Y, stand.FontSize = "\uE000", tables[t].x0, tables[t].y1, 10
		return stand
	}
	out := make([]glyph, 0, len(glyphs)+len(tables))
	for i, g := range glyphs {
		for _, t := range before[i] {
			out = append(out, standIn(t))
		}
		if !drop[i] {
			out = append(out, g)
		}
	}
	for _, t := range last {
		out = append(out, standIn(t))
	}
	return out
}

// buildLines groups glyphs into lines in drawing order, which keeps the reading order of columns. An
// accent drawn as its own glyph belongs to the line of the letter before it and never starts a line.
func buildLines(glyphs []glyph) []*line {
	var lines []*line
	var cur *line
	var prev *glyph
	for i := range glyphs {
		g := glyphs[i]
		if g.isSpace() {
			if cur != nil {
				cur.glyphs = append(cur.glyphs, g)
			}
			continue
		}
		newLine := cur == nil || g.table > 0 || (prev != nil && prev.table > 0)
		if !newLine && !g.isMark() {
			size := math.Max(g.size(), prev.size()) // a script moves against the size of the text it belongs to
			dy := math.Abs(g.Y - prev.Y)
			newLine = dy > size*lineShift || g.X-(prev.X+prev.W) < -size*backwardGap
		}
		if newLine {
			cur = &line{y: g.Y, x0: g.X}
			lines = append(lines, cur)
		}
		cur.glyphs = append(cur.glyphs, g)
		if !g.isMark() {
			prev = &glyphs[i]
		}
	}
	for _, l := range lines {
		finishLine(l)
	}
	out := lines[:0]
	for _, l := range lines {
		if l.size > 0 {
			out = append(out, l)
		}
	}
	return out
}

func finishLine(l *line) {
	counts := map[float64]int{}
	l.x0, l.x1 = math.Inf(1), math.Inf(-1)
	for _, g := range l.glyphs {
		if g.isSpace() || g.isMark() {
			continue
		}
		counts[math.Round(g.size()*10)/10]++
		l.x0 = math.Min(l.x0, g.gx)
		l.x1 = math.Max(l.x1, g.gx+g.gw)
	}
	best, bestCount := 0.0, 0
	for size, n := range counts {
		if n > bestCount || (n == bestCount && size > best) {
			best, bestCount = size, n
		}
	}
	l.size = best
	// The baseline of the line is that of its main-size glyphs, not of a superscript that opened it.
	for _, g := range l.glyphs {
		if !g.isSpace() && !g.isMark() && math.Abs(g.size()-best) < 0.5 {
			l.y = g.Y
			break
		}
	}
}

// bodySize is the most common glyph size among text that is not code.
func bodySize(lines []*line) float64 {
	counts := map[float64]int{}
	for _, l := range lines {
		for _, g := range l.glyphs {
			if !g.isSpace() && !g.mono {
				counts[math.Round(g.size()*10)/10]++
			}
		}
	}
	if len(counts) == 0 {
		for _, l := range lines {
			counts[math.Round(l.size*10)/10]++
		}
	}
	best, bestCount := 10.0, 0
	for size, n := range counts {
		if n > bestCount || (n == bestCount && size < best) {
			best, bestCount = size, n
		}
	}
	return best
}

// bodyMargins returns the left edge of body text and the right edge of its longest lines.
func bodyMargins(lines []*line, body float64) (left, right float64) {
	left, right = math.Inf(1), math.Inf(-1)
	var rights []float64
	for _, l := range lines {
		if math.Abs(l.size-body) > body*0.1 || len(l.glyphs) < 30 || isMonoLine(l) {
			continue
		}
		left = math.Min(left, l.x0)
		rights = append(rights, l.x1)
	}
	if len(rights) == 0 {
		for _, l := range lines {
			left = math.Min(left, l.x0)
			right = math.Max(right, l.x1)
		}
		return left, right
	}
	sort.Float64s(rights)
	return left, rights[len(rights)*9/10]
}

func share(l *line, pick func(glyph) bool) float64 {
	total, hit := 0, 0
	for _, g := range l.glyphs {
		if g.isSpace() || g.isMark() {
			continue
		}
		total++
		if pick(g) {
			hit++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

func isMonoLine(l *line) bool { return share(l, func(g glyph) bool { return g.mono }) >= 0.7 }

func hasMath(l *line) bool {
	for _, g := range l.glyphs {
		if g.math {
			return true
		}
	}
	return false
}

// classify decides what each line is.
func classify(lines []*line, body float64, cols columns) {
	for _, l := range lines {
		l.text = plainLine(l)
		switch {
		case isMonoLine(l):
			l.kind = kindCode
		case l.size >= body*headingRatio && isHeadingText(l.text) && !hasMath(l):
			l.kind = kindHeading
			l.level = headingLevel(l.size / body)
		}
	}
	// A display formula: indented well past the margin, set in a math font, short.
	seeds := make([]bool, len(lines))
	for i, l := range lines {
		if l.kind == kindText && isFormulaLine(l) && l.x0 > cols.margin(l)+body*displayIndent && len(l.glyphs) <= maxDisplayWidth {
			seeds[i] = true
		}
	}
	for i, l := range lines {
		if l.kind != kindText || seeds[i] {
			continue
		}
		// Fragments of a formula (a numerator, a denominator, a limit) are short lines beside a seed.
		if len(l.glyphs) <= 12 && l.x0 > cols.margin(l)+body*displayIndent && (nearSeed(lines, seeds, i, -1, body) || nearSeed(lines, seeds, i, 1, body)) {
			seeds[i] = true
		}
	}
	for i, l := range lines {
		if seeds[i] {
			l.kind = kindMath
		}
	}
}

// isHeadingText: short, with words in it, and not a sentence (a long line ending in a full stop is a note).
func isHeadingText(text string) bool {
	n := len([]rune(text))
	if n > 100 || !hasWordChars.MatchString(text) {
		return false
	}
	return !(n > 60 && strings.HasSuffix(strings.TrimSpace(text), "."))
}

// isFormulaLine: a line that is mostly set in a math font, or holds a relation. An author line with a
// dagger set in a math font is not a formula.
func isFormulaLine(l *line) bool {
	if !hasMath(l) {
		return false
	}
	if share(l, func(g glyph) bool { return g.math }) >= 0.3 {
		return true
	}
	for _, g := range l.glyphs {
		if g.math && relations[g.S] {
			return true
		}
	}
	return false
}

func nearSeed(lines []*line, seeds []bool, i, step int, body float64) bool {
	for j := i + step; j >= 0 && j < len(lines); j += step {
		if math.Abs(lines[j].y-lines[i].y) > body*2.2 {
			return false
		}
		if seeds[j] {
			return true
		}
		if len(lines[j].glyphs) > 12 {
			return false
		}
	}
	return false
}

func headingLevel(ratio float64) int {
	switch {
	case ratio >= 1.9:
		return 1
	case ratio >= 1.3:
		return 2
	default:
		return 3
	}
}

// assemble writes the blocks of a page in order.
func assemble(lines []*line, body float64, cols columns) string {
	var blocks []string
	var para []*line
	flush := func() {
		if len(para) > 0 {
			blocks = append(blocks, paragraphs(para, body, cols)...)
			para = nil
		}
	}
	for i := 0; i < len(lines); {
		l := lines[i]
		switch l.kind {
		case kindCode:
			flush()
			j := i
			for j < len(lines) && lines[j].kind == kindCode {
				j++
			}
			blocks = append(blocks, codeBlock(lines[i:j]))
			i = j
		case kindTable:
			flush()
			blocks = append(blocks, l.text)
			i++
		case kindMath:
			flush()
			j := i
			for j < len(lines) && lines[j].kind == kindMath {
				j++
			}
			for _, f := range linearizeMath(lines[i:j]) {
				blocks = append(blocks, "$$\n"+f+"\n$$")
			}
			i = j
		case kindHeading:
			flush()
			j := i
			var parts []string
			for j < len(lines) && lines[j].kind == kindHeading && math.Abs(lines[j].size-l.size) < 0.6 &&
				(j == i || !sectionNumber.MatchString(lines[j].text)) && len(parts) < 3 {
				parts = append(parts, lines[j].text)
				j++
			}
			blocks = append(blocks, strings.Repeat("#", l.level)+" "+strings.Join(parts, " "))
			i = j
		default:
			para = append(para, l)
			i++
		}
	}
	flush()
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

// paragraphs turns consecutive text lines into paragraphs and list items.
func paragraphs(lines []*line, body float64, cols columns) []string {
	var out []string
	var cur strings.Builder
	var prev *line
	known := compoundsIn(lines)
	listIndent := math.Inf(1)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		listIndent = math.Inf(1)
	}
	for _, l := range lines {
		text := strings.TrimSpace(l.text)
		if text == "" {
			continue
		}
		item := listMarker.MatchString(text)
		newBlock := prev == nil || item
		if !newBlock {
			gap := prev.y - l.y
			margin, rightEdge := cols.margin(l), cols.rightEdge(prev)
			ended := endsSentence(prev.text) && prev.x1 < rightEdge-body*4
			switch {
			case gap > body*paragraphGap || gap < 0:
				newBlock = true
			case ended:
				newBlock = true
			case (prev.x1 < rightEdge-body*12 || strings.Contains(prev.text, " … ")) && math.IsInf(listIndent, 1):
				// A line that stops well short of the right edge, or an entry with dot leaders, ends its
				// paragraph: this is a list of short lines (a table of contents, an address), which keeps its
				// line breaks.
				cur.WriteByte('\n')
				cur.WriteString(text)
				prev = l
				continue
			case l.x0 > listIndent+body*0.5 && !math.IsInf(listIndent, 1):
				// a hanging indent continues the list item
			case l.x0 > margin+body*1.2 && prev.x0 <= margin+body*0.5 && math.IsInf(listIndent, 1):
				newBlock = true // a first-line indent starts a paragraph
			}
		}
		if newBlock {
			flush()
			if item {
				cur.WriteString("- ")
				text = listMarker.ReplaceAllString(text, "")
				listIndent = l.x0
			}
			cur.WriteString(text)
		} else {
			joinLine(&cur, text, known)
		}
		prev = l
	}
	flush()
	return out
}

func endsSentence(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, ":") || strings.HasSuffix(s, "»")
}

// compounds is the evidence a page gives about words broken at the end of a line: the words it writes with a hyphen
// in the middle of a line, lower case. "task-" at the end of a line followed by "specific" is a syllable break when the page
// never writes "task-specific" and a compound when it does.
type compounds map[string]bool

func compoundsIn(lines []*line) compounds {
	found := compounds{}
	for _, l := range lines {
		for _, word := range strings.Fields(l.text) {
			word = strings.ToLower(strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
			if strings.Contains(word, "-") {
				found[word] = true
			}
		}
	}
	return found
}

// writes says whether the page has a compound that is, or begins with, the word: "pre-train" is written by a page that has
// "pre-trained".
func (c compounds) writes(word string) bool {
	for compound := range c {
		if strings.HasPrefix(compound, word) {
			return true
		}
	}
	return false
}

// joinLine continues a paragraph with the next line, undoing a hyphen the typesetter added at a line end.
func joinLine(cur *strings.Builder, next string, known compounds) {
	s := cur.String()
	if strings.HasSuffix(s, "-") && len(s) >= 2 {
		before, _ := utf8.DecodeLastRuneInString(s[:len(s)-1])
		first, _ := utf8.DecodeRuneInString(next)
		if unicode.IsLetter(before) && unicode.IsLower(first) {
			word, _, _ := strings.Cut(next, " ")
			if strings.Contains(word, "-") {
				// "English-" then "to-German" is a compound word split at its own hyphen: the hyphen stays and no
				// space is added.
				cur.WriteString(next)
				return
			}
			left := s
			if i := strings.LastIndexAny(s, " \n"); i >= 0 {
				left = s[i+1:]
			}
			piece := strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			if known.writes(strings.ToLower(left + piece)) {
				cur.WriteString(next) // the page writes this compound with its hyphen elsewhere
				return
			}
			// A syllable break ("approxima-" then "teurs") is followed by a plain fragment.
			cur.Reset()
			cur.WriteString(s[:len(s)-1])
			cur.WriteString(next)
			return
		}
	}
	cur.WriteByte(' ')
	cur.WriteString(next)
}

// codeBlock writes monospace lines as a fenced block. In a monospace font every character is one column
// wide, so each glyph is put at its column: that restores the indentation and the spacing inside lines
// exactly, whatever widths the PDF reports for the font.
func codeBlock(lines []*line) string {
	minX := math.Inf(1)
	for _, l := range lines {
		for _, g := range l.glyphs {
			if !g.isSpace() && !g.isMark() {
				minX = math.Min(minX, g.X)
			}
		}
	}
	advance := monoAdvance(lines, minX)

	var pitches []float64
	for i := 1; i < len(lines); i++ {
		pitches = append(pitches, lines[i-1].y-lines[i].y)
	}
	sort.Float64s(pitches)
	pitch := lines[0].size * 1.2
	if len(pitches) > 0 {
		pitch = pitches[len(pitches)/2]
	}

	var sb strings.Builder
	sb.WriteString("```\n")
	for i, l := range lines {
		if i > 0 && pitch > 0 && lines[i-1].y-l.y > pitch*1.7 {
			sb.WriteByte('\n') // a blank line in the listing
		}
		sb.WriteString(monoLine(l, minX, advance))
		sb.WriteByte('\n')
	}
	sb.WriteString("```")
	return sb.String()
}

// monoAdvance finds the width of one character. Every drawn string starts at a whole number of
// characters from the left edge of the listing, so the width is the one for which all those offsets are
// closest to whole numbers, searched between 0.45 and 0.7 of the font size (a monospace character is
// about 0.6).
func monoAdvance(lines []*line, minX float64) float64 {
	var offsets []float64
	size := 0.0
	for _, l := range lines {
		size = math.Max(size, l.size)
		last := math.NaN()
		for _, g := range l.glyphs {
			if g.isSpace() || g.isMark() || g.X == last {
				continue
			}
			last = g.X
			offsets = append(offsets, g.X-minX)
		}
	}
	best, bestErr := size*0.6, math.Inf(1)
	for a := size * 0.45; a <= size*0.7; a += size * 0.0005 {
		sum := 0.0
		for _, o := range offsets {
			f := o/a - math.Round(o/a)
			sum += f * f
		}
		// Several widths can fit (half the true one fits whenever the offsets are even), so among the
		// best the one closest to the usual 0.6 wins.
		if sum < bestErr-1e-9 || (sum <= bestErr+1e-9 && math.Abs(a-size*0.6) < math.Abs(best-size*0.6)) {
			best, bestErr = a, math.Min(sum, bestErr)
		}
	}
	return best
}

func monoLine(l *line, minX, advance float64) string {
	var cells []string // one string per column, so a glyph with an accent stays in its column
	put := func(col int, s string) {
		for len(cells) <= col {
			cells = append(cells, " ")
		}
		cells[col] = s
	}
	// A drawn string either starts at its own x, which includes the width of what came before, or, when
	// the library could not advance by widths, only moves by the spacing between strings. The two are told
	// apart by the distance to the previous string: shorter than that string is long means the widths
	// were not applied, and the string goes right after the previous one, plus the gap the distance gives.
	last := 0
	var (
		haveRun           bool
		runX              float64
		runCol, runLength int
		pos               int // glyph index within the current run
	)
	for _, g := range l.glyphs {
		if g.isSpace() {
			continue
		}
		if g.isMark() {
			if last < len(cells) {
				cells[last] += g.S
			}
			continue
		}
		if !haveRun || g.X != runX {
			col := max(0, int(math.Round((g.X-minX)/advance)))
			if haveRun {
				if dx := (g.X - runX) / advance; dx < float64(runLength)-0.5 {
					col = runCol + runLength + max(0, int(math.Round(dx)))
				}
			}
			haveRun, runX, runCol, pos = true, g.X, col, 0
		}
		put(runCol+pos, g.S)
		last = runCol + pos
		pos++
		runLength = pos
	}
	return norm.NFC.String(strings.TrimRight(strings.Join(cells, ""), " "))
}

// plainLine is the text of one line, with inline formulas wrapped in $...$ and their scripts marked.
func plainLine(l *line) string {
	var sb strings.Builder
	var prev *glyph
	endX := 0.0
	pendingSpace := false
	inMath := false
	heldMark := ""
	script := byte(0) // 0, '^' or '_': the script being written, whose closing brace is pending

	closeScript := func() {
		if script != 0 {
			sb.WriteByte('}')
			script = 0
		}
	}
	closeMath := func() {
		closeScript()
		if inMath {
			trimTrailingSpace(&sb)
			// Punctuation that follows a formula belongs to the sentence, not to the formula.
			s := sb.String()
			tail := ""
			for len(s) > 0 && (strings.ContainsRune(".,;:", rune(s[len(s)-1])) ||
				(s[len(s)-1] == ')' && strings.Count(s, ")") > strings.Count(s, "("))) {
				tail = string(s[len(s)-1]) + tail
				s = strings.TrimRight(s[:len(s)-1], " ")
			}
			sb.Reset()
			sb.WriteString(s)
			sb.WriteByte('$')
			sb.WriteString(tail)
			inMath = false
		}
	}

	for i := range l.glyphs {
		g := l.glyphs[i]
		if g.isSpace() {
			pendingSpace = true
			continue
		}
		if g.isMark() {
			// An accent normally follows its letter. Drawn before a math letter that follows a plain
			// one (a hat over a variable), it belongs to the math letter that comes next.
			if i+1 < len(l.glyphs) && l.glyphs[i+1].math && !l.glyphs[i+1].isMark() && (prev == nil || !prev.math) {
				heldMark += g.S
				continue
			}
			sb.WriteString(g.S)
			endX = math.Max(endX, g.X+g.W)
			continue
		}
		size := l.size
		if prev != nil {
			if gap := g.X - endX; gap > size*wordGap {
				pendingSpace = true
			}
		}
		isScript := inMath && g.size() < size*scriptRatio && math.Abs(g.Y-l.y) > size*scriptShift
		wantMath := g.math || (inMath && (isScript || !unicode.IsLetter(firstRune(g.S))))

		switch {
		case wantMath && !inMath:
			if pendingSpace && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			pendingSpace = false
			sb.WriteByte('$')
			inMath = true
		case !wantMath && inMath:
			closeMath()
		}
		if inMath {
			want := byte(0)
			if isScript {
				want = '^'
				if g.Y < l.y {
					want = '_'
				}
			}
			if want != script {
				closeScript()
				if want != 0 {
					sb.WriteByte(want)
					sb.WriteByte('{')
					script = want
					pendingSpace = false
				}
			}
		}
		if pendingSpace && sb.Len() > 0 && script == 0 && !strings.HasSuffix(sb.String(), " ") {
			sb.WriteByte(' ')
		}
		pendingSpace = false
		sb.WriteString(g.S)
		sb.WriteString(heldMark)
		heldMark = ""
		prev = &l.glyphs[i]
		endX = g.X + g.W
	}
	closeMath()
	out := norm.NFC.String(strings.TrimSpace(sb.String()))
	return dotLeaders.ReplaceAllString(out, " … ")
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}
