package agents

import (
	"context"
	"fmt"
	"strings"
	"time"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const resumeAgentName = "resume_agent"
const resumeAgentSearchHint = "resume a completed sub-agent session with a new task"
const resumeAgentDescription = `Resume a previously completed sub-agent session and send it a new task.

The resumed agent picks up with its full conversation history intact — it remembers everything it learned, all files it read, and all work it did. Use this to continue a long investigation, request a follow-up analysis, or ask for revisions without restarting from scratch.

## How to get a session_id
Call ` + "`wait_agent`" + ` after ` + "`spawn_agent`" + ` completes — the result contains a ` + "`session_id`" + ` field.
You can also supply ` + "`agent_id`" + ` (the ID returned by ` + "`spawn_agent`" + `) directly if the agent is still in the registry.

## When to use
- Ask a research agent for more details or a different angle after seeing its first result
- Request revisions from a writing agent without restating all the context
- Send a follow-up implementation task to a coding agent that already read the codebase
- Chain multi-phase work: plan → implement → test → fix, each as a resume

## Parameters
- ` + "`session_id`" + `: session ID from ` + "`wait_agent`" + ` result (preferred)
- ` + "`agent_id`" + `: fallback — the agent_id from ` + "`spawn_agent`" + ` (only works if agent is still registered)
- ` + "`task`" + `: new instruction to send to the resumed agent
- ` + "`max_turns`" + `: max autonomous turns for this continuation (default: 10)
- ` + "`async`" + `: if true, run in the background and return an agent_id (like spawn_agent)`

// ResumeAgentTool resumes a persisted sub-agent session with a new task.
type ResumeAgentTool struct {
	manager *coreagent.AsyncAgentManager
	eng     *engine.Engine
	tools   []tool.Tool
	reg     *coreagent.AgentRegistry
}

func NewResumeAgentTool(eng *engine.Engine, tools []tool.Tool, reg *coreagent.AgentRegistry) *ResumeAgentTool {
	return &ResumeAgentTool{
		manager: coreagent.GetDefaultAsyncManager(),
		eng:     eng,
		tools:   tools,
		reg:     reg,
	}
}

func (t *ResumeAgentTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        resumeAgentName,
		DisplayName: "ResumeAgent",
		SearchHint:  resumeAgentSearchHint,
		Description: resumeAgentDescription,
		Category:    "agents",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Session ID from a previous wait_agent result. Preferred over agent_id.",
				},
				"agent_id": map[string]any{
					"type":        "string",
					"description": "Agent ID from spawn_agent. Used to look up the session_id automatically. Only works while the agent is still registered (before close_agent).",
				},
				"task": map[string]any{
					"type":        "string",
					"description": "New task or instruction to send to the resumed agent. The agent sees its full prior history before this message.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Maximum autonomous turns for this continuation. Default: 10.",
					"minimum":     1,
					"maximum":     50,
				},
				"async": map[string]any{
					"type":        "boolean",
					"description": "If true, run the resumed agent in the background and return an agent_id immediately (like spawn_agent). Default: false (blocks until done).",
				},
			},
			"oneOf": []any{
				map[string]any{"required": []string{"session_id", "task"}},
				map[string]any{"required": []string{"agent_id", "task"}},
			},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  true,
		RequiresPermission: false,
	}
}

func (t *ResumeAgentTool) IsEnabled() bool                         { return true }
func (t *ResumeAgentTool) IsReadOnly(_ map[string]any) bool        { return false }
func (t *ResumeAgentTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *ResumeAgentTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *ResumeAgentTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *ResumeAgentTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	sid, _ := in["session_id"].(string)
	aid, _ := in["agent_id"].(string)
	task, _ := in["task"].(string)
	if strings.TrimSpace(sid) == "" && strings.TrimSpace(aid) == "" {
		return nil, fmt.Errorf("one of session_id or agent_id is required")
	}
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task is required")
	}
	return in, nil
}
func (t *ResumeAgentTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *ResumeAgentTool) Description(_ context.Context) (string, error) {
	return resumeAgentDescription, nil
}

func (t *ResumeAgentTool) Call(
	ctx context.Context,
	input tool.CallInput,
	_ types.CanUseToolFn,
) (tool.CallResult, error) {
	sessionID, _ := input.Parsed["session_id"].(string)
	sessionID = strings.TrimSpace(sessionID)
	agentID, _ := input.Parsed["agent_id"].(string)
	agentID = strings.TrimSpace(agentID)
	task, _ := input.Parsed["task"].(string)
	task = strings.TrimSpace(task)
	runAsync, _ := input.Parsed["async"].(bool)
	maxTurns := 10
	if v, ok := input.Parsed["max_turns"].(float64); ok && v >= 1 {
		maxTurns = int(v)
	}

	if task == "" {
		return tool.NewErrorResult(fmt.Errorf("task is required")), nil
	}

	// Resolve session_id — either supplied directly or looked up via agent_id.
	if sessionID == "" {
		if agentID == "" {
			return tool.NewErrorResult(fmt.Errorf("one of session_id or agent_id is required")), nil
		}
		ag, err := t.manager.GetAgent(agentID)
		if err != nil {
			return tool.NewErrorResult(fmt.Errorf("agent not found: %s — use the session_id from wait_agent instead", agentID)), nil
		}
		if ag.SessionID == "" {
			return tool.NewErrorResult(fmt.Errorf("agent %s has no session_id yet — wait for it to complete first, then use the session_id from wait_agent", agentID)), nil
		}
		sessionID = string(ag.SessionID)
	}

	config := &coreagent.RunConfig{
		AgentType:           "general-purpose",
		Task:                task,
		Engine:              t.eng,
		MaxTurns:            maxTurns,
		Context:             ctx,
		Registry:            t.reg,
		InheritedToolPolicy: coreagent.ToolPolicyFromContext(ctx),
		ResumeFromSessionID: types.SessionID(sessionID),
		// The resumed run is always typed general-purpose (the original type is
		// not persisted), but a never parent must still not be escalated.
		PermissionMode: resolveSubAgentPermissionMode(input.ToolContextValue().PermissionMode, resolveSpawnAgentDef(t.reg, "general-purpose")),
	}

	if runAsync {
		ag, err := t.manager.StartAgent(config)
		if err != nil {
			return tool.NewErrorResult(fmt.Errorf("resume_agent (async) failed: %w", err)), nil
		}
		resp := map[string]any{
			"agent_id":   ag.ID,
			"status":     ag.CollabStatus(),
			"session_id": sessionID,
			"message":    fmt.Sprintf("Resumed agent '%s' running in background. Use wait_agent('%s') to get the result.", ag.ID, ag.ID),
		}
		res := tool.NewJSONResult(resp)
		res.Content = fmt.Sprintf("Resumed agent spawned: %s (session: %s)", ag.ID, sessionID)
		return res, nil
	}

	// Synchronous: block until the resumed run completes.
	startTime := time.Now()
	result, err := coreagent.RunAgent(config)
	elapsed := time.Since(startTime)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("resume_agent failed: %w", err)), nil
	}
	if !result.Success {
		return tool.NewErrorResult(fmt.Errorf("resume_agent errored: %s", result.Error)), nil
	}

	resp := map[string]any{
		"status":          "completed",
		"output":          result.Output,
		"turns":           result.Turns,
		"tool_uses":       result.ToolUses,
		"elapsed_seconds": elapsed.Seconds(),
		"session_id":      string(result.SessionID),
	}
	if len(result.Sources) > 0 {
		resp["sources"] = result.Sources
	}
	res := tool.NewJSONResult(resp)

	summary := fmt.Sprintf("Resumed agent completed in %.1fs (%d turns):\n%s", elapsed.Seconds(), result.Turns, result.Output)
	if len(result.Sources) > 0 {
		summary += fmt.Sprintf("\n\nSources consulted (%d):", len(result.Sources))
		for _, s := range result.Sources {
			summary += fmt.Sprintf("\n  [%s] %s", s.Type, s.Value)
		}
	}
	res.Content = summary
	return res, nil
}
