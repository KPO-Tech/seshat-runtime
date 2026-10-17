package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/hooks"
	"github.com/KPO-Tech/seshat-runtime/internal/monitoring"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	providerretry "github.com/KPO-Tech/seshat-runtime/internal/providers/retry"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/schema"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	toolsearch "github.com/KPO-Tech/seshat-runtime/internal/tools/special/tool_search"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Loop represents the main query loop state machine.
type Loop struct {
	apiClient              *providers.Client
	activeFallbackClient   *providers.Client
	activeBaseModel        *types.ModelIdentifier
	activeStreamingTools   *streamingToolCoordinator
	callModelFn            func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error)
	sendAPIStreamRequestFn func(ctx context.Context, apiReq types.APIRequest, onChunk func(types.APIResponseChunk)) (*types.APIResponse, error)
	sendAPIRequestFn       func(ctx context.Context, apiReq types.APIRequest) (*types.APIResponse, error)
	transitionObserver     func(Transition)
	orchestrator           *execution.Orchestrator
	compactor              compact.CompactionStrategy
	promptAssembler        *prompt.Assembler
	permissionIntegrator   *permissions.Integrator
	hookExecutor           *hooks.Executor
	config                 *LoopConfig
	// providerConfig holds provider routing and fallback configuration
	providerConfig       *providers.Config
	currentFallbackIndex int
	// monitoring provides centralized metrics and logging
	monitoring *monitoring.System
}

// LoopConfig represents the loop configuration.
type LoopConfig struct {
	MaxIterations int  `json:"max_iterations"`
	AutoCompact   bool `json:"auto_compact"`
	// MaxTurns is currently dead weight: nothing in this file reads it. The
	// live session-turn limit is engine.Config.MaxTurns, enforced by
	// Session.enforceMaxTurns (M3 in TODO.md - its default used to disagree
	// with this one, 200 vs 100, despite neither mattering here).
	MaxTurns                     int  `json:"max_turns"`
	EnableStreaming              bool `json:"enable_streaming"`
	MaxOutputTokensRecoveryLimit int  `json:"max_output_tokens_recovery_limit"`
	ContinuationNudgeLimit       int  `json:"continuation_nudge_limit"`
	// TaskNudgeLimit caps how many times one turn sends the model back to its
	// own open tracked task list before letting the turn end. 0 = loop default
	// (3); a negative value disables the reminder entirely. See
	// maybeNudgeForOpenTasks (task_progress.go).
	TaskNudgeLimit             int        `json:"task_nudge_limit"`
	TurnTokenBudget            int        `json:"turn_token_budget,omitempty"`
	BudgetContinuationLimit    int        `json:"budget_continuation_limit,omitempty"`
	BudgetCompletionThreshold  float64    `json:"budget_completion_threshold,omitempty"`
	BudgetDiminishingThreshold int        `json:"budget_diminishing_threshold,omitempty"`
	StopHooks                  []StopHook `json:"-"`
	// StopHookConfig controls stop hook execution behavior
	StopHookTimeout         int    `json:"stop_hook_timeout,omitempty"`           // milliseconds, 0 = no timeout
	StopHookMode            string `json:"stop_hook_mode,omitempty"`              // "first" (default) or "all"
	StopHookContinueOnError bool   `json:"stop_hook_continue_on_error,omitempty"` // continue if hook errors
	// Additional recovery options
	MaxRecoveryAttempts       int `json:"max_recovery_attempts,omitempty"`         // total recovery attempts per turn
	RecoveryBackoffMultiplier int `json:"recovery_backoff_multiplier,omitempty"`   // milliseconds; base delay for attempt 1, doubled each attempt after
	RecoveryBackoffMaxDelay   int `json:"recovery_backoff_max_delay_ms,omitempty"` // milliseconds; caps the exponential growth, 0 = default (30s)
}

// DefaultLoopConfig returns default loop configuration.
//
// MaxIterations is the number of model-call rounds the engine executes within a
// single RunPrompt/StreamPrompt call. Each iteration is one LLM call plus any
// tool executions that follow. Increasing this allows complex autonomous tasks
// (many sequential tool-use rounds) without requiring multiple user messages.
//
// MaxTurns is the session-level turn counter limit across all RunPrompt calls.
func DefaultLoopConfig() *LoopConfig {
	return normalizeLoopConfig(&LoopConfig{
		MaxIterations:                100,
		AutoCompact:                  true,
		MaxTurns:                     100,
		EnableStreaming:              true,
		MaxOutputTokensRecoveryLimit: 3,
		ContinuationNudgeLimit:       3,
		TaskNudgeLimit:               defaultTaskNudgeLimit,
	})
}

func normalizeLoopConfig(config *LoopConfig) *LoopConfig {
	if config == nil {
		config = &LoopConfig{}
	}
	if config.MaxIterations <= 0 {
		config.MaxIterations = 100
	}
	if config.MaxTurns <= 0 {
		config.MaxTurns = 100
	}
	if !config.EnableStreaming {
		config.EnableStreaming = true
	}
	if config.MaxOutputTokensRecoveryLimit <= 0 {
		config.MaxOutputTokensRecoveryLimit = 3
	}
	if config.ContinuationNudgeLimit <= 0 {
		config.ContinuationNudgeLimit = 3
	}
	// 0 means "use the default"; a negative value is left as-is so a caller can
	// turn the reminder off (maybeNudgeForOpenTasks treats limit <= 0 as off).
	if config.TaskNudgeLimit == 0 {
		config.TaskNudgeLimit = defaultTaskNudgeLimit
	}
	if config.BudgetContinuationLimit <= 0 {
		config.BudgetContinuationLimit = defaultBudgetContinuationLimit
	}
	if config.BudgetCompletionThreshold <= 0 {
		config.BudgetCompletionThreshold = defaultBudgetCompletionThreshold
	}
	if config.BudgetDiminishingThreshold <= 0 {
		config.BudgetDiminishingThreshold = defaultBudgetDiminishingTokens
	}
	return config
}

// NewLoop creates a new query loop.
func NewLoop(
	apiClient *providers.Client,
	orchestrator *execution.Orchestrator,
	compactor compact.CompactionStrategy,
	promptAssembler *prompt.Assembler,
	permissionIntegrator *permissions.Integrator,
	hookExecutor *hooks.Executor,
	config *LoopConfig,
	monitoringSys *monitoring.System,
	providerConfigs ...*providers.Config,
) *Loop {
	loop := &Loop{
		apiClient:            apiClient,
		orchestrator:         orchestrator,
		compactor:            compactor,
		promptAssembler:      promptAssembler,
		permissionIntegrator: permissionIntegrator,
		hookExecutor:         hookExecutor,
		config:               normalizeLoopConfig(config),
		monitoring:           monitoringSys,
		currentFallbackIndex: -1,
	}
	if len(providerConfigs) > 0 && providerConfigs[0] != nil {
		loop.providerConfig = providerConfigs[0]
	}
	return loop
}

// RunRequest represents a request to run the query loop.
type RunRequest struct {
	Messages           []types.Message           `json:"messages"`
	SystemPrompt       string                    `json:"system_prompt"`
	SystemPromptBlocks []types.SystemPromptBlock `json:"system_prompt_blocks,omitempty"`
	ProviderTools      []types.APIToolDefinition `json:"tools,omitempty"`
	Tools              map[string]tool.Tool      `json:"-"`
	// Toolsets are resolved dynamically at the start of each iteration.
	// Their tools are merged into Tools each iteration (Toolset wins on name collision).
	Toolsets     []tool.Toolset `json:"-"`
	ToolRegistry *tool.Registry `json:"-"`
	// DeferredScope are tools the main agent can find with tool_search besides the registry's deferred ones (see
	// toolsearch.WithScope). Empty for sub-agents.
	DeferredScope       []string                                                                                                                            `json:"-"`
	RefreshSystemPrompt func(ctx context.Context, tools map[string]tool.Tool, pendingDeferred []string, stage prompt.ExecutionStage) (PromptRefresh, error) `json:"-"`
	// AutoDetectStage enables per-iteration stage detection. The engine sets this
	// when no static PromptStage is configured so that stage overlays fire
	// automatically without manual configuration.
	AutoDetectStage       bool                         `json:"-"`
	SessionID             types.SessionID              `json:"session_id"`
	TurnID                types.TurnID                 `json:"turn_id"`
	WorkingDirectory      string                       `json:"working_directory,omitempty"`
	PermissionMode        types.PermissionMode         `json:"permission_mode"`
	PermissionContext     *types.PermissionContext     `json:"-"`
	PermissionCheck       types.CanUseToolFn           `json:"-"`
	DenialTracking        *types.DenialTrackingState   `json:"-"`
	Model                 types.ModelIdentifier        `json:"model"`
	MaxTokens             int                          `json:"max_tokens"`
	ProgressCallback      func(types.ToolProgress)     `json:"-"`
	ResponseChunkCallback func(types.APIResponseChunk) `json:"-"`
	// EventQueue receives every streaming chunk emitted during this turn.
	// Callers read from EventQueue.Recv() in a separate goroutine to avoid
	// blocking the loop. Overflow is counted but never blocks.
	EventQueue      *execution.EventQueue `json:"-"`
	TurnTokenBudget int                   `json:"turn_token_budget,omitempty"`

	// OutputSchema, when set, constrains the model to return JSON matching the
	// schema. Passed through to types.APIRequest.OutputSchema.
	OutputSchema *schema.StructuredOutputInfo `json:"-"`

	// AutoCompactFailureCount carries the session's consecutive auto-compact
	// failure count into this turn, so the circuit breaker in
	// compact.Engine.AutoCompact persists across turns instead of resetting
	// to zero every Run. The caller (Session.SubmitMessage) is responsible
	// for threading RunResult.AutoCompactFailureCount back in on the next turn.
	AutoCompactFailureCount int `json:"auto_compact_failure_count,omitempty"`
}

// RunResult represents the result of running the loop.
type RunResult struct {
	Messages           []types.Message          `json:"messages"`
	StopReason         string                   `json:"stop_reason"`
	ToolUses           []types.ToolUseContent   `json:"tool_uses"`
	ToolResults        []tool.CallResult        `json:"tool_results"`
	Usage              *types.TokenUsage        `json:"usage"`
	PermissionContext  *types.PermissionContext `json:"-"`
	Compacted          bool                     `json:"compacted"`
	CompactPreTokens   int                      `json:"compact_pre_tokens,omitempty"`
	CompactPostTokens  int                      `json:"compact_post_tokens,omitempty"`
	Iterations         int                      `json:"iterations"`
	DiscoveredDeferred []string                 `json:"discovered_deferred,omitempty"`
	Error              error                    `json:"error,omitempty"`
	// RecoveryContext captures the turn execution state at loop exit.
	// Persisted by the engine into session metadata so that interrupted or
	// errored sessions can expose what was in-flight at the time of failure.
	RecoveryContext *RecoveryContext `json:"-"`
	// AutoCompactFailureCount is the session's consecutive auto-compact
	// failure count after this turn. The caller must store it (e.g. on
	// SessionState) and pass it back in as the next turn's
	// RunRequest.AutoCompactFailureCount for the circuit breaker to work
	// across turns.
	AutoCompactFailureCount int `json:"auto_compact_failure_count,omitempty"`
}

type PromptRefresh struct {
	SystemPrompt       string
	SystemPromptBlocks []types.SystemPromptBlock
}

type toolDiscoveryUpdate struct {
	HadToolSearch bool
	Discovered    []string
	Unresolved    []string
	Malformed     bool
	RefreshFailed error
}

// Run executes the main query loop.
func (l *Loop) Run(ctx context.Context, req RunRequest) RunResult {
	ctx = toolsearch.WithScope(ctx, req.DeferredScope)
	startTime := time.Now()
	l.currentFallbackIndex = -1
	l.activeFallbackClient = nil
	l.activeBaseModel = nil
	l.activeStreamingTools = nil
	req = normalizeRunRequestPermissionContext(req)
	state := l.initializeState(req)

	// Record query start in monitoring
	if l.monitoring != nil {
		l.monitoring.RecordQueryTurn()
	}

	// Execute query start hook
	l.executeHook(ctx, types.HookEventQueryStart, map[string]any{
		"session_id":      req.SessionID,
		"turn_id":         req.TurnID,
		"permission_mode": req.PermissionMode,
		"model":           req.Model,
		"max_tokens":      req.MaxTokens,
	})

	for state.Iterations < l.config.MaxIterations {
		select {
		case <-ctx.Done():
			return RunResult{Messages: state.Messages, StopReason: types.StopReasonEndTurn, Iterations: state.Iterations, Error: ctx.Err(), RecoveryContext: state.RecoveryContext, AutoCompactFailureCount: state.AutoCompactFailureCount}
		default:
		}

		state.Iterations++

		// Record iteration in monitoring
		if l.monitoring != nil {
			l.monitoring.RecordQueryIteration()
		}

		// When AutoDetectStage is enabled, detect the current stage from accumulated
		// messages and refresh the system prompt when it changes. This injects the
		// right stage overlay (tool_result, continuation, …) per iteration without
		// requiring callers to configure PromptStage manually.
		if req.AutoDetectStage && req.RefreshSystemPrompt != nil {
			detectedStage := DetectTurnStage(state.Messages, req.TurnID)
			if detectedStage != state.CurrentStage {
				state.CurrentStage = detectedStage
				refresh, refreshErr := req.RefreshSystemPrompt(ctx, req.Tools, remainingDeferredToolNames(req.ToolRegistry, state.DiscoveredDeferred, req.DeferredScope), detectedStage)
				if refreshErr == nil {
					req.SystemPrompt = refresh.SystemPrompt
					req.SystemPromptBlocks = append([]types.SystemPromptBlock(nil), refresh.SystemPromptBlocks...)
				}
			}
		}

		// Execute iteration start hook
		if l.executeHookAndStop(ctx, state, types.HookEventIterationStart, map[string]any{
			"iteration":     state.Iterations,
			"session_id":    req.SessionID,
			"turn_id":       req.TurnID,
			"message_count": len(state.Messages),
			"total_tokens":  state.TotalTurnTokens,
		}) {
			l.setTransition(state, Terminate("hook_stop", types.StopReasonEndTurn))
			break
		}

		l.prepareMessagesForIteration(state)
		l.resolveToolsets(ctx, &req)

		if err := l.maybeAutoCompact(ctx, state, req); err != nil {
			// Record error in monitoring
			if l.monitoring != nil {
				l.monitoring.RecordQueryFailure(err, time.Since(startTime))
			}
			l.setTransition(state, Terminate("auto_compact_error", types.StopReasonEndTurn))
			return RunResult{
				Messages:                state.Messages,
				StopReason:              types.StopReasonEndTurn,
				Iterations:              state.Iterations,
				Error:                   err,
				RecoveryContext:         state.RecoveryContext,
				AutoCompactFailureCount: state.AutoCompactFailureCount,
			}
		}

		l.emitContextUsage(ctx, state, req)

		apiResp, err := l.callModel(ctx, state, req)
		if err != nil {
			// Record error in monitoring
			if l.monitoring != nil {
				l.monitoring.RecordQueryFailure(err, time.Since(startTime))
			}
			if l.isRecoverableError(err) {
				recovered, recoveryErr := l.tryRecovery(ctx, state, req, err)
				if recoveryErr != nil {
					l.setTransition(state, Terminate("recovery_error", types.StopReasonEndTurn))
					return RunResult{Messages: state.Messages, StopReason: types.StopReasonEndTurn, Iterations: state.Iterations, Error: fmt.Errorf("recovery failed: %w", recoveryErr), RecoveryContext: state.RecoveryContext, AutoCompactFailureCount: state.AutoCompactFailureCount}
				}
				if recovered {
					continue
				}
			}
			l.setTransition(state, Terminate("api_error_terminal", types.StopReasonEndTurn))
			return RunResult{Messages: state.Messages, StopReason: types.StopReasonEndTurn, Iterations: state.Iterations, Error: err, RecoveryContext: state.RecoveryContext, AutoCompactFailureCount: state.AutoCompactFailureCount}
		}

		l.updateUsage(state, &apiResp.Usage)
		assistantMessage := buildAssistantMessage(apiResp, req.TurnID, len(state.Messages))
		state.Messages = append(state.Messages, assistantMessage)
		recordUsageAnchor(state, &apiResp.Usage)
		l.emitContextUsage(ctx, state, req)

		toolUses := l.extractToolUses(apiResp.Content)
		if len(toolUses) > 0 {
			outcome := l.handleToolUsesIteration(ctx, state, &req, toolUses)
			if outcome.result != nil {
				return *outcome.result
			}
			if outcome.shouldBreak {
				break
			}
			continue
		}

		if l.shouldStop(apiResp.StopReason) {
			// Execute iteration stop hook
			l.executeHook(ctx, types.HookEventIterationStop, map[string]any{
				"iteration":    state.Iterations,
				"stop_reason":  apiResp.StopReason,
				"session_id":   req.SessionID,
				"turn_id":      req.TurnID,
				"total_tokens": state.TotalTurnTokens,
			})

			continued, stopErr := l.handleTerminalStop(ctx, state, req, apiResp.StopReason, assistantMessage, toolUses)
			if stopErr != nil {
				l.setTransition(state, Terminate("stop_hook_error", types.StopReasonEndTurn))
				return RunResult{Messages: state.Messages, StopReason: types.StopReasonEndTurn, Iterations: state.Iterations, Error: stopErr, RecoveryContext: state.RecoveryContext, AutoCompactFailureCount: state.AutoCompactFailureCount}
			}
			if continued {
				continue
			}
			break
		}

		continued, noToolErr := l.handleNoToolUses(ctx, state, req, assistantMessage)
		if noToolErr != nil {
			l.setTransition(state, Terminate("stop_hook_error", types.StopReasonEndTurn))
			return RunResult{Messages: state.Messages, StopReason: types.StopReasonEndTurn, Iterations: state.Iterations, Error: noToolErr, RecoveryContext: state.RecoveryContext, AutoCompactFailureCount: state.AutoCompactFailureCount}
		}
		if continued {
			// Execute iteration continue hook
			l.executeHook(ctx, types.HookEventIterationContinue, map[string]any{
				"iteration":     state.Iterations,
				"continue_type": "no_tool_uses",
				"session_id":    req.SessionID,
				"turn_id":       req.TurnID,
			})
			continue
		}

		// Execute iteration complete hook
		l.executeHook(ctx, types.HookEventIterationComplete, map[string]any{
			"iteration":    state.Iterations,
			"session_id":   req.SessionID,
			"turn_id":      req.TurnID,
			"total_tokens": state.TotalTurnTokens,
		})

		state.StopReason = types.StopReasonEndTurn
		break
	}

	if state.StopReason == "" && state.Iterations >= l.config.MaxIterations {
		state.StopReason = types.StopReasonEndTurn
		l.setTransition(state, Terminate("max_iterations_reached", types.StopReasonEndTurn))
	}

	// Execute query complete hook
	l.executeHook(ctx, types.HookEventQueryComplete, map[string]any{
		"session_id":         req.SessionID,
		"turn_id":            req.TurnID,
		"total_iterations":   state.Iterations,
		"stop_reason":        state.StopReason,
		"total_tokens":       state.TotalTurnTokens,
		"compacted":          state.Compacted,
		"tool_uses_count":    len(state.ToolUses),
		"tool_results_count": len(state.ToolResults),
	})

	// Record query success in monitoring (assume success if we reach this point)
	if l.monitoring != nil {
		l.monitoring.RecordQuerySuccess(time.Since(startTime))
	}

	return RunResult{
		Messages:                state.Messages,
		StopReason:              terminalStopReason(state.StopReason),
		ToolUses:                state.ToolUses,
		ToolResults:             state.ToolResults,
		Usage:                   state.Usage,
		PermissionContext:       clonePermissionContext(state.PermissionContext),
		Compacted:               state.Compacted,
		CompactPreTokens:        state.CompactPreTokens,
		CompactPostTokens:       state.CompactPostTokens,
		Iterations:              state.Iterations,
		DiscoveredDeferred:      append([]string(nil), state.DiscoveredDeferred...),
		RecoveryContext:         state.RecoveryContext,
		AutoCompactFailureCount: state.AutoCompactFailureCount,
	}
}

// toolUsesOutcome tells Run's main loop what to do after one iteration's
// tool uses have been handled: return result immediately when non-nil,
// otherwise break the loop when shouldBreak, otherwise continue it.
type toolUsesOutcome struct {
	result      *RunResult
	shouldBreak bool
}

// handleToolUsesIteration runs the hooks, tool execution, and deferred-tool
// discovery for one iteration's tool uses - split out of Run to keep that
// function's own branch count down. req is a pointer because
// integrateDiscoveredDeferredTools mutates it (e.g. appending newly
// discovered deferred tools) and Run's own req must see that update on its
// next iteration; every other call here only reads req, so it gets *req.
func (l *Loop) handleToolUsesIteration(ctx context.Context, state *MutableState, req *RunRequest, toolUses []types.ToolUseContent) toolUsesOutcome {
	// Execute tool uses start hook
	if l.executeHookAndStop(ctx, state, types.HookEventToolUsesStart, map[string]any{
		"iteration":  state.Iterations,
		"tool_count": len(toolUses),
		"session_id": req.SessionID,
		"turn_id":    req.TurnID,
	}) {
		l.setTransition(state, Terminate("hook_stop", types.StopReasonEndTurn))
		return toolUsesOutcome{shouldBreak: true}
	}

	if l.applyPreToolUseHooks(ctx, state, *req, toolUses) {
		l.setTransition(state, Terminate("hook_stop", types.StopReasonEndTurn))
		return toolUsesOutcome{shouldBreak: true}
	}

	state.ToolUses = append(state.ToolUses, toolUses...)

	transcriptBeforeCurrentAction := append([]types.Message(nil), state.Messages[:len(state.Messages)-1]...)
	execResult, execErr := l.executeToolsForResponse(ctx, toolUses, *req, state, transcriptBeforeCurrentAction)

	if execErr != nil {
		l.setTransition(state, Terminate("tool_execution_error", types.StopReasonEndTurn))
		return toolUsesOutcome{result: &RunResult{Messages: state.Messages, StopReason: types.StopReasonEndTurn, Iterations: state.Iterations, Error: types.WrapError(types.ErrCodeToolExecution, "tool execution failed", execErr), RecoveryContext: state.RecoveryContext, AutoCompactFailureCount: state.AutoCompactFailureCount}}
	}

	state.PermissionContext = permissionContextFromToolContext(state.PermissionContext, execResult.FinalToolContext)
	if state.PermissionContext != nil {
		state.PermissionMode = state.PermissionContext.Mode
	}

	state.Messages = append(state.Messages, execResult.Messages...)
	state.ToolResults = append(state.ToolResults, execResult.Results...)

	// Execute post tool use hook. Fires after the merge above so a
	// hook that stops the turn here still leaves a well-formed
	// transcript - the tool already ran and its results are already
	// recorded (H4 in TODO.md).
	if l.applyPostToolUseHooks(ctx, state, *req, toolUses, execResult) {
		l.setTransition(state, Terminate("hook_stop", types.StopReasonEndTurn))
		return toolUsesOutcome{shouldBreak: true}
	}

	// Execute tool uses complete hook
	l.executeHook(ctx, types.HookEventToolUsesComplete, map[string]any{
		"iteration":      state.Iterations,
		"tool_count":     len(toolUses),
		"success_count":  len(execResult.Results),
		"error_count":    len(execResult.Errors),
		"session_id":     req.SessionID,
		"turn_id":        req.TurnID,
		"total_duration": execResult.TotalDuration,
	})

	discovery := l.integrateDiscoveredDeferredTools(ctx, state, req, toolUses, execResult.Results)
	if recoveryMessage := toolLoopRecoveryMessage(discovery); recoveryMessage != "" {
		state.Messages = append(state.Messages, newContinuationMessage(req.TurnID, recoveryMessage))
		state.ContinuationNudgeCount = 0
		state.MaxOutputTokensRecoveryCount = 0
		resetTaskNudge(state)
		l.setTransition(state, ContinueWithRecovery("tool_loop_recovery", RecoveryTypeContinuationNudge))
		return toolUsesOutcome{}
	}
	state.ContinuationNudgeCount = 0
	state.MaxOutputTokensRecoveryCount = 0
	l.setTransition(state, Continue("tool_results_appended"))
	return toolUsesOutcome{}
}

func (l *Loop) setTransition(state *MutableState, transition Transition) {
	state.Transition = transition
	if l.transitionObserver != nil {
		l.transitionObserver(transition)
	}

	// Capture recovery context for richer resume
	state.RecoveryContext = l.captureRecoveryContext(state, transition)
}

func (l *Loop) captureRecoveryContext(state *MutableState, transition Transition) *RecoveryContext {
	ctx := &RecoveryContext{
		TurnProgress: &TurnProgress{
			IterationsCompleted: state.Iterations,
			TotalTokensUsed:     state.TotalTurnTokens,
		},
	}

	// Extract details from transition
	if cont, ok := transition.(ContinueTransition); ok {
		ctx.LastTransitionReason = cont.Reason
		if cont.RecoveryType != "" {
			ctx.LastRecoveryType = cont.RecoveryType
		}
	} else if term, ok := transition.(TerminalTransition); ok {
		ctx.LastTransitionReason = term.Reason
		ctx.LastStopReason = state.StopReason
	}

	// Capture compaction snapshot if present in metadata
	if len(state.Messages) > 0 {
		lastMsg := state.Messages[len(state.Messages)-1]
		if lastMsg.Metadata != nil && lastMsg.Metadata.Compaction != nil {
			comp := lastMsg.Metadata.Compaction
			ctx.CompactionSnapshot = &CompactionSnapshot{
				PreCompactionTokenCount:  comp.PreCompactTokens,
				PostCompactionTokenCount: comp.PostCompactTokens,
				FirstPreservedMessageID:  comp.FirstPreservedMessageID,
				LastPreservedMessageID:   comp.LastPreservedMessageID,
				PreservedTailHash:        comp.PreservedTailHash,
				BoundaryVersion:          comp.BoundaryVersion,
			}
		}
	}

	// Capture last assistant message for resume
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == types.RoleAssistant {
			ctx.TurnProgress.LastAssistantMessageID = state.Messages[i].ID
			break
		}
	}

	// Capture pending tool uses/results
	ctx.TurnProgress.PendingToolUses = make([]types.ToolUseContent, len(state.ToolUses))
	copy(ctx.TurnProgress.PendingToolUses, state.ToolUses)
	ctx.TurnProgress.PendingToolResults = make([]tool.CallResult, len(state.ToolResults))
	copy(ctx.TurnProgress.PendingToolResults, state.ToolResults)

	return ctx
}

// shouldNudgeContinuation reports whether the loop should inject a continuation
// message after a response that used no tools.
//
// The nudge is warranted only when:
//   - the model already executed at least one tool this turn (priorToolUseCount > 0),
//     meaning it was in the middle of active work when it stopped, AND
//   - the response contains an explicit forward-looking signal that confirms the
//     model considers itself mid-work.
//
// When priorToolUseCount == 0 the model gave a direct answer with no tool use;
// nudging in that case produces spurious continuation loops on plain responses
// like "Let me know if you have questions."
func (l *Loop) shouldNudgeContinuation(message types.Message, priorToolUseCount int) bool {
	if priorToolUseCount == 0 {
		return false
	}

	text := assistantTextContent(message)
	if text == "" {
		return false
	}

	// Explicit finish signals suppress the nudge even when the model had tool uses.
	for _, done := range []string{
		"done", "finished", "complete", "all set", "that's all",
		"there you go", "let me know", "feel free", "hope that",
		"happy to", "if you need", "summary", "in summary",
	} {
		if strings.Contains(text, done) {
			return false
		}
	}

	// Conservative forward-looking signals: only nudge when the model explicitly
	// signals it was mid-work and intended to continue.
	for _, signal := range []string{
		// English
		"let me continue", "i'll continue", "i will continue",
		"next i'll", "next i will", "next step",
		"moving on", "continuing with", "proceeding to",
		"i'll now", "i will now",
		// French
		"je vais continuer", "je continue", "passons à", "ensuite je",
		"prochaine étape", "je vais maintenant",
		// Spanish
		"voy a continuar", "continuando con", "siguiente paso",
		"ahora voy a", "procedo a",
		// German
		"ich fahre fort", "weiter mit", "als nächstes", "ich werde nun",
		// Chinese (Simplified)
		"接下来", "继续", "下一步", "我将",
		// Japanese
		"次に", "続けて", "引き続き",
	} {
		if strings.Contains(text, signal) {
			return true
		}
	}

	return false
}

func assistantTextContent(message types.Message) string {
	if message.Role != types.RoleAssistant {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.Content {
		if text, ok := block.(types.TextContent); ok {
			trimmed := strings.TrimSpace(text.Text)
			if trimmed == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteString(" ")
			}
			builder.WriteString(strings.ToLower(trimmed))
		}
	}
	return builder.String()
}

func (l *Loop) prepareMessagesForIteration(state *MutableState) {
	if state == nil || len(state.Messages) == 0 {
		return
	}
	state.Messages = normalizeLeadingContinuationArtifacts(state.Messages)
}

func normalizeLeadingContinuationArtifacts(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return messages
	}
	trimmed := append([]types.Message(nil), messages...)
	for len(trimmed) > 1 && isEmptyContinuationArtifact(trimmed[len(trimmed)-1]) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

func isEmptyContinuationArtifact(message types.Message) bool {
	if message.Role != types.RoleUser {
		return false
	}
	if len(message.Content) == 0 {
		return true
	}
	for _, block := range message.Content {
		switch content := block.(type) {
		case types.TextContent:
			if strings.TrimSpace(content.Text) != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func flattenMessageText(message types.Message) string {
	var builder strings.Builder
	for _, block := range message.Content {
		if text, ok := block.(types.TextContent); ok {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

// resolveToolsets evaluates every Toolset in req and merges the returned tools
// into req.Tools. Toolset-provided tools take precedence over statically
// registered ones, allowing context-aware overrides per iteration.
// Provider tool definitions are rebuilt when any toolset returns tools.
func (l *Loop) resolveToolsets(ctx context.Context, req *RunRequest) {
	if len(req.Toolsets) == 0 {
		return
	}
	added := false
	if req.Tools == nil {
		req.Tools = make(map[string]tool.Tool)
	}
	for _, ts := range req.Toolsets {
		for _, t := range ts.Tools(ctx) {
			if t == nil || !t.IsEnabled() {
				continue
			}
			req.Tools[t.Definition().Name] = t
			added = true
		}
	}
	if added {
		req.ProviderTools = prompt.BuildProviderToolDefinitions(req.Tools)
	}
}

func (l *Loop) initializeState(req RunRequest) *MutableState {
	state := NewMutableState(req.Messages)
	state.PermissionContext = clonePermissionContext(req.PermissionContext)
	state.PermissionMode = req.PermissionMode
	state.Compacted = false
	state.CompactPreTokens = 0
	state.CompactPostTokens = 0
	// Seed from the session's cross-turn count instead of leaving the
	// zero value, otherwise the auto-compact circuit breaker can never
	// accumulate failures past one turn (see compact.Engine.AutoCompact).
	state.AutoCompactFailureCount = req.AutoCompactFailureCount
	return state
}

func (l *Loop) maybeAutoCompact(ctx context.Context, state *MutableState, req RunRequest) error {
	if !l.config.AutoCompact {
		return nil
	}

	hookDenied := false
	if preResult := l.executeHookWithResult(ctx, types.HookEventPreCompact, map[string]any{
		"session_id": req.SessionID,
		"turn_id":    req.TurnID,
	}); preResult != nil && (preResult.Action == types.HookActionStop || preResult.Action == types.HookActionDeny) {
		// A hook denied this round's compaction attempt - not a failure,
		// just skip it this turn, exactly like ShouldCompact deciding
		// compaction isn't needed yet (H4 in TODO.md). Still falls through to
		// the blocking-limit check below (M1): a denied compaction must not
		// let an oversized request reach the provider silently.
		appendSystemNoteFromHook(state, types.HookEventPreCompact, preResult.Message)
		hookDenied = true
	}

	if !hookDenied {
		l.emitCompacting(ctx, state, req)
		result, err := l.compactor.AutoCompact(
			ctx,
			req.SystemPrompt,
			state.Messages,
			req.Model,
			req.SessionID,
			req.TurnID,
			compactionTracking(state, req),
		)
		if err != nil {
			state.AutoCompactFailureCount = result.ConsecutiveFailures
			return fmt.Errorf("auto-compact failed: %w", err)
		}
		state.AutoCompactFailureCount = result.ConsecutiveFailures
		if result.DidCompact {
			state.Messages = result.Messages
			state.Compacted = true
			state.CompactPreTokens = result.PreCompactTokens
			state.CompactPostTokens = result.PostCompactTokens
			// Compaction rewrote Messages, so any previously recorded Codex
			// continuation reference no longer describes a valid prefix of it -
			// see recordPreviousResponse/buildAPIRequest.
			state.PreviousResponseID = ""
			state.PreviousResponseMessageCount = 0
			// The provider's figure described the messages that were just replaced.
			state.UsageAnchor = nil
			l.emitCompacted(ctx, req, result.PreCompactTokens, result.PostCompactTokens)
		}
		// Post-compact notification, like every other *_complete hook in this
		// file: fire-and-forget, not stop-capable (there is nothing left to gate
		// once compaction already ran).
		l.executeHook(ctx, types.HookEventPostCompact, map[string]any{
			"session_id":  req.SessionID,
			"turn_id":     req.TurnID,
			"did_compact": result.DidCompact,
			"pre_tokens":  result.PreCompactTokens,
			"post_tokens": result.PostCompactTokens,
		})
	}

	// Even after compaction, the request may still not fit (e.g. a single
	// huge tool result, or compaction itself failing to shrink enough).
	// Fail locally with a typed error instead of letting the provider reject
	// an oversized request (M1 in TODO.md).
	if err := l.compactor.CheckBlockingLimit(req.SystemPrompt, state.Messages, req.Model, compactionTracking(state, req)); err != nil {
		return err
	}
	return nil
}

// buildChunkCallback returns a combined callback that forwards each streaming
// chunk to both the host callback (if set) and the EventQueue (if set).
func buildChunkCallback(cb func(types.APIResponseChunk), queue *execution.EventQueue) func(types.APIResponseChunk) {
	if cb == nil && queue == nil {
		return nil
	}
	return func(chunk types.APIResponseChunk) {
		if cb != nil {
			cb(chunk)
		}
		if queue != nil {
			queue.Emit(chunk)
		}
	}
}

func (l *Loop) callModel(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
	if l.callModelFn != nil {
		return l.callModelFn(ctx, state, req)
	}
	model := l.getFallbackModel(req.Model)
	chunkFn := buildChunkCallback(req.ResponseChunkCallback, req.EventQueue)
	l.activeStreamingTools.discard() // release any goroutines from a previous iteration
	l.activeStreamingTools = nil
	if l.config.EnableStreaming {
		if coordinator := newStreamingToolCoordinator(ctx, l, req, state, append([]types.Message(nil), state.Messages...)); coordinator != nil {
			l.activeStreamingTools = coordinator
			baseChunkFn := chunkFn
			chunkFn = func(chunk types.APIResponseChunk) {
				if baseChunkFn != nil {
					baseChunkFn(chunk)
				}
				coordinator.observe(chunk)
			}
		}
	}
	resp, err := l.sendAPIRequestWithClient(ctx, l.effectiveAPIClient(), l.buildAPIRequest(state, req, model), chunkFn)
	if err != nil {
		// Discard any in-flight pre-streamed tool goroutines before falling back.
		l.activeStreamingTools.discard()
		l.activeStreamingTools = nil
		return l.tryFallbackModel(ctx, state, req, err, model)
	}
	if breaker := l.circuitBreaker(); breaker != nil {
		breaker.RecordSuccess()
	}
	l.recordPreviousResponse(state, model, resp)
	return resp, nil
}

// getFallbackModel returns the current fallback model or the original model
func (l *Loop) getFallbackModel(original types.ModelIdentifier) types.ModelIdentifier {
	base := original
	if l.activeBaseModel != nil {
		base = *l.activeBaseModel
	}
	providerConfig := l.effectiveProviderConfig()
	if providerConfig == nil || providerConfig.Routing == nil {
		return base
	}
	if l.currentFallbackIndex >= 0 && l.currentFallbackIndex < len(providerConfig.Routing.FallbackModels) {
		return providerConfig.Routing.FallbackModels[l.currentFallbackIndex]
	}
	return base
}

// tryFallbackModel attempts to use a fallback model if available.
func (l *Loop) tryFallbackModel(
	ctx context.Context,
	state *MutableState,
	req RunRequest,
	err error,
	attemptedModel types.ModelIdentifier,
) (*types.APIResponse, error) {
	providerConfig := l.effectiveProviderConfig()
	if err == nil || providerConfig == nil || providerConfig.Routing == nil {
		return nil, err
	}

	breaker := l.circuitBreaker()
	if breaker != nil {
		breaker.RecordFailure()
		if !breaker.IsAvailable() {
			return nil, err
		}
	}

	if !l.isRecoverableError(err) {
		return nil, err
	}

	lastErr := err
	chunkFn := buildChunkCallback(req.ResponseChunkCallback, req.EventQueue)
	fallbacks := providerConfig.Routing.FallbackModels
	for idx, candidate := range fallbacks {
		if idx <= l.currentFallbackIndex || sameModelIdentifier(candidate, attemptedModel) {
			continue
		}

		resp, fallbackErr := l.sendAPIRequestWithClient(ctx, l.effectiveAPIClient(), l.buildAPIRequest(state, req, candidate), chunkFn)
		if fallbackErr == nil {
			l.currentFallbackIndex = idx
			if breaker != nil {
				breaker.RecordSuccess()
			}
			l.recordPreviousResponse(state, candidate, resp)
			return resp, nil
		}

		lastErr = fallbackErr
		if breaker != nil {
			breaker.RecordFailure()
		}
		if !l.isRecoverableError(fallbackErr) {
			return nil, lastErr
		}
	}

	providerResp, providerErr := l.tryFallbackProvider(ctx, state, req, lastErr)
	if providerErr == nil {
		if breaker != nil {
			breaker.RecordSuccess()
		}
		return providerResp, nil
	}

	lastErr = providerErr
	return nil, lastErr
}

func (l *Loop) buildAPIRequest(state *MutableState, req RunRequest, model types.ModelIdentifier) types.APIRequest {
	apiReq := types.APIRequest{
		Model:              model,
		Messages:           state.Messages,
		MaxTokens:          req.MaxTokens,
		SystemPrompt:       req.SystemPrompt,
		SystemPromptBlocks: append([]types.SystemPromptBlock(nil), req.SystemPromptBlocks...),
		Tools:              append([]types.APIToolDefinition(nil), req.ProviderTools...),
		Stream:             l.config.EnableStreaming,
		OutputSchema:       req.OutputSchema,
	}
	// Only offer the continuation hint to the same provider that produced
	// it, and only while the message count it was recorded against still
	// matches what's about to be sent (see recordPreviousResponse and
	// maybeAutoCompact's invalidation) - a mismatch means either compaction
	// rewrote history or a different provider answered in between, and the
	// stored reference no longer describes a valid prefix of Messages.
	if state.PreviousResponseID != "" &&
		model.Provider == types.APIProviderCodex &&
		state.PreviousResponseMessageCount <= len(state.Messages) {
		apiReq.PreviousResponseID = state.PreviousResponseID
		apiReq.PreviousResponseMessageCount = state.PreviousResponseMessageCount
	}
	return apiReq
}

// recordPreviousResponse used to update state's continuation hint after a
// successful Codex call, so the next iteration could send only the message
// delta via previous_response_id instead of the whole growing history.
// Disabled: confirmed live against a real ChatGPT-account session, the
// chatgpt.com/backend-api/codex backend requires "store": false on every
// request (see client_codex.go's buildCodexRequestBody), and
// previous_response_id can only ever reference a response the API actually
// stored - so this continuation hint could never be validly redeemed
// server-side to begin with. Left as a no-op (never populates
// state.PreviousResponseID) rather than removing the surrounding
// plumbing (APIRequest/MutableState fields, buildAPIRequest's read side),
// in case a store-compatible path for this backend is found later.
func (l *Loop) recordPreviousResponse(state *MutableState, _ types.ModelIdentifier, _ *types.APIResponse) {
	state.PreviousResponseID = ""
	state.PreviousResponseMessageCount = 0
}

func (l *Loop) sendAPIRequestWithClient(ctx context.Context, client *providers.Client, apiReq types.APIRequest, onChunk func(types.APIResponseChunk)) (*types.APIResponse, error) {
	if l.config.EnableStreaming {
		if l.sendAPIStreamRequestFn != nil {
			return l.sendAPIStreamRequestFn(ctx, apiReq, onChunk)
		}
		if l.sendAPIRequestFn != nil {
			return l.sendAPIRequestFn(ctx, apiReq)
		}
		if client == nil {
			return nil, types.NewError(types.ErrCodeAPIRequest, "api client not configured")
		}
		streamResult, err := client.CreateMessageStreamResultWithCallback(ctx, apiReq, onChunk)
		if err != nil {
			return nil, err
		}
		return &streamResult.Response, nil
	}
	if l.sendAPIRequestFn != nil {
		return l.sendAPIRequestFn(ctx, apiReq)
	}
	if client == nil {
		return nil, types.NewError(types.ErrCodeAPIRequest, "api client not configured")
	}
	return client.CreateMessage(ctx, apiReq)
}

func (l *Loop) effectiveAPIClient() *providers.Client {
	if l.activeFallbackClient != nil {
		return l.activeFallbackClient
	}
	return l.apiClient
}

func (l *Loop) effectiveProviderConfig() *providers.Config {
	if l.activeFallbackClient != nil {
		return l.activeFallbackClient.Config()
	}
	return l.providerConfig
}

func (l *Loop) circuitBreaker() *providers.CircuitBreakerConfig {
	providerConfig := l.effectiveProviderConfig()
	if providerConfig == nil || providerConfig.Routing == nil {
		return nil
	}
	return providerConfig.Routing.CircuitBreaker
}

func sameModelIdentifier(left, right types.ModelIdentifier) bool {
	return left.Provider == right.Provider && left.Model == right.Model && left.Version == right.Version
}

func (l *Loop) tryFallbackProvider(
	ctx context.Context,
	state *MutableState,
	req RunRequest,
	lastErr error,
) (*types.APIResponse, error) {
	providerConfig := l.effectiveProviderConfig()
	if providerConfig == nil || providerConfig.Routing == nil || len(providerConfig.Routing.FallbackProviders) == 0 {
		return nil, lastErr
	}

	chunkFn := buildChunkCallback(req.ResponseChunkCallback, req.EventQueue)
	for _, fallbackProvider := range providerConfig.Routing.FallbackProviders {
		baseModel, ok := providers.DefaultModelIdentifier(fallbackProvider)
		if !ok {
			continue
		}

		fallbackClient, err := providers.NewFallbackClient(ctx, fallbackProvider)
		if err != nil {
			lastErr = err
			continue
		}
		if l.monitoring != nil {
			fallbackClient.SetMonitoring(l.monitoring)
		}

		resp, fallbackErr := l.sendAPIRequestWithClient(ctx, fallbackClient, l.buildAPIRequest(state, req, baseModel), chunkFn)
		if fallbackErr == nil {
			l.activeFallbackClient = fallbackClient
			l.activeBaseModel = &baseModel
			l.currentFallbackIndex = -1
			return resp, nil
		}

		lastErr = fallbackErr
		if !l.isRecoverableError(fallbackErr) {
			return nil, lastErr
		}
	}

	return nil, lastErr
}

func (l *Loop) shouldStop(stopReason string) bool {
	return stopReason == types.StopReasonEndTurn || stopReason == types.StopReasonMaxTokens || stopReason == types.StopReasonStopSequence
}

func (l *Loop) extractToolUses(content []types.ContentBlock) []types.ToolUseContent {
	toolUses := make([]types.ToolUseContent, 0)
	for _, block := range content {
		if toolUse, ok := block.(types.ToolUseContent); ok {
			toolUses = append(toolUses, toolUse)
		}
	}
	return toolUses
}

func (l *Loop) integrateDiscoveredDeferredTools(ctx context.Context, state *MutableState, req *RunRequest, toolUses []types.ToolUseContent, results []tool.CallResult) toolDiscoveryUpdate {
	update := toolDiscoveryUpdate{}
	if state == nil || req == nil || req.ToolRegistry == nil || len(toolUses) == 0 || len(results) == 0 {
		return update
	}

	discovery := extractDiscoveredDeferredToolNames(toolUses, results)
	update.HadToolSearch = discovery.HadToolSearch
	update.Malformed = discovery.Malformed
	discovered := discovery.Names
	if len(discovered) == 0 {
		return update
	}
	if req.Tools == nil {
		req.Tools = make(map[string]tool.Tool)
	}

	seen := make(map[string]bool, len(state.DiscoveredDeferred))
	for _, name := range state.DiscoveredDeferred {
		seen[name] = true
	}

	// Only what tool_search could have offered this agent may be loaded: the registry's deferred tools, and the main
	// agent's own scope. A name that is neither never reaches the request, whatever the model asked for.
	findable := make(map[string]bool)
	for _, deferred := range req.ToolRegistry.ListDeferred() {
		if deferred != nil {
			findable[deferred.Definition().Name] = true
		}
	}
	for _, name := range scopedDeferredNames(req.ToolRegistry, req.DeferredScope) {
		findable[name] = true
	}

	added := false
	for _, name := range discovered {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if !findable[name] {
			update.Unresolved = append(update.Unresolved, name)
			continue
		}
		resolved, ok := req.ToolRegistry.Resolve(name)
		if !ok {
			update.Unresolved = append(update.Unresolved, name)
			continue
		}
		req.Tools[name] = resolved
		seen[name] = true
		state.DiscoveredDeferred = append(state.DiscoveredDeferred, name)
		update.Discovered = append(update.Discovered, name)
		added = true
	}

	if !added {
		sort.Strings(update.Unresolved)
		return update
	}

	sort.Strings(state.DiscoveredDeferred)
	sort.Strings(update.Discovered)
	sort.Strings(update.Unresolved)
	req.ProviderTools = prompt.BuildProviderToolDefinitions(req.Tools)
	if req.RefreshSystemPrompt != nil {
		refresh, err := req.RefreshSystemPrompt(ctx, req.Tools, remainingDeferredToolNames(req.ToolRegistry, state.DiscoveredDeferred, req.DeferredScope), state.CurrentStage)
		if err != nil {
			update.RefreshFailed = err
			return update
		}
		req.SystemPrompt = refresh.SystemPrompt
		req.SystemPromptBlocks = append([]types.SystemPromptBlock(nil), refresh.SystemPromptBlocks...)
	}
	return update
}

func remainingDeferredToolNames(reg *tool.Registry, discovered []string, scope []string) []string {
	if reg == nil {
		return nil
	}
	seen := make(map[string]bool, len(discovered))
	for _, name := range discovered {
		seen[name] = true
	}
	deferred := reg.ListDeferred()
	names := make([]string, 0, len(deferred))
	unique := make(map[string]bool, len(deferred))
	for _, deferredTool := range deferred {
		if deferredTool == nil {
			continue
		}
		name := deferredTool.Definition().Name
		if name == "" || seen[name] || unique[name] {
			continue
		}
		unique[name] = true
		names = append(names, name)
	}
	for _, name := range scopedDeferredNames(reg, scope) {
		if seen[name] || unique[name] {
			continue
		}
		unique[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// scopedDeferredNames returns the names of the registry's enabled tools that the host listed as findable by the main
// agent (Config.MainAgentDeferredTools). A name the registry does not know, or whose tool is disabled, is left out.
func scopedDeferredNames(reg *tool.Registry, scope []string) []string {
	if reg == nil || len(scope) == 0 {
		return nil
	}
	names := make([]string, 0, len(scope))
	for _, name := range scope {
		resolved, ok := reg.Resolve(name)
		if !ok || resolved == nil || !resolved.IsEnabled() {
			continue
		}
		names = append(names, resolved.Definition().Name)
	}
	return names
}

type toolSearchDiscovery struct {
	HadToolSearch bool
	Names         []string
	Malformed     bool
}

func extractDiscoveredDeferredToolNames(toolUses []types.ToolUseContent, results []tool.CallResult) toolSearchDiscovery {
	discovery := toolSearchDiscovery{}
	if len(toolUses) == 0 || len(results) == 0 {
		return discovery
	}

	seen := make(map[string]bool)
	names := make([]string, 0)
	for i, toolUse := range toolUses {
		if toolUse.Name != toolsearch.ToolSearchToolName || i >= len(results) {
			continue
		}
		discovery.HadToolSearch = true
		resultNames, valid := toolSearchMatches(results[i])
		if !valid {
			discovery.Malformed = true
			continue
		}
		for _, name := range resultNames {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	discovery.Names = names
	return discovery
}

func toolSearchMatches(result tool.CallResult) ([]string, bool) {
	if result.IsError() {
		return nil, true
	}
	if result.ContentType == tool.ContentTypeText {
		return nil, true
	}
	switch data := result.Data.(type) {
	case toolsearch.ToolSearchOutput:
		return matchNames(data.Matches), true
	case *toolsearch.ToolSearchOutput:
		if data == nil {
			return nil, false
		}
		return matchNames(data.Matches), true
	case map[string]any:
		names, ok := toolSearchMatchesFromMap(data)
		return names, ok
	default:
		return nil, false
	}
}

// matchNames extracts tool names from a []SearchMatch slice.
func matchNames(matches []toolsearch.SearchMatch) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	return names
}

func toolSearchMatchesFromMap(data map[string]any) ([]string, bool) {
	raw, ok := data["matches"]
	if !ok {
		return nil, false
	}

	switch matches := raw.(type) {
	case []string:
		return append([]string(nil), matches...), true
	case []any:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			name, ok := match.(string)
			if !ok {
				continue
			}
			names = append(names, name)
		}
		return names, true
	default:
		return nil, false
	}
}

func toolLoopRecoveryMessage(update toolDiscoveryUpdate) string {
	if update.RefreshFailed != nil && len(update.Discovered) > 0 {
		return fmt.Sprintf(
			"ToolSearch completed and these tools are now callable: %s. The runtime could not refresh the deferred-tool prompt, so continue using the updated tool surface directly without re-running ToolSearch.",
			strings.Join(update.Discovered, ", "),
		)
	}
	if !update.HadToolSearch {
		return ""
	}
	if update.Malformed {
		return "ToolSearch returned a non-usable discovery payload. If you still need a deferred tool, retry ToolSearch with a narrower `select:<tool_name>` query or continue with the current callable tools."
	}
	if len(update.Unresolved) > 0 && len(update.Discovered) == 0 {
		return fmt.Sprintf(
			"ToolSearch returned tool names that are not callable in this runtime: %s. Retry ToolSearch with a narrower query or continue with the current callable tools.",
			strings.Join(update.Unresolved, ", "),
		)
	}
	if len(update.Unresolved) > 0 {
		return fmt.Sprintf(
			"Some ToolSearch matches were not callable in this runtime: %s. Continue with the discovered callable tools or retry ToolSearch if you still need the unresolved ones.",
			strings.Join(update.Unresolved, ", "),
		)
	}
	return ""
}

func (l *Loop) executeTools(ctx context.Context, toolUses []types.ToolUseContent, req RunRequest, state *MutableState, transcript []types.Message) (execution.ExecuteResult, error) {
	return l.orchestrator.Execute(ctx, l.buildExecuteRequest(ctx, toolUses, req, state, transcript))
}

func (l *Loop) isRecoverableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var engineErr *types.EngineError
	if errors.As(err, &engineErr) {
		if engineErr.IsRetryable() {
			return true
		}
		// Every provider's stream parser wraps body-read/decode I/O errors as
		// ErrCodeAPIResponse, whether the cause was a transient dropped
		// connection or a genuinely malformed payload. IsRetryable() treats
		// that code as permanent, so a mid-stream socket death (the incident
		// that motivated this check) was never retried. Fall through to the
		// structured network classifier on the wrapped cause instead of
		// trusting the generic code alone.
		if engineErr.Code == types.ErrCodeAPIResponse && engineErr.Err != nil {
			return l.isRecoverableNetworkError(engineErr.Err)
		}
		return false
	}
	// For non-EngineErrors (network errors, etc.) use the structured classifier
	// rather than brittle string matching.
	return l.isRecoverableNetworkError(err)
}

func (l *Loop) isRecoverableNetworkError(err error) bool {
	switch providerretry.ClassifyHTTPError(err, 0) {
	case providerretry.RetryClassificationClientError,
		providerretry.RetryClassificationAuthError:
		return false
	default:
		return true
	}
}

func normalizeRunRequestPermissionContext(req RunRequest) RunRequest {
	if req.PermissionContext == nil {
		req.PermissionContext = &types.PermissionContext{
			Mode:                             req.PermissionMode,
			IsBypassPermissionsModeAvailable: req.PermissionMode == types.PermissionModeBypass,
			IsAutoModeAvailable:              req.PermissionMode == types.PermissionModeAuto,
		}
	}
	if req.PermissionContext.Mode == "" {
		req.PermissionContext.Mode = req.PermissionMode
	}
	if req.PermissionContext.Mode == "" {
		req.PermissionContext.Mode = types.PermissionModeOnRequest
	}
	if req.PermissionContext.Mode == types.PermissionModeBypass {
		req.PermissionContext.IsBypassPermissionsModeAvailable = true
	}
	req.PermissionContext.NormalizeLegacyPlanMode()
	req.PermissionMode = req.PermissionContext.Mode
	return req
}

func (l *Loop) tryRecovery(ctx context.Context, state *MutableState, req RunRequest, err error) (bool, error) {
	if state.MaxOutputTokensRecoveryCount >= l.config.MaxOutputTokensRecoveryLimit {
		return false, fmt.Errorf(
			"api retry recovery limit exceeded after %d attempts: %w",
			state.MaxOutputTokensRecoveryCount,
			err,
		)
	}

	nextAttempt := state.MaxOutputTokensRecoveryCount + 1
	if limit := l.config.MaxRecoveryAttempts; limit > 0 && nextAttempt > limit {
		return false, fmt.Errorf(
			"api retry recovery limit exceeded after %d attempts: %w",
			limit,
			err,
		)
	}

	if err := l.waitRecoveryBackoff(ctx, nextAttempt); err != nil {
		return false, err
	}

	state.MaxOutputTokensRecoveryCount++
	l.setTransition(state, ContinueWithRecovery(l.recoveryLabel(err), RecoveryTypeAPIRetry))
	return true, nil
}

// recoveryLabel returns a stable label for the recovery transition based on the
// structured error code. Falls back to a generic label for non-EngineErrors
// (network errors, context cancellation, etc.) so the transition log stays
// meaningful without relying on error message strings.
func (l *Loop) recoveryLabel(err error) string {
	var engineErr *types.EngineError
	if errors.As(err, &engineErr) {
		switch engineErr.Code {
		case types.ErrCodeAPIRateLimit:
			return "recoverable_rate_limit"
		case types.ErrCodeAPITimeout:
			return "recoverable_timeout"
		case types.ErrCodeAPIResponse:
			return "recoverable_api_response"
		default:
			return "recoverable_api_retry"
		}
	}
	return "recoverable_network"
}

// defaultRecoveryBackoffMultiplierMs and defaultRecoveryBackoffMaxDelayMs are
// the fallbacks used when the corresponding LoopConfig field is unset (<=0).
const (
	defaultRecoveryBackoffMultiplierMs = 250
	defaultRecoveryBackoffMaxDelayMs   = 30000 // 30s
)

// computeRecoveryBackoff returns the delay before recovery attempt N
// (1-indexed). Exponential (doubling each attempt, capped at maxDelayMs)
// rather than linear (multiplierMs*attempt, as this used to be), plus up to
// 25% jitter, so many sessions/jobs hitting the same upstream outage don't
// retry in lockstep - a synchronized "thundering herd" against an
// already-struggling provider. Idea from studying neul-labs/m9m (MIT),
// which does the same for its own retry policy. Pulled out of
// waitRecoveryBackoff as a pure function so the growth/cap/jitter math is
// unit-testable without a real timer.
func computeRecoveryBackoff(attempt, multiplierMs, maxDelayMs int) time.Duration {
	if multiplierMs <= 0 {
		multiplierMs = defaultRecoveryBackoffMultiplierMs
	}
	if maxDelayMs <= 0 {
		maxDelayMs = defaultRecoveryBackoffMaxDelayMs
	}
	delayMs := float64(multiplierMs) * math.Pow(2, float64(attempt-1))
	if delayMs > float64(maxDelayMs) {
		delayMs = float64(maxDelayMs)
	}
	delayMs += delayMs * 0.25 * rand.Float64()
	return time.Duration(delayMs) * time.Millisecond
}

func (l *Loop) waitRecoveryBackoff(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	delay := computeRecoveryBackoff(attempt, l.config.RecoveryBackoffMultiplier, l.config.RecoveryBackoffMaxDelay)
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *Loop) updateUsage(state *MutableState, usage *types.TokenUsage) {
	if usage == nil {
		return
	}
	state.Usage = usage
	state.TotalTurnTokens += usage.InputTokens + usage.OutputTokens
}

func (l *Loop) handleTerminalStop(ctx context.Context, state *MutableState, req RunRequest, stopReason string, assistantMessage types.Message, toolUses []types.ToolUseContent) (bool, error) {
	if stopReason == types.StopReasonMaxTokens && state.MaxOutputTokensRecoveryCount < l.config.MaxOutputTokensRecoveryLimit {
		state.Messages = append(state.Messages, newContinuationMessage(req.TurnID, "Output token limit hit. Resume directly without apology or recap. Continue the unfinished work in smaller pieces."))
		state.MaxOutputTokensRecoveryCount++
		l.setTransition(state, ContinueWithRecovery("max_output_tokens_recovery", RecoveryTypeMaxOutputTokens))
		return true, nil
	}
	if len(toolUses) == 0 {
		if decision := l.maybeContinueForBudget(state, req, assistantMessage); decision.continueLoop {
			state.Messages = append(state.Messages, newContinuationMessage(req.TurnID, decision.nudgeMessage))
			l.setTransition(state, ContinueWithRecovery("token_budget_continuation", RecoveryTypeTokenBudget))
			return true, nil
		}
	}
	stopHookResult, err := l.runStopHooks(ctx, state, req, terminalStopReason(stopReason))
	if err != nil {
		return false, err
	}
	appendHookMessages(state, stopHookResult, req.TurnID)
	if stopHookResult.Continue {
		l.setTransition(state, ContinueWithRecovery("stop_hook_continue", RecoveryTypeStopHook))
		return true, nil
	}
	state.StopReason = terminalStopReason(stopReason)
	l.setTransition(state, Terminate("stop_reason_terminal", state.StopReason))
	return false, nil
}

func (l *Loop) handleNoToolUses(ctx context.Context, state *MutableState, req RunRequest, assistantMessage types.Message) (bool, error) {
	if l.shouldNudgeContinuation(assistantMessage, len(state.ToolUses)) && state.ContinuationNudgeCount < l.config.ContinuationNudgeLimit {
		state.Messages = append(state.Messages, newContinuationMessage(req.TurnID, "Continue with the task. Use the appropriate tools to proceed."))
		state.ContinuationNudgeCount++
		l.setTransition(state, ContinueWithRecovery("continuation_nudge", RecoveryTypeContinuationNudge))
		return true, nil
	}
	if decision := l.maybeContinueForBudget(state, req, assistantMessage); decision.continueLoop {
		state.Messages = append(state.Messages, newContinuationMessage(req.TurnID, decision.nudgeMessage))
		l.setTransition(state, ContinueWithRecovery("token_budget_continuation", RecoveryTypeTokenBudget))
		return true, nil
	}
	// The model is about to end the turn: if its own task list still has
	// unfinished items, send it back to work instead. This is the enforcement
	// half of the tracked list, and it sits after the token-budget decision
	// because a budget stop is a deliberate stop.
	if decision := l.maybeNudgeForOpenTasks(ctx, state, req); decision.continueLoop {
		state.Messages = append(state.Messages, newContinuationMessage(req.TurnID, decision.nudgeMessage))
		l.setTransition(state, ContinueWithRecovery("open_tasks_continuation", RecoveryTypeContinuationNudge))
		return true, nil
	}
	stopHookResult, err := l.runStopHooks(ctx, state, req, types.StopReasonEndTurn)
	if err != nil {
		return false, err
	}
	appendHookMessages(state, stopHookResult, req.TurnID)
	if stopHookResult.Continue {
		l.setTransition(state, ContinueWithRecovery("stop_hook_continue", RecoveryTypeStopHook))
		return true, nil
	}
	state.StopReason = types.StopReasonEndTurn
	l.setTransition(state, Terminate("no_tool_uses", types.StopReasonEndTurn))
	return false, nil
}

func buildAssistantMessage(apiResp *types.APIResponse, turnID types.TurnID, msgIndex int) types.Message {
	msg := types.AssistantMessage(fmt.Sprintf("msg-%d", msgIndex+1), apiResp.Content)
	msg.Metadata = &types.MessageMetadata{
		TurnID:     turnID.String(),
		StopReason: apiResp.StopReason,
		Usage:      &apiResp.Usage,
	}
	return msg
}

func newContinuationMessage(turnID types.TurnID, content string) types.Message {
	msg := types.UserMessage(fmt.Sprintf("msg-%d", GetCurrentTime().UnixNano()), content)
	msg.Metadata = &types.MessageMetadata{TurnID: turnID.String()}
	return msg
}

func appendHookMessages(state *MutableState, result StopHookResult, turnID types.TurnID) {
	for _, msg := range result.Messages {
		if msg.Metadata == nil {
			msg.Metadata = &types.MessageMetadata{TurnID: turnID.String()}
		}
		state.Messages = append(state.Messages, msg)
	}
}

func terminalStopReason(stopReason string) string {
	if stopReason == "" {
		return types.StopReasonEndTurn
	}
	return stopReason
}

// GetCurrentTime returns the current time (overridable for testing).
var GetCurrentTime = func() time.Time {
	return time.Now()
}

// executeHook executes lifecycle hooks for an event.
// Returns true if any hook requested Stop or Deny (early termination).
// This method is safe to call even if hookExecutor is nil.
func (l *Loop) executeHook(ctx context.Context, event types.HookEvent, data map[string]any) bool {
	result := l.executeHookWithResult(ctx, event, data)
	return result != nil && (result.Action == types.HookActionStop || result.Action == types.HookActionDeny)
}

// executeHookWithResult executes hooks and returns the first actionable result.
// Returns nil if no hook requested a non-continue action or hookExecutor is nil.
// Callers that need Modify data (e.g. UpdatedInput) should use this method.
func (l *Loop) executeHookWithResult(ctx context.Context, event types.HookEvent, data map[string]any) *types.HookResult {
	if l.hookExecutor == nil {
		return nil
	}
	result := l.hookExecutor.ExecuteFirst(ctx, event, data)
	if result == nil || result.Action == types.HookActionContinue || result.Action == "" {
		return nil
	}
	switch result.Action {
	case types.HookActionStop, types.HookActionDeny:
		slog.Warn("hook blocked execution", "event", event, "action", result.Action, "message", result.Message)
	case types.HookActionModify:
		slog.Debug("hook modified execution data", "event", event, "keys", len(result.UpdatedInput))
	case types.HookActionRetry:
		// Retry is intentionally not implemented: nothing re-runs the tool
		// call or API call this refers to. Logged, not silently dropped, so
		// it is visible that a hook's retry request had no effect (H4 in
		// TODO.md - implementing it safely needs a rework of the batched
		// tool-execution pipeline in streaming_tools.go, out of scope here).
		slog.Warn("hook requested retry, which is not implemented - the turn proceeds as if it returned continue", "event", event, "message", result.Message)
	}
	return result
}

// appendSystemNoteFromHook appends message (if non-empty) to state.Messages
// as a system note attributed to event, so a hook's Stop/Deny reason
// actually reaches the model/transcript instead of only being logged (H4 in
// TODO.md).
func appendSystemNoteFromHook(state *MutableState, event types.HookEvent, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	id := fmt.Sprintf("hook_denied_%s_%d", event, state.Iterations)
	state.Messages = append(state.Messages, types.SystemMessage(id, message))
}

// appendHookDenialAndStop reports whether result is a Stop/Deny action,
// first appending its Message via appendSystemNoteFromHook if so.
func appendHookDenialAndStop(state *MutableState, event types.HookEvent, result *types.HookResult) bool {
	if result == nil || (result.Action != types.HookActionStop && result.Action != types.HookActionDeny) {
		return false
	}
	appendSystemNoteFromHook(state, event, result.Message)
	return true
}

// executeHookAndStop runs event through executeHookWithResult and reports
// whether the turn should stop, via appendHookDenialAndStop. Returns false
// for every outcome but Stop/Deny, including a nil hookExecutor.
func (l *Loop) executeHookAndStop(ctx context.Context, state *MutableState, event types.HookEvent, data map[string]any) bool {
	return appendHookDenialAndStop(state, event, l.executeHookWithResult(ctx, event, data))
}

// applyPreToolUseHooks runs the pre_tool_use hook for each tool call about to
// execute. A Modify result rewrites that tool's Input in place before
// execution - the orchestrator sees the modified input, though the assistant
// message already recorded in state.Messages is not retroactively rewritten,
// the same convention permission-based UpdatedInput already follows
// elsewhere in this loop. A Stop/Deny result ends the whole turn, like every
// other stop-capable hook in this file: there is no narrower "skip only this
// tool" primitive. Returns true if the turn should stop.
func (l *Loop) applyPreToolUseHooks(ctx context.Context, state *MutableState, req RunRequest, toolUses []types.ToolUseContent) bool {
	for i := range toolUses {
		result := l.executeHookWithResult(ctx, types.HookEventPreToolUse, map[string]any{
			"tool_name":   toolUses[i].Name,
			"tool_input":  toolUses[i].Input,
			"tool_use_id": toolUses[i].ID,
			"session_id":  req.SessionID,
			"turn_id":     req.TurnID,
		})
		if appendHookDenialAndStop(state, types.HookEventPreToolUse, result) {
			return true
		}
		if result != nil && result.Action == types.HookActionModify && len(result.UpdatedInput) > 0 {
			toolUses[i].Input = result.UpdatedInput
		}
	}
	return false
}

// applyPostToolUseHooks fires the post_tool_use hook for each tool call that
// just executed, carrying whether it succeeded. Stop/Deny ends the whole
// turn (the tools already ran; callers must merge execResult into state
// before calling this, so the transcript stays well-formed either way - see
// the call site in Run). Returns true if the turn should stop.
func (l *Loop) applyPostToolUseHooks(ctx context.Context, state *MutableState, req RunRequest, toolUses []types.ToolUseContent, execResult execution.ExecuteResult) bool {
	for i := range toolUses {
		data := map[string]any{
			"tool_name":   toolUses[i].Name,
			"tool_use_id": toolUses[i].ID,
			"session_id":  req.SessionID,
			"turn_id":     req.TurnID,
			"success":     true,
		}
		if i < len(execResult.Results) && execResult.Results[i].Error != nil {
			data["success"] = false
			data["error"] = execResult.Results[i].Error.Error()
		}
		result := l.executeHookWithResult(ctx, types.HookEventPostToolUse, data)
		if appendHookDenialAndStop(state, types.HookEventPostToolUse, result) {
			return true
		}
	}
	return false
}
