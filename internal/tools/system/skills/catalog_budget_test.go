package skills

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func manySkills(n int, descLen int) []Skill {
	out := make([]Skill, n)
	for i := range out {
		out[i] = Skill{
			Name:        fmt.Sprintf("skill-%03d", i),
			Description: strings.Repeat("é", descLen),
			WhenToUse:   "when the user asks for something this skill does",
			Triggers:    []string{"do the thing", "run the thing"},
		}
	}
	return out
}

func TestCatalogSmallListIsUnchanged(t *testing.T) {
	got := FormatSkillsListWithin(manySkills(3, 30), 8_000)
	if !strings.Contains(got, "(use when:") || !strings.Contains(got, "[auto-activates when user says:") {
		t.Errorf("a list within budget keeps every field, got %q", got)
	}
	if strings.Contains(got, "shortened") {
		t.Errorf("a list within budget carries no degradation note, got %q", got)
	}
}

func TestCatalogNeverExceedsItsBudget(t *testing.T) {
	for _, n := range []int{5, 40, 200, 2_000} {
		for _, d := range []int{20, 300, 2_000} {
			got := FormatSkillsListWithin(manySkills(n, d), 8_000)
			if c := utf8.RuneCountInString(got); c > 8_000 {
				t.Errorf("%d skills of %d chars: %d characters, over the budget", n, d, c)
			}
			if !utf8.ValidString(got) {
				t.Errorf("%d skills of %d chars: invalid UTF-8 (a character was cut)", n, d)
			}
		}
	}
}

func TestCatalogDegradesByLevels(t *testing.T) {
	// Fits as descriptions only, not with the extra fields.
	got := FormatSkillsListWithin(manySkills(40, 100), 8_000)
	if strings.Contains(got, "use when") || !strings.Contains(got, "shortened") {
		t.Errorf("expected descriptions only plus a note, got %q", got[:200])
	}
	// Too many for descriptions: names stay, descriptions shrink.
	got = FormatSkillsListWithin(manySkills(100, 400), 8_000)
	if !strings.Contains(got, "skill-099") || !strings.Contains(got, "...") {
		t.Errorf("expected every name with shortened descriptions, got %q", got[:200])
	}
	// Too many even for that: names only, and the leftovers are counted.
	got = FormatSkillsListWithin(manySkills(2_000, 50), 8_000)
	if strings.Contains(got, "skill-000:") || !strings.Contains(got, "more skills are not listed") {
		t.Errorf("expected names only and a count of the omitted ones, got %q", got[:200])
	}
}

func TestCatalogHiddenSkillsAreSkipped(t *testing.T) {
	skills := []Skill{{Name: "shown", Description: "x"}, {Name: "secret", Description: "y", IsHidden: true}}
	got := FormatSkillsListWithin(skills, 8_000)
	if strings.Contains(got, "secret") || !strings.Contains(got, "shown") {
		t.Errorf("hidden skills must not be listed, got %q", got)
	}
	if got := FormatSkillsListWithin([]Skill{{Name: "secret", IsHidden: true}}, 8_000); got != "No skills available" {
		t.Errorf("only hidden skills means none available, got %q", got)
	}
}
