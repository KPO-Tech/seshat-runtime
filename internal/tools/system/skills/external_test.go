package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillByName(skills []Skill, name string) *Skill {
	for i := range skills {
		if skills[i].Name == name {
			return &skills[i]
		}
	}
	return nil
}

func TestExternalSkillDirsAreReadInPlaceForTheirUser(t *testing.T) {
	cwd := t.TempDir()
	claude := t.TempDir()
	writeSkill(t, claude, "from-claude-code", "A skill that lies in another tool's folder")
	t.Cleanup(func() { SetExternalDirs("ext-user", nil); SetExternalDirs("other-user", nil) })

	SetExternalDirs("ext-user", []ExternalDir{{Path: claude, Source: "claudeCode"}})

	all, err := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "ext-user")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	found := skillByName(all, "from-claude-code")
	if found == nil {
		t.Fatalf("the skill of the external folder must be available, got %d skills", len(all))
	}
	if string(found.LoadedFrom) != "claudeCode" {
		t.Errorf("LoadedFrom = %q, want claudeCode", found.LoadedFrom)
	}

	// Another user does not get it, and nothing was copied next to the folder.
	other, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "other-user")
	if skillByName(other, "from-claude-code") != nil {
		t.Error("the external folders of a user must not leak to another user")
	}

	// Edited in place, the change is the next turn's skill.
	writeSkill(t, claude, "from-claude-code", "Edited description")
	again, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "ext-user")
	if got := skillByName(again, "from-claude-code"); got == nil || got.Description != "Edited description" {
		t.Errorf("an edit in the external folder must be read at the next load, got %+v", got)
	}

	// An empty list removes them; a folder that does not exist is ignored.
	SetExternalDirs("ext-user", []ExternalDir{{Path: filepath.Join(claude, "missing"), Source: "x"}})
	if gone, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "ext-user"); skillByName(gone, "from-claude-code") != nil {
		t.Error("a missing folder must not provide skills")
	}
}

func TestOwnSkillsWinOverExternalOnes(t *testing.T) {
	cwd := t.TempDir()
	project := filepath.Join(cwd, ".claude", "skills")
	writeSkill(t, project, "shared-name", "Project version")
	external := t.TempDir()
	writeSkill(t, external, "shared-name", "External version")
	t.Cleanup(func() { SetExternalDirs("win-user", nil) })
	SetExternalDirs("win-user", []ExternalDir{{Path: external, Source: "codex"}})

	all, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "win-user")
	if got := skillByName(all, "shared-name"); got == nil || got.Description != "Project version" {
		t.Errorf("the project's skill must win over an external one of the same name, got %+v", got)
	}
}

func TestSharedExternalDirsReachEveryUser(t *testing.T) {
	cwd := t.TempDir()
	shared := t.TempDir()
	writeSkill(t, shared, "device-wide", "Shared by every user of the device")
	t.Cleanup(func() { SetExternalDirs("", nil) })
	SetExternalDirs("", []ExternalDir{{Path: shared, Source: "localFolder"}})

	for _, user := range []string{"", "anyone", "someone-else"} {
		all, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, user)
		if skillByName(all, "device-wide") == nil {
			t.Errorf("the shared external folder must reach user %q", user)
		}
	}
}
