package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSkillGetPromptForCommandIsPopulated(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "greet")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nname: \"greet\"\ndescription: \"say hello\"\nuser-invocable: true\n---\n\nHello, world!\n"
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	loaded, err := loader.LoadSkills()
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(loaded))
	}
	sk := loaded[0]
	if sk.GetPromptForCommand == nil {
		t.Fatal("GetPromptForCommand should not be nil for file-based skills")
	}
	blocks, err := sk.GetPromptForCommand("", context.Background())
	if err != nil {
		t.Fatalf("GetPromptForCommand: %v", err)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0].Text, "Hello, world!") {
		t.Fatalf("unexpected prompt content: %v", blocks)
	}
}

func TestFileSkillLoaderLoadsSkillMarkdownFile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "users", "usr_test")
	skillDir := filepath.Join(base, "team-only")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := "---\nname: \"team-only\"\ndescription: \"test skill\"\nuser-invocable: true\n---\n\nUse the admin scoped skill\n"
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	files, err := findSkillMarkdownFiles(base, false)
	if err != nil {
		t.Fatalf("findSkillMarkdownFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 skill markdown file, got %d (%v)", len(files), files)
	}

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	loaded, err := loader.LoadSkills()
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded skill, got %d", len(loaded))
	}
	if loaded[0].Name != "team-only" {
		t.Fatalf("unexpected skill name %q", loaded[0].Name)
	}
}

// TestLookupSkill_FilePriorityOverBundled verifies that a file-based skill wins
// over a bundled skill of the same name. Previously findSkillByName checked
// bundled skills first, which reversed the intended priority.
func TestLookupSkill_FilePriorityOverBundled(t *testing.T) {
	defer ClearBundledSkills()

	RegisterBundledSkill(BundledSkillDefinition{
		Name:          "greet",
		Description:   "bundled version",
		UserInvocable: true,
		GetPromptForCommand: func(args string, ctx context.Context) ([]ContentBlock, error) {
			return []ContentBlock{{Type: "text", Text: "bundled prompt"}}, nil
		},
	})

	base := t.TempDir()
	// Project skills live at <cwd>/.claude/skills/<name>/skill.md
	skillDir := filepath.Join(base, ".claude", "skills", "greet")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nname: \"greet\"\ndescription: \"file version\"\nuser-invocable: true\n---\n\nfile prompt\n"
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tool := NewSkillTool(nil)
	tool.SetCwd(base)
	tool.userID = ""

	skill, err := tool.lookupSkill("greet")
	if err != nil {
		t.Fatalf("lookupSkill error: %v", err)
	}
	if skill == nil {
		t.Fatal("expected skill 'greet', got nil")
	} else if skill.Source == SourceBundled {
		t.Errorf("expected file-based skill to win, got bundled skill (description: %q)", skill.Description)
	}

	blocks, err := skill.GetPromptForCommand("", context.Background())
	if err != nil {
		t.Fatalf("GetPromptForCommand: %v", err)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0].Text, "file prompt") {
		t.Errorf("expected file prompt content, got: %v", blocks)
	}
}

// TestLookupSkill_BundledFallback verifies that bundled skills are still
// reachable when no file-based skill of the same name exists.
func TestLookupSkill_BundledFallback(t *testing.T) {
	defer ClearBundledSkills()

	RegisterBundledSkill(BundledSkillDefinition{
		Name:          "only-bundled",
		Description:   "bundled only",
		UserInvocable: true,
		GetPromptForCommand: func(args string, ctx context.Context) ([]ContentBlock, error) {
			return []ContentBlock{{Type: "text", Text: "bundled only"}}, nil
		},
	})

	tool := NewSkillTool(nil)
	tool.SetCwd(t.TempDir())

	skill, err := tool.lookupSkill("only-bundled")
	if err != nil {
		t.Fatalf("lookupSkill error: %v", err)
	}
	if skill == nil {
		t.Fatal("expected bundled skill 'only-bundled', got nil")
	} else if skill.Source != SourceBundled {
		t.Errorf("expected bundled source, got %q", skill.Source)
	}
}

// TestLookupSkill_NotFound verifies nil is returned for an unknown skill name.
func TestLookupSkill_NotFound(t *testing.T) {
	tool := NewSkillTool(nil)
	tool.SetCwd(t.TempDir())

	skill, err := tool.lookupSkill("does-not-exist-xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill != nil {
		t.Errorf("expected nil for unknown skill, got %q", skill.Name)
	}
}

// TestParsePreambleTier verifies all accepted tier formats.
func TestParsePreambleTier(t *testing.T) {
	cases := []struct {
		input interface{}
		want  int
	}{
		{nil, 0},
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 0},
		{"basic", 1},
		{"standard", 2},
		{"full", 3},
		{"BASIC", 1},
		{"1", 1},
		{"2", 2},
		{"3", 3},
		{"unknown", 0},
		{float64(2), 2},
	}
	for _, c := range cases {
		got := ParsePreambleTier(c.input)
		if got != c.want {
			t.Errorf("ParsePreambleTier(%v) = %d, want %d", c.input, got, c.want)
		}
	}
}

// TestTriggersLoadedFromFrontmatter checks that trigger phrases are parsed and
// stored on the Skill struct when specified in frontmatter.
func TestTriggersLoadedFromFrontmatter(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "browse")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nname: \"browse\"\ndescription: \"browse pages\"\ntriggers:\n  - browse this page\n  - take a screenshot\n---\n\nNavigate to the URL.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	loaded, err := loader.LoadSkills()
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(loaded))
	}
	sk := loaded[0]
	if len(sk.Triggers) != 2 {
		t.Fatalf("expected 2 triggers, got %d: %v", len(sk.Triggers), sk.Triggers)
	}
	if !sk.MatchesTrigger("please browse this page for me") {
		t.Error("expected MatchesTrigger to return true for 'browse this page'")
	}
	if sk.MatchesTrigger("hello world") {
		t.Error("expected MatchesTrigger to return false for unrelated input")
	}
}

// TestPreambleTierInjection verifies that tier 1 suppresses the base-dir header
// while tier 0 (default) preserves it.
func TestPreambleTierInjection(t *testing.T) {
	base := t.TempDir()
	writeSkill := func(name, tier string) {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: \"" + name + "\"\ndescription: \"test\"\n"
		if tier != "" {
			body += "preamble-tier: " + tier + "\n"
		}
		body += "---\n\nHello.\n"
		if err := os.WriteFile(filepath.Join(dir, "skill.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeSkill("default-tier", "")
	writeSkill("basic-tier", "1")
	writeSkill("standard-tier", "2")

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	loaded, err := loader.LoadSkills()
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(loaded))
	}

	byName := make(map[string]Skill, len(loaded))
	for _, s := range loaded {
		byName[s.Name] = s
	}

	check := func(skillName string, wantHeader bool) {
		sk, ok := byName[skillName]
		if !ok {
			t.Fatalf("skill %q not found", skillName)
		}
		blocks, err := sk.GetPromptForCommand("", context.Background())
		if err != nil {
			t.Fatalf("%s: GetPromptForCommand: %v", skillName, err)
		}
		if len(blocks) == 0 {
			t.Fatalf("%s: expected content blocks", skillName)
		}
		hasHeader := strings.Contains(blocks[0].Text, "skill base directory")
		if hasHeader != wantHeader {
			t.Errorf("%s: wantHeader=%v but got hasHeader=%v\ncontent: %q",
				skillName, wantHeader, hasHeader, blocks[0].Text)
		}
	}

	check("default-tier", true)  // tier 0 → base dir injected
	check("basic-tier", false)   // tier 1 → no base dir
	check("standard-tier", true) // tier 2 → base dir injected
}

// TestFindSkillMarkdownFiles_RootLevel verifies that a skill.md placed directly
// at the repo root (single-skill repos like code-review-skill) is discovered.
func TestFindSkillMarkdownFiles_RootLevel(t *testing.T) {
	base := t.TempDir()
	// Write SKILL.md at root level (case-insensitive)
	if err := os.WriteFile(filepath.Join(base, "SKILL.md"), []byte("---\nname: root-skill\ndescription: test\n---\nHello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also write a normal sub-skill to ensure both are found
	subDir := filepath.Join(base, "sub-skill")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "skill.md"), []byte("---\nname: sub\ndescription: sub skill\n---\nSub"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := findSkillMarkdownFiles(base, false)
	if err != nil {
		t.Fatalf("findSkillMarkdownFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 skill files (root + sub), got %d: %v", len(files), files)
	}

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	skills, err := loader.LoadSkills()
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	// Root-level skill should be named after the base directory
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["sub-skill"] {
		t.Errorf("expected skill named 'sub-skill', got: %v", names)
	}
}

// TestRequiresPreflightInjection verifies that a skill with requires: injects a
// pre-flight block into the prompt and that the main content is still present.
func TestRequiresPreflightInjection(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "brainstorming")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: brainstorming
description: visual brainstorming
requires:
  - type: node
    check: "node --version"
    install-cmd: "cd ${SESHAT_SKILL_DIR}/scripts && npm install"
    packages: [express, ws]
---

Run the brainstorming server.
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	loaded, err := loader.LoadSkills()
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(loaded))
	}
	sk := loaded[0]
	if len(sk.Requires) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(sk.Requires))
	}
	if sk.Requires[0].Type != "node" {
		t.Errorf("expected type 'node', got %q", sk.Requires[0].Type)
	}

	blocks, err := sk.GetPromptForCommand("", context.Background())
	if err != nil {
		t.Fatalf("GetPromptForCommand: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("expected content blocks")
	}
	text := blocks[0].Text
	if !strings.Contains(text, "Pre-flight") {
		t.Error("expected pre-flight section in prompt")
	}
	if !strings.Contains(text, "node --version") {
		t.Error("expected check command in prompt")
	}
	if !strings.Contains(text, "npm install") {
		t.Error("expected install command in prompt")
	}
	if !strings.Contains(text, "brainstorming server") {
		t.Error("expected original skill content in prompt")
	}
}

// TestShellBeforeInjection verifies that a skill with shell.before injects a
// setup block instructing the agent to run those commands via Bash (with
// permission), and that the main content is still present.
func TestShellBeforeInjection(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "security-audit")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: security-audit
description: run a security scan
shell:
  before:
    - "which gosec || go install github.com/securego/gosec/v2/cmd/gosec@latest"
  after:
    - "echo done"
---

Run gosec and report findings.
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewFileSkillLoaderWithSource(base, SettingSourceUserSettings)
	loaded, err := loader.LoadSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(loaded))
	}
	sk := loaded[0]
	if sk.Shell == nil || len(sk.Shell.Before) != 1 {
		t.Fatalf("expected shell.before to be parsed, got %+v", sk.Shell)
	}

	blocks, err := sk.GetPromptForCommand("", context.Background())
	if err != nil {
		t.Fatalf("GetPromptForCommand: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("expected content blocks")
	}
	text := blocks[0].Text
	if !strings.Contains(text, "Setup: Shell Commands") {
		t.Error("expected shell setup section in prompt")
	}
	if !strings.Contains(text, "gosec") {
		t.Error("expected the before command in prompt")
	}
	if !strings.Contains(text, "permission") {
		t.Error("expected a permission instruction in prompt")
	}
	if !strings.Contains(text, "Run gosec and report findings") {
		t.Error("expected original skill content in prompt")
	}
	// after: is parsed but intentionally not injected (no post-completion hook exists yet).
	if strings.Contains(text, "echo done") {
		t.Error("shell.after should not be injected into the prompt yet")
	}
}

// TestMatchTrigger verifies that MatchTrigger finds the right skill.
func TestMatchTrigger(t *testing.T) {
	skills := []Skill{
		{Name: "browse", Triggers: []string{"browse this page", "take a screenshot"}},
		{Name: "review", Triggers: []string{"review the pr", "code review"}},
		{Name: "notrigger"},
	}
	got := MatchTrigger("please take a screenshot of the page", skills)
	if got == nil || got.Name != "browse" {
		t.Errorf("expected 'browse', got %v", got)
	}
	got = MatchTrigger("can you do a code review", skills)
	if got == nil || got.Name != "review" {
		t.Errorf("expected 'review', got %v", got)
	}
	got = MatchTrigger("hello world", skills)
	if got != nil {
		t.Errorf("expected nil, got %q", got.Name)
	}
}
