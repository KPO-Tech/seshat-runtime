package pdftext

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/KPO-Tech/seshat-runtime/internal/mdtable"
)

// foundTable is a table read off a page: where it is, so its text can be taken out of the running text, and what
// it comes to as markdown.
type foundTable struct {
	x0, y0, x1, y1 float64
	markdown       string
	// anchor is the index of the glyph the table is written before, for a table with no glyphs of its own on the
	// page (one that is a picture): without it the table goes where its first glyph was. -1 means not needed.
	anchor int
}

const (
	chunkGap         = 0.5  // a gap wider than this many em separates two cells of a row; a word space is a quarter
	minGapEm         = 0.45 // a white column this wide, with nothing crossing it, separates two columns
	crossTolerance   = 0.2  // up to this share of the rows may run across a column gap (a heading that spans columns)
	bandRowRules     = 2    // at least this many rules between the rows, with short bands, make each band a row
	maxBandLines     = 4    // a band with more lines than this is not one row
	edgeSlack        = 1.5  // points of slack when deciding whether a glyph is inside a region
	minFill          = 0.30 // a table whose cells are mostly empty is a drawing or a form, not data
	maxTwoRuleHeight = 350  // a ruled block of two rules taller than this, in points, is the page's frame
	minBlockRows     = 3    // a ruled block of fewer rows is a boxed note or a caption, not a table
	splitGapEm       = 0.6  // two header words this far apart, on a column separator, are two headings
)

// chunk is a run of text on one baseline with no wide gap in it: the content of one cell, or a piece of it.
type chunk struct {
	x0, x1 float64
	y      float64 // baseline
	size   float64
	text   string
	words  []chunk // the chunk's words, left to right, when it has more than one
}

// textRow is the chunks that share a baseline.
type textRow struct {
	y      float64
	size   float64
	chunks []chunk
}

// buildTables turns the regions of a page into tables, using the page's glyphs for the text. Each glyph used is
// reported so the caller can take it out of the running text.
func buildTables(rul rulings, glyphs []glyph, pageWidth float64) ([]foundTable, [][]int) {
	var tables []foundTable
	var used [][]int
	claimed := map[int]bool{}
	var split []tableRegion
	for _, reg := range findRegions(rul) {
		if reg.grid {
			split = append(split, reg)
		} else {
			split = append(split, splitBlock(reg, glyphs)...)
		}
	}
	build := func(split []tableRegion) {
		for _, reg := range split {
			var idx []int
			for i, g := range glyphs {
				if claimed[i] || g.isJunk() {
					continue
				}
				if inside(reg, g) {
					idx = append(idx, i)
				}
			}
			if len(idx) == 0 {
				continue
			}
			picked := make([]glyph, len(idx))
			for k, i := range idx {
				picked[k] = glyphs[i]
			}
			table, ok := buildTable(reg, picked, pageWidth)
			if !ok {
				continue
			}
			var sb strings.Builder
			table.RenderMarkdown(&sb)
			md := strings.TrimSpace(sb.String())
			if md == "" {
				continue
			}
			for _, i := range idx {
				claimed[i] = true
			}
			tables = append(tables, foundTable{x0: reg.x0, y0: reg.y0, x1: reg.x1, y1: reg.y1, markdown: md, anchor: -1})
			used = append(used, idx)
		}
	}
	build(split)
	// A rule under a header row, with lines under it, is a table that has no other rule to be found by.
	var unclaimed []glyph
	for i, g := range glyphs {
		if !claimed[i] {
			unclaimed = append(unclaimed, g)
		}
	}
	build(headerRuleRegions(rul, unclaimed, tables))
	return tables, used
}

var captionStart = regexp.MustCompile(`^(?:Table|Tab\.|Figure|Fig\.)\s*[0-9IVX]+[.:]?`)

// splitBlock cuts a ruled block where the text between two of its rules is not a table's: a caption, or a
// sentence that spans the width. Rules of the same width on one page are not always one table: two tables one
// above the other, with a caption between, have equal rules. Each piece keeps the rules around its own rows.
func splitBlock(reg tableRegion, glyphs []glyph) []tableRegion {
	rules := reg.horizontal
	if len(rules) < 3 {
		return []tableRegion{reg}
	}
	sorted := append([]hline(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].y > sorted[j].y })

	width := reg.x1 - reg.x0
	var pieces []tableRegion
	start := 0
	for k := 0; k+1 < len(sorted); k++ {
		var inBand []glyph
		for _, g := range glyphs {
			cx, cy := g.gx+g.gw/2, g.Y+g.size()*0.3
			if cx >= reg.x0-edgeSlack && cx <= reg.x1+edgeSlack && cy < sorted[k].y && cy > sorted[k+1].y && !g.isJunk() {
				inBand = append(inBand, g)
			}
		}
		if !proseBand(textRows(inBand), width) {
			continue
		}
		if k-start+1 >= minRegionRules {
			pieces = append(pieces, blockOf(reg, sorted[start:k+1]))
		}
		start = k + 1
	}
	if len(sorted)-start >= minRegionRules {
		pieces = append(pieces, blockOf(reg, sorted[start:]))
	}
	return pieces
}

func blockOf(reg tableRegion, rules []hline) tableRegion {
	out := tableRegion{x0: reg.x0, x1: reg.x1, y1: rules[0].y, y0: rules[len(rules)-1].y}
	out.horizontal = append([]hline(nil), rules...)
	return out
}

// proseBand says whether the rows between two rules hold a caption or running text. A caption starts with "Table 2"
// or "Figure 3" (its first letters may be set apart, so spaces are ignored), and running text is a row that is one
// long chunk across nearly the whole width.
func proseBand(rows []textRow, width float64) bool {
	for _, r := range rows {
		var joined strings.Builder
		for _, c := range r.chunks {
			joined.WriteString(strings.ReplaceAll(c.text, " ", ""))
		}
		if captionStart.MatchString(joined.String()) {
			return true
		}
		if len(r.chunks) != 1 {
			continue
		}
		c := r.chunks[0]
		if len([]rune(c.text)) >= 35 && c.x1-c.x0 >= 0.85*width {
			return true
		}
	}
	return false
}

// inside reports whether a glyph lies in the region. For a ruled block the top and bottom rules bound it; for a
// grid its outline does. A glyph counts by its centre.
func inside(reg tableRegion, g glyph) bool {
	cx := g.gx + g.gw/2
	cy := g.Y + g.size()*0.3
	return cx >= reg.x0-edgeSlack && cx <= reg.x1+edgeSlack && cy >= reg.y0-edgeSlack && cy <= reg.y1+edgeSlack
}

func buildTable(reg tableRegion, glyphs []glyph, pageWidth float64) (mdtable.Table, bool) {
	rows := textRows(glyphs)
	if len(rows) < 2 || mostlyCode(glyphs) || mixedSizes(rows) || reg.x0 < 3 {
		return mdtable.Table{}, false
	}

	// The columns are read off the body of the table: a header is wider than its column and spans several, and
	// would hide the gaps between them. Only the rows below the header rule count, if there is one.
	headerY := math.Inf(-1)
	if interior := interiorRules(reg); len(interior) > 0 {
		headerY = interior[0]
	}
	body := rows
	if headerY > math.Inf(-1) {
		var below []textRow
		for _, r := range rows {
			if r.y+r.size*0.3 < headerY {
				below = append(below, r)
			}
		}
		if len(below) >= 2 {
			body = below
		}
	}
	seps := gapSeparators(body, reg)
	if reg.grid {
		rules := gridSeparators(reg)
		seps = mergeSeparators(rules, withoutRuledGaps(withoutGapsInRuledColumns(seps, reg), rules))
	}
	if len(seps) < 1 {
		return mdtable.Table{}, false
	}
	cols := len(seps) + 1

	if !reg.grid && (looksLikeProse(rows, cols, reg, pageWidth) || (!reg.headerRule && !rulesHugContent(rows, reg))) {
		return mdtable.Table{}, false
	}

	cells := make([][]cell, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, rowCells(r, seps, cols, r.y+r.size*0.3 > headerY))
	}
	grouped, starts, bandMode := groupRows(rows, cells, reg, cols)
	if len(grouped) < 2 || !tabular(grouped) || (!reg.grid && len(grouped) < minBlockRows) || paragraphCells(grouped, reg.grid) || (!reg.grid && (!regular(grouped) || codeTokens(grouped))) {
		return mdtable.Table{}, false
	}

	t, ok := tableFromCells(grouped, cols)
	if !ok {
		return mdtable.Table{}, false
	}
	t.HeaderRows = headerRows(reg, rows, starts, grouped, bandMode)
	return withTitleRow(t, cols), true
}

// tableFromCells writes grouped rows of cells as a table of cols columns: a cell that spans columns has its text
// in each of them. A table whose cells are mostly empty is not a table.
func tableFromCells(grouped [][]cell, cols int) (mdtable.Table, bool) {
	t := mdtable.Table{}
	filled, total := 0, 0
	for _, row := range grouped {
		line := make([]string, cols)
		for _, c := range row {
			for k := c.from; k <= c.to && k < cols; k++ {
				line[k] = c.text
			}
			if strings.TrimSpace(c.text) != "" {
				filled++
			}
		}
		total += cols
		t.Rows = append(t.Rows, line)
	}
	if total == 0 || float64(filled)/float64(total) < minFill {
		return mdtable.Table{}, false
	}
	return t, true
}

// withTitleRow takes a first row that is one cell across the whole table as the table's caption.
func withTitleRow(t mdtable.Table, cols int) mdtable.Table {
	if caption, rest, ok := titleRow(t.Rows, cols); ok {
		t.Caption, t.Rows = caption, rest
		t.HeaderRows = max(t.HeaderRows-1, 1)
	}
	return t
}

// textRows groups glyphs into rows by baseline, top of the page first, and each row into chunks. The rows are
// found from the glyphs of the text's usual size; a smaller glyph (a superscript, a subscript) joins the nearest
// row instead of making one of its own. The glyphs are ordered by baseline first and only then by x inside a row:
// ordering by both at once is not a consistent order when baselines differ a little, and scrambles the letters.
func textRows(glyphs []glyph) []textRow {
	var gs []glyph
	for _, g := range glyphs {
		if !g.isSpace() {
			gs = append(gs, g)
		}
	}
	if len(gs) == 0 {
		return nil
	}
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].Y > gs[j].Y })
	main := textSize(gs)

	type group struct {
		y     float64
		size  float64
		glyph []glyph
	}
	var groups []*group
	var small []glyph
	for _, g := range gs {
		if g.size() < 0.85*main && !g.isMark() {
			small = append(small, g)
			continue
		}
		if n := len(groups); n > 0 && math.Abs(groups[n-1].y-g.Y) <= 0.4*groups[n-1].size {
			groups[n-1].glyph = append(groups[n-1].glyph, g)
			continue
		}
		groups = append(groups, &group{y: g.Y, size: g.size(), glyph: []glyph{g}})
	}
	for _, g := range small {
		best, dist := (*group)(nil), 0.8*main
		for _, gr := range groups {
			if d := math.Abs(gr.y - g.Y); d <= dist {
				best, dist = gr, d
			}
		}
		if best != nil {
			best.glyph = append(best.glyph, g)
			continue
		}
		groups = append(groups, &group{y: g.Y, size: g.size(), glyph: []glyph{g}})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].y > groups[j].y })

	var rows []textRow
	for _, gr := range groups {
		line := gr.glyph
		orderByString(line)
		rows = append(rows, textRow{y: gr.y, size: gr.size, chunks: chunksOf(line)})
	}
	return rows
}

// stringDrift is how far to the left, as a share of the font size, the x of a glyph may drift from the one before it and still be
// part of the same drawn string. The library gives one x for a whole string, but the invisible text that a scanner's OCR writes
// over a page makes it drift a little at each glyph (0.02 point for a size of 6.8): sorted by that x, the letters of a word come
// out in reverse order. A drift to the right keeps the order and is not touched.
const stringDrift = 0.05

// orderByString puts the glyphs of a line in reading order. Glyphs of one drawn string share the string's x (give or take the
// drift above) and keep their drawing order; the positions inside a string are estimates and must not be used to order strings
// against each other, so each glyph takes the x its string starts at.
func orderByString(line []glyph) {
	anchor := make([]float64, len(line))
	for i, g := range line {
		anchor[i] = g.X
		if i == 0 {
			continue
		}
		prev := line[i-1]
		dx := g.X - prev.X
		if g.W == 0 && math.Abs(g.Y-prev.Y) < 0.01 && (math.Abs(dx) < 0.01 || (dx < 0 && -dx < stringDrift*g.size())) {
			anchor[i] = anchor[i-1]
		}
	}
	order := make([]int, len(line))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return anchor[order[a]] < anchor[order[b]] })
	sorted := make([]glyph, len(line))
	for i, k := range order {
		sorted[i] = line[k]
	}
	copy(line, sorted)
}

// textSize is the most common glyph size, the text's own size.
func textSize(gs []glyph) float64 {
	counts := map[float64]int{}
	for _, g := range gs {
		counts[math.Round(g.size()*10)/10]++
	}
	best, bestN := 10.0, 0
	for size, n := range counts {
		if n > bestN || (n == bestN && size > best) {
			best, bestN = size, n
		}
	}
	return best
}

func chunksOf(line []glyph) []chunk {
	var out []chunk
	var cur []glyph
	endX := 0.0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		c := chunk{x0: cur[0].gx, y: cur[0].Y, size: cur[0].size()}
		for _, g := range cur {
			c.x1 = math.Max(c.x1, g.gx+g.gw)
		}
		c.text = chunkText(cur)
		c.words = wordsOf(cur)
		if strings.TrimSpace(c.text) != "" {
			out = append(out, c)
		}
		cur = nil
	}
	for _, g := range line {
		if g.isMark() {
			cur = append(cur, g)
			endX = math.Max(endX, g.gx+g.gw)
			continue
		}
		if len(cur) > 0 && g.gx-endX > chunkGap*g.size() {
			flush()
		}
		cur = append(cur, g)
		endX = g.gx + g.gw
	}
	flush()
	return out
}

// wordsOf cuts a run of glyphs into words where the geometry says a space was meant.
func wordsOf(gs []glyph) []chunk {
	var words []chunk
	var cur []glyph
	endX := 0.0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		w := chunk{x0: cur[0].gx, y: cur[0].Y, size: cur[0].size(), text: chunkText(cur)}
		for _, g := range cur {
			w.x1 = math.Max(w.x1, g.gx+g.gw)
		}
		if w.text != "" {
			words = append(words, w)
		}
		cur = nil
	}
	for _, g := range gs {
		if !g.isMark() && len(cur) > 0 && g.gx-endX > g.size()*wordGap {
			flush()
		}
		cur = append(cur, g)
		endX = math.Max(endX, g.gx+g.gw)
	}
	flush()
	if len(words) < 2 {
		return nil
	}
	return words
}

// splitAtSeparators cuts the chunks of a header row that run across a column separator where a gap between two
// words (wider than a word space) lies on it: two headings set close together read as one chunk otherwise.
func splitAtSeparators(chunks []chunk, seps []separator, size float64) []chunk {
	var out []chunk
	for _, c := range chunks {
		out = append(out, splitChunk(c, seps, size)...)
	}
	return out
}

func splitChunk(c chunk, seps []separator, size float64) []chunk {
	if len(c.words) < 2 {
		return []chunk{c}
	}
	for _, s := range seps {
		if c.x0 >= s.x || c.x1 <= s.x {
			continue
		}
		for i := 0; i+1 < len(c.words); i++ {
			left, right := c.words[i], c.words[i+1]
			if (left.x0+left.x1)/2 < s.x && (right.x0+right.x1)/2 >= s.x && right.x0-left.x1 >= splitGapEm*size {
				a := joinWords(c, c.words[:i+1])
				b := joinWords(c, c.words[i+1:])
				return append(splitChunk(a, seps, size), splitChunk(b, seps, size)...)
			}
		}
	}
	return []chunk{c}
}

func joinWords(c chunk, words []chunk) chunk {
	out := chunk{x0: words[0].x0, x1: words[len(words)-1].x1, y: c.y, size: c.size}
	var parts []string
	for _, w := range words {
		parts = append(parts, w.text)
	}
	out.text = strings.Join(parts, " ")
	if len(words) > 1 {
		out.words = words
	}
	return out
}

// chunkText writes the glyphs of one run as text, with a space where the geometry says one was meant.
func chunkText(gs []glyph) string {
	var sb strings.Builder
	endX := 0.0
	for i, g := range gs {
		if g.isMark() {
			sb.WriteString(g.S)
			continue
		}
		if i > 0 && g.gx-endX > g.size()*wordGap && !strings.HasSuffix(sb.String(), " ") {
			sb.WriteByte(' ')
		}
		sb.WriteString(g.S)
		endX = g.gx + g.gw
	}
	return strings.TrimSpace(norm.NFC.String(sb.String()))
}

// separator is the line between two columns: a vertical ruling, or a white gap in the text.
type separator struct {
	x     float64
	rule  *vline // the ruling, for a grid; nil for a gap
	width float64
}

func gridSeparators(reg tableRegion) []separator {
	var out []separator
	for i := range reg.vertical {
		v := reg.vertical[i]
		if v.x <= reg.x0+edgeSlack || v.x >= reg.x1-edgeSlack {
			continue // the outline is not between two columns
		}
		if len(out) > 0 && math.Abs(out[len(out)-1].x-v.x) < 1 {
			continue
		}
		out = append(out, separator{x: v.x, rule: &reg.vertical[i]})
	}
	return out
}

// gapSeparators finds the white columns of a block of text: stretches of the width that (almost) no row's text
// crosses. A heading that spans several columns crosses some; a share of rows may, which is crossTolerance.
func gapSeparators(rows []textRow, reg tableRegion) []separator {
	size := 0.0
	for _, r := range rows {
		size = math.Max(size, r.size)
	}
	if size == 0 {
		return nil
	}
	type edge struct {
		x     float64
		delta int
	}
	var edges []edge
	for _, r := range rows {
		for _, c := range r.chunks {
			edges = append(edges, edge{c.x0, +1}, edge{c.x1, -1})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].x == edges[j].x {
			return edges[i].delta < edges[j].delta
		}
		return edges[i].x < edges[j].x
	})
	allowed := int(math.Floor(crossTolerance * float64(len(rows))))
	var out []separator
	cover := 0
	gapStart := math.NaN() // where the current stretch of low coverage began
	for _, e := range edges {
		cover += e.delta
		switch {
		case cover <= allowed:
			if math.IsNaN(gapStart) {
				gapStart = e.x
			}
		case !math.IsNaN(gapStart):
			// Coverage just rose above what a column gap may have: the stretch that began at gapStart ends here.
			if e.x-gapStart >= minGapEm*size && gapStart > reg.x0+size && e.x < reg.x1-size && edgesAligned(rows, gapStart, e.x, size) {
				out = append(out, separator{x: (gapStart + e.x) / 2, width: e.x - gapStart})
			}
			gapStart = math.NaN()
		}
	}
	return out
}

// ruledColumnMinShare is the share of a table's height that a vertical ruling must span to be taken as a divider of its columns.
const ruledColumnMinShare = 0.6

// withoutGapsInRuledColumns drops the white gaps that lie inside one column the rulings make. Where two consecutive vertical
// rulings run down most of the table, what is between them is one column, whatever its text leaves blank: a header set to the
// left over numbers set to the right leaves a white stretch in the middle of the column, and taking it for a column of its own
// shifts the cells under it.
func withoutGapsInRuledColumns(gaps []separator, reg tableRegion) []separator {
	height := math.Abs(reg.y1 - reg.y0)
	var xs []float64
	for _, v := range reg.vertical {
		if math.Abs(v.y1-v.y0) >= ruledColumnMinShare*height {
			xs = append(xs, v.x)
		}
	}
	sort.Float64s(xs)
	var out []separator
	for _, g := range gaps {
		inside := false
		for i := 1; i < len(xs); i++ {
			if g.x > xs[i-1] && g.x < xs[i] {
				inside = true
			}
		}
		if !inside {
			out = append(out, g)
		}
	}
	return out
}

// withoutRuledGaps drops the gaps that a ruling already divides: a white stretch between two pieces of text in
// different ruled columns is the ruling's, and not a column of its own inside one of them. The gaps's width is
// not kept, so a gap is judged by its middle and by how far it reaches: if a ruling lies within the gap, it goes.
func withoutRuledGaps(gaps, rules []separator) []separator {
	var out []separator
	for _, g := range gaps {
		divided := false
		for _, r := range rules {
			if math.Abs(r.x-g.x) <= g.width/2+1 {
				divided = true
			}
		}
		if !divided {
			out = append(out, g)
		}
	}
	return out
}

// mergeSeparators puts the rulings and the gaps together in order. A gap beside a ruling is the ruling's.
func mergeSeparators(rules, gaps []separator) []separator {
	out := append([]separator(nil), rules...)
	for _, g := range gaps {
		near := false
		for _, r := range rules {
			if math.Abs(r.x-g.x) < 4 {
				near = true
			}
		}
		if !near {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].x < out[j].x })
	return out
}

// edgesAligned says whether the text on one side of a gap lines up: the chunks that start right after it start at
// about the same x (left-aligned columns), or the chunks that end right before it end at about the same x
// (right-aligned numbers). Running text has white gaps too, but its lines do not line up on both sides.
func edgesAligned(rows []textRow, from, to, size float64) bool {
	var starts, ends, centers []float64
	for _, r := range rows {
		// The first chunk after the gap and the last before it, in each row.
		var next, prev *chunk
		for i := range r.chunks {
			c := &r.chunks[i]
			if c.x0 >= to-0.01 && (next == nil || c.x0 < next.x0) {
				next = c
			}
			if c.x1 <= from+0.01 && (prev == nil || c.x1 > prev.x1) {
				prev = c
			}
		}
		if next != nil {
			starts = append(starts, next.x0)
			centers = append(centers, (next.x0+next.x1)/2)
		}
		if prev != nil {
			ends = append(ends, prev.x1)
			centers = append(centers, (prev.x0+prev.x1)/2)
		}
	}
	return clustered(starts, size*0.5, len(rows)) || clustered(ends, size*0.5, len(rows)) || clustered(centers, size*0.6, len(rows))
}

// clustered says whether at least half of the values (and at least two) fall within tolerance of one value.
func clustered(values []float64, tolerance float64, rows int) bool {
	if len(values) < 2 {
		return false
	}
	sort.Float64s(values)
	best := 0
	for i := range values {
		n := 0
		for j := i; j < len(values) && values[j]-values[i] <= tolerance; j++ {
			n++
		}
		best = max(best, n)
	}
	return best >= 2 && float64(best) >= 0.5*float64(min(rows, len(values)))
}

// cell is the text of one cell of one row, and the columns it covers.
type cell struct {
	from, to int
	text     string
}

// rowCells assigns a row's chunks to cells. Where a ruling separates two columns it does so on the rows it covers;
// elsewhere a merged cell is only recognised in a header row: a heading as wide as several columns, with nothing
// else of the row in them, spans them. A heading wider than its own column (it runs into the next one's space)
// goes in the column it is centred on. In the body a chunk belongs to the column it starts in.
func rowCells(r textRow, seps []separator, cols int, header bool) []cell {
	if header {
		r.chunks = splitAtSeparators(r.chunks, seps, r.size)
	}
	active := make([]bool, len(seps))
	for k, s := range seps {
		active[k] = true
		if s.rule != nil {
			y := r.y + r.size*0.3
			active[k] = y >= s.rule.y0-edgeSlack && y <= s.rule.y1+edgeSlack
		}
	}
	splitMergedByText(r, seps, active)
	colOf := func(x float64) int {
		k := 0
		for k < len(seps) && x > seps[k].x {
			k++
		}
		return k
	}
	// cellStart/cellEnd: the first and last column of the cell a column belongs to, after the rulings that do not
	// cover this row are set aside.
	cellStart := make([]int, cols)
	cellEnd := make([]int, cols)
	start := 0
	for c := 0; c < cols; c++ {
		cellStart[c] = start
		if c < len(seps) && active[c] {
			start = c + 1
		}
	}
	end := cols - 1
	for c := cols - 1; c >= 0; c-- {
		cellEnd[c] = end
		if c > 0 && c-1 < len(seps) && active[c-1] {
			end = c - 1
		}
	}

	starts := make([]int, len(r.chunks))
	for i, c := range r.chunks {
		starts[i] = colOf(c.x0 + 0.5)
	}
	byStart := map[int]*cell{}
	var order []int
	for i, c := range r.chunks {
		from, to := starts[i], starts[i]
		if header {
			last := colOf(c.x1 - 0.5)
			if last > from {
				free := true
				for j, other := range starts {
					if j != i && other >= from && other <= last {
						free = false
					}
				}
				if free {
					to = last
				} else {
					from = colOf((c.x0 + c.x1) / 2)
					to = from
				}
			}
		}
		from, to = cellStart[from], max(cellEnd[to], to)
		cl := byStart[from]
		if cl == nil {
			cl = &cell{from: from, to: to}
			byStart[from] = cl
			order = append(order, from)
		}
		cl.to = max(cl.to, to)
		if cl.text != "" {
			cl.text += " "
		}
		cl.text += c.text
	}
	sort.Ints(order)
	out := make([]cell, 0, len(order))
	for _, from := range order {
		out = append(out, *byStart[from])
	}
	return out
}

// groupRows turns the text rows into table rows. Between rules that bound every row, a band is a row. Elsewhere
// each baseline is a row, except that a line that only continues a text cell above it (the second line of a
// wrapped label) is joined to that row. A rule between two lines always starts a new row.
func groupRows(rows []textRow, cells [][]cell, reg tableRegion, cols int) (grouped [][]cell, starts []int, bandMode bool) {
	interior := interiorRules(reg)
	if bandsAreRows(rows, interior) {
		grouped, starts = groupByBands(rows, cells, interior, numericColumns(cells, cols))
		return grouped, starts, true
	}

	numeric := numericColumns(cells, cols)
	heading := headerEnd(cells)
	var prevY float64
	for i, r := range rows {
		cur := cells[i]
		columns := numeric
		if i < heading {
			columns = nil
		}
		if len(grouped) > 0 && !ruleBetween(interior, prevY, r.y) && continues(grouped[len(grouped)-1], cur, columns) {
			grouped[len(grouped)-1] = appendRow(grouped[len(grouped)-1], cur)
		} else {
			grouped = append(grouped, cur)
			starts = append(starts, i)
		}
		prevY = r.y
	}
	return grouped, starts, false
}

// bandsAreRows says whether the rules between the rows of a table bound every row, so that a band of the table
// is one row (a cell of several lines is still one cell). It is when there are several rules and no band holds
// more than a few lines: a body of many lines between two rules is rows without rules between them.
func bandsAreRows(rows []textRow, interior []float64) bool {
	if len(interior) < bandRowRules {
		return false
	}
	counts := map[int]int{}
	for _, r := range rows {
		b := 0
		for _, y := range interior {
			if r.y+r.size*0.3 < y {
				b++
			}
		}
		counts[b]++
	}
	if len(counts) < 3 {
		return false
	}
	for _, n := range counts {
		if n > maxBandLines {
			return false
		}
	}
	return true
}

// interiorRules are the horizontal rules strictly inside a region (not its top or bottom edge).
func interiorRules(reg tableRegion) []float64 {
	var ys []float64
	for _, h := range reg.horizontal {
		if h.y < reg.y1-2 && h.y > reg.y0+2 {
			ys = append(ys, h.y)
		}
	}
	sort.Float64s(ys)
	// top to bottom
	for i, j := 0, len(ys)-1; i < j; i, j = i+1, j-1 {
		ys[i], ys[j] = ys[j], ys[i]
	}
	return ys
}

func ruleBetween(rules []float64, upper, lower float64) bool {
	for _, y := range rules {
		if y < upper+2 && y > lower-2 {
			return true
		}
	}
	return false
}

func groupByBands(rows []textRow, cells [][]cell, interior []float64, numeric []bool) ([][]cell, []int) {
	bandOf := func(y float64) int {
		b := 0
		for _, r := range interior {
			if y < r {
				b++
			}
		}
		return b
	}
	var out [][]cell
	var starts []int
	lastBand := -1
	for i, r := range rows {
		band := bandOf(r.y + r.size*0.3)
		if band == lastBand && len(out) > 0 && (complements(out[len(out)-1], cells[i]) || carriesOn(out[len(out)-1], cells[i], numeric)) {
			out[len(out)-1] = appendRow(out[len(out)-1], cells[i])
		} else {
			out = append(out, cells[i])
			starts = append(starts, i)
		}
		lastBand = band
	}
	return out, starts
}

// complements says whether a line fills columns the row above leaves empty: a heading drawn lower than its
// neighbours in a row of headings, or a cell set at another height. Two lines that both have a value in some column
// are two rows.
func complements(prev, cur []cell) bool {
	if len(cur) == 0 {
		return false
	}
	for _, c := range cur {
		for _, p := range prev {
			if strings.TrimSpace(p.text) != "" && p.from <= c.to && c.from <= p.to {
				return false
			}
		}
	}
	return true
}

// carriesOn says whether a line inside a band is the rest of the row above it: words only, in columns the row above
// has text in. A line with numbers in it is a row of its own, even where a rule sits only between groups of rows.
func carriesOn(prev, cur []cell, numeric []bool) bool {
	if len(cur) == 0 || len(cur) > len(prev) {
		return false
	}
	for _, c := range cur {
		if (c.from < len(numeric) && numeric[c.from]) || isNumeric(c.text) {
			return false
		}
		found := false
		for _, p := range prev {
			if p.from <= c.from && c.from <= p.to && strings.TrimSpace(p.text) != "" {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// appendRow adds a continuation row's text to the cells of the row above it, cell by cell.
func appendRow(dst, src []cell) []cell {
	for _, s := range src {
		merged := false
		for k := range dst {
			if dst[k].from <= s.from && s.from <= dst[k].to {
				dst[k].text = strings.TrimSpace(dst[k].text + " " + s.text)
				merged = true
				break
			}
		}
		if !merged {
			dst = append(dst, s)
		}
	}
	sort.Slice(dst, func(i, j int) bool { return dst[i].from < dst[j].from })
	return dst
}

// continues says whether row cur only carries on text cells of prev: it has content in fewer columns, all of
// them columns that hold words (not numbers), and prev has content in each of them.
func continues(prev, cur []cell, numeric []bool) bool {
	if len(cur) == 0 || len(cur) >= len(prev) {
		return false
	}
	for _, c := range cur {
		if c.from < len(numeric) && numeric[c.from] {
			return false
		}
		if isNumeric(c.text) {
			return false
		}
		found := false
		for _, p := range prev {
			if p.from <= c.from && c.from <= p.to && strings.TrimSpace(p.text) != "" {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// headerEnd is the index of the first row with a figure in it (a number that is not a year). The rows before it are the heading of
// the table: no column is "a column of numbers" there, and a second line of words ("Longer" over "run", above the figures)
// carries on the first. Only for a table whose rows are not bounded by rules: where every band is a row, the lines of a heading
// stay rows of their own and are written "Average / Accuracy", which is what the rest of the table builder expects.
func headerEnd(cells [][]cell) int {
	for i, row := range cells {
		for _, c := range row {
			if isNumeric(c.text) && !isYear(c.text) {
				return i
			}
		}
	}
	return len(cells)
}

func isYear(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 4 || (s[0] != '1' && s[0] != '2') {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// numericColumns marks the columns where most of the cells are numbers.
func numericColumns(cells [][]cell, cols int) []bool {
	num := make([]int, cols)
	all := make([]int, cols)
	for _, row := range cells {
		for _, c := range row {
			if c.from < cols {
				all[c.from]++
				if isNumeric(c.text) {
					num[c.from]++
				}
			}
		}
	}
	out := make([]bool, cols)
	for i := range out {
		out[i] = all[i] >= 2 && float64(num[i]) >= 0.6*float64(all[i])
	}
	return out
}

func isNumeric(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits++
		case strings.ContainsRune(" .,;:%$€£¥()+-−–±×·⋅*/eE^<>≈≤≥", r), unicode.Is(unicode.Sc, r):
		default:
			return false
		}
	}
	return digits > 0
}

// headerRows is how many of the table's rows come before its header rule: the first rule below the top of the
// table, when it is near the top; otherwise one row. Where every band is a row there is no way to tell, so one.
// starts[i] is the index of the text row that table row i begins with.
func headerRows(reg tableRegion, rows []textRow, starts []int, grouped [][]cell, bandMode bool) int {
	interior := interiorRules(reg)
	if bandMode {
		// Every band is a row, so no rule says where the header ends; but a first row with merged cells (a heading
		// over several columns) or an empty first cell (a heading over several rows) has its second line below.
		return bandHeader(grouped)
	}
	if len(interior) == 0 {
		return 1
	}
	above := 0
	for _, r := range rows {
		if r.y+r.size*0.3 > interior[0] {
			above++
		}
	}
	if above == 0 || above > len(rows)/2+1 {
		return 1
	}
	count := 0
	for _, start := range starts {
		if start < above {
			count++
		}
	}
	return max(min(count, len(starts)-1), 1)
}

// bandHeader is 2 when the first row of a table whose bands are rows has a heading that spans columns, or no text
// in its first column, and 1 otherwise.
func bandHeader(grouped [][]cell) int {
	if len(grouped) < 3 {
		return 1
	}
	first := grouped[0]
	empty := len(first) == 0 || first[0].from != 0
	for _, c := range first {
		if c.to > c.from {
			empty = true
		}
	}
	if empty {
		return 2
	}
	// Two lines of words before the first number are a header of two lines ("TEDS" over "Complex").
	if !hasNumber(grouped[0]) && !hasNumber(grouped[1]) && hasNumber(grouped[2]) {
		return 2
	}
	return 1
}

func hasNumber(row []cell) bool {
	for _, c := range row {
		if isNumeric(c.text) {
			return true
		}
	}
	return false
}

// titleRow takes a first row that is one cell across the whole table as the table's title.
func titleRow(rows [][]string, cols int) (string, [][]string, bool) {
	if len(rows) < 3 {
		return "", rows, false
	}
	first := rows[0]
	for _, c := range first {
		if c != first[0] {
			return "", rows, false
		}
	}
	if strings.TrimSpace(first[0]) == "" || cols < 2 {
		return "", rows, false
	}
	return first[0], rows[1:], true
}

// paragraphCells says whether the cells hold paragraphs rather than values: a page laid out in columns between
// rules has cells of whole sentences. A ruled grid is excused, because its rulings say these are cells.
func paragraphCells(rows [][]cell, grid bool) bool {
	limit, share := 70, 0.4
	if grid {
		limit, share = 110, 0.5
	}
	long, total := 0, 0
	for _, row := range rows {
		for _, c := range row {
			if strings.TrimSpace(c.text) == "" {
				continue
			}
			total++
			if len([]rune(c.text)) > limit {
				long++
			}
		}
	}
	return total > 0 && float64(long)/float64(total) > share
}

// mixedSizes says whether the rows are set in several sizes: a table is set in one size, apart from a header, and
// a layout of headlines and captions, as on a cover page, is not a table.
func mixedSizes(rows []textRow) bool {
	sizes := map[float64]bool{}
	small, large := math.Inf(1), 0.0
	for _, r := range rows {
		sizes[math.Round(r.size)] = true
		small, large = math.Min(small, r.size), math.Max(large, r.size)
	}
	return len(sizes) >= 3 && large >= 1.3*small
}

// regular says whether the rows have about the same number of cells. The rows of a table do (a few title and
// header rows apart); the lines of a listing, split into "cells" by the gaps between its tokens, do not.
func regular(rows [][]cell) bool {
	counts := map[int]int{}
	for _, row := range rows {
		n := 0
		for _, c := range row {
			if strings.TrimSpace(c.text) != "" {
				n += c.to - c.from + 1 // a cell that spans columns fills them all
			}
		}
		counts[n]++
	}
	best := 0
	for _, n := range counts {
		best = max(best, n)
	}
	return float64(best) >= 0.30*float64(len(rows))
}

// codeTokens says whether many cells are a bracket, a comma or an operator on its own: the pieces of a line of code
// that the gaps between its tokens cut apart. A table's cells are words and numbers.
func codeTokens(rows [][]cell) bool {
	punct, total := 0, 0
	for _, row := range rows {
		for _, c := range row {
			text := strings.TrimSpace(c.text)
			if text == "" {
				continue
			}
			total++
			if len([]rune(text)) <= 3 && strings.Trim(text, "(){}[]<>=,.;:/\\|+&^%$#@!?'\"`~") == "" {
				punct++
			}
		}
	}
	return total > 0 && float64(punct)/float64(total) > 0.12
}

// mostlyCode says whether the text is set in a monospaced font: a boxed listing is not a table.
func mostlyCode(glyphs []glyph) bool {
	mono, total := 0, 0
	for _, g := range glyphs {
		if g.isSpace() {
			continue
		}
		total++
		if g.mono {
			mono++
		}
	}
	return total > 0 && float64(mono) >= 0.7*float64(total)
}

// tabular says whether the rows look like rows of a table: most of them have text in more than one cell. Running
// text between two rules (a page's header and footer rules enclose all of it) has one chunk per line.
func tabular(cells [][]cell) bool {
	if len(cells) == 0 {
		return false
	}
	multi := 0
	for _, row := range cells {
		if len(row) >= 2 {
			multi++
		}
	}
	return float64(multi) >= 0.6*float64(len(cells))
}

// rulesHugContent says whether the outer rules of a ruled block sit next to its first and last rows, as the rules
// of a table do. The header and footer rules of a page are far from the text between them, which is not a table.
func rulesHugContent(rows []textRow, reg tableRegion) bool {
	// Two rules far apart are a page's header and footer rules, whatever is between them.
	if len(reg.horizontal) <= 2 && reg.y1-reg.y0 > maxTwoRuleHeight {
		return false
	}
	first, last := rows[0], rows[len(rows)-1]
	return reg.y1-(first.y+first.size) <= 2.0*first.size && last.y-reg.y0 <= 2.0*last.size
}

// looksLikeProse rejects a ruled block that is really running text: two columns of paragraphs between a header
// rule and a footer rule have white gaps like a table does, but their lines are long and fill the width.
func looksLikeProse(rows []textRow, cols int, reg tableRegion, pageWidth float64) bool {
	if cols > 3 {
		return false
	}
	long, total := 0, 0
	for _, r := range rows {
		for _, c := range r.chunks {
			total++
			if len([]rune(c.text)) > 45 {
				long++
			}
		}
	}
	wide := pageWidth > 0 && reg.x1-reg.x0 > 0.6*pageWidth
	return total > 0 && float64(long)/float64(total) > 0.5 && wide
}

// splitMergedByText sets active a ruling that does not cover the row when the cell the row merges around it holds text on
// both sides of it and none of the text crosses it. A table that draws the rules between its year columns only in the header
// (2024 | 2025 | 2026 above, one wide cell under) has three numbers in that cell, side by side; read as one cell they were
// written three times over, in each of its columns. A single chunk of text in a merged cell, or one that runs over the
// ruling, is a cell that really spans the columns, and is left merged.
func splitMergedByText(r textRow, seps []separator, active []bool) {
	const slack = 0.5
	was := append([]bool(nil), active...)
	for k, s := range seps {
		if s.rule == nil || was[k] {
			continue
		}
		// The merged cell is bounded by the nearest rulings that do cover the row.
		lo, hi := math.Inf(-1), math.Inf(1)
		for j := k - 1; j >= 0; j-- {
			if was[j] {
				lo = seps[j].x
				break
			}
		}
		for j := k + 1; j < len(seps); j++ {
			if was[j] {
				hi = seps[j].x
				break
			}
		}
		left, right, crossing := false, false, false
		for _, c := range r.chunks {
			if c.x1 <= lo || c.x0 >= hi {
				continue
			}
			switch {
			case c.x0 < s.x-slack && c.x1 > s.x+slack:
				crossing = true
			case c.x1 <= s.x+slack:
				left = true
			case c.x0 >= s.x-slack:
				right = true
			}
		}
		if left && right && !crossing {
			active[k] = true
		}
	}
}
