package htmltext

import (
	"strings"
	"testing"
)

func convert(t *testing.T, doc string) string {
	t.Helper()
	r, err := Convert([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return r.Markdown
}

func page(body string) string {
	return "<!doctype html><html><head><title>Page title</title><style>p{color:red}</style><script>var x = 1;</script></head><body>" + body + "</body></html>"
}

func contains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func lacks(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(got, bad) {
			t.Errorf("should not contain %q:\n%s", bad, got)
		}
	}
}

func TestHeadingsParagraphsAndInlineMarkup(t *testing.T) {
	got := convert(t, page(`<h1>Report</h1><h2>Scope</h2><p>Plain <strong>bold</strong> and <em>emphasis</em>, <del>gone</del>, <code>x := 1</code>.</p><p>Second<br>line</p>`))
	contains(t, got, "# Report\n\n## Scope\n\nPlain **bold** and *emphasis*, ~~gone~~, `x := 1`.\n\nSecond\nline")
}

func TestScriptsStylesAndHiddenElementsAreLeftOut(t *testing.T) {
	got := convert(t, page(`<p>Visible</p><script>alert("no")</script><style>.x{}</style><div hidden>secret</div><div style="display: none">gone</div><div aria-hidden="true">decor</div><noscript>enable js</noscript><nav><a href="/">Home</a></nav>`))
	contains(t, got, "Visible")
	lacks(t, got, "alert", "secret", "gone", "decor", "enable js", "Home", "color:red")
}

func TestThePageTitleBecomesTheFirstHeadingWhenThereIsNoH1(t *testing.T) {
	if got := convert(t, page(`<p>Text</p>`)); !strings.HasPrefix(got, "# Page title\n\nText") {
		t.Fatalf("got:\n%s", got)
	}
	if got := convert(t, page(`<h1>Own heading</h1>`)); strings.Contains(got, "Page title") {
		t.Fatalf("a page with its own h1 keeps it:\n%s", got)
	}
}

func TestNestedAndNumberedLists(t *testing.T) {
	got := convert(t, page(`<ul><li>One<ul><li>Inner a</li><li>Inner b</li></ul></li><li>Two</li></ul><ol start="3"><li>Third</li><li>Fourth<ol><li>Sub</li></ol></li></ol>`))
	contains(t, got, "- One\n  - Inner a\n  - Inner b\n- Two", "3. Third\n4. Fourth\n   1. Sub")
}

func TestAListItemWithTwoParagraphsKeepsBoth(t *testing.T) {
	got := convert(t, page(`<ul><li><p>First para</p><p>Second para</p></li><li>Next</li></ul>`))
	contains(t, got, "- First para\n\n  Second para\n- Next")
}

func TestLinksKeepTheirAddressUnlessItGoesNowhere(t *testing.T) {
	got := convert(t, page(`<p><a href="https://example.com/a b">site</a> <a href="#top">same page</a> <a href="javascript:void(0)">script</a> <a href="/rel">relative</a></p>`))
	contains(t, got, "[site](https://example.com/a%20b) same page script [relative](/rel)")
}

func TestPermalinkMarkersAfterHeadingsAreDropped(t *testing.T) {
	got := convert(t, page(`<h2>Install<a class="headerlink" href="#install">¶</a></h2><h2>Usage <a href="#usage">#</a></h2>`))
	contains(t, got, "## Install\n", "## Usage")
	lacks(t, got, "¶", "#install", "Usage #")
}

func TestCodeBlocksKeepTheirLayoutAndLanguage(t *testing.T) {
	got := convert(t, page("<pre><code class=\"language-go\">func main() {\n\tprintln(\"hi\")\n}\n</code></pre><pre>plain\n  indented</pre>"))
	contains(t, got, "```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```", "```\nplain\n  indented\n```")
}

func TestCodeContainingBackticksCannotCloseItsFence(t *testing.T) {
	got := convert(t, page("<pre>use ```fences``` carefully</pre><p>and <code>a`b</code></p>"))
	contains(t, got, "````\nuse ```fences``` carefully\n````", "``a`b``")
}

func TestBlockquotesAndRules(t *testing.T) {
	got := convert(t, page(`<blockquote><p>Quoted</p><p>Twice</p></blockquote><hr><p>After</p>`))
	contains(t, got, "> Quoted\n>\n> Twice", "---", "After")
}

func TestDefinitionLists(t *testing.T) {
	got := convert(t, page(`<dl><dt>HTTP</dt><dd>A protocol</dd><dt>TLS</dt><dd>Encryption</dd></dl>`))
	contains(t, got, "- **HTTP**: A protocol\n- **TLS**: Encryption")
}

func TestTablesKeepHeadersCaptionAndMergedCells(t *testing.T) {
	got := convert(t, page(`<table><caption>Sales</caption>
	  <thead><tr><th rowspan="2">Region</th><th colspan="2">2024</th></tr><tr><th>Plan</th><th>Actual</th></tr></thead>
	  <tbody><tr><td>North</td><td>10</td><td>12</td></tr><tr><td>South</td><td>7 | 8</td><td></td></tr></tbody></table>`))
	contains(t, got,
		"**Sales**\n\n| Region | 2024 / Plan | 2024 / Actual |\n| --- | --- | --- |\n| North | 10 | 12 |\n| South | 7 \\| 8 |  |")
}

func TestATableWithoutHeaderTagsUsesItsFirstRow(t *testing.T) {
	got := convert(t, page(`<table><tr><td>A</td><td>B</td></tr><tr><td>1</td><td>2</td></tr></table>`))
	contains(t, got, "| A | B |\n| --- | --- |\n| 1 | 2 |")
}

func TestCellsKeepListsBreaksAndNestedTables(t *testing.T) {
	got := convert(t, page(`<table><tr><th>Item</th><th>Detail</th></tr><tr><td>Plan</td><td><ul><li>a</li><li>b</li></ul>after<br>break</td></tr><tr><td>Grid</td><td><table><tr><td>x</td><td>y</td></tr></table></td></tr></table>`))
	contains(t, got, "| Plan | - a<br>- b<br>after<br>break |", "| Grid | x; y |")
}

func TestPicturesKeepTheirDescriptionAndLinkedIconsAreDropped(t *testing.T) {
	got := convert(t, page(`<p><img src="a.png" alt="Bar chart of sales"><img src="b.png"></p><p><a href="/help"><img src="i.png" alt="Help icon"></a></p>`))
	contains(t, got, "[Image: Bar chart of sales]")
	lacks(t, got, "Help icon")
}

func TestTheMainContentIsUsedWhenThePageHasOne(t *testing.T) {
	filler := strings.Repeat("Real content sentence. ", 20)
	got := convert(t, page(`<header><p>Site banner</p></header><div class="sidebar-box">menu junk</div><main><h1>Article</h1><p>`+filler+`</p></main><footer>copyright</footer>`))
	contains(t, got, "# Article")
	lacks(t, got, "Site banner", "menu junk", "copyright")
}

func TestASmallMainDoesNotHideTheRestOfThePage(t *testing.T) {
	got := convert(t, page(`<main><p>Short.</p></main><p>The rest of the document is here.</p>`))
	contains(t, got, "Short.", "The rest of the document is here.")
}

func TestWellKnownSiteChromeIsLeftOut(t *testing.T) {
	got := convert(t, page(`<h2>Topic<span class="mw-editsection"><a href="/edit">modifier</a></span></h2><p>Body</p><div class="navbox">Related pages</div><a class="skip-link" href="#c">Skip to content</a>`))
	contains(t, got, "## Topic\n", "Body")
	lacks(t, got, "modifier", "Related pages", "Skip to content")
}

func TestTheDeclaredEncodingIsHonoured(t *testing.T) {
	latin1 := "<html><head><meta charset=\"iso-8859-1\"><title>t</title></head><body><p>caf\xe9 cr\xe8me</p></body></html>"
	contains(t, convert(t, latin1), "café crème")
	utf8 := `<html><head><meta charset="utf-8"></head><body><p>café — “quoted”</p></body></html>`
	contains(t, convert(t, utf8), "café — “quoted”")
	noDeclaration := `<html><body><p>naïve façade — ok</p></body></html>`
	contains(t, convert(t, noDeclaration), "naïve façade — ok")
}

func TestWhitespaceInHTMLSourceIsNotTheTextsLayout(t *testing.T) {
	got := convert(t, page("<p>\n   Spread   over\n   several\n lines&nbsp;here.\n</p>"))
	contains(t, got, "Spread over several lines here.")
}

func TestAFragmentWithoutHtmlOrBodyTagsStillConverts(t *testing.T) {
	contains(t, convert(t, `<h2>Fragment</h2><p>Text</p>`), "## Fragment\n\nText")
}

func TestVeryDeepNestingDoesNotOverflowTheStack(t *testing.T) {
	deep := strings.Repeat("<div>", 5000) + "<p>deep</p>" + strings.Repeat("</div>", 5000)
	if _, err := Convert([]byte(page(deep))); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyAndGarbageInputDoNotFail(t *testing.T) {
	for _, in := range []string{"", "   ", "<<<>>>", "\x00\x01\x02", "plain text, no tags"} {
		if _, err := Convert([]byte(in)); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	contains(t, convert(t, "plain text, no tags"), "plain text, no tags")
}

func TestLinksOnlyKeepAddressesAReaderCanFollow(t *testing.T) {
	got := convert(t, page(`<p>
<a href="https://example.com/a">web</a>
<a href="/docs/page">relative</a>
<a href="mailto:me@example.com">mail</a>
<a href="javascript:alert(1)">script</a>
<a href="JaVaScRiPt:alert(1)">mixed</a>
<a href="jav&#x09;ascript:alert(1)">tab</a>
<a href="data:text/html,<script>alert(1)</script>">data</a>
<a href="vbscript:msgbox(1)">vb</a>
<a href="file:///etc/passwd">file</a>
<a href="/search?q=a:b">query with a colon</a>
</p>`))
	contains(t, got,
		"[web](https://example.com/a)", "[relative](/docs/page)", "[mail](mailto:me@example.com)", "[query with a colon](/search?q=a:b)",
		"script", "mixed", "tab", "data", "vb", "file")
	lacks(t, got, "javascript", "JaVaScRiPt", "data:", "vbscript", "file:///", "](jav")
}
