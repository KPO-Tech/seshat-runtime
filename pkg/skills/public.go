package skills

import internalskills "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"

type (
	// FrontmatterData is the YAML header of a skill file (name, description, when to use it, allowed tools, arguments, model, invocation rules, version...), as it was written.
	FrontmatterData = internalskills.FrontmatterData
	// SkillSource says where a skill comes from: bundled with the runtime, a command file, a plugin, managed, or an MCP server.
	SkillSource = internalskills.SkillSource
	// RepoInstall is what a host writes beside a cloned skill repository when the person installs it: the skills chosen and the environment made for them.
	RepoInstall = internalskills.RepoInstall
)

// RepoInstallFile is the name, at the root of a cloned skill repository, of the file that holds its RepoInstall.
const RepoInstallFile = internalskills.RepoInstallFile

// LoadRepoInstall reads the install file of a cloned repository; ok is false when there is none.
func LoadRepoInstall(repoRoot string) (RepoInstall, bool) {
	return internalskills.LoadRepoInstall(repoRoot)
}

const (
	SourceBundled  = internalskills.SourceBundled
	SourceCommands = internalskills.SourceCommands
	SourcePlugin   = internalskills.SourcePlugin
	SourceManaged  = internalskills.SourceManaged
	SourceMCP      = internalskills.SourceMCP
)

// GetSkillsRootPath returns the directory under the runtime root that holds all the skills.
func GetSkillsRootPath() string {
	return internalskills.GetSkillsRootPath()
}

// GetBuiltinSkillsPath returns the directory of the built-in skills under the skills root.
func GetBuiltinSkillsPath() string {
	return internalskills.GetBuiltinSkillsPath()
}

// GetManagedSkillsPath returns the directory for admin-managed (policy) skills.
// Respects SESHAT_RUNTIME_ROOT so the path moves with the deployment.
func GetManagedSkillsPath() string {
	return internalskills.GetManagedSkillsPath()
}

// GetSkillReposPath returns the directory where skill repositories are cloned, under the skills root.
func GetSkillReposPath() string {
	return internalskills.GetSkillReposPath()
}

// GetUserSkillsPath returns the default single-user skill path (legacy CLI mode).
func GetUserSkillsPath() string {
	return internalskills.GetUserSkillsPath()
}

// ParseFrontmatter splits a skill file into its YAML header and its body. A file that does not start with a "---" header gives an empty FrontmatterData and the whole content.
func ParseFrontmatter(content string, filePath string) (FrontmatterData, string) {
	return internalskills.ParseFrontmatter(content, filePath)
}

// ParseBooleanFrontmatter reads a boolean from a YAML header value: a boolean as it is, the text "true" (any case) or "1" as true, a non-zero integer as true, and anything else as false.
func ParseBooleanFrontmatter(value any) bool {
	return internalskills.ParseBooleanFrontmatter(value)
}

// GetUserSkillsPathForUser returns the directory of the skills of a user: the shared user directory when userID is blank, a directory of that user otherwise.
func GetUserSkillsPathForUser(userID string) string {
	return internalskills.GetUserSkillsPathForUser(userID)
}
