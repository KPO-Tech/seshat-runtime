package skills

import (
	"os"
	"path/filepath"
	"sync"
)

// ExternalDir is a folder of skills that belongs to another tool or to the person (Claude Code's ~/.claude/skills, Codex's
// ~/.codex/skills, a folder of their own). It is read where it lies: nothing is copied, so a skill edited there is the skill the
// agent uses at the next turn.
type ExternalDir struct {
	// Path is the folder; every sub-folder holding a SKILL.md is a skill.
	Path string
	// Source names where the skills come from. It is shown with each skill (Skill.LoadedFrom), for example "claudeCode".
	Source SettingSource
}

var externalDirs = struct {
	sync.RWMutex
	byUser map[string][]ExternalDir
}{byUser: map[string][]ExternalDir{}}

// SetExternalDirs sets the external skill folders of a user, replacing the previous list. The folders set for the blank user are
// shared: every user gets them (a device-level setting), besides their own.
func SetExternalDirs(userID string, dirs []ExternalDir) {
	externalDirs.Lock()
	defer externalDirs.Unlock()
	if len(dirs) == 0 {
		delete(externalDirs.byUser, userID)
		return
	}
	externalDirs.byUser[userID] = append([]ExternalDir(nil), dirs...)
}

// ExternalDirsFor returns the external skill folders a user gets: the shared ones, then their own.
func ExternalDirsFor(userID string) []ExternalDir {
	externalDirs.RLock()
	defer externalDirs.RUnlock()
	dirs := append([]ExternalDir(nil), externalDirs.byUser[""]...)
	if userID != "" {
		dirs = append(dirs, externalDirs.byUser[userID]...)
	}
	return dirs
}

// externalLoaders returns a loader for each external folder that exists. They come last in the order of sources: a skill of the
// project, of the user or of an installed collection with the same name wins over one that only lies in another tool's folder.
func externalLoaders(userID string) []SkillLoader {
	var loaders []SkillLoader
	for _, dir := range ExternalDirsFor(userID) {
		if dir.Path == "" {
			continue
		}
		if info, err := os.Stat(dir.Path); err != nil || !info.IsDir() {
			continue
		}
		loaders = append(loaders, NewCollectionSkillLoader(filepath.Clean(dir.Path), dir.Source))
	}
	return loaders
}
