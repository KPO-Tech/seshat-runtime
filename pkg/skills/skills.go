package skills

import (
	"context"

	internalskills "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"
)

type (
	// Alias maps a skill name to the skill it stands for: using the Alias name runs the Target skill.
	Alias = internalskills.SkillAlias
	// Skill is a skill the model can use: its name and description, when to use it, the tools it may call, its arguments, the model and effort it asks for, whether the user or the model may invoke it, and the instructions it carries. Where it was loaded from is in Source and LoadedFrom.
	Skill = internalskills.Skill
)

// ExternalDir is a folder of skills read where it lies (see SetExternalDirs).
type ExternalDir = internalskills.ExternalDir

// SetExternalDirs makes the skills of other folders available to the agent of a user: Claude Code's
// and Codex's skill folders, or one of the person's own. They are read in place, never copied, and rank below the project's, the
// user's and the installed collections' skills of the same name. The folders set for the blank user are shared by every user. Call it again to change the list; an empty list removes it.
func SetExternalDirs(userID string, dirs []ExternalDir) {
	internalskills.SetExternalDirs(userID, dirs)
}

// LoadExternalDir loads the skills of a folder as the agent would, without the host's Skip filter, so a host can show what it leaves
// out and why. Hidden folders (.trash, .system...) are not read.
func LoadExternalDir(path string, source string) ([]Skill, error) {
	return internalskills.LoadExternalDir(path, internalskills.SettingSource(source))
}

// ExternalDirsFor returns the external skill folders set for a user.
func ExternalDirsFor(userID string) []ExternalDir {
	return internalskills.ExternalDirsFor(userID)
}

// All returns the skills available for the working directory cwd: those found in files, with the project's overriding the managed, user and built-in ones, then the bundled skills, which have the lowest priority.
func All(cwd string) ([]Skill, error) {
	return internalskills.GetAllSkills(cwd)
}

// AllForUser is All for a given user: it also reads the skills of that user's own directory.
func AllForUser(cwd string, userID string) ([]Skill, error) {
	return internalskills.GetAllSkillsForUser(cwd, userID)
}

// ForContext returns skills active for the given file paths. Skills with a
// paths restriction are included only when a file matches; all others are
// always returned. Equivalent to AllForUser when filePaths is empty.
func ForContext(cwd string, filePaths []string, userID string) ([]Skill, error) {
	return internalskills.GetSkillsForContext(cwd, filePaths, userID)
}

// MatchTrigger returns the first skill whose trigger phrases match userInput, or nil.
func MatchTrigger(userInput string, skills []Skill) *Skill {
	return internalskills.MatchTrigger(userInput, skills)
}

// UserPath returns the directory of the skills of a user: the shared user directory when userID is blank, a directory of that user otherwise.
func UserPath(userID string) string {
	return internalskills.GetUserSkillsPathForUser(userID)
}

// ReadEnabled reads the user-invocable (enabled) status of a skill.md file.
// Returns true when the file cannot be read or the field is absent.
func ReadEnabled(path string) bool {
	return internalskills.ReadSkillEnabled(path)
}

// Bundled returns the skills that ship with the runtime.
func Bundled() []Skill {
	return internalskills.GetBundledSkills()
}

// DiscoverMCP returns the skills offered by the MCP servers that are connected: one per tool they expose. Cancelling ctx stops the listing.
func DiscoverMCP(ctx context.Context) ([]Skill, error) {
	return internalskills.DiscoverMCPSkills(ctx)
}

// MCPServers returns the names of the connected MCP servers, or an empty list when MCP is not initialised.
func MCPServers() []string {
	return internalskills.GetMCPServers()
}

// MCPHealth reports the state of MCP for the skills: "not initialized", or the number and the names of the connected servers.
func MCPHealth() map[string]string {
	return internalskills.GetMCPHealth()
}

// Aliases returns the skill aliases that were registered.
func Aliases() []Alias {
	return internalskills.GetSkillAliases()
}

// RegisterAlias makes the skill tool accept alias as another name for the
// skill target: invoking alias through the skill tool runs target. Call it
// before the session starts; the registry is process-wide and not persisted.
func RegisterAlias(alias string, target string) {
	internalskills.RegisterSkillAlias(alias, target)
}
