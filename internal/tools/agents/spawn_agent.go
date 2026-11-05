package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const spawnAgentName = "spawn_agent"
const spawnAgentSearchHint = "spawn a background sub-agent with a task and return a stable agent_id"
const spawnAgentDescription = `Launch a new background sub-agent to work on a specific task concurrently.

Returns immediately with a stable ` + "`agent_id`" + ` you can use with ` + "`wait_agent`" + `, ` + "`send_agent_message`" + `, and ` + "`close_agent`" + `.

## When to use
- Parallelise independent sub-tasks (research + implementation, analysis + writing, …)
- Delegate specialised work to a focused agent with a different role
- Run a long-running background task while you continue other work

## Lifecycle
1. Call ` + "`spawn_agent`" + ` → get back ` + "`agent_id`" + ` + initial ` + "`status`" + `
2. Optionally call ` + "`send_agent_message`" + ` to steer the agent between turns
3. Call ` + "`wait_agent`" + ` to block until the agent finishes and get its result
4. Call ` + "`close_agent`" + ` if you want to terminate the agent early

## agent_type
Use one of the built-in agent types: ` + "`general-purpose`" + `, ` + "`explore`" + `, ` + "`plan`" + `, ` + "`browse`" + ` (deep read-only web/multi-source research), ` + "`verify`" + `. Leave empty for ` + "`general-purpose`" + `.`

// SpawnAgentTool launches a sub-agent asynchronously and returns immediately.
// Mirrors Codex's CollabAgentTool = "spawnAgent" + CollabAgentSpawnBeginEvent / CollabAgentSpawnEndEvent.
type SpawnAgentTool struct {
	manager *coreagent.AsyncAgentManager
	eng     *engine.Engine
	tools   []tool.Tool
	reg     *coreagent.AgentRegistry
}

func NewSpawnAgentTool(eng *engine.Engine, tools []tool.Tool, reg *coreagent.AgentRegistry) *SpawnAgentTool {
	return &SpawnAgentTool{
		manager: coreagent.GetDefaultAsyncManager(),
		eng:     eng,
		tools:   tools,
		reg:     reg,
	}
}

func (t *SpawnAgentTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        spawnAgentName,
		DisplayName: "SpawnAgent",
		SearchHint:  spawnAgentSearchHint,
		Description: spawnAgentDescription,
		Category:    "agents",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The task or instruction to send to the new agent. Be specific and self-contained.",
				},
				"agent_type": map[string]any{
					"type":        "string",
					"description": "Agent type: 'general-purpose' (default), 'explore', 'plan', 'browse' (deep read-only web/multi-source research), or 'verify'.",
					"enum":        []string{"general-purpose", "explore", "plan", "browse", "verify"},
				},
				"role": map[string]any{
					"type":        "string",
					"description": "Optional role description for this agent (e.g. 'code reviewer', 'data analyst'). Informational only.",
				},
				"nickname": map[string]any{
					"type":        "string",
					"description": "Optional human-friendly name for this agent (e.g. 'Orion'). Informational only.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Maximum autonomous turns the agent may run. Default: 10.",
					"minimum":     1,
					"maximum":     50,
				},
			},
			"required": []string{"prompt"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  true,
		RequiresPermission: false,
	}
}

func (t *SpawnAgentTool) IsEnabled() bool                         { return true }
func (t *SpawnAgentTool) IsReadOnly(_ map[string]any) bool        { return false }
func (t *SpawnAgentTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *SpawnAgentTool) FormatResult(data any) string            { return fmt.Sprintf("%v", data) }
func (t *SpawnAgentTool) BackfillInput(_ context.Context, in map[string]any) map[string]any {
	return in
}
func (t *SpawnAgentTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	if p, _ := in["prompt"].(string); strings.TrimSpace(p) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	return in, nil
}
func (t *SpawnAgentTool) CheckPermissions(_ context.Context, in map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(in)
}
func (t *SpawnAgentTool) Description(_ context.Context) (string, error) {
	return spawnAgentDescription, nil
}

func (t *SpawnAgentTool) Call(
	ctx context.Context,
	input tool.CallInput,
	_ types.CanUseToolFn,
) (tool.CallResult, error) {
	prompt, _ := input.Parsed["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return tool.NewErrorResult(fmt.Errorf("prompt is required")), nil
	}

	agentType, _ := input.Parsed["agent_type"].(string)
	if agentType == "" {
		agentType = "general-purpose"
	}
	role, _ := input.Parsed["role"].(string)
	nickname, _ := input.Parsed["nickname"].(string)

	// Resolve the agent's own MaxTurns (e.g. 35 for BrowseAgent, a deep
	// research agent that legitimately needs more turns than a general
	// task) before falling back to the generic default of 10 - this used
	// to always default to 10 regardless of agent_type, silently
	// truncating a browse agent's designed budget by more than half
	// whenever the caller (the parent LLM turn) didn't pass an explicit
	// max_turns override, which it rarely does.
	agentDef := resolveSpawnAgentDef(t.reg, agentType)
	explicitMaxTurns, hasExplicitMaxTurns := input.Parsed["max_turns"].(float64)
	maxTurns := resolveSpawnAgentMaxTurns(agentDef, explicitMaxTurns, hasExplicitMaxTurns)

	toolCtx := input.ToolContextValue()
	callID := toolCtx.ToolUseID

	// Emit spawn.begin — mirrors Codex CollabAgentSpawnBeginEvent.
	emitAgentEvent(ctx, types.RuntimeEventTypeAgentSpawnBegin, &types.AgentRuntimeEvent{
		CallID:        callID,
		AgentID:       "",
		AgentNickname: nickname,
		AgentRole:     role,
		Prompt:        prompt,
		Status:        "pendingInit",
		StartedAtMs:   nowMs(),
	})

	var agentEventFn func(types.RuntimeEvent)
	if emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent)); ok && emitter != nil {
		agentEventFn = func(event types.RuntimeEvent) {
			event.AgentToolUseID = callID
			emitter(event)
		}
	}

	config := &coreagent.RunConfig{
		AgentType:       agentType,
		Task:            prompt,
		Engine:          t.eng,
		MaxTurns:        maxTurns,
		Context:         ctx,
		Registry:        t.reg,
		Nickname:        nickname,
		Role:            role,
		ParentSessionID: toolCtx.SessionID,
		// Background agents run in a goroutine that outlives the parent turn;
		// bypass permissions so tools are not stuck waiting for an interactive
		// prompt after the parent's turn context has been canceled. The
		// definition's own mode (clamped to the parent's) and a never parent
		// still win over that default, see resolveSubAgentPermissionMode.
		PermissionMode: resolveSubAgentPermissionMode(toolCtx.PermissionMode, agentDef),
		EventFn:        agentEventFn,
		// Without this, AsyncAgentManager.runAgent's default continuation
		// nudge is a bare "Continue with the task." — no structural push to
		// wrap up and synthesize once enough ground has been covered, unlike
		// the synchronous agent tool's equivalent config. A background
		// research agent (e.g. browse) can otherwise keep gathering more
		// sources turn after turn with nothing telling it "you can stop now"
		// until it hits a hard MaxTurns/timeout wall.
		ContinuationMessage: func(_ int, _ string) string {
			return "If you have completed all steps of the assigned task, return your final answer now. If there is still work remaining, continue with the next step."
		},
	}

	ag, err := t.manager.StartAgent(config)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("spawn_agent failed: %w", err)), nil
	}

	// Spawn a background goroutine that blocks on ag.Wait() and notifies the TUI
	// when the subagent completes its task, marking the spawn_agent tool as done.
	// This goroutine outlives the parent tool call (and often the parent HTTP
	// request that originated it), so it must never take the process down with
	// it: recover any panic here instead of letting it propagate, the same way
	// AsyncAgentManager.runAgent and its event dispatch workers do.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("spawn_agent completion notifier panic",
					"panic", r, "tool_use_id", callID, "agent_id", ag.ID)
			}
		}()

		ag.Wait()
		if emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent)); ok && emitter != nil {
			status := "completed"
			if ag.Status == coreagent.AgentStatusFailed {
				status = "failed"
			}
			metadata := map[string]any{"subagent_finished": true}
			if ag.Result != nil {
				if content := strings.TrimSpace(ag.Result.Output); content != "" {
					metadata["content"] = content
				}
				if ag.Result.SessionID != "" {
					metadata["session_id"] = string(ag.Result.SessionID)
				}
			}
			if ag.Error != nil {
				metadata["error"] = ag.Error.Error()
			}
			emitter(types.RuntimeEvent{
				Type:      types.RuntimeEventTypeToolProgress,
				Timestamp: time.Now(),
				ToolProgress: &types.ToolProgress{
					ToolUseID: callID,
					ToolName:  "spawn_agent",
					Stage:     types.ToolProgressStage(status),
					Metadata:  metadata,
				},
			})
		}
	}()

	// Emit spawn.end — mirrors Codex CollabAgentSpawnEndEvent.
	emitAgentEvent(ctx, types.RuntimeEventTypeAgentSpawnEnd, &types.AgentRuntimeEvent{
		CallID:        callID,
		AgentID:       ag.ID,
		AgentNickname: ag.Nickname,
		AgentRole:     ag.Role,
		Prompt:        prompt,
		Status:        ag.CollabStatus(),
		CompletedAtMs: nowMs(),
	})

	resp := map[string]any{
		"agent_id": ag.ID,
		"status":   ag.CollabStatus(),
	}
	if ag.Nickname != "" {
		resp["nickname"] = ag.Nickname
	}
	if ag.Role != "" {
		resp["role"] = ag.Role
	}
	resp["message"] = fmt.Sprintf("Agent '%s' spawned successfully. Use wait_agent('%s') to get the result.", ag.ID, ag.ID)

	res := tool.NewJSONResult(resp)
	res.Content = fmt.Sprintf("Agent spawned: %s (status: %s)", ag.ID, ag.CollabStatus())
	return res, nil
}

// ─── shared helpers ───────────────────────────────────────────────────────────

// resolveSpawnAgentDef looks up an agent definition using the registry
// first, then built-ins - mirrors AgentTool.resolveAgentDef (agent_tool.go),
// the synchronous agent tool's equivalent lookup.
func resolveSpawnAgentDef(registry *coreagent.AgentRegistry, agentType string) *coreagent.AgentDefinition {
	if registry != nil {
		if def, ok := registry.Get(agentType); ok {
			return def
		}
	}
	if builtIn := coreagent.GetBuiltInAgentByType(agentType); builtIn != nil {
		return coreagent.ToAgentDefinition(*builtIn)
	}
	return nil
}

// resolveSpawnAgentMaxTurns computes the turn budget for a spawned agent: an
// explicit max_turns input always wins; otherwise the agent's own designed
// MaxTurns (e.g. 35 for BrowseAgent, a deep research agent that legitimately
// needs more turns than a general task) is used; falling back to the generic
// default of 10 only when neither is available. Previously this always
// defaulted to 10 regardless of agent_type, silently truncating a browse
// agent's designed budget by more than half whenever the caller (the parent
// LLM turn) didn't pass an explicit override, which it rarely does.
func resolveSpawnAgentMaxTurns(agentDef *coreagent.AgentDefinition, explicit float64, hasExplicit bool) int {
	if hasExplicit && explicit >= 1 {
		return int(explicit)
	}
	if agentDef != nil && agentDef.MaxTurns > 0 {
		return agentDef.MaxTurns
	}
	return 10
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func emitAgentEvent(ctx context.Context, eventType types.RuntimeEventType, payload *types.AgentRuntimeEvent) {
	emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent))
	if !ok || emitter == nil {
		return
	}
	emitter(types.RuntimeEvent{
		Type:           eventType,
		Timestamp:      time.Now(),
		AgentToolUseID: payload.CallID,
		AgentEvent:     payload,
	})
}
