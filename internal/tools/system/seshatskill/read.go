package seshatskill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	skills "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ReadTool implements seshat_read_skill.
type ReadTool struct{ baseTool }

func NewReadTool() *ReadTool { return &ReadTool{} }

func (t *ReadTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "seshat_read_skill",
		SearchHint:  "read the full instructions of a skill by name",
		DisplayName: "Read Skill",
		Description: "Read the full content of a Seshat skill by name, including skill.md and the bundled files present in that skill directory. Use this before modifying a skill or when looking for an existing pattern to reuse.",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The slash-command skill name or display name to read.",
				},
				"user_skills_dir": map[string]any{
					"type":        "string",
					"description": "Optional override path for additional user skills to search.",
				},
			},
			"required": []string{"name"},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: false,
	}
}

func (t *ReadTool) Call(ctx context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	name, _ := input.Parsed["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return tool.NewErrorResult(fmt.Errorf("name is required")), nil
	}
	userSkillsDir, _ := input.Parsed["user_skills_dir"].(string)
	cwd := workingDirFromInput(input)

	listed, err := listRuntimeSkills(cwd)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to load skills: %w", err)), nil
	}
	listed = appendOverrideUserSkills(listed, userSkillsDir)

	selected := findListedSkill(name, listed)
	if selected == nil {
		selected = findOverrideUserSkill(name, userSkillsDir)
	}
	if selected == nil {
		return tool.NewErrorResult(fmt.Errorf("skill %q not found in the current runtime", name)), nil
	}
	if selected.SkillDir == "" {
		return tool.NewErrorResult(fmt.Errorf("skill %q does not resolve to a readable directory", selected.Skill.Name)), nil
	}

	skillMdPath := filepath.Join(selected.SkillDir, "skill.md")
	content, err := os.ReadFile(skillMdPath)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to read skill.md: %w", err)), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## skill: %s\n", selected.Skill.Name))
	if selected.Skill.DisplayName != "" && selected.Skill.DisplayName != selected.Skill.Name {
		sb.WriteString(fmt.Sprintf("## display name: %s\n", selected.Skill.DisplayName))
	}
	sb.WriteString(fmt.Sprintf("## collection: %s\n", selected.Collection))
	sb.WriteString(fmt.Sprintf("## path: %s\n\n", selected.SkillDir))
	sb.WriteString("### skill.md\n\n")
	sb.WriteString(string(content))

	bundled := listBundledFiles(selected.SkillDir)
	if len(bundled) > 0 {
		sb.WriteString("\n\n### Bundled files\n\n")
		for _, rel := range bundled {
			sb.WriteString(fmt.Sprintf("- %s\n", rel))
		}
	}

	return tool.NewTextResult(sb.String()), nil
}

func (t *ReadTool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	if name, ok := input["name"].(string); !ok || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name is required and must be a non-empty string")
	}
	return input, nil
}

func (t *ReadTool) Description(_ context.Context) (string, error) {
	return "Read the full content of a Seshat skill by name", nil
}

func findOverrideUserSkill(name, userSkillsDir string) *listedSkill {
	if strings.TrimSpace(userSkillsDir) == "" {
		return nil
	}
	loader := skills.NewFileSkillLoaderWithSource(userSkillsDir, skills.SettingSourceUserSettings)
	overrideSkills, err := loader.LoadSkills()
	if err != nil {
		return nil
	}
	for _, sk := range overrideSkills {
		if strings.EqualFold(sk.Name, name) || strings.EqualFold(sk.DisplayName, name) {
			return &listedSkill{Skill: sk, Collection: "user", SkillDir: resolveSkillDir(sk)}
		}
	}
	return nil
}

func listBundledFiles(skillDir string) []string {
	var files []string
	_ = filepath.Walk(skillDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(skillDir, path)
		if relErr != nil || rel == "skill.md" {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(files)
	return files
}
