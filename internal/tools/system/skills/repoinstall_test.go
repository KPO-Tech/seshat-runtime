package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoSkill(t *testing.T, root, dir, name string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test skill " + name + "\n---\nBody of " + name + "\n"
	if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillNames(skills []Skill) string {
	var out []string
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return strings.Join(out, ",")
}

func TestRepoSkillLoaderKeepsToWhatWasChosen(t *testing.T) {
	root := t.TempDir()
	writeRepoSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeRepoSkill(t, root, filepath.Join("skills", "beta"), "beta")

	all, _ := NewRepoSkillLoader(root, SettingSourceProjectSettings).LoadSkills()
	if len(all) != 2 {
		t.Fatalf("a repository with no install file gives every skill, got %q", skillNames(all))
	}

	if err := os.WriteFile(filepath.Join(root, RepoInstallFile), []byte(`{"include":["Beta"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	chosen, _ := NewRepoSkillLoader(root, SettingSourceProjectSettings).LoadSkills()
	if len(chosen) != 1 || !strings.HasSuffix(chosen[0].Name, "beta") {
		t.Fatalf("only the chosen skill is loaded, got %q", skillNames(chosen))
	}
}

func TestRepoSkillLoaderOffersNothingWhileInstallIsPending(t *testing.T) {
	root := t.TempDir()
	writeRepoSkill(t, root, "alpha", "alpha")
	if err := os.WriteFile(filepath.Join(root, RepoInstallFile), []byte(`{"pending":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := NewRepoSkillLoader(root, SettingSourceProjectSettings).LoadSkills()
	if len(got) != 0 {
		t.Fatalf("a repository being installed offers no skill, got %q", skillNames(got))
	}
}

func TestRepoSkillLoaderTellsTheAgentWhichPythonToUse(t *testing.T) {
	root := t.TempDir()
	writeRepoSkill(t, root, "alpha", "alpha")
	if err := os.WriteFile(filepath.Join(root, RepoInstallFile), []byte(`{"python":"C:/env/python.exe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := NewRepoSkillLoader(root, SettingSourceProjectSettings).LoadSkills()
	if len(got) != 1 {
		t.Fatalf("want one skill, got %q", skillNames(got))
	}
	blocks, err := got[0].GetPromptForCommand("", context.Background())
	if err != nil || len(blocks) == 0 {
		t.Fatalf("prompt: %v", err)
	}
	if !strings.Contains(blocks[0].Text, "C:/env/python.exe") || !strings.Contains(blocks[0].Text, "Body of alpha") {
		t.Fatalf("the prompt carries the interpreter and the skill, got %q", blocks[0].Text)
	}
}
