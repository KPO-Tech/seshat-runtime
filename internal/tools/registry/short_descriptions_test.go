package registry

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortDescriptionPrefersTheToolsOwnThenTheCatalogThenTheFirstSentence(t *testing.T) {
	own := Definition{Name: "git_status", ShortDescription: "  Its own line. ", Description: "Long description. More."}
	if got := ShortDescription(own); got != "Its own line." {
		t.Errorf("own = %q, want the tool's own short description, trimmed", got)
	}
	catalog := Definition{Name: "git_status", Description: "Long description. More."}
	if got := ShortDescription(catalog); got != builtinShortDescriptions["git_status"] {
		t.Errorf("catalog = %q, want the line written for git_status", got)
	}
	mcp := Definition{Name: "mcp__files__search", Description: "Search the shared drive for a file.\n\nTakes a query."}
	if got := ShortDescription(mcp); got != "Search the shared drive for a file." {
		t.Errorf("mcp = %q, want the first sentence", got)
	}
}

func TestFirstSentenceCutsAtTheEndOfTheSentenceAndAtTheLimit(t *testing.T) {
	if got := firstSentence("Does a thing. Then another thing.", 100); got != "Does a thing." {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("word ", 60)
	got := firstSentence(long, 40)
	if utf8.RuneCountInString(got) > 40 || !strings.HasSuffix(got, "…") {
		t.Errorf("got %q (%d runes), want at most 40 ending with an ellipsis", got, utf8.RuneCountInString(got))
	}
	if got := firstSentence("", 40); got != "" {
		t.Errorf("empty description gave %q", got)
	}
}

func TestEveryBuiltinShortDescriptionIsOneShortLine(t *testing.T) {
	for name, line := range builtinShortDescriptions {
		if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\n\r") {
			t.Errorf("%s: %q must be one non-empty line", name, line)
		}
		if n := utf8.RuneCountInString(line); n > maxShortDescription {
			t.Errorf("%s: %d characters, want at most %d", name, n, maxShortDescription)
		}
	}
}
