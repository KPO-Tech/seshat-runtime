package skills

import (
	"os"
	"path/filepath"
	"strings"
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

// Another tool keeps its deleted skills (.trash) and its own bundled ones (.system) in hidden folders: they are not skills the
// person chose and must not reach the agent.
func TestExternalDirsLeaveOutHiddenFolders(t *testing.T) {
	cwd := t.TempDir()
	root := t.TempDir()
	writeSkill(t, root, "chosen", "A skill the person has")
	writeSkill(t, filepath.Join(root, ".trash", "1792149802225-3656-abc"), "deleted-one", "Deleted long ago")
	writeSkill(t, filepath.Join(root, ".system"), "tool-internal", "Bundled by the tool")
	t.Cleanup(func() { SetExternalDirs("hid-user", nil) })
	SetExternalDirs("hid-user", []ExternalDir{{Path: root, Source: "claudeCode"}})

	all, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "hid-user")
	if skillByName(all, "chosen") == nil {
		t.Fatal("the skill the person has must be available")
	}
	for _, name := range []string{"deleted-one", "tool-internal"} {
		if skillByName(all, name) != nil {
			t.Errorf("%s lies in a hidden folder and must not be read", name)
		}
	}
	loaded, err := LoadExternalDir(root, "claudeCode")
	if err != nil || len(loaded) != 1 {
		t.Errorf("LoadExternalDir must skip hidden folders too: %d skills, err %v", len(loaded), err)
	}
}

func TestExternalDirSkipFilterLeavesSkillsOut(t *testing.T) {
	cwd := t.TempDir()
	root := t.TempDir()
	writeSkill(t, root, "keep-me", "Generic")
	writeSkill(t, root, "app-only", "Depends on the other tool's app")
	t.Cleanup(func() { SetExternalDirs("skip-user", nil) })
	SetExternalDirs("skip-user", []ExternalDir{{Path: root, Source: "claudeCode", Skip: func(s Skill) bool { return s.Name == "app-only" }}})

	all, _ := NewFileSkillLoader("").GetSkillDirCommandsForUser(cwd, "skip-user")
	if skillByName(all, "keep-me") == nil || skillByName(all, "app-only") != nil {
		t.Errorf("Skip must leave out exactly the skills it returns true for: keep=%v app-only=%v", skillByName(all, "keep-me") != nil, skillByName(all, "app-only") != nil)
	}
}

// With skills to offer the skill tool is loaded with every request (its description is the index of the skills, and finding it with
// tool_search would cost a round trip to the model each time a skill is used); with none it stays deferred.
func TestSkillToolIsLoadedOnlyWhenThereAreSkillsToOffer(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())
	cwd := t.TempDir()
	tool := NewSkillTool(nil)
	tool.SetCwd(cwd)
	tool.SetUserID("defer-user")
	if !tool.Definition().ShouldDefer {
		t.Error("with no skill the tool stays deferred")
	}

	writeSkill(t, filepath.Join(cwd, ".claude", "skills"), "offered", "A skill to offer")
	fresh := NewSkillTool(nil)
	fresh.SetCwd(cwd)
	fresh.SetUserID("defer-user")
	def := fresh.Definition()
	if def.ShouldDefer {
		t.Error("with a skill to offer the tool must be loaded, not deferred")
	}
	if !strings.Contains(def.Description, "offered") {
		t.Errorf("the description must carry the index of the skills: %q", def.Description)
	}
}
