package pdftext

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// The helpers draw text the way the library reports it for many real PDFs: one x for a whole string and
// no width, so spaces show only as the distance to the next string.

func str(s, font string, size, x, y float64) []pdf.Text {
	var out []pdf.Text
	for _, r := range s {
		out = append(out, pdf.Text{Font: font, FontSize: size, X: x, Y: y, S: string(r)})
	}
	return out
}

func page(parts ...[]pdf.Text) []pdf.Text { return join(parts...) }

const (
	fSerif = "Serif"
	fBold  = "Serif-Bold"
	fMono  = "DejaVuSansMono"
	fMath  = "LatinModernMath-Regular"
)

// bodyLines draws n lines of 10pt body text at the left margin, from y downward, so the page has a body size.
func bodyLines(n int, y float64) []pdf.Text {
	var out []pdf.Text
	for i := 0; i < n; i++ {
		out = append(out, str("Une ligne de texte courant assez longue pour compter comme du corps", fSerif, 10, 72, y-float64(i)*12)...)
	}
	return out
}

func TestMarkdown_AHeadingIsLargerThanTheBody(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("CHAPITRE", fBold, 24, 72, 700),
		str("Une section", fBold, 14, 72, 650),
		bodyLines(6, 600),
	))
	if !strings.Contains(md, "# CHAPITRE") || !strings.Contains(md, "## Une section") {
		t.Fatalf("headings missing:\n%s", md)
	}
}

func TestMarkdown_BoldBodyTextIsNotAHeading(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(str("En-tête courant", fBold, 10, 72, 750), bodyLines(6, 700)))
	if strings.Contains(md, "#") {
		t.Fatalf("a fBold line at body size is a running header, not a heading:\n%s", md)
	}
}

func TestMarkdown_WrappedLinesBecomeOneParagraphAndAHyphenIsUndone(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("Un texte assez long pour remplir toute la ligne et couper le mot approxima-", fSerif, 10, 72, 700),
		str("teurs rapides et continue sur la ligne suivante.", fSerif, 10, 72, 688),
		bodyLines(4, 600),
	))
	if !strings.Contains(md, "approximateurs rapides") {
		t.Fatalf("lines were not joined and unhyphenated:\n%s", md)
	}
}

func TestMarkdown_ABigGapStartsANewParagraph(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("Premier paragraphe, une ligne.", fSerif, 10, 72, 700),
		str("Second paragraphe, plus bas.", fSerif, 10, 72, 660),
		bodyLines(4, 600),
	))
	if !strings.Contains(md, "Premier paragraphe, une ligne.\n\nSecond paragraphe") {
		t.Fatalf("paragraphs were merged:\n%s", md)
	}
}

func TestMarkdown_BulletsBecomeListItems(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("— premier point de la liste", fSerif, 10, 72, 700),
		str("— second point de la liste", fSerif, 10, 72, 688),
		bodyLines(4, 600),
	))
	if !strings.Contains(md, "- premier point de la liste\n\n- second point de la liste") {
		t.Fatalf("list items missing:\n%s", md)
	}
}

func TestMarkdown_MonospaceLinesBecomeAFencedBlockWithTheirIndentation(t *testing.T) {
	t.Parallel()
	// Strings start at whole numbers of 5.4pt columns: "for" at 0, "x" at 4, then an indented body at 4 columns.
	md := layoutMarkdown(page(
		bodyLines(4, 700),
		str("for", fMono, 9, 77, 600), str("x", fMono, 9, 77+4*5.4, 600),
		str("print(x)", fMono, 9, 77+4*5.4, 589),
	))
	if !strings.Contains(md, "```\nfor x\n    print(x)\n```") {
		t.Fatalf("code block wrong:\n%s", md)
	}
}

func TestMarkdown_InlineMathIsWrappedAndPunctuationStaysOutside(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("la fonction d'activation ", fSerif, 10, 72, 700),
		str("𝜑", fMath, 10, 172, 700),
		str(".", fSerif, 10, 180, 700),
		bodyLines(4, 650),
	))
	if !strings.Contains(md, "d'activation $𝜑$.") {
		t.Fatalf("inline fMath not wrapped as expected:\n%s", md)
	}
}

func TestMarkdown_AScriptInsideInlineMathIsMarked(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("notée ", fSerif, 10, 72, 700),
		str("𝜑", fMath, 10, 110, 700),
		str("out", fSerif, 7, 116, 697),
		str(" pour la sortie", fSerif, 10, 140, 700),
		bodyLines(4, 650),
	))
	if !strings.Contains(md, "$𝜑_{out}$") {
		t.Fatalf("subscript not marked:\n%s", md)
	}
}

func TestMarkdown_ADisplayFractionIsWrittenAsAFraction(t *testing.T) {
	t.Parallel()
	// An indented formula: "f =" on the baseline, then a numerator above and a denominator below.
	md := layoutMarkdown(page(
		bodyLines(6, 700),
		str("f", fMath, 10, 250, 600), str("=", fMath, 10, 262, 600),
		str("2", fMath, 10, 280, 607),
		str("1+𝑒", fMath, 10, 270, 593), str("−𝑥", fMath, 7, 290, 596),
		bodyLines(3, 540),
	))
	if !strings.Contains(md, `$$`) || !strings.Contains(md, `f = \frac{2}{1+𝑒^{−𝑥}}`) {
		t.Fatalf("fraction not recovered:\n%s", md)
	}
}

func TestMarkdown_RowsAfterABraceAreCases(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		bodyLines(6, 700),
		str("r", fMath, 10, 250, 600), str("=", fMath, 10, 260, 600), str("{", fMath, 10, 272, 600),
		str("a", fMath, 10, 285, 607), str(">", fMath, 10, 296, 607), str("0", fMath, 10, 306, 607),
		str("0", fMath, 10, 285, 593), str("sinon", fSerif, 10, 296, 593),
		bodyLines(3, 540),
	))
	if !strings.Contains(md, `\begin{cases} a > 0 \\ 0 sinon \end{cases}`) {
		t.Fatalf("cases not recovered:\n%s", md)
	}
}

func TestMarkdown_TwoFormulasStayApart(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		bodyLines(6, 700),
		str("a", fMath, 10, 250, 600), str("=", fMath, 10, 262, 600), str("1", fMath, 10, 274, 600),
		str("b", fMath, 10, 250, 560), str("=", fMath, 10, 262, 560), str("2", fMath, 10, 274, 560),
		bodyLines(3, 520),
	))
	if strings.Count(md, "$$\n") != 4 { // opening and closing of two blocks
		t.Fatalf("want two separate formulas:\n%s", md)
	}
}

func TestMarkdown_DotLeadersAreCollapsed(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(page(
		str("1.1 Un premier modèle . . . . . . . . . . . . . 3", fSerif, 10, 72, 700),
		bodyLines(4, 650),
	))
	if strings.Contains(md, ". . . .") || !strings.Contains(md, "…") {
		t.Fatalf("dot leaders not collapsed:\n%s", md)
	}
}

func TestMarkdown_ATextOnlyPageStaysReadable(t *testing.T) {
	t.Parallel()
	md := layoutMarkdown(bodyLines(5, 700))
	if strings.Contains(md, "#") || strings.Contains(md, "```") || strings.Contains(md, "$") {
		t.Fatalf("plain prose must stay plain:\n%s", md)
	}
}

func TestPageMarkdown_ALetterEncodedAsCode10IsKeptNotTakenForTheLibrarysMarker(t *testing.T) {
	t.Parallel()
	// In this subset font the byte 10 is a real letter ("r", "w" and "c" in its three fonts). Dropping what
	// a font decodes its newline marker to would delete those letters from the text.
	data, err := os.ReadFile(filepath.Join("testdata", "text_layer.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	md, err := PageMarkdown(reader.Page(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Sample Report", "paragraph with some plain text", "multi-paragraph extraction works", "| Name | Score |", "| Alice | 90 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}
