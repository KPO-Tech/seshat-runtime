package types

import "time"

// RuntimeEventType identifies a structured runtime event emitted during a turn.
type RuntimeEventType string

const (
	RuntimeEventTypeTurnStarted            RuntimeEventType = "turn.started"
	RuntimeEventTypeTurnCompleted          RuntimeEventType = "turn.completed"
	RuntimeEventTypeTurnFailed             RuntimeEventType = "turn.failed"
	RuntimeEventTypeResponseChunk          RuntimeEventType = "response.chunk"
	RuntimeEventTypeToolProgress           RuntimeEventType = "tool.progress"
	RuntimeEventTypeBrowserSession         RuntimeEventType = "browser.session"
	RuntimeEventTypeBrowserAction          RuntimeEventType = "browser.action"
	RuntimeEventTypeBrowserPage            RuntimeEventType = "browser.page"
	RuntimeEventTypeBrowserSnapshot        RuntimeEventType = "browser.snapshot"
	RuntimeEventTypeBrowserScreenshot      RuntimeEventType = "browser.screenshot"
	RuntimeEventTypeToolPermissionRequired RuntimeEventType = "tool.permission_required"
	RuntimeEventTypePromptRequired         RuntimeEventType = "prompt.request"
	RuntimeEventTypePlanSubmitted          RuntimeEventType = "plan.submitted"
	RuntimeEventTypeExecutionModeChanged   RuntimeEventType = "execution_mode.changed"
	RuntimeEventTypeTaskChanged            RuntimeEventType = "task.changed"

	// RuntimeEventTypeProviderRetry is emitted before the runtime waits to retry a
	// request the provider refused or dropped (a rate limit, an overload, a reset
	// connection), so that a client can say what it is waiting for instead of a
	// spinner that looks frozen.
	RuntimeEventTypeProviderRetry RuntimeEventType = "provider.retry"

	// RuntimeEventTypeStage marks a named phase of pre-generation turn setup
	// (loading tools, resuming a session, searching knowledge) so a client can
	// show what's happening instead of a generic spinner during the otherwise
	// silent gap before the first response chunk arrives.
	RuntimeEventTypeStage RuntimeEventType = "turn.stage"

	// RuntimeEventTypeCompaction is emitted alongside turn.completed whenever
	// that turn triggered auto-compaction - otherwise compaction happens
	// completely silently even though the before/after token counts are
	// already computed internally (see CompactionResult in
	// internal/runtime/memory). Lets a client show a "conversation
	// compacted, saved N tokens" notice.
	RuntimeEventTypeCompaction RuntimeEventType = "context.compacted"

	// RuntimeEventTypeCompacting is emitted when the runtime starts compacting the conversation, before the summary
	// is requested, so a host can say so during the pause instead of showing a frozen turn. context.compacted follows
	// when it is done (a failed attempt is followed by nothing).
	RuntimeEventTypeCompacting RuntimeEventType = "context.compacting"

	// RuntimeEventTypeContextUsage reports how full the model's context window is, so a host can draw a gauge that
	// moves during a turn instead of waiting for turn.completed. Emitted at the start of every loop iteration (an
	// estimate, or the provider's own figure plus the messages added since) and again when a model response arrives
	// (the figure the provider reported).
	RuntimeEventTypeContextUsage RuntimeEventType = "context.usage"

	// Goal events — mirrors Codex ThreadGoalUpdatedNotification / ThreadGoalUpdatedEvent.
	RuntimeEventTypeGoalUpdated RuntimeEventType = "goal.updated"

	// Multi-agent collab events — mirrors Codex CollabAgent* protocol events.
	RuntimeEventTypeAgentSpawnBegin       RuntimeEventType = "agent.spawn.begin"
	RuntimeEventTypeAgentSpawnEnd         RuntimeEventType = "agent.spawn.end"
	RuntimeEventTypeAgentWaitBegin        RuntimeEventType = "agent.wait.begin"
	RuntimeEventTypeAgentWaitEnd          RuntimeEventType = "agent.wait.end"
	RuntimeEventTypeAgentInteractionBegin RuntimeEventType = "agent.interaction.begin"
	RuntimeEventTypeAgentInteractionEnd   RuntimeEventType = "agent.interaction.end"
	RuntimeEventTypeAgentCloseBegin       RuntimeEventType = "agent.close.begin"
	RuntimeEventTypeAgentCloseEnd         RuntimeEventType = "agent.close.end"
)

// RuntimeEvent is the structured event envelope emitted by the runtime.
type RuntimeEvent struct {
	Type RuntimeEventType `json:"type"`

	SessionID SessionID `json:"session_id,omitempty"`
	TurnID    TurnID    `json:"turn_id,omitempty"`

	// TurnNumber is 1-indexed for the active turn being processed.
	TurnNumber int `json:"turn_number,omitempty"`

	Timestamp time.Time `json:"timestamp,omitempty"`

	StopReason    string      `json:"stop_reason,omitempty"`
	ExecutionMode string      `json:"execution_mode,omitempty"`
	Usage         *TokenUsage `json:"usage,omitempty"`

	Chunk             *APIResponseChunk      `json:"chunk,omitempty"`
	ToolProgress      *ToolProgress          `json:"tool_progress,omitempty"`
	Browser           *BrowserRuntimeEvent   `json:"browser,omitempty"`
	PermissionRequest *ToolPermissionRequest `json:"permission_request,omitempty"`
	PromptRequest     *PromptRequest         `json:"prompt_request,omitempty"`
	PlanEvent         *PlanRuntimeEvent      `json:"plan_event,omitempty"`
	TaskEvent         *TaskRuntimeEvent      `json:"task_event,omitempty"`

	// AgentToolUseID is set when this event originates from a sub-agent.
	// It identifies the parent agent tool_use block that spawned the sub-agent.
	AgentToolUseID string `json:"agent_tool_use_id,omitempty"`

	// GoalEvent carries structured payload for goal.updated events.
	GoalEvent *GoalRuntimeEvent `json:"goal_event,omitempty"`

	// AgentEvent carries structured payload for multi-agent collab events.
	AgentEvent *AgentRuntimeEvent `json:"agent_event,omitempty"`

	// StageEvent carries structured payload for turn.stage events.
	StageEvent *StageRuntimeEvent `json:"stage_event,omitempty"`

	// ProviderRetryEvent carries structured payload for provider.retry events.
	ProviderRetryEvent *ProviderRetryRuntimeEvent `json:"provider_retry_event,omitempty"`

	// CompactionEvent carries structured payload for context.compacted events.
	CompactionEvent *CompactionRuntimeEvent `json:"compaction_event,omitempty"`

	// ContextUsageEvent carries structured payload for context.usage events.
	ContextUsageEvent *ContextUsageRuntimeEvent `json:"context_usage_event,omitempty"`

	Error string `json:"error,omitempty"`
}

// GoalRuntimeEvent is the structured payload for goal.updated events.
// Mirrors Codex's ThreadGoalUpdatedEvent / ThreadGoalUpdatedNotification.
type GoalRuntimeEvent struct {
	// SessionID identifies the session this goal belongs to.
	SessionID string `json:"session_id"`
	// Objective is the current goal objective text.
	Objective string `json:"objective"`
	// Status is the current ThreadGoalStatus string.
	Status string `json:"status"`
	// TokenBudget is the optional max token budget.
	TokenBudget *int64 `json:"token_budget,omitempty"`
	// TokensUsed is the cumulative tokens consumed so far.
	TokensUsed int64 `json:"tokens_used"`
	// TimeUsedSeconds is the elapsed wall-clock time since goal creation.
	TimeUsedSeconds int64 `json:"time_used_seconds"`
	// CreatedAt is the Unix millisecond timestamp when the goal was created.
	CreatedAt int64 `json:"created_at"`
	// UpdatedAt is the Unix millisecond timestamp of the last update.
	UpdatedAt int64 `json:"updated_at"`
}

// AgentRuntimeEvent is the structured payload for agent.spawn/wait/interaction/close events.
// Mirrors Codex's CollabAgent*Event family from protocol.rs.
type AgentRuntimeEvent struct {
	// CallID is the tool_use ID of the spawning / waiting / interaction call.
	CallID string `json:"call_id"`
	// AgentID is the stable agent identifier (returned by spawn_agent).
	AgentID string `json:"agent_id"`
	// AgentNickname is the optional random human-friendly name (e.g. "Orion").
	AgentNickname string `json:"agent_nickname,omitempty"`
	// AgentRole is the optional role the agent was spawned with (e.g. "reviewer").
	AgentRole string `json:"agent_role,omitempty"`
	// Prompt is the initial or follow-up prompt (may be empty to prevent leaking CoT).
	Prompt string `json:"prompt,omitempty"`
	// Status is the last known CollabAgentStatus of the agent.
	Status string `json:"status,omitempty"`
	// Message is the inter-agent message text for interaction events.
	Message string `json:"message,omitempty"`
	// StartedAtMs is the Unix millisecond timestamp for begin events.
	StartedAtMs int64 `json:"started_at_ms,omitempty"`
	// CompletedAtMs is the Unix millisecond timestamp for end events.
	CompletedAtMs int64 `json:"completed_at_ms,omitempty"`
}

// PlanRuntimeEvent is emitted when a plan document is submitted or its status changes.
type PlanRuntimeEvent struct {
	PlanID   string `json:"plan_id"`
	Slug     string `json:"slug"`
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Version  int    `json:"version"`
	Content  string `json:"content,omitempty"`
}

type TaskRuntimeEvent struct {
	TaskID  string `json:"task_id"`
	Action  string `json:"action"`
	Status  string `json:"status,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// StageRuntimeEvent is the structured payload for turn.stage events - a
// named phase of pre-generation turn setup, superseded implicitly once real
// generation begins (turn.started/response.chunk arrive on the same channel
// and already update client-side agent state, so no explicit "stage ended"
// event is emitted).
type StageRuntimeEvent struct {
	// Stage is a stable machine-readable identifier (e.g. "tools", "session", "knowledge").
	Stage string `json:"stage"`
	// Label is the human-readable phrase a client can show directly.
	Label string `json:"label"`
}

// CompactionRuntimeEvent is the structured payload for context.compacted events.
type CompactionRuntimeEvent struct {
	PreCompactTokens  int `json:"pre_compact_tokens"`
	PostCompactTokens int `json:"post_compact_tokens"`
}

// ContextUsageRuntimeEvent is the structured payload for context.usage events.
type ContextUsageRuntimeEvent struct {
	// UsedTokens is the size of the next request: system prompt, tool definitions and messages.
	UsedTokens int `json:"used_tokens"`
	// WindowTokens is the model's context window.
	WindowTokens int `json:"window_tokens"`
	// AutoCompactAtTokens is the size at which the conversation is compacted before the next request.
	AutoCompactAtTokens int `json:"auto_compact_at_tokens"`
	// BlockingLimitTokens is the size above which a request is refused locally.
	BlockingLimitTokens int `json:"blocking_limit_tokens"`
	// Source is "estimate" when the figure is counted by the runtime, or "reported" when it starts from what the
	// provider said the last request held.
	Source string `json:"source"`
}

// BrowserRuntimeEvent is the structured payload emitted for browser lifecycle and interaction activity.
type BrowserRuntimeEvent struct {
	Action string `json:"action,omitempty"`

	PageID       string `json:"page_id,omitempty"`
	URL          string `json:"url,omitempty"`
	Title        string `json:"title,omitempty"`
	ActivePageID string `json:"active_page_id,omitempty"`

	PageCount    int `json:"page_count,omitempty"`
	ActionCount  int `json:"action_count,omitempty"`
	ElementCount int `json:"element_count,omitempty"`
	HeadingCount int `json:"heading_count,omitempty"`
	TextLength   int `json:"text_length,omitempty"`

	Bytes         int    `json:"bytes,omitempty"`
	PersistedPath string `json:"persisted_path,omitempty"`
	PersistedSize int    `json:"persisted_size,omitempty"`
	FullPage      bool   `json:"full_page,omitempty"`
}

// ProviderRetryRuntimeEvent is the structured payload for provider.retry events.
type ProviderRetryRuntimeEvent struct {
	// Attempt is the number of the attempt about to be made (2 for the first retry).
	Attempt int `json:"attempt"`
	// MaxAttempts is the number of attempts the runtime makes in all.
	MaxAttempts int `json:"max_attempts"`
	// WaitMs is how long the runtime waits before that attempt, in milliseconds.
	WaitMs int64 `json:"wait_ms"`
	// Reason says why the request is retried: "rate limited (429)", "server error (503)",
	// "connection error".
	Reason string `json:"reason"`
}
