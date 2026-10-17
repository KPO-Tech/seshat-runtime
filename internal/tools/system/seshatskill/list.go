package seshatskill

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ListTool implements seshat_list_skills.
type ListTool struct{ baseTool }

func NewListTool() *ListTool { return &ListTool{} }

func (t *ListTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "seshat_list_skills",
		SearchHint:  "list available skills and their descriptions",
		DisplayName: "List Skills",
		Description: "List the skills available to the current Seshat runtime, including builtin, managed, user, project, MCP, and installed repo collections. Use this before creating a new skill or when searching for an existing one.",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"collection": map[string]any{
					"type":        "string",
					"description": "Optional collection filter: all (default), builtin, managed, user, project, mcp, or repo for any installed repository collection.",
					"enum":        []string{"all", "builtin", "managed", "user", "project", "mcp", "repo"},
				},
				"user_skills_dir": map[string]any{
					"type":        "string",
					"description": "Optional override path for additional user skills to include in the listing.",
				},
			},
		}),
		IsReadOnly:         true,
		IsConcurrencySafe:  true,
		IsDestructive:      false,
		RequiresPermission: false,
	}
}

func (t *ListTool) Call(ctx context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	collection, _ := input.Parsed["collection"].(string)
	if collection == "" {
		collection = "all"
	}
	userSkillsDir, _ := input.Parsed["user_skills_dir"].(string)
	cwd := workingDirFromInput(input)

	listed, err := listRuntimeSkills(cwd)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("failed to list skills: %w", err)), nil
	}
	listed = appendOverrideUserSkills(listed, userSkillsDir)

	filtered := make([]listedSkill, 0, len(listed))
	for _, item := range listed {
		if matchesCollection(collection, item.Collection) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return tool.NewTextResult("No skills found."), nil
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Collection != filtered[j].Collection {
			return filtered[i].Collection < filtered[j].Collection
		}
		return filtered[i].Skill.Name < filtered[j].Skill.Name
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d skill(s):\n\n", len(filtered)))
	sb.WriteString("| Slash Command | Collection | Description |\n")
	sb.WriteString("|---------------|------------|-------------|\n")
	for _, item := range filtered {
		description := item.Skill.Description
		if description == "" {
			description = item.Skill.WhenToUse
		}
		sb.WriteString(fmt.Sprintf("| /%s | %s | %s |\n", item.Skill.Name, item.Collection, truncate(description, 100)))
	}
	sb.WriteString("\nUse seshat_read_skill to inspect the full content of a specific skill.")

	return tool.NewTextResult(sb.String()), nil
}

func (t *ListTool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	if col, ok := input["collection"].(string); ok && col != "" {
		switch col {
		case "all", "builtin", "managed", "user", "project", "mcp", "repo":
		default:
			return nil, fmt.Errorf("collection must be one of: all, builtin, managed, user, project, mcp, repo")
		}
	}
	return input, nil
}

func (t *ListTool) Description(_ context.Context) (string, error) {
	return "List the skills available to the current Seshat runtime", nil
}
