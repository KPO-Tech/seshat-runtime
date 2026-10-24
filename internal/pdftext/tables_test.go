package pdftext

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// testdata/tables.pdf was written with fpdf2, line by line, so each page is exactly one kind of layout:
//
//	1  a fully ruled grid: a header that spans columns, a cell merged over two rows, three body rows
//	2  a page with no table
//	3  a table with a top rule, a header rule and a bottom rule and no vertical lines, spanning header included
//	4  vertical rules through the whole table and rules only at the top, under the header and at the bottom
//	5  a header rule and a footer rule around prose in two columns, a boxed listing: no table at all
func tablesPage(t *testing.T, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "tables.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	md, err := PageMarkdown(reader.Page(n))
	if err != nil {
		t.Fatal(err)
	}
	return md
}

func TestAGridWithMergedCellsKeepsItsStructure(t *testing.T) {
	md := tablesPage(t, 1)
	want := "| Region | 2023 / Sales | 2023 / Cost | 2024 / Sales | 2024 / Cost |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| North | 10 | 4 | 12 | 5 |\n" +
		"| South | 7 | 3 | 9 | 4 |\n" +
		"| East | 3 | 1 | 4 | 2 |"
	if !strings.Contains(md, want) {
		t.Fatalf("got:\n%s\nwant it to contain:\n%s", md, want)
	}
	// The table stands where it was: after the sentence that introduces it and before the one that follows.
	intro, table, after := strings.Index(md, "The table below"), strings.Index(md, "| Region"), strings.Index(md, "Figures are in millions")
	if !(intro >= 0 && intro < table && table < after) {
		t.Fatalf("the table is out of place (%d, %d, %d):\n%s", intro, table, after, md)
	}
	if strings.Count(md, "North") != 1 {
		t.Fatalf("the text of the table is repeated outside it:\n%s", md)
	}
}

func TestATableWithOnlyHorizontalRulesFindsItsColumnsFromTheGaps(t *testing.T) {
	md := tablesPage(t, 3)
	want := "| Model | BLEU / EN-DE | BLEU / EN-FR | Cost (FLOPs) / EN-DE | Cost (FLOPs) / EN-FR |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| ByteNet [18] | 23.75 |  |  |  |\n" +
		"| GNMT + RL [38] | 24.6 | 39.92 | 2.3e19 | 1.4e20 |\n" +
		"| ConvS2S [9] | 25.16 | 40.46 | 9.6e18 | 1.5e20 |\n" +
		"| Deep-Att + PosUnk Ensemble [39] |  | 40.4 |  | 8.0e20 |\n" +
		"| Transformer (big) | 28.4 | 41.8 | 2.3e19 |  |"
	if !strings.Contains(md, want) {
		t.Fatalf("got:\n%s\nwant it to contain:\n%s", md, want)
	}
	if !strings.Contains(md, "Table 2: BLEU scores and training cost.") || !strings.Contains(md, "Residual dropout") {
		t.Fatalf("the text around the table is missing:\n%s", md)
	}
}

func TestVerticalRulesWithRowsThatAreNotRuled(t *testing.T) {
	md := tablesPage(t, 4)
	want := "| Class | Count | Train | Test |\n| --- | --- | --- | --- |\n| Caption | 22524 | 2.04 | 1.77 |"
	if !strings.Contains(md, want) {
		t.Fatalf("got:\n%s", md)
	}
	for _, row := range []string{"| List-item | 185660 | 17.19 | 13.34 |", "| Title | 5071 | 0.47 | 0.30 |"} {
		if !strings.Contains(md, row) {
			t.Errorf("missing %q in:\n%s", row, md)
		}
	}
	if strings.Count(md, "\n| --- |") != 1 {
		t.Fatalf("exactly one table expected:\n%s", md)
	}
}

func TestPagesWithoutATableAreLeftAlone(t *testing.T) {
	if md := tablesPage(t, 2); strings.Contains(md, "|") {
		t.Fatalf("a page with no rulings gained a table:\n%s", md)
	}
	// A header rule and a footer rule around two columns of prose, and a boxed listing, are not tables.
	md := tablesPage(t, 5)
	if strings.Contains(md, "| ---") {
		t.Fatalf("prose or code became a table:\n%s", md)
	}
	for _, want := range []string{"The committee met on Tuesday", "A second item concerned the relocation", "Ordinary text continues below the listing"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

// --- rulings ---------------------------------------------------------------------------------------------------

func TestRulingsThatTouchOrOverlapAreOne(t *testing.T) {
	r := rulings{
		h: []hline{{100, 10, 50}, {100.4, 49, 90}, {100, 200, 240}, {130, 10, 90}},
		v: []vline{{10, 100, 120}, {10.5, 119, 140}, {60, 100, 140}},
	}.merged()
	if len(r.h) != 3 || r.h[0].x0 != 10 || r.h[0].x1 != 90 {
		t.Fatalf("horizontal = %+v", r.h)
	}
	if len(r.v) != 2 || r.v[0].y0 != 100 || r.v[0].y1 != 140 {
		t.Fatalf("vertical = %+v", r.v)
	}
}

func TestMatricesComposeInThePDFOrder(t *testing.T) {
	scale := affine{2, 0, 0, 2, 0, 0}
	move := affine{1, 0, 0, 1, 10, 5}
	x, y := scale.mul(move).apply(3, 4) // scale first, then move
	if x != 16 || y != 13 {
		t.Fatalf("got (%v, %v)", x, y)
	}
}

func TestDistinctVerticalPositionsIgnorePiecesOfTheSameLine(t *testing.T) {
	if n := distinctX([]vline{{10, 0, 5}, {10.2, 6, 9}, {60, 0, 9}}); n != 2 {
		t.Fatalf("distinctX = %d", n)
	}
}

// --- regions ---------------------------------------------------------------------------------------------------

func TestACrossingSetOfRulingsIsAGridAndAStackOfEqualRulesIsABlock(t *testing.T) {
	grid := rulings{
		h: []hline{{100, 50, 250}, {120, 50, 250}, {140, 50, 250}},
		v: []vline{{50, 100, 140}, {150, 100, 140}, {250, 100, 140}},
	}
	regions := findRegions(grid.merged())
	if len(regions) != 1 || !regions[0].grid || regions[0].x0 != 50 || regions[0].x1 != 250 {
		t.Fatalf("regions = %+v", regions)
	}

	block := rulings{h: []hline{{300, 60, 500}, {280, 60, 500}, {200, 60, 500}}}
	regions = findRegions(block.merged())
	if len(regions) != 1 || regions[0].grid || len(regions[0].horizontal) != 3 {
		t.Fatalf("regions = %+v", regions)
	}

	// A rule of another width is not part of the block.
	mixed := rulings{h: []hline{{300, 60, 500}, {280, 60, 500}, {250, 100, 150}}}
	if regions := findRegions(mixed.merged()); len(regions) != 1 || len(regions[0].horizontal) != 2 {
		t.Fatalf("regions = %+v", regions)
	}
}

func TestDrawingsWithThousandsOfLinesAreNotSearchedForTables(t *testing.T) {
	var r rulings
	for i := 0; i <= maxRulings; i++ {
		r.h = append(r.h, hline{float64(i) * 3, 0, 100})
	}
	if regions := findRegions(r); regions != nil {
		t.Fatalf("a plot is not a table: %d regions", len(regions))
	}
}

func TestSameSpanNeedsRulesAsWideAsEachOther(t *testing.T) {
	if !sameSpan(hline{0, 50, 450}, hline{0, 50, 452}) {
		t.Error("equal rules")
	}
	if sameSpan(hline{0, 50, 450}, hline{0, 50, 200}) {
		t.Error("a rule half as wide is not the same table's")
	}
	if sameSpan(hline{0, 50, 60}, hline{0, 50, 60}) {
		t.Error("a short stroke is not a rule")
	}
}

// --- what is not a table ---------------------------------------------------------------------------------------

func cellsOf(rows ...[]string) [][]cell {
	var out [][]cell
	for _, row := range rows {
		var r []cell
		for i, text := range row {
			r = append(r, cell{from: i, to: i, text: text})
		}
		out = append(out, r)
	}
	return out
}

func TestTabularRowsHaveSeveralCells(t *testing.T) {
	if !tabular(cellsOf([]string{"a", "b"}, []string{"c", "d"}, []string{"e"})) {
		t.Error("two rows of three have cells in more than one column")
	}
	if tabular(cellsOf([]string{"a"}, []string{"b"}, []string{"c", "d"})) {
		t.Error("running text with one chunk per line is not tabular")
	}
}

func TestRowsOfTheSameWidthAreRegularAndTheLinesOfAListingAreNot(t *testing.T) {
	if !regular(cellsOf([]string{"a", "b", "c"}, []string{"d", "e", "f"}, []string{"Title"})) {
		t.Error("a table with a title row")
	}
	if regular(cellsOf([]string{"a"}, []string{"a", "b"}, []string{"a", "b", "c"}, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d", "e"})) {
		t.Error("rows of every width")
	}
}

func TestBracketsAndOperatorsOnTheirOwnAreCode(t *testing.T) {
	code := cellsOf([]string{"WHEN", "VERIFY", "(", "x", ")", "="}, []string{"THEN", "y", ",", "(", "z", ")"})
	if !codeTokens(code) {
		t.Error("tokens of a listing")
	}
	if codeTokens(cellsOf([]string{"Alice", "90", "-"}, []string{"Bob", "85", "-"})) {
		t.Error("a dash is an empty value, not code")
	}
}

func TestCellsOfWholeSentencesAreParagraphs(t *testing.T) {
	long := strings.Repeat("a long sentence of running text ", 4)
	if !paragraphCells(cellsOf([]string{long, long}, []string{long, "x"}), false) {
		t.Error("paragraphs in cells")
	}
	if paragraphCells(cellsOf([]string{"Name", "Value"}, []string{"alpha", "1"}), false) {
		t.Error("short cells")
	}
	if paragraphCells(cellsOf([]string{"x", strings.Repeat("y", 90)}, []string{"x", "short"}), true) {
		t.Error("a ruled grid is allowed longer cells")
	}
}

func TestNumbersAreRecognisedWithTheirDecoration(t *testing.T) {
	for _, s := range []string{"10", "1,234.50", "-3.2", "(45)", "12%", "$9", "2.3e19", "1.0 · 10", "−7"} {
		if !isNumeric(s) {
			t.Errorf("%q should be a number", s)
		}
	}
	for _, s := range []string{"North", "n/a", "", "x1"} {
		if isNumeric(s) {
			t.Errorf("%q should not be a number", s)
		}
	}
}

func TestSizesOfACoverPageAreMixedAndThoseOfATableAreNot(t *testing.T) {
	cover := []textRow{{size: 24}, {size: 11}, {size: 7.4}, {size: 11}}
	if !mixedSizes(cover) {
		t.Error("a cover page")
	}
	if mixedSizes([]textRow{{size: 10}, {size: 10}, {size: 9}}) {
		t.Error("one table size and a smaller header")
	}
}

func TestACaptionOrASentenceBetweenRulesSplitsThemApart(t *testing.T) {
	row := func(texts ...string) textRow {
		r := textRow{size: 10}
		x := 0.0
		for _, s := range texts {
			r.chunks = append(r.chunks, chunk{x0: x, x1: x + float64(len(s))*5, text: s})
			x += float64(len(s))*5 + 40
		}
		return r
	}
	if !proseBand([]textRow{row("Ta", "ble 2: results of the second experiment")}, 300) {
		t.Error("a caption whose first letters are set apart")
	}
	if !proseBand([]textRow{row("Figure 3. Overview")}, 300) {
		t.Error("a figure caption")
	}
	if !proseBand([]textRow{row("This sentence runs across nearly all of the width of the column.")}, 340) {
		t.Error("running text")
	}
	if proseBand([]textRow{row("Human", "-", "82.3", "91.2"), row("Top Leaderboard Systems")}, 300) {
		t.Error("a table's rows and sub-heading")
	}
}

func TestAGapNextToARulingBelongsToTheRuling(t *testing.T) {
	rules := []separator{{x: 170}}
	gaps := []separator{{x: 147, width: 104}, {x: 300, width: 20}}
	got := withoutRuledGaps(gaps, rules)
	if len(got) != 1 || got[0].x != 300 {
		t.Fatalf("got %+v", got)
	}
	merged := mergeSeparators(rules, got)
	if len(merged) != 2 || merged[0].x != 170 || merged[1].x != 300 {
		t.Fatalf("merged %+v", merged)
	}
}

func TestMergedCellsRepeatOverTheColumnsTheyCover(t *testing.T) {
	seps := []separator{{x: 100}, {x: 200}}
	row := textRow{y: 500, size: 10, chunks: []chunk{{x0: 110, x1: 190, y: 500, size: 10, text: "Wide"}}}
	header := rowCells(row, seps, 3, true)
	if len(header) != 1 || header[0].from != 1 || header[0].to != 1 {
		t.Fatalf("header cells = %+v", header)
	}
	spanning := textRow{y: 500, size: 10, chunks: []chunk{{x0: 110, x1: 290, y: 500, size: 10, text: "Over two"}}}
	cells := rowCells(spanning, seps, 3, true)
	if len(cells) != 1 || cells[0].from != 1 || cells[0].to != 2 {
		t.Fatalf("a heading over two columns spans them: %+v", cells)
	}
	body := rowCells(spanning, seps, 3, false)
	if len(body) != 1 || body[0].to != 1 {
		t.Fatalf("in the body a long cell stays in its column: %+v", body)
	}
}

func TestGapsBetweenColumnsAreFoundAndRunningTextHasNone(t *testing.T) {
	reg := tableRegion{x0: 0, x1: 400}
	row := func(y float64, cells ...[2]float64) textRow {
		r := textRow{y: y, size: 10}
		for _, c := range cells {
			r.chunks = append(r.chunks, chunk{x0: c[0], x1: c[1], y: y, size: 10, text: "x"})
		}
		return r
	}
	table := []textRow{
		row(100, [2]float64{20, 80}, [2]float64{150, 190}, [2]float64{260, 300}),
		row(88, [2]float64{20, 90}, [2]float64{150, 185}, [2]float64{262, 298}),
		row(76, [2]float64{20, 70}, [2]float64{152, 188}, [2]float64{258, 300}),
		row(64, [2]float64{20, 85}, [2]float64{150, 190}, [2]float64{260, 296}),
	}
	seps := gapSeparators(table, reg)
	if len(seps) != 2 || math.Abs(seps[0].x-120) > 1 || math.Abs(seps[1].x-225) > 1 {
		t.Fatalf("separators = %+v", seps)
	}
	var prose []textRow
	for i := 0; i < 5; i++ {
		prose = append(prose, row(100-float64(i)*12, [2]float64{20, 140 + float64(i*9)}))
	}
	if seps := gapSeparators(prose, reg); len(seps) != 0 {
		t.Fatalf("running text has no column gaps: %+v", seps)
	}
}

// testdata/header_rule.pdf: page 1 is an invoice whose lines have one rule, under the column titles, and no other;
// page 2 is prose under an underlined heading, which has a rule too and is no table.
func headerRulePage(t *testing.T, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "header_rule.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	md, err := PageMarkdown(reader.Page(n))
	if err != nil {
		t.Fatal(err)
	}
	return md
}

func TestARuleUnderTheColumnTitlesMakesATableOfTheLinesBelow(t *testing.T) {
	md := headerRulePage(t, 1)
	want := "| Item | Description | Price | VAT | Quantity | Amount |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| E103184 | Maintenance kit CA6707/10 | 49,99 | 21 % | 1 PCS | 49,99 |\n" +
		"| E103560 | Flyer promotion | 0,00 | 21 % | 1 PCS | 0,00 |\n" +
		"| E104002 | Gasket set | 12,50 | 21 % | 2 PCS | 25,00 |"
	if !strings.Contains(md, want) {
		t.Fatalf("got:\n%s\nwant it to contain:\n%s", md, want)
	}
	table, after := strings.Index(md, "| Item"), strings.Index(md, "Prices include VAT")
	if table < 0 || after < table || strings.Index(md, "Billed to") > table {
		t.Fatalf("the table is out of place:\n%s", md)
	}
	if strings.Count(md, "Gasket set") != 1 {
		t.Fatalf("the table's text is repeated outside it:\n%s", md)
	}
}

func TestARuleUnderAHeadingOverProseIsNoTable(t *testing.T) {
	md := headerRulePage(t, 2)
	if strings.Contains(md, "|") || !strings.Contains(md, "Goods remain the property of the supplier") {
		t.Fatalf("got:\n%s", md)
	}
}
