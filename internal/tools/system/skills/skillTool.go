package skills

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type SkillInput struct {
	Skill string `json:"skill"`
	Args  string `json:"args,omitempty"`
}

type SkillOutput struct {
	Skill   string `json:"skill"`
	Args    string `json:"args,omitempty"`
	Output  string `json:"output"`
	Success bool   `json:"success"`
}

const skillDefCacheTTL = 10 * time.Second

type skillDefCache struct {
	def      contract.Definition
	cachedAt time.Time
	cwd      string
}

type SkillTool struct {
	loader        SkillLoader
	cwd           string
	userID        string
	allowedSkills []string
	forkRunner    ForkRunner
	defCacheMu    sync.Mutex
	defCache      *skillDefCache
}

// ForkRunner runs a skill declared with `context: fork` as an isolated
// sub-agent instead of inlining its prompt into the current conversation,
// and returns that sub-agent's final text output. allowedTools and model
// come straight from the skill's own frontmatter (Skill.AllowedTools,
// Skill.Model) - they only mean anything in this forked path, never in the
// default inline one.
//
// internal/agent already imports this package (for RestrictSessionSkills),
// so this package cannot import internal/agent back without a cycle: a
// host that can reach the agent-running infrastructure implements this
// interface and wires it in via SkillTool.SetForkRunner. See
// internal/tools/agents.SkillForkRunner for the real implementation.
type ForkRunner interface {
	// parentPermissionMode is the forking session's current permission
	// mode, passed through unresolved so the implementation (which has
	// access to the same sub-agent permission-inheritance policy every
	// other sub-agent spawn path uses) decides what the forked skill
	// actually runs under, instead of this package hardcoding a choice.
	RunForkedSkill(ctx context.Context, skillName, task string, allowedTools []string, model string, parentPermissionMode types.PermissionMode) (string, error)
}

// SetForkRunner wires the sub-agent infrastructure a `context: fork` skill
// needs. A skill tool built by NewSkillTool has none set: calling such a
// skill returns an error naming the missing runner instead of silently
// running inline.
func (t *SkillTool) SetForkRunner(runner ForkRunner) {
	t.forkRunner = runner
}

func NewSkillTool(loader SkillLoader) *SkillTool {
	if loader == nil {
		loader = NewFileSkillLoader("")
	}
	cwd, _ := os.Getwd()
	return &SkillTool{loader: loader, cwd: cwd}
}

func (t *SkillTool) SetCwd(cwd string) {
	t.cwd = cwd
}

func (t *SkillTool) SetUserID(userID string) {
	t.userID = userID
}

// SetAllowedSkills restricts this tool to the given skill names (matched
// against Skill.Name or Skill.DisplayName): every other skill behaves as if
// it did not exist, in the tool's own description and in lookups alike. A
// nil or empty list means no restriction (every skill stays visible), which
// is also the default for a tool built by NewSkillTool.
func (t *SkillTool) SetAllowedSkills(names []string) {
	t.allowedSkills = names
}

// filterAllowed narrows all down to SetAllowedSkills's list, or returns all
// unchanged when no restriction was set.
func (t *SkillTool) filterAllowed(all []Skill) []Skill {
	if len(t.allowedSkills) == 0 {
		return all
	}
	allowed := make(map[string]bool, len(t.allowedSkills))
	for _, name := range t.allowedSkills {
		allowed[name] = true
	}
	result := make([]Skill, 0, len(all))
	for _, sk := range all {
		if allowed[sk.Name] || allowed[sk.DisplayName] {
			result = append(result, sk)
		}
	}
	return result
}

func (t *SkillTool) Definition() contract.Definition {
	t.defCacheMu.Lock()
	defer t.defCacheMu.Unlock()
	if t.defCache != nil && t.defCache.cwd == t.cwd && time.Since(t.defCache.cachedAt) < skillDefCacheTTL {
		return t.defCache.def
	}

	skills, _ := GetAllSkillsForUser(t.cwd, t.userID)
	skills = t.filterAllowed(skills)
	skillList := FormatSkillsList(skills)
	visible := 0
	for _, sk := range skills {
		if !sk.IsHidden {
			visible++
		}
	}

	def := contract.Definition{
		Name:        SkillToolName,
		Description: GetPromptWithSkills(skillList),
		SearchHint:  "execute skill command plugin slash command",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill": map[string]any{
					"type":        "string",
					"description": "Name of the skill to execute",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional arguments to pass to the skill",
				},
			},
			"required": []string{"skill"},
		}),
		IsReadOnly:        false,
		IsConcurrencySafe: false,
		// With skills to offer the tool is loaded with every request: its description is the index of the skills (within its budget),
		// which is how the model knows what exists, and finding the tool with tool_search first costs a round trip to the model
		// each time a skill is used. Without any skill there is nothing to offer and it stays deferred.
		ShouldDefer: visible == 0,
	}
	t.defCache = &skillDefCache{def: def, cachedAt: time.Now(), cwd: t.cwd}
	return def
}

func (t *SkillTool) Call(ctx context.Context, input contract.CallInput, permissionCheck types.CanUseToolFn) (contract.CallResult, error) {
	parsed := input.Parsed
	if parsed == nil {
		return contract.NewErrorResult(fmt.Errorf("no input parsed")), nil
	}

	skillName, _ := parsed["skill"].(string)
	if skillName == "" {
		return contract.NewErrorResult(fmt.Errorf("skill name is required")), nil
	}

	args, _ := parsed["args"].(string)

	skill, err := t.lookupSkill(skillName)
	if err != nil {
		return contract.NewErrorResult(fmt.Errorf("failed to load skill: %w", err)), nil
	}

	if skill == nil {
		all, _ := GetAllSkillsForUser(t.cwd, t.userID)
		all = t.filterAllowed(all)
		var available []string
		for _, s := range all {
			available = append(available, s.Name)
		}
		return contract.NewErrorResult(fmt.Errorf("skill '%s' not found. Available skills: %s", skillName, strings.Join(available, ", "))), nil
	}

	// Execute the skill
	var promptResult []ContentBlock
	if skill.GetPromptForCommand != nil {
		promptResult, err = skill.GetPromptForCommand(args, ctx)
	} else {
		promptResult, err = ExecuteSkillPrompt(*skill, args, ctx)
	}

	if err != nil {
		return contract.NewErrorResult(fmt.Errorf("skill execution failed: %w", err)), nil
	}

	var promptText string
	for _, block := range promptResult {
		promptText += block.Text + "\n"
	}

	if skill.Context == ExecutionContextFork {
		if t.forkRunner == nil {
			return contract.NewErrorResult(fmt.Errorf("skill '%s' declares context: fork, but no fork runner is configured for this host", skillName)), nil
		}
		output, err := t.forkRunner.RunForkedSkill(ctx, skill.Name, promptText, skill.AllowedTools, skill.Model, input.ToolContextValue().PermissionMode)
		if err != nil {
			return contract.NewErrorResult(fmt.Errorf("skill '%s' fork failed: %w", skillName, err)), nil
		}
		return contract.NewTextResult(fmt.Sprintf("Skill '%s' invoked (forked) with args: '%s'\n\n%s", skillName, args, output)), nil
	}

	return contract.NewTextResult(fmt.Sprintf("Skill '%s' invoked with args: '%s'\n\n%s", skillName, args, promptText)), nil
}

// lookupSkill resolves a skill by name using the canonical priority order
// (managed > project > user > builtin > repos > bundled). All callers go through
// GetAllSkillsForUser so the deduplication logic is consistent with every other
// resolution path (HTTP /skills, seshat_list_skills, resolveSkillPrompt).
func (t *SkillTool) lookupSkill(name string) (*Skill, error) {
	all, err := GetAllSkillsForUser(t.cwd, t.userID)
	if err != nil {
		return nil, err
	}
	all = t.filterAllowed(all)
	for i := range all {
		if all[i].Name == name || all[i].DisplayName == name {
			return &all[i], nil
		}
	}
	if resolved := ResolveSkillAlias(name); resolved != name {
		for i := range all {
			if all[i].Name == resolved || all[i].DisplayName == resolved {
				return &all[i], nil
			}
		}
	}
	return nil, nil
}

func (t *SkillTool) Description(ctx context.Context) (string, error) {
	skills, _ := GetAllSkillsForUser(t.cwd, t.userID)
	skills = t.filterAllowed(skills)
	return GetPromptWithSkills(FormatSkillsList(skills)), nil
}

func (t *SkillTool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	return input, nil
}

func (t *SkillTool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx contract.ToolUseContext) types.PermissionResult {
	return types.AllowWithInput("", input)
}

func (t *SkillTool) IsConcurrencySafe(input map[string]any) bool {
	return false
}

func (t *SkillTool) IsReadOnly(input map[string]any) bool {
	return false
}

func (t *SkillTool) IsEnabled() bool {
	return true
}

func (t *SkillTool) FormatResult(data any) string {
	if output, ok := data.(SkillOutput); ok {
		if output.Success {
			return fmt.Sprintf("Skill '%s' executed with args: '%s'\n\n%s", output.Skill, output.Args, output.Output)
		}
		return fmt.Sprintf("Skill failed: %s", output.Output)
	}
	return fmt.Sprintf("%v", data)
}

func (t *SkillTool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

func GetPrompt() string {
	return `Execute a skill within the main conversation

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.

How to invoke:
- Use this tool with the skill name and optional arguments
- Examples:
  - skill: "pdf" - invoke the pdf skill
  - skill: "commit", args: "-m 'Fix bug'" - invoke with arguments
  - skill: "review-pr", args: "123" - invoke with arguments

Important:
- Available skills are listed in system-reminder messages in the conversation
- When a skill matches the user's request, invoke the relevant Skill tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running`
}

// FormatSkillsList renders the skill index within DefaultCatalogBudgetChars.
func FormatSkillsList(skills []Skill) string {
	return FormatSkillsListWithin(skills, DefaultCatalogBudgetChars)
}

func GetPromptWithSkills(skillsList string) string {
	return `Execute a skill within the main conversation

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.

Available skills:
` + skillsList + `

How to invoke:
- Use this tool with the skill name and optional arguments
- Examples:
  - skill: "pdf" - invoke the pdf skill
  - skill: "commit", args: "-m 'Fix bug'" - invoke with arguments
  - skill: "review-pr", args: "123" - invoke with arguments
  - skill: "namespace:skill-name" - invoke namespaced skill

Important:
- Available skills are listed in system-reminder messages in the conversation
- When a skill matches the user's request, invoke the relevant Skill tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running
- Skill prompts support ${SESHAT_SKILL_DIR} and ${SESHAT_SESSION_ID} substitution`
}
