package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	"github.com/KPO-Tech/seshat-runtime/internal/runtime/tasks"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	worktreeTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/worktree"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// subAgentDepthKey is the context key used to propagate spawn depth so that
// each nested agent call can check whether it has exceeded the configured limit.
type subAgentDepthKey struct{}

// effectiveMaxDepth returns the active spawn depth limit for this request.
// Reads the per-request value set by types.WithSubAgentMaxDepth (injected by
// the API handler from the user's preference), clamped to MaxAbsoluteSubAgentDepth.
// Falls back to the compile-time constant MaxSubAgentDepth when no override exists.
func effectiveMaxDepth(ctx context.Context) int {
	if d := types.SubAgentMaxDepthFromContext(ctx); d > 0 {
		if d > coreagent.MaxAbsoluteSubAgentDepth {
			return coreagent.MaxAbsoluteSubAgentDepth
		}
		return d
	}
	return coreagent.MaxSubAgentDepth
}

// subAgentDepth returns the current spawn depth stored in ctx (0 = top-level).
// Returns 0 safely for a nil context.
func subAgentDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if d, ok := ctx.Value(subAgentDepthKey{}).(int); ok {
		return d
	}
	return 0
}

// withSubAgentDepth returns a child context carrying depth+1.
// Falls back to context.Background() if ctx is nil.
func withSubAgentDepth(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, subAgentDepthKey{}, subAgentDepth(ctx)+1)
}

// subAgentPermissionMode derives the permission mode a spawned sub-agent
// should run under from its parent session's current mode.
//
// Sub-agents are headless and synchronous: there is no UI to show an
// interactive prompt to, so onRequest/auto/acceptEdits/bypass/granular in the
// parent all collapse to bypass for the child, same as before this existed.
// The one exception is PermissionModeNever, whose own contract (see its doc
// comment) is "used for headless/background agents that cannot display UI
// prompts" - a sub-agent is exactly that, so a parent in never mode (asking
// for nothing to be escalated, failing closed instead) must not let a child
// escalate into bypass; the child stays in never too.
func subAgentPermissionMode(parentMode types.PermissionMode) types.PermissionMode {
	if parentMode == types.PermissionModeNever {
		return types.PermissionModeNever
	}
	return types.PermissionModeBypass
}

// resolveSubAgentPermissionMode returns the permission mode a sub-agent of
// type def runs under, given the spawning session's mode.
//
// Rules, in order:
//
//  1. def is nil or declares no PermissionMode: unchanged behaviour, see
//     subAgentPermissionMode (bypass, or never under a never parent).
//  2. def declares a mode: the child gets the STRICTER of that mode and the
//     parent's own mode (order: types.PermissionModeStrictness). A definition
//     can tighten a sub-agent but never loosen it past the agent that spawned
//     it. A parent whose mode is empty or unknown counts as onRequest.
//  3. def declares a value that is not one of the known modes: it is not
//     trusted and not silently widened. The child gets the stricter of the
//     parent's mode and onRequest, and the mismatch is logged.
//
// Since never is the strictest mode, a never parent always yields never
// (the contract of subAgentPermissionMode holds in every branch).
func resolveSubAgentPermissionMode(parentMode types.PermissionMode, def *coreagent.AgentDefinition) types.PermissionMode {
	if def == nil || def.PermissionMode == "" {
		return subAgentPermissionMode(parentMode)
	}

	ceiling := types.NormalizePermissionModeOrDefault(parentMode, types.PermissionModeOnRequest)

	requested, ok := types.NormalizePermissionMode(string(def.PermissionMode))
	if !ok {
		slog.Warn("agent definition declares an unknown permission mode; using the stricter of the parent mode and onRequest",
			"agent_type", def.AgentType, "declared_mode", string(def.PermissionMode), "parent_mode", string(ceiling))
		return types.StricterPermissionMode(ceiling, types.PermissionModeOnRequest)
	}

	effective := types.StricterPermissionMode(requested, ceiling)
	if effective != requested {
		slog.Info("agent definition permission mode clamped to the spawning session's mode",
			"agent_type", def.AgentType, "declared_mode", string(requested), "parent_mode", string(ceiling), "effective_mode", string(effective))
	}
	return effective
}

// Config for Agent tool
type AgentToolConfig struct {
	Enabled bool
}

// Default config
func DefaultAgentToolConfig() *AgentToolConfig {
	return &AgentToolConfig{Enabled: true}
}

// AgentTool is the agent tool
type AgentTool struct {
	config   *AgentToolConfig
	runner   *coreagent.Runner
	engine   *engine.Engine
	registry *coreagent.AgentRegistry

	// runFn replaces the agent run when set. Tests use it to observe the
	// RunConfig a spawn path builds; production code leaves it nil.
	runFn func(context.Context, *coreagent.RunConfig) (*coreagent.RunResult, error)

	// createTaskFn replaces the background task creation when set (tests only),
	// so the permission mode handed to the task manager can be observed.
	createTaskFn func(permMode types.PermissionMode) (*tasks.Task, error)
}

// NewAgentTool creates a new agent tool
func NewAgentTool(config *AgentToolConfig) *AgentTool {
	if config == nil {
		config = DefaultAgentToolConfig()
	}
	return &AgentTool{config: config}
}

// SetRunner sets an advanced runner for the agent tool
func (t *AgentTool) SetRunner(runner *coreagent.Runner) {
	t.runner = runner
}

// GetRunner returns the current runner (if any)
func (t *AgentTool) GetRunner() *coreagent.Runner {
	return t.runner
}

// SetEngine binds this tool instance to a specific engine.
func (t *AgentTool) SetEngine(e *engine.Engine) {
	t.engine = e
}

// SetRegistry sets the agent registry for resolving dynamic (skill-based) agent types.
func (t *AgentTool) SetRegistry(r *coreagent.AgentRegistry) {
	t.registry = r
}

// Definition returns the tool definition
func (t *AgentTool) Definition() tool.Definition {
	return tool.Definition{
		Name:               coreagent.ToolNameAgent,
		DisplayName:        "agent",
		SearchHint:         coreagent.SearchHintAgent,
		Description:        coreagent.DescriptionAgent + "\n\n" + coreagent.AgentPrompt,
		Category:           "agent",
		IsReadOnly:         false,
		IsDestructive:      false,
		IsConcurrencySafe:  false,
		RequiresPermission: true,
		Metadata: map[string]any{
			"is_stateful":   false,
			"subagent_type": "agent",
		},
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{
					"type":        "string",
					"description": "Agent type to run. Must be one of the built-in types.",
					"enum":        []string{"general-purpose", "explore", "browse", "plan", "verify"},
				},
				"task": map[string]any{
					"type":        "string",
					"description": "The self-contained task description for the agent. Include exact file paths, goals, constraints, and what to return.",
				},
				"maxTurns": map[string]any{
					"type":        "integer",
					"description": "Maximum autonomous turns. Default: 10 for sub-agents.",
					"minimum":     1,
					"maximum":     200,
				},
				"run_in_background": map[string]any{
					"type":        "boolean",
					"description": "When true, the agent runs asynchronously and returns a task ID immediately. Use TaskGet to poll status.",
					"default":     false,
				},
				"fork": map[string]any{
					"type":        "boolean",
					"description": "When true, the sub-agent inherits the parent session's message history.",
					"default":     false,
				},
				"isolation": map[string]any{
					"type":        "string",
					"description": "Set to 'worktree' to run the agent in an isolated git worktree. Changes are isolated until merged.",
					"enum":        []string{"worktree"},
				},
				"tools": map[string]any{
					"type":        "array",
					"description": "Optional list of tool name patterns to restrict the sub-agent's tool surface.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"type", "task"},
		}),
	}
}

// Call executes the tool
func (t *AgentTool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	var parsedInput map[string]any
	if err := json.Unmarshal([]byte(input.Raw), &parsedInput); err != nil {
		return tool.CallResult{
			Data:    map[string]any{"error": fmt.Sprintf("Failed to parse input: %v", err)},
			Content: fmt.Sprintf("Failed to parse input: %v", err),
		}, nil
	}

	agentType := ""
	task := ""

	// Accept both "type" and "agent_type" — the LLM sometimes uses the spawn_agent
	// convention. Priority: "type" > "agent_type" to keep backwards compat.
	if v, ok := parsedInput["type"].(string); ok {
		agentType = v
	}
	if agentType == "" {
		if v, ok := parsedInput["agent_type"].(string); ok {
			agentType = v
		}
	}
	if v, ok := parsedInput["task"].(string); ok {
		task = v
	}
	if v, ok := parsedInput["prompt"].(string); ok {
		task = v
	}

	if agentType == "" {
		available := t.listAvailableAgents()
		typeList := ""
		for _, a := range available {
			typeList += fmt.Sprintf("  - %s: %s\n", a["type"], a["whenToUse"])
		}
		return tool.CallResult{
			Data: map[string]any{
				"error":     "type is required",
				"available": available,
			},
			Content: fmt.Sprintf("Error: 'type' field is required. Available agent types:\n%s", typeList),
		}, nil
	}
	if task == "" {
		return tool.CallResult{
			Data:    map[string]any{"error": "task is required"},
			Content: "Error: task is required",
		}, nil
	}

	var agentDef *coreagent.AgentDefinition
	if t.registry != nil {
		agentDef, _ = t.registry.Get(agentType)
	}
	if agentDef == nil {
		if builtIn := coreagent.GetBuiltInAgentByType(agentType); builtIn != nil {
			agentDef = coreagent.ToAgentDefinition(*builtIn)
		}
	}
	if agentDef == nil {
		available := t.listAvailableAgents()
		typeList := ""
		for _, a := range available {
			typeList += fmt.Sprintf("  - %s: %s\n", a["type"], a["whenToUse"])
		}
		return tool.CallResult{
			Data: map[string]any{
				"error":     fmt.Sprintf("unknown agent type: %s", agentType),
				"available": available,
			},
			Content: fmt.Sprintf("Error: unknown agent type '%s'. Available types:\n%s", agentType, typeList),
		}, nil
	}

	maxTurns := 0
	if m, ok := parsedInput["maxTurns"].(float64); ok {
		maxTurns = int(m)
	}

	var allowedTools []string
	if toolsVal, ok := parsedInput["tools"].([]any); ok {
		for _, v := range toolsVal {
			if s, ok := v.(string); ok {
				allowedTools = append(allowedTools, s)
			}
		}
	}

	runInBackground := false
	if rb, ok := parsedInput["run_in_background"].(bool); ok {
		runInBackground = rb
	}

	forkMode := false
	if f, ok := parsedInput["fork"].(bool); ok {
		forkMode = f
	}

	isolationMode := ""
	if iso, ok := parsedInput["isolation"].(string); ok {
		isolationMode = iso
	}

	fullPrompt := buildAgentTaskPrompt(agentDef, task)
	permMode := resolveSubAgentPermissionMode(input.ToolContextValue().PermissionMode, agentDef)

	// Build a sub-agent event emitter that tags every forwarded event with this tool use ID,
	// so the frontend can route streaming events to the correct sub-agent card.
	var agentEventFn func(types.RuntimeEvent)
	if emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent)); ok && emitter != nil {
		toolUseID := input.ToolUseID
		agentEventFn = func(event types.RuntimeEvent) {
			event.AgentToolUseID = toolUseID
			emitter(event)
		}
	}

	if isolationMode == "worktree" {
		result, err := t.runAgentInWorktree(ctx, agentType, fullPrompt, maxTurns, allowedTools, parsedInput, agentEventFn, permMode)
		if err != nil {
			return tool.CallResult{Data: map[string]any{"error": err.Error()}, Content: err.Error()}, nil
		}
		if !result.Success {
			return tool.CallResult{
				Data:    map[string]any{"error": result.Error, "agentType": agentType},
				Content: fmt.Sprintf("Agent failed (worktree): %s\n\nError: %s", result.Output, result.Error),
			}, nil
		}
		wtData := map[string]any{
			"agentType":    agentType,
			"task":         task,
			"turns":        result.Turns,
			"toolUses":     result.ToolUses,
			"success":      result.Success,
			"isolation":    "worktree",
			"worktreePath": result.WorktreePath,
		}
		if len(result.Sources) > 0 {
			wtData["sources"] = result.Sources
		}
		return tool.CallResult{
			Data:    wtData,
			Content: result.Output + "\n\nWorktree created at: " + result.WorktreePath + "\nUse ExitWorktree to merge or discard changes.",
		}, nil
	}

	if runInBackground {
		result, err := t.runAgentBackground(ctx, agentType, fullPrompt, allowedTools, input.ToolUseID, permMode)
		if err != nil {
			return tool.CallResult{Data: map[string]any{"error": err.Error()}, Content: err.Error()}, nil
		}
		return result, nil
	}

	if forkMode {
		forkMessages := extractForkMessagesFromInput(input, parsedInput)
		result := t.runForkAgent(ctx, agentType, task, maxTurns, allowedTools, forkMessages, agentEventFn, permMode)
		if !result.Success {
			return tool.CallResult{
				Data:    map[string]any{"error": result.Error, "agentType": agentType},
				Content: fmt.Sprintf("Fork agent failed: %s\n\nError: %s", result.Output, result.Error),
			}, nil
		}
		forkData := map[string]any{
			"agentType": agentType,
			"task":      task,
			"turns":     result.Turns,
			"toolUses":  result.ToolUses,
			"success":   result.Success,
			"fork":      true,
		}
		if len(result.Sources) > 0 {
			forkData["sources"] = result.Sources
		}
		return tool.CallResult{Data: forkData, Content: result.Output}, nil
	}

	result := t.runAgent(ctx, agentType, task, maxTurns, allowedTools, agentEventFn, permMode)
	if !result.Success {
		return tool.CallResult{
			Data:    map[string]any{"error": result.Error, "agentType": agentType},
			Content: fmt.Sprintf("Agent failed: %s\n\nError: %s", result.Output, result.Error),
		}, nil
	}
	data := map[string]any{
		"agentType": agentType,
		"task":      task,
		"turns":     result.Turns,
		"toolUses":  result.ToolUses,
		"success":   result.Success,
	}
	if len(result.Sources) > 0 {
		data["sources"] = result.Sources
	}
	return tool.CallResult{Data: data, Content: result.Output}, nil
}

// runAgent executes the agent synchronously.
//
// Resource limits applied here:
//  1. Spawn depth — rejected if the call stack already reached MaxSubAgentDepth.
//     Prevents infinite delegation chains (A→B→C→…).
//  2. Wall-clock timeout — sub-agents receive a context bounded by
//     DefaultSubAgentTimeout so a stalled LLM cannot block the parent forever.
//  3. MaxTurns reduction — sub-agents default to DefaultSubAgentMaxTurns
//     (lower than the top-level DefaultMaxTurns) to limit token consumption.
func (t *AgentTool) runAgent(ctx context.Context, agentType, task string, maxTurns int, allowedTools []string, eventFn func(types.RuntimeEvent), permMode types.PermissionMode) *coreagent.RunResult {
	// ── 1. Spawn depth guard ────────────────────────────────────────────────
	depth := subAgentDepth(ctx)
	maxDepth := effectiveMaxDepth(ctx)
	if depth >= maxDepth {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error: fmt.Sprintf(
				"sub-agent spawn rejected: maximum depth %d reached (current depth %d). "+
					"Reduce agent nesting or increase the limit in Settings → Agent.",
				maxDepth, depth,
			),
		}
	}

	eng := t.engine
	if eng == nil {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     "engine not available - call agentToolInstance.SetEngine(engine) first",
		}
	}

	agentDef := t.resolveAgentDef(agentType)
	if agentDef == nil {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     fmt.Sprintf("unknown agent type: %s", agentType),
		}
	}

	// ── 2. MaxTurns — sub-agents get a tighter default than top-level ───────
	effectiveMaxTurns := maxTurns
	if effectiveMaxTurns == 0 {
		effectiveMaxTurns = agentDef.MaxTurns
	}
	if effectiveMaxTurns == 0 {
		if depth > 0 {
			effectiveMaxTurns = coreagent.DefaultSubAgentMaxTurns
		} else {
			effectiveMaxTurns = coreagent.DefaultMaxTurns
		}
	}

	// ── 3. Wall-clock timeout ───────────────────────────────────────────────
	// Always bound sub-agent execution so a stalled model cannot block the
	// parent session indefinitely. The caller's context deadline (if tighter)
	// still takes precedence via context cancellation chaining.
	timeout := time.Duration(coreagent.DefaultSubAgentTimeout) * time.Second
	subCtx, cancel := context.WithTimeout(withSubAgentDepth(ctx), timeout)
	defer cancel()

	filteredToolPatterns := filterToolPatterns(allowedTools, agentDef.GetToolPatterns())

	config := &coreagent.RunConfig{
		AgentType:      agentType,
		Task:           buildAgentTaskPrompt(agentDef, task),
		Engine:         eng,
		MaxTurns:       effectiveMaxTurns,
		Context:        subCtx,
		Callback:       nil,
		ToolFilter:     filteredToolPatterns,
		PermissionMode: permMode,
		ForkRunner:     NewSkillForkRunner(eng),
		EventFn:        eventFn,
		Registry:       t.registry,
		ContinuationMessage: func(_ int, _ string) string {
			return "If you have completed all steps of the assigned task, return your final answer now. If there is still work remaining, continue with the next step."
		},
	}

	result, err := t.executeRunConfig(subCtx, config)
	if err != nil {
		if result == nil {
			return &coreagent.RunResult{
				AgentType: agentType,
				Success:   false,
				Error:     fmt.Sprintf("agent execution failed: %v", err),
			}
		}
		result.Error = fmt.Sprintf("%s\nExecution error: %v", result.Error, err)
		result.Success = false
	}
	return result
}

// buildAgentTaskPrompt returns the task as-is. The agent's system prompt is
// injected via SetSystemPromptTemplate on the session (see RunAgent in internal/agent).
func buildAgentTaskPrompt(_ *coreagent.AgentDefinition, task string) string {
	return task
}

// ValidateInput validates tool input
func (t *AgentTool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	if input["type"] == nil || input["type"] == "" {
		return map[string]any{
			"result":  false,
			"message": "type is required",
		}, nil
	}
	if input["task"] == nil && input["prompt"] == nil {
		return map[string]any{
			"result":  false,
			"message": "task or prompt is required",
		}, nil
	}
	return input, nil
}

// CheckPermissions checks permissions
func (t *AgentTool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	return types.PermissionResult{Behavior: types.PermissionBehaviorAsk}
}

// IsConcurrencySafe returns whether tool is concurrency safe
func (t *AgentTool) IsConcurrencySafe(input map[string]any) bool {
	return false
}

// IsReadOnly returns whether tool is read-only
func (t *AgentTool) IsReadOnly(input map[string]any) bool {
	return false
}

// IsEnabled returns whether tool is enabled
func (t *AgentTool) IsEnabled() bool {
	return t.config.Enabled
}

// FormatResult formats result
func (t *AgentTool) FormatResult(data any) string {
	bytes, _ := json.Marshal(data)
	return string(bytes)
}

// BackfillInput enriches input
func (t *AgentTool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	if input["maxTurns"] == nil {
		// Resolve the effective default for the requested agent type so the
		// displayed value matches what the runner will actually use.
		agentType, _ := input["type"].(string)
		if agentType == "" {
			agentType, _ = input["agent_type"].(string)
		}
		def := t.resolveAgentDef(agentType)
		if def != nil && def.MaxTurns > 0 {
			input["maxTurns"] = def.MaxTurns
		} else {
			input["maxTurns"] = coreagent.DefaultSubAgentMaxTurns
		}
	}
	return input
}

// Description returns description
func (t *AgentTool) Description(ctx context.Context) (string, error) {
	return coreagent.DescriptionAgent, nil
}

// resolveAgentDef looks up an agent definition using the registry first, then built-ins.
func (t *AgentTool) resolveAgentDef(agentType string) *coreagent.AgentDefinition {
	if t.registry != nil {
		if def, ok := t.registry.Get(agentType); ok {
			return def
		}
	}
	if builtIn := coreagent.GetBuiltInAgentByType(agentType); builtIn != nil {
		return coreagent.ToAgentDefinition(*builtIn)
	}
	return nil
}

// listAvailableAgents returns all agents visible to this tool.
func (t *AgentTool) listAvailableAgents() []map[string]any {
	if t.registry != nil {
		defs := t.registry.All()
		result := make([]map[string]any, 0, len(defs))
		for _, def := range defs {
			result = append(result, map[string]any{
				"type":      def.AgentType,
				"whenToUse": def.WhenToUse,
				"maxTurns":  def.MaxTurns,
			})
		}
		return result
	}
	return coreagent.ListAvailableAgents()
}

// filterToolPatterns computes the intersection of main agent's tools and agent definition's allowed patterns.
func filterToolPatterns(mainAgentTools []string, agentPatterns []string) []string {
	if len(mainAgentTools) == 0 {
		return agentPatterns
	}
	if len(agentPatterns) == 0 || (len(agentPatterns) == 1 && agentPatterns[0] == "*") {
		return mainAgentTools
	}
	result := make([]string, 0)
	for _, t := range mainAgentTools {
		if matchesPattern(t, agentPatterns) {
			result = append(result, t)
		}
	}
	return result
}

// matchesPattern checks if a tool name matches any of the patterns.
func matchesPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == name {
			return true
		}
		if len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
			prefix := pattern[:len(pattern)-1]
			if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
				return true
			}
		}
		if len(pattern) > 1 && pattern[0] == '*' {
			suffix := pattern[1:]
			if len(name) >= len(suffix) && name[len(name)-len(suffix):] == suffix {
				return true
			}
		}
	}
	return false
}

// runAgentBackground runs an agent in the background using the task manager.
//
// Unlike spawn_agent (spawn_agent.go), which uses its own AsyncAgentManager
// and always notifies the frontend of real completion via a second
// tool.progress event, this used to leave the caller with nothing but
// TaskGet/TaskList polling: the outer tool call returns almost instantly
// (dispatch only), and the engine's generic tool-execution pipeline emits its
// own "completed" tool.progress event for that dispatch alone. Since this
// tool is literally named "agent" - the same name the synchronous, blocking
// call path uses, whose "completed" event genuinely IS the real
// completion - the frontend (useChat.ts) had no way to tell a real
// completion apart from a mere dispatch acknowledgement for this specific
// mode, and showed the sub-agent as "Completed" within milliseconds while
// the actual research kept running for tens of seconds server-side.
//
// Fixed the same way spawn_agent already does it: a background goroutine
// blocks on the task manager's own WaitForTask (the manager already bounds
// task execution internally via ManagerConfig.TaskTimeout, so no additional
// timeout is applied here) and emits a second tool.progress event carrying
// metadata.subagent_finished=true once the task actually reaches a terminal
// state - the same marker spawn_agent's own notifier uses, so the frontend
// can apply one shared rule for both.
func (t *AgentTool) runAgentBackground(ctx context.Context, agentType, prompt string, allowedTools []string, callID string, permMode types.PermissionMode) (tool.CallResult, error) {
	manager := tasks.GlobalManager()
	if manager == nil {
		return tool.CallResult{
			Data:    map[string]any{"error": "task manager not available"},
			Content: "Error: background task manager not available",
		}, nil
	}

	eng := t.engine
	if eng == nil {
		return tool.CallResult{
			Data:    map[string]any{"error": "engine not available"},
			Content: "Error: engine not available for background agent",
		}, nil
	}

	// Resolve the requested type's system prompt and tool restriction so the
	// background run actually behaves like that type, same as the
	// synchronous spawn paths - agentType/allowedTools were otherwise only
	// used for display in the result below, never to pick what the
	// sub-agent's identity or visible tools actually are.
	var systemPrompt string
	var toolPatterns []string
	if agentDef := t.resolveAgentDef(agentType); agentDef != nil {
		if agentDef.GetSystemPrompt != nil {
			systemPrompt = agentDef.GetSystemPrompt()
		}
		toolPatterns = filterToolPatterns(allowedTools, agentDef.GetToolPatterns())
	}

	var task *tasks.Task
	var err error
	if t.createTaskFn != nil {
		task, err = t.createTaskFn(permMode)
	} else {
		task, err = manager.CreateAgentTask(ctx, prompt, nil, permMode, systemPrompt, toolPatterns)
	}
	if err != nil {
		return tool.CallResult{
			Data:    map[string]any{"error": fmt.Sprintf("failed to create background agent: %v", err)},
			Content: fmt.Sprintf("Error: failed to create background agent: %v", err),
		}, nil
	}

	if emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent)); ok && emitter != nil {
		go t.notifyAgentTaskCompletion(manager, task.ID, callID, emitter) // #nosec G118 -- outlives the turn on purpose, see notifyAgentTaskCompletion
	}

	return tool.CallResult{
		Data: map[string]any{
			"agentType":   agentType,
			"taskId":      string(task.ID),
			"status":      string(task.Status),
			"background":  true,
			"description": task.Description,
		},
		Content: fmt.Sprintf("Agent '%s' started in background (task ID: %s)\n\nThe result is not delivered by itself: call task_get(task_id='%s', wait_seconds=N). It waits for the agent and returns its final answer. Do not wait with a sleep in a shell.", agentType, task.ID, task.ID),
	}, nil
}

// notifyAgentTaskCompletion blocks until the background agent task reaches a
// terminal state and emits the real completion as a tool.progress event.
// Mirrors spawn_agent.go's own completion notifier goroutine (same
// subagent_finished marker, same panic-recovery discipline - this goroutine
// outlives the parent tool call, and often the parent HTTP request that
// originated it, so it must never take the process down with it).
func (t *AgentTool) notifyAgentTaskCompletion(manager *tasks.Manager, taskID tasks.TaskID, callID string, emitter func(types.RuntimeEvent)) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("agent background completion notifier panic",
				"panic", r, "tool_use_id", callID, "task_id", taskID)
		}
	}()

	// context.Background(): the parent turn's context is expected to be
	// canceled well before a long-running background task finishes (that's
	// the whole point of running it in the background) - waiting on it here
	// would just mean this notifier gives up right when it's needed most.
	// No explicit timeout either: the task itself is already bounded by
	// ManagerConfig.TaskTimeout inside Manager.runAgentTask.
	finished, waitErr := manager.WaitForTask(context.Background(), taskID, 0)

	status := "completed"
	metadata := map[string]any{"subagent_finished": true}
	switch {
	case waitErr != nil:
		status = "failed"
		metadata["error"] = waitErr.Error()
	case finished == nil:
		status = "failed"
		metadata["error"] = "background agent task result unavailable"
	default:
		if finished.Status == tasks.TaskStatusFailed || finished.Status == tasks.TaskStatusKilled {
			status = "failed"
			if finished.Error != "" {
				metadata["error"] = finished.Error
			}
		}
		if content := strings.TrimSpace(finished.Output); content != "" {
			metadata["content"] = content
		}
	}

	emitter(types.RuntimeEvent{
		Type:      types.RuntimeEventTypeToolProgress,
		Timestamp: time.Now(),
		ToolProgress: &types.ToolProgress{
			ToolUseID: callID,
			ToolName:  coreagent.ToolNameAgent,
			Stage:     types.ToolProgressStage(status),
			Metadata:  metadata,
		},
	})
}

// extractForkMessagesFromInput extracts inherited transcript for fork mode.
func extractForkMessagesFromInput(callInput tool.CallInput, input map[string]any) []types.Message {
	if messages := transcriptMessagesFromToolContext(callInput); len(messages) > 0 {
		return messages
	}
	if messages := transcriptMessagesFromRawInput(input); len(messages) > 0 {
		return messages
	}
	return nil
}

// runForkAgent runs an agent in fork mode (inherits parent's messages).
// Applies the same depth and timeout guards as runAgent.
func (t *AgentTool) runForkAgent(ctx context.Context, agentType, task string, maxTurns int, allowedTools []string, forkMessages []types.Message, eventFn func(types.RuntimeEvent), permMode types.PermissionMode) *coreagent.RunResult {
	depth := subAgentDepth(ctx)
	maxDepth := effectiveMaxDepth(ctx)
	if depth >= maxDepth {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error: fmt.Sprintf(
				"fork agent spawn rejected: maximum depth %d reached (current depth %d)",
				maxDepth, depth,
			),
		}
	}

	eng := t.engine
	if eng == nil {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     "engine not available - call agentToolInstance.SetEngine(engine) first",
		}
	}

	agentDef := t.resolveAgentDef(agentType)
	if agentDef == nil {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     fmt.Sprintf("unknown agent type: %s", agentType),
		}
	}

	effectiveMaxTurns := maxTurns
	if effectiveMaxTurns == 0 {
		effectiveMaxTurns = agentDef.MaxTurns
	}
	if effectiveMaxTurns == 0 {
		if depth > 0 {
			effectiveMaxTurns = coreagent.DefaultSubAgentMaxTurns
		} else {
			effectiveMaxTurns = coreagent.DefaultMaxTurns
		}
	}

	timeout := time.Duration(coreagent.DefaultSubAgentTimeout) * time.Second
	subCtx, cancel := context.WithTimeout(withSubAgentDepth(ctx), timeout)
	defer cancel()

	config := &coreagent.RunConfig{
		AgentType:        agentType,
		Task:             buildAgentTaskPrompt(agentDef, task),
		Engine:           eng,
		MaxTurns:         effectiveMaxTurns,
		Context:          subCtx,
		ToolFilter:       filterToolPatterns(allowedTools, agentDef.GetToolPatterns()),
		ForkFromMessages: forkMessages,
		Callback:         nil,
		PermissionMode:   permMode,
		ForkRunner:       NewSkillForkRunner(eng),
		EventFn:          eventFn,
		Registry:         t.registry,
		ContinuationMessage: func(_ int, _ string) string {
			return "If you have completed all steps of the assigned task, return your final answer now. If there is still work remaining, continue with the next step."
		},
	}

	result, err := t.executeRunConfig(subCtx, config)
	if err != nil {
		if result == nil {
			return &coreagent.RunResult{
				AgentType: agentType,
				Success:   false,
				Error:     fmt.Sprintf("fork agent execution failed: %v", err),
			}
		}
		result.Error = fmt.Sprintf("%s\nExecution error: %v", result.Error, err)
		result.Success = false
	}
	return result
}

func transcriptMessagesFromToolContext(callInput tool.CallInput) []types.Message {
	toolCtx := callInput.ToolContextValue()
	if len(toolCtx.Metadata) == 0 {
		return nil
	}
	rawMessages, ok := toolCtx.Metadata["transcript_messages"]
	if !ok {
		return nil
	}
	messages, ok := rawMessages.([]types.Message)
	if !ok {
		return nil
	}
	cloned := make([]types.Message, len(messages))
	copy(cloned, messages)
	return cloned
}

func transcriptMessagesFromRawInput(input map[string]any) []types.Message {
	rawMessages, ok := input["messages"]
	if !ok {
		return nil
	}
	messageMaps, ok := rawMessages.([]any)
	if !ok {
		return nil
	}

	messages := make([]types.Message, 0, len(messageMaps))
	for _, rawMessage := range messageMaps {
		messageMap, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role, _ := messageMap["role"].(string)
		text, _ := messageMap["text"].(string)
		if role == "" || text == "" {
			continue
		}
		message := types.Message{
			ID:      types.MessageID(fmt.Sprintf("fork-%d", len(messages)+1)),
			Role:    types.Role(role),
			Content: []types.ContentBlock{types.TextContent{Text: text}},
		}
		messages = append(messages, message)
	}
	return messages
}

func (t *AgentTool) executeRunConfig(ctx context.Context, config *coreagent.RunConfig) (*coreagent.RunResult, error) {
	if t.runFn != nil {
		return t.runFn(ctx, config)
	}
	if t.runner != nil {
		return t.runner.RunAgentAdvanced(ctx, config)
	}
	return coreagent.RunAgent(config)
}

// runAgentInWorktree runs an agent in an isolated git worktree
func (t *AgentTool) runAgentInWorktree(ctx context.Context, agentType, task string, maxTurns int, allowedTools []string, input map[string]any, eventFn func(types.RuntimeEvent), permMode types.PermissionMode) (*coreagent.RunResult, error) {
	worktreeManager := worktreeTool.NewWorktreeManager(worktreeTool.WorktreeConfig{
		UseGitWorktree:  true,
		WorktreeBaseDir: ".worktrees",
	})

	worktreeSlug := fmt.Sprintf("agent-%s-%d", agentType, time.Now().Unix())

	branch := ""
	if b, ok := input["branch"].(string); ok {
		branch = b
	}

	session, err := worktreeManager.CreateWorktree(ctx, worktreeSlug, branch, ".")
	if err != nil {
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     fmt.Sprintf("failed to create worktree: %v", err),
		}, nil
	}

	eng := t.engine
	if eng == nil {
		_ = worktreeManager.RemoveWorktree(context.WithoutCancel(ctx), session, false)
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     "engine not available",
		}, nil
	}

	agentDef := t.resolveAgentDef(agentType)
	if agentDef == nil {
		_ = worktreeManager.RemoveWorktree(context.WithoutCancel(ctx), session, false)
		return &coreagent.RunResult{
			AgentType: agentType,
			Success:   false,
			Error:     fmt.Sprintf("unknown agent type: %s", agentType),
		}, nil
	}

	effectiveMaxTurns := maxTurns
	if effectiveMaxTurns == 0 {
		effectiveMaxTurns = agentDef.MaxTurns
	}
	if effectiveMaxTurns == 0 {
		effectiveMaxTurns = coreagent.DefaultMaxTurns
	}

	config := &coreagent.RunConfig{
		AgentType:      agentType,
		Task:           buildAgentTaskPrompt(agentDef, task),
		Engine:         eng,
		MaxTurns:       effectiveMaxTurns,
		Context:        ctx,
		ToolFilter:     filterToolPatterns(allowedTools, agentDef.GetToolPatterns()),
		WorktreeDir:    session.WorktreePath,
		Callback:       nil,
		PermissionMode: permMode,
		ForkRunner:     NewSkillForkRunner(eng),
		EventFn:        eventFn,
		Registry:       t.registry,
	}

	runWorktree := coreagent.RunAgent
	if t.runFn != nil {
		runWorktree = func(c *coreagent.RunConfig) (*coreagent.RunResult, error) { return t.runFn(ctx, c) }
	}
	result, err := runWorktree(config)
	if err != nil {
		if result == nil {
			_ = worktreeManager.RemoveWorktree(context.WithoutCancel(ctx), session, false)
			return &coreagent.RunResult{
				AgentType: agentType,
				Success:   false,
				Error:     fmt.Sprintf("worktree agent execution failed: %v", err),
			}, nil
		}
		result.Error = fmt.Sprintf("%s\nExecution error: %v", result.Error, err)
		result.Success = false
	}

	result.WorktreePath = session.WorktreePath
	return result, nil
}
