package skills

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultCatalogBudgetChars caps the skill index in the `skill` tool description. About 2,000 tokens, which is 2 %
	// of a 100k window: the index costs context on every request, so it must not grow with the number of skills.
	DefaultCatalogBudgetChars = 8_000

	maxCatalogDescriptionChars = 1_024
	maxCatalogHintChars        = 512
	// minCatalogDescriptionChars is the shortest description worth keeping; below it the entry is listed by name only.
	minCatalogDescriptionChars = 40
)

// FormatSkillsListWithin renders the skill index within budgetChars characters, degrading it level by level rather
// than letting it grow: full entries (description, when-to-use, triggers), then descriptions only, then descriptions shortened to an
// even share, then names only, then as many names as fit. A degraded index ends with a line saying so.
func FormatSkillsListWithin(skills []Skill, budgetChars int) string {
	visible := make([]Skill, 0, len(skills))
	for _, s := range skills {
		if !s.IsHidden {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return "No skills available"
	}

	if out := renderCatalog(visible, func(s Skill) string { return fullCatalogLine(s) }); chars(out) <= budgetChars {
		return out
	}
	if out := renderCatalog(visible, func(s Skill) string {
		return "- " + s.Name + ": " + truncateRunes(s.Description, maxCatalogDescriptionChars)
	}); chars(out) <= budgetChars {
		return out + degradedNote
	}

	// The names and separators are the fixed cost; the rest is shared evenly between the descriptions.
	fixed := 0
	for _, s := range visible {
		fixed += chars("- "+s.Name+": ") + 1
	}
	if share := (budgetChars - fixed - chars(degradedNote)) / len(visible); share >= minCatalogDescriptionChars {
		return renderCatalog(visible, func(s Skill) string {
			return "- " + s.Name + ": " + truncateRunes(s.Description, share)
		}) + degradedNote
	}

	return namesOnly(visible, budgetChars)
}

const degradedNote = "(The skill list was shortened to fit its budget. Invoke a skill to read all of it.)\n"

func renderCatalog(skills []Skill, line func(Skill) string) string {
	var b strings.Builder
	for _, s := range skills {
		b.WriteString(line(s))
		b.WriteByte('\n')
	}
	return b.String()
}

func fullCatalogLine(s Skill) string {
	line := "- " + s.Name + ": " + truncateRunes(s.Description, maxCatalogDescriptionChars)
	if s.WhenToUse != "" {
		line += " (use when: " + truncateRunes(s.WhenToUse, maxCatalogHintChars) + ")"
	}
	if len(s.Triggers) > 0 {
		line += " [auto-activates when user says: " + truncateRunes(strings.Join(s.Triggers, " / "), maxCatalogHintChars) + "]"
	}
	return line
}

func namesOnly(skills []Skill, budgetChars int) string {
	var b strings.Builder
	listed, used := 0, 0
	for _, s := range skills {
		line := "- " + s.Name + "\n"
		// Keep room for the note about the names left out.
		if used+chars(line)+chars(omittedNote(len(skills)-listed)) > budgetChars {
			break
		}
		b.WriteString(line)
		used += chars(line)
		listed++
	}
	if listed < len(skills) {
		b.WriteString(omittedNote(len(skills) - listed))
	} else {
		b.WriteString(degradedNote)
	}
	return b.String()
}

func omittedNote(n int) string {
	return fmt.Sprintf("(%d more skills are not listed because the list is over its budget.)\n", n)
}

func chars(s string) int { return utf8.RuneCountInString(s) }

// truncateRunes cuts s to at most n characters without splitting a multi-byte character, marking the cut.
func truncateRunes(s string, n int) string {
	if n <= 0 || chars(s) <= n {
		return s
	}
	r := []rune(s)
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}
