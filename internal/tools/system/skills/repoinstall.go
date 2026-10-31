package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RepoInstallFile is the name, at the root of a cloned skill repository, of the file that says how the person installed it.
const RepoInstallFile = ".seshat-install.json"

// RepoInstall is what the host wrote beside a cloned skill repository when the person installed it: which of its skills they chose, and
// the environment prepared for them. A repository with no such file is used as it is, every skill of it.
type RepoInstall struct {
	// Include lists the skills that were chosen, by the name of their folder. Empty means every skill of the repository.
	Include []string `json:"include,omitempty"`
	// Pending is true while the installation is not finished (the person has not confirmed, or the environment is being made):
	// the repository's skills are not offered to the agent until then.
	Pending bool `json:"pending,omitempty"`
	// Python is the interpreter of the environment made for this repository.
	Python string `json:"python,omitempty"`
	// NodeModules is the folder of the Node packages installed for this repository.
	NodeModules string `json:"node_modules,omitempty"`
}

// LoadRepoInstall reads the install file of a repository; ok is false when there is none or it cannot be read.
func LoadRepoInstall(repoRoot string) (RepoInstall, bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, RepoInstallFile))
	if err != nil {
		return RepoInstall{}, false
	}
	var install RepoInstall
	if err := json.Unmarshal(data, &install); err != nil {
		return RepoInstall{}, false
	}
	return install, true
}

// includes reports whether a skill is one the person chose, by the name of its folder: every skill when nothing was chosen.
func (r RepoInstall) includes(skillName string) bool {
	if len(r.Include) == 0 {
		return true
	}
	last := skillName
	if at := strings.LastIndex(last, ":"); at >= 0 {
		last = last[at+1:]
	}
	for _, wanted := range r.Include {
		if strings.EqualFold(wanted, last) {
			return true
		}
	}
	return false
}

// environmentNote tells the agent which interpreter and packages belong to the skill, so that its commands do not use the ones of the
// system. Empty when the repository has no environment.
func (r RepoInstall) environmentNote() string {
	var lines []string
	if r.Python != "" {
		lines = append(lines,
			fmt.Sprintf("Python for this skill: %q. Run its Python scripts with this interpreter (never the system python or python3), and install a missing package with: %q -m pip install <package>", r.Python, r.Python))
	}
	if r.NodeModules != "" {
		lines = append(lines, fmt.Sprintf("Node packages for this skill are installed in %q (set NODE_PATH to it, or run Node from its parent folder).", r.NodeModules))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Environment prepared when this skill was installed:\n- " + strings.Join(lines, "\n- ")
}

// withEnvironmentNote puts the note at the start of what the skill gives the agent.
func withEnvironmentNote(skill Skill, note string) Skill {
	if note == "" || skill.GetPromptForCommand == nil {
		return skill
	}
	inner := skill.GetPromptForCommand
	skill.GetPromptForCommand = func(args string, ctx context.Context) ([]ContentBlock, error) {
		blocks, err := inner(args, ctx)
		if err != nil || len(blocks) == 0 {
			return blocks, err
		}
		blocks[0].Text = note + "\n\n" + blocks[0].Text
		return blocks, nil
	}
	return skill
}

// NewRepoSkillLoader is the loader of a cloned repository: a collection that keeps to what the person chose when they installed it, and
// that gives the agent the environment made for it. A repository still being installed offers no skill.
func NewRepoSkillLoader(repoRoot string, source SettingSource) *FileSkillLoader {
	loader := NewCollectionSkillLoader(repoRoot, source)
	install, ok := LoadRepoInstall(repoRoot)
	if !ok {
		return loader
	}
	if install.Pending {
		loader.Skip = func(Skill) bool { return true }
		return loader
	}
	loader.include = install.includes
	loader.environmentNote = install.environmentNote()
	return loader
}
