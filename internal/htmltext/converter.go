package htmltext

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/KPO-Tech/seshat-runtime/internal/mdtable"
)

// converter walks a parsed page and writes markdown blocks.
type converter struct {
	sawH1     bool
	inHeading bool // a permalink marker ("¶", "#") after a heading's text is not part of the heading
	cellDepth int  // above 0 while inside a table cell, where a nested table is flattened to text
}

// lineBreak marks a <br> in running text until the paragraph is tidied.
const lineBreak = "\x00"

var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "body": true, "center": true, "details": true,
	"dialog": true, "dd": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "header": true,
	"hgroup": true, "hr": true, "html": true, "li": true, "main": true, "ol": true, "p": true, "pre": true, "section": true,
	"summary": true, "table": true, "ul": true, "caption": true, "thead": true, "tbody": true, "tfoot": true, "tr": true,
	"td": true, "th": true,
}

func (c *converter) title(doc *html.Node) string {
	if t := find(doc, func(n *html.Node) bool { return isElement(n, "title") }); t != nil {
		return normalizeSpace(textOf(t))
	}
	return ""
}

// blocks writes the children of a node as a list of markdown blocks: runs of inline content become paragraphs,
// block elements become what they are.
func (c *converter) blocks(n *html.Node, depth int) []string {
	if depth > maxDepth {
		return nil
	}
	var out []string
	var pending strings.Builder
	flush := func() {
		if text := tidy(pending.String()); text != "" {
			out = append(out, text)
		}
		pending.Reset()
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		switch child.Type {
		case html.TextNode:
			pending.WriteString(child.Data)
		case html.ElementNode:
			if skipped(child) {
				continue
			}
			if blockTags[child.Data] {
				flush()
				out = append(out, c.block(child, depth+1)...)
			} else {
				c.inline(&pending, child, depth+1)
			}
		}
	}
	flush()
	return out
}

func (c *converter) block(n *html.Node, depth int) []string {
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		var sb strings.Builder
		c.inHeading = true
		c.children(&sb, n, depth)
		c.inHeading = false
		text := normalizeSpace(strings.ReplaceAll(sb.String(), lineBreak, " "))
		if text == "" {
			return nil
		}
		level := int(n.Data[1] - '0')
		if level == 1 {
			c.sawH1 = true
		}
		return []string{strings.Repeat("#", level) + " " + text}
	case "ul", "ol":
		if list := c.list(n, depth); list != "" {
			return []string{list}
		}
		return nil
	case "dl":
		if list := c.definitions(n, depth); list != "" {
			return []string{list}
		}
		return nil
	case "blockquote":
		inner := strings.Join(c.blocks(n, depth), "\n\n")
		if inner == "" {
			return nil
		}
		lines := strings.Split(inner, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimRight("> "+line, " ")
		}
		return []string{strings.Join(lines, "\n")}
	case "pre":
		if code := c.codeBlock(n); code != "" {
			return []string{code}
		}
		return nil
	case "hr":
		return []string{"---"}
	case "table":
		if table := c.table(n, depth); table != "" {
			return []string{table}
		}
		return nil
	}
	return c.blocks(n, depth)
}

// children writes the inline content of a node into sb.
func (c *converter) children(sb *strings.Builder, n *html.Node, depth int) {
	if depth > maxDepth {
		return
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		switch child.Type {
		case html.TextNode:
			sb.WriteString(child.Data)
		case html.ElementNode:
			if !skipped(child) {
				c.inline(sb, child, depth+1)
			}
		}
	}
}

// inline writes an inline element (or the inline content of an unknown one) into sb.
func (c *converter) inline(sb *strings.Builder, n *html.Node, depth int) {
	switch n.Data {
	case "br":
		sb.WriteString(lineBreak)
	case "img":
		if alt := normalizeSpace(attr(n, "alt")); alt != "" {
			sb.WriteString("[Image: " + alt + "]")
		}
	case "a":
		c.link(sb, n, depth)
	case "strong", "b":
		c.wrapped(sb, n, depth, "**")
	case "em", "i":
		c.wrapped(sb, n, depth, "*")
	case "del", "s", "strike":
		c.wrapped(sb, n, depth, "~~")
	case "code", "kbd", "samp", "tt":
		var inner strings.Builder
		c.children(&inner, n, depth)
		text := normalizeSpace(strings.ReplaceAll(inner.String(), lineBreak, " "))
		if text == "" {
			return
		}
		fence := "`"
		for strings.Contains(text, fence) {
			fence += "`"
		}
		pad := ""
		if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
			pad = " "
		}
		sb.WriteString(fence + pad + text + pad + fence)
	default:
		if blockTags[n.Data] { // a block inside inline content: keep its text, on its own line
			sb.WriteString(lineBreak)
			c.children(sb, n, depth)
			sb.WriteString(lineBreak)
			return
		}
		c.children(sb, n, depth)
	}
}

// wrapped writes the content of an element between markers, keeping the spaces outside them: "**bold **text"
// is not bold in markdown.
func (c *converter) wrapped(sb *strings.Builder, n *html.Node, depth int, marker string) {
	var inner strings.Builder
	c.children(&inner, n, depth)
	text := inner.String()
	core := strings.TrimSpace(strings.ReplaceAll(text, lineBreak, " "))
	if core == "" || strings.Contains(text, lineBreak) {
		sb.WriteString(text)
		return
	}
	lead := text[:len(text)-len(strings.TrimLeftFunc(text, unicode.IsSpace))]
	trail := text[len(strings.TrimRightFunc(text, unicode.IsSpace)):]
	sb.WriteString(lead + marker + core + marker + trail)
}

var permalinkClass = regexp.MustCompile(`(?i)\b(headerlink|permalink|anchor|heading-link|hash-link)\b`)

// link writes [text](address). A link whose address goes nowhere useful (a script, a place on the same page) is
// just its text; a permalink marker after a heading ("¶", "#") is nothing.
func (c *converter) link(sb *strings.Builder, n *html.Node, depth int) {
	var inner strings.Builder
	c.children(&inner, n, depth)
	text := tidy(inner.String())
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "[Image: ") && strings.HasSuffix(text, "]") && strings.Count(text, "[") == 1 {
		return // a link that is only a picture is an icon or a button
	}
	if permalinkClass.MatchString(attr(n, "class")) || (c.inHeading && isMarker(text)) {
		return
	}
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" || strings.HasPrefix(href, "#") || !safeLinkTarget(href) || strings.Contains(text, "\n") {
		sb.WriteString(text)
		return
	}
	sb.WriteString("[" + text + "](" + strings.ReplaceAll(href, " ", "%20") + ")")
}

// safeLinkTarget is true for an address a reader can follow without running anything: a web address, a mail or phone link, or a
// place on the same site. Any other scheme (javascript:, data:, vbscript:, file:) is dropped and only the words of the link stay. A
// browser ignores tabs and line breaks inside a scheme, so "jav&#x09;ascript:" is read as "javascript:"; they are taken out before
// the scheme is read.
func safeLinkTarget(href string) bool {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, strings.TrimSpace(href))
	colon := strings.IndexByte(cleaned, ':')
	if colon < 0 {
		return true // a relative address: no scheme
	}
	if slash := strings.IndexAny(cleaned, "/?#"); slash >= 0 && slash < colon {
		return true // the colon comes after a path, not after a scheme
	}
	switch strings.ToLower(cleaned[:colon]) {
	case "http", "https", "mailto", "tel":
		return true
	}
	return false
}

func isMarker(text string) bool {
	if len([]rune(text)) > 2 {
		return false
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// tidy turns the raw inline text of a paragraph into lines of single-spaced text.
func tidy(s string) string {
	var lines []string
	for _, part := range strings.Split(s, lineBreak) {
		if part = normalizeSpace(part); part != "" {
			lines = append(lines, part)
		}
	}
	return strings.Join(lines, "\n")
}

// list writes a ul or ol. An item's nested blocks are indented under its marker, so nesting survives.
func (c *converter) list(n *html.Node, depth int) string {
	ordered := n.Data == "ol"
	number := 1
	if v, err := strconv.Atoi(attr(n, "start")); err == nil {
		number = v
	}
	var items []string
	for li := n.FirstChild; li != nil; li = li.NextSibling {
		if !isElement(li, "li") || skipped(li) {
			continue
		}
		blocks := c.blocks(li, depth+1)
		if len(blocks) == 0 {
			continue
		}
		marker := "-"
		if ordered {
			marker = strconv.Itoa(number) + "."
			number++
		}
		separator := "\n"
		if paragraphsIn(blocks) > 1 {
			separator = "\n\n"
		}
		items = append(items, hang(marker, strings.Join(blocks, separator)))
	}
	return strings.Join(items, "\n")
}

// paragraphsIn counts the blocks that are not lists: an item with a sentence and a sub-list is one tight item,
// an item with two paragraphs is not.
func paragraphsIn(blocks []string) int {
	n := 0
	for _, b := range blocks {
		if !strings.HasPrefix(b, "- ") && !(len(b) > 2 && b[0] >= '0' && b[0] <= '9' && strings.Contains(b[:min(len(b), 5)], ". ")) {
			n++
		}
	}
	return n
}

// hang puts a marker before the first line and indents the others to line up under the text.
func hang(marker, body string) string {
	pad := strings.Repeat(" ", len(marker)+1)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		switch {
		case i == 0:
			lines[i] = marker + " " + line
		case line != "":
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

// definitions writes a dl as "- **term**: definition" lines.
func (c *converter) definitions(n *html.Node, depth int) string {
	var lines []string
	term := ""
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || skipped(child) {
			continue
		}
		var sb strings.Builder
		c.children(&sb, child, depth)
		text := normalizeSpace(strings.ReplaceAll(sb.String(), lineBreak, " "))
		switch child.Data {
		case "dt":
			term = text
		case "dd":
			switch {
			case term != "" && text != "":
				lines = append(lines, "- **"+term+"**: "+text)
			case text != "":
				lines = append(lines, "- "+text)
			}
		}
	}
	return strings.Join(lines, "\n")
}

var languageClass = regexp.MustCompile(`(?:language|lang|highlight-source|brush:)[-\s]?([A-Za-z0-9_+#-]+)`)

// codeBlock writes a pre as a fenced block, with the language the page names for it. The fence is longer than
// any run of backticks in the code, so the code cannot close it.
func (c *converter) codeBlock(pre *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			sb.WriteString(n.Data)
		case isElement(n, "br"):
			sb.WriteByte('\n')
		case n.Type == html.ElementNode && !skipped(n):
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				walk(ch)
			}
		}
	}
	walk(pre)
	code := strings.TrimLeft(strings.ReplaceAll(sb.String(), "\r\n", "\n"), "\n")
	code = strings.TrimRight(code, " \t\n")
	if strings.TrimSpace(code) == "" {
		return ""
	}
	language := ""
	classes := attr(pre, "class")
	for ch := pre.FirstChild; ch != nil; ch = ch.NextSibling {
		if isElement(ch, "code") {
			classes += " " + attr(ch, "class")
		}
	}
	if m := languageClass.FindStringSubmatch(classes); m != nil {
		language = m[1]
	}
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	return fence + language + "\n" + code + "\n" + fence
}

const maxSpan = 200

// table reads a table into mdtable. A table inside a table cell is flattened to its text: a markdown cell is one
// line.
func (c *converter) table(n *html.Node, depth int) string {
	if c.cellDepth > 0 {
		var rows []string
		for _, tr := range rowsOf(n) {
			var cells []string
			for _, cell := range cellsOf(tr) {
				if text := normalizeSpace(c.cellText(cell, depth)); text != "" {
					cells = append(cells, text)
				}
			}
			if len(cells) > 0 {
				rows = append(rows, strings.Join(cells, "; "))
			}
		}
		return strings.Join(rows, "; ")
	}

	t := mdtable.Table{}
	if caption := find(n, func(x *html.Node) bool { return isElement(x, "caption") }); caption != nil {
		t.Caption = normalizeSpace(textOf(caption))
	}
	var spans [][]mdtable.SpanCell
	headerRows, inHeader := 0, true
	for _, tr := range rowsOf(n) {
		var row []mdtable.SpanCell
		allHeads := true
		for _, cell := range cellsOf(tr) {
			row = append(row, mdtable.SpanCell{
				Text:    c.cellText(cell, depth),
				ColSpan: span(cell, "colspan"),
				RowSpan: span(cell, "rowspan"),
			})
			if cell.Data != "th" {
				allHeads = false
			}
		}
		if len(row) == 0 {
			continue
		}
		spans = append(spans, row)
		if inHeader && (allHeads || inThead(tr)) {
			headerRows++
		} else {
			inHeader = false
		}
	}
	t.Rows, t.HeaderRows = mdtable.FromSpanRows(spans), headerRows
	var sb strings.Builder
	t.RenderMarkdown(&sb)
	return strings.TrimSpace(sb.String())
}

func (c *converter) cellText(cell *html.Node, depth int) string {
	c.cellDepth++
	defer func() { c.cellDepth-- }()
	return strings.Join(c.blocks(cell, depth+1), "\n")
}

func span(cell *html.Node, key string) int {
	v, err := strconv.Atoi(strings.TrimSpace(attr(cell, key)))
	if err != nil || v < 1 {
		return 1
	}
	return min(v, maxSpan)
}

func inThead(tr *html.Node) bool { return tr.Parent != nil && isElement(tr.Parent, "thead") }

// rowsOf lists the rows of a table in order, wherever they sit (directly, or under thead, tbody and tfoot), and
// not those of a table nested in a cell.
func rowsOf(table *html.Node) []*html.Node {
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			switch {
			case isElement(ch, "tr"):
				rows = append(rows, ch)
			case isElement(ch, "thead"), isElement(ch, "tbody"), isElement(ch, "tfoot"):
				walk(ch)
			}
		}
	}
	walk(table)
	return rows
}

func cellsOf(tr *html.Node) []*html.Node {
	var cells []*html.Node
	for ch := tr.FirstChild; ch != nil; ch = ch.NextSibling {
		if isElement(ch, "td") || isElement(ch, "th") {
			cells = append(cells, ch)
		}
	}
	return cells
}
