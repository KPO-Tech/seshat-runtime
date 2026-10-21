package engine

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/modes"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	contract "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Transition represents a state transition in the query loop
// Based on OpenClaude's query.ts transitions
type Transition interface {
	IsContinue() bool
	IsTerminal() bool
}

// ContinueTransition indicates the loop should continue
type ContinueTransition struct {
	// Reason explains why we're continuing
	Reason string

	// RecoveryType indicates the type of recovery (if any)
	RecoveryType RecoveryType
}

// RecoveryType represents the type of recovery
type RecoveryType string

const (
	// RecoveryTypeNone means no recovery
	RecoveryTypeNone RecoveryType = "none"

	// RecoveryTypeMaxOutputTokens means max_output_tokens recovery
	RecoveryTypeMaxOutputTokens RecoveryType = "max_output_tokens"

	// RecoveryTypePromptTooLong means prompt_too_long recovery
	RecoveryTypePromptTooLong RecoveryType = "prompt_too_long"

	// RecoveryTypeContinuationNudge means continuation nudge
	RecoveryTypeContinuationNudge RecoveryType = "continuation_nudge"

	// RecoveryTypeAPIRetry means a narrow retry of a retryable API failure.
	RecoveryTypeAPIRetry RecoveryType = "api_retry"

	// RecoveryTypeTokenBudget means a budget-based continuation nudge fired.
	RecoveryTypeTokenBudget RecoveryType = "token_budget"

	// RecoveryTypeStopHook means a stop hook requested continuation.
	RecoveryTypeStopHook RecoveryType = "stop_hook"
)

const defaultBudgetContinuationLimit = 3

// IsContinue returns true
func (c ContinueTransition) IsContinue() bool {
	return true
}

// IsTerminal returns false
func (c ContinueTransition) IsTerminal() bool {
	return false
}

// TerminalTransition indicates the loop should stop
type TerminalTransition struct {
	// Reason explains why we're stopping
	Reason string

	// StopReason is the API stop reason
	StopReason string
}

// IsContinue returns false
func (t TerminalTransition) IsContinue() bool {
	return false
}

// IsTerminal returns true
func (t TerminalTransition) IsTerminal() bool {
	return true
}

// Continue creates a continue transition
func Continue(reason string) Transition {
	return ContinueTransition{
		Reason:       reason,
		RecoveryType: RecoveryTypeNone,
	}
}

// ContinueWithRecovery creates a continue transition with recovery
func ContinueWithRecovery(reason string, recoveryType RecoveryType) Transition {
	return ContinueTransition{
		Reason:       reason,
		RecoveryType: recoveryType,
	}
}

// Terminate creates a terminal transition
func Terminate(reason string, stopReason string) Transition {
	return TerminalTransition{
		Reason:     reason,
		StopReason: stopReason,
	}
}

// SessionState is the long-lived, session-owned conversation state.
//
// The query loop owns short-lived per-turn execution details in MutableState;
// this struct keeps only the canonical transcript, tool surface, and aggregate
// counters that must survive across turns and restores.
type SessionState struct {
	SessionID          types.SessionID
	TurnID             types.TurnID
	Messages           []types.Message
	Tools              map[string]tool.Tool
	DiscoveredDeferred []string
	// DeferredScope are the tools the main agent can find with tool_search besides the registry's deferred ones.
	DeferredScope       []string
	PermissionContext   *types.PermissionContext
	DenialTracking      *types.DenialTrackingState
	TurnNumber          int
	TotalTokens         int
	Metadata            *types.SessionMetadata
	LastCompaction      *types.CompactionMetadata
	CompactionCount     int
	CanonicalTranscript int

	// AutoCompactFailureCount tracks consecutive auto-compact failures across
	// turns, so the circuit breaker in compact.Engine.AutoCompact can actually
	// trip. MutableState's own copy of this is per-turn scratch space seeded
	// from here and folded back after each Run (see Loop.initializeState /
	// Session.SubmitMessage).
	AutoCompactFailureCount int

	// UsageAnchor is the size of the conversation as the provider counted it at the end of the last completed turn. Anything that
	// rewrites the messages (ReplaceMessages) drops it, since it described the messages before; UsageAnchorModel is the model that
	// counted, because another model counts differently.
	UsageAnchor      *compact.UsageAnchor
	UsageAnchorModel string
}

func NewSessionState(sessionID types.SessionID, turnID types.TurnID, messages []types.Message, tools map[string]tool.Tool, turnNumber int, totalTokens int, metadata *types.SessionMetadata) *SessionState {
	canonicalMessages := types.CanonicalTranscriptMessages(messages)
	compactionCount := types.CountCompactionMessages(canonicalMessages)
	discoveredDeferred := discoveredDeferredToolsFromMetadata(metadata)
	return &SessionState{
		SessionID:           sessionID,
		TurnID:              turnID,
		Messages:            append([]types.Message(nil), messages...),
		Tools:               tools,
		DiscoveredDeferred:  discoveredDeferred,
		PermissionContext:   permissionContextFromMetadata(metadata),
		DenialTracking:      &types.DenialTrackingState{},
		TurnNumber:          turnNumber,
		TotalTokens:         totalTokens,
		Metadata:            metadata,
		LastCompaction:      types.CompactionMetadataFromMessages(canonicalMessages),
		CompactionCount:     compactionCount,
		CanonicalTranscript: len(canonicalMessages),
	}
}

func (s *SessionState) CloneMessages() []types.Message {
	return append([]types.Message(nil), s.Messages...)
}

func (s *SessionState) ReplaceMessages(messages []types.Message) {
	s.Messages = append([]types.Message(nil), messages...)
	s.UsageAnchor = nil
	s.persistUsageAnchor()
	canonicalMessages := types.CanonicalTranscriptMessages(messages)
	s.CanonicalTranscript = len(canonicalMessages)
	s.LastCompaction = types.CompactionMetadataFromMessages(canonicalMessages)
	s.CompactionCount = types.CountCompactionMessages(canonicalMessages)
	if s.Metadata != nil {
		if s.Metadata.Additional == nil {
			s.Metadata.Additional = make(map[string]any)
		}
		s.Metadata.Additional["canonical_transcript"] = map[string]any{
			"message_count": s.CanonicalTranscript,
			"turn_count":    types.CountDistinctTurnIDs(canonicalMessages),
			"tool_results":  types.CountToolResultMessages(canonicalMessages),
		}
		s.Metadata.CompactCount = s.CompactionCount
		if s.LastCompaction != nil {
			now := GetCurrentTime()
			s.Metadata.LastCompactedAt = &now
		} else {
			s.Metadata.LastCompactedAt = nil
		}
	}
}

// AdvanceTurn folds a completed loop result back into the canonical session
// state. This is the handoff point between per-turn runtime execution and the
// long-lived conversation owner.
func (s *SessionState) AdvanceTurn(usage *types.TokenUsage, updatedMessages []types.Message) {
	s.ReplaceMessages(updatedMessages)
	s.TurnNumber++
	if s.Metadata != nil {
		s.Metadata.TotalTurns = s.TurnNumber
	}
	if usage != nil {
		s.TotalTokens += usage.InputTokens + usage.OutputTokens
		if s.Metadata != nil {
			s.Metadata.TotalTokens = s.TotalTokens
		}
	}
	if s.Metadata != nil {
		s.Metadata.UpdatedAt = GetCurrentTime()
	}
	s.TurnID = nextTurnID(s.Messages)
}

// RememberUsageAnchor keeps the provider's count at the end of a completed turn for the next one, in memory and in the session
// metadata (so a host that loads the session again for each turn gets it too). Call it after AdvanceTurn, which drops the anchor
// along with any other rewrite of the messages.
func (s *SessionState) RememberUsageAnchor(anchor *compact.UsageAnchor, model types.ModelIdentifier) {
	if anchor == nil {
		s.UsageAnchor = nil
		s.persistUsageAnchor()
		return
	}
	copied := *anchor
	s.UsageAnchor, s.UsageAnchorModel = &copied, model.String()
	s.persistUsageAnchor()
}

// UsageAnchorFor is the anchor to seed a turn with, or nil when none is known or it was counted by another model: a session whose
// model was switched between two turns starts from an estimate, since another model counts the same text differently.
func (s *SessionState) UsageAnchorFor(model types.ModelIdentifier) *compact.UsageAnchor {
	if s.UsageAnchor == nil || s.UsageAnchorModel != model.String() {
		return nil
	}
	copied := *s.UsageAnchor
	return &copied
}

func (s *SessionState) persistUsageAnchor() {
	if s.Metadata == nil {
		return
	}
	if s.UsageAnchor == nil {
		s.Metadata.UsageAnchorTokens, s.Metadata.UsageAnchorMessages, s.Metadata.UsageAnchorModel = 0, 0, ""
		return
	}
	s.Metadata.UsageAnchorTokens, s.Metadata.UsageAnchorMessages, s.Metadata.UsageAnchorModel = s.UsageAnchor.Tokens, s.UsageAnchor.MessageCount, s.UsageAnchorModel
}

// restoreUsageAnchor reads the anchor back from the metadata of a loaded session. It is kept only when it covers exactly the
// messages that were loaded; anything else means the transcript changed behind it.
func (s *SessionState) restoreUsageAnchor() {
	m := s.Metadata
	if m == nil || m.UsageAnchorTokens <= 0 || m.UsageAnchorModel == "" || m.UsageAnchorMessages != len(s.Messages) {
		return
	}
	s.UsageAnchor = &compact.UsageAnchor{Tokens: m.UsageAnchorTokens, MessageCount: m.UsageAnchorMessages}
	s.UsageAnchorModel = m.UsageAnchorModel
}

func (s *SessionState) MarkInterrupted() {
	if s.Metadata != nil {
		s.Metadata.Status = types.SessionStatusInterrupt
		s.Metadata.UpdatedAt = GetCurrentTime()
	}
}

// GetLastRecoveryContext returns the most recent recovery context for resume
func (s *SessionState) GetLastRecoveryContext() *RecoveryContext {
	if s.Metadata == nil || s.Metadata.Additional == nil {
		return nil
	}
	if rec, ok := s.Metadata.Additional["recovery_context"]; ok {
		if rc, ok := rec.(*RecoveryContext); ok {
			return rc
		}
	}
	return nil
}

// StoreRecoveryContext stores recovery context in metadata for persistence
func (s *SessionState) StoreRecoveryContext(ctx *RecoveryContext) {
	if s.Metadata == nil {
		return
	}
	if s.Metadata.Additional == nil {
		s.Metadata.Additional = make(map[string]any)
	}
	s.Metadata.Additional["recovery_context"] = ctx
}

func (s *SessionState) MarkClosed() {
	if s.Metadata != nil {
		s.Metadata.Status = types.SessionStatusClosed
		s.Metadata.UpdatedAt = GetCurrentTime()
	}
}

func (s *SessionState) MetadataSnapshot() *types.SessionMetadata {
	return s.Metadata
}

func (s *SessionState) PermissionContextSnapshot() *types.PermissionContext {
	return clonePermissionContext(s.PermissionContext)
}

func (s *SessionState) SetPermissionContext(permissionContext *types.PermissionContext) {
	s.PermissionContext = clonePermissionContext(permissionContext)
	if s.PermissionContext != nil {
		s.PermissionContext.NormalizeLegacyPlanMode()
	}
	if s.Metadata == nil || s.PermissionContext == nil {
		return
	}
	if s.Metadata.Additional == nil {
		s.Metadata.Additional = make(map[string]any)
	}
	s.Metadata.Additional["permission_context"] = clonePermissionContext(s.PermissionContext)
}

func (s *SessionState) CurrentPermissionMode() types.PermissionMode {
	if s.PermissionContext == nil || s.PermissionContext.Mode == "" {
		return types.PermissionModeOnRequest
	}
	return s.PermissionContext.Mode
}

func (s *SessionState) CurrentExecutionMode() string {
	if s.PermissionContext == nil || s.PermissionContext.ExecutionMode == "" {
		return string(modes.ExecutionModeExecute)
	}
	return s.PermissionContext.ExecutionMode
}

func (s *SessionState) DenialTrackingState() *types.DenialTrackingState {
	if s.DenialTracking == nil {
		s.DenialTracking = &types.DenialTrackingState{}
	}
	return s.DenialTracking
}

func (s *SessionState) ToolSurface() map[string]tool.Tool {
	return s.Tools
}

// dynamicAvailability is an optional interface for tools whose availability
// changes at runtime (e.g., after a per-request runner is injected).
// When implemented, IsAvailableNow is checked at request time in EffectiveToolSurface
// to hide unconfigured tools from the model without affecting session registration.
type dynamicAvailability interface {
	IsAvailableNow() bool
}

func (s *SessionState) EffectiveToolSurface(reg *tool.Registry) map[string]tool.Tool {
	surface := make(map[string]tool.Tool, len(s.Tools)+len(s.DiscoveredDeferred))
	for name, resolved := range s.Tools {
		if checker, ok := resolved.(dynamicAvailability); ok && !checker.IsAvailableNow() {
			continue
		}
		surface[name] = resolved
	}
	if reg == nil {
		return surface
	}
	for _, name := range s.DiscoveredDeferred {
		resolved, ok := reg.Resolve(name)
		if !ok {
			continue
		}
		surface[name] = resolved
	}
	return surface
}

func (s *SessionState) PendingDeferredToolNames(reg *tool.Registry) []string {
	if reg == nil {
		return nil
	}
	profile := tool.ToolSurfaceProfileMonoRun
	if s.Metadata != nil && s.Metadata.Additional != nil {
		if rawProfile, ok := s.Metadata.Additional["tool_surface_profile"].(string); ok && strings.TrimSpace(rawProfile) != "" {
			profile = rawProfile
		}
	}
	discovered := make(map[string]bool, len(s.DiscoveredDeferred))
	for _, name := range s.DiscoveredDeferred {
		discovered[name] = true
	}
	deferred := reg.ListDeferred()
	names := make([]string, 0, len(deferred))
	seen := make(map[string]bool, len(deferred))
	for _, deferredTool := range deferred {
		if deferredTool == nil {
			continue
		}
		name := deferredTool.Definition().Name
		if seen[name] || discovered[name] {
			continue
		}
		if !tool.VisibleInSurfaceProfile(tool.ToolDefinition{
			Name:        name,
			Description: deferredTool.Definition().Description,
			Category:    deferredTool.Definition().Category,
			InputSchema: deferredTool.Definition().InputSchema,
			Metadata:    deferredTool.Definition().Metadata,
		}, profile) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, name := range scopedDeferredNames(reg, s.DeferredScope) {
		if seen[name] || discovered[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *SessionState) RegisterDiscoveredDeferredTools(names []string) {
	if len(names) == 0 {
		return
	}
	seen := make(map[string]bool, len(s.DiscoveredDeferred))
	for _, name := range s.DiscoveredDeferred {
		seen[name] = true
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		s.DiscoveredDeferred = append(s.DiscoveredDeferred, name)
	}
	sort.Strings(s.DiscoveredDeferred)
	if s.Metadata == nil {
		return
	}
	if s.Metadata.Additional == nil {
		s.Metadata.Additional = make(map[string]any)
	}
	values := make([]string, len(s.DiscoveredDeferred))
	copy(values, s.DiscoveredDeferred)
	s.Metadata.Additional["discovered_deferred_tools"] = values
}

func discoveredDeferredToolsFromMetadata(metadata *types.SessionMetadata) []string {
	if metadata == nil || metadata.Additional == nil {
		return nil
	}
	raw, ok := metadata.Additional["discovered_deferred_tools"]
	if !ok {
		return nil
	}

	seen := make(map[string]bool)
	names := make([]string, 0)
	switch values := raw.(type) {
	case []string:
		for _, name := range values {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	case []any:
		for _, rawName := range values {
			name, ok := rawName.(string)
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func permissionContextFromMetadata(metadata *types.SessionMetadata) *types.PermissionContext {
	if metadata == nil || metadata.Additional == nil {
		return nil
	}
	raw, ok := metadata.Additional["permission_context"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var permissionContext types.PermissionContext
	if err := json.Unmarshal(data, &permissionContext); err != nil {
		return nil
	}
	permissionContext.NormalizeLegacyPlanMode()
	if permissionContext.Mode == "" {
		return nil
	}
	return &permissionContext
}

func clonePermissionContext(permissionContext *types.PermissionContext) *types.PermissionContext {
	if permissionContext == nil {
		return nil
	}
	cloned := *permissionContext
	if permissionContext.StrippedDangerousRules != nil {
		cloned.StrippedDangerousRules = make(map[string][]string, len(permissionContext.StrippedDangerousRules))
		for key, values := range permissionContext.StrippedDangerousRules {
			cloned.StrippedDangerousRules[key] = append([]string(nil), values...)
		}
	}
	return &cloned
}

func normalizePermissionContext(current *types.PermissionContext, fallback *types.PermissionContext) *types.PermissionContext {
	if current == nil {
		normalized := clonePermissionContext(fallback)
		if normalized != nil {
			normalized.NormalizeLegacyPlanMode()
		}
		return normalized
	}
	normalized := clonePermissionContext(current)
	normalized.NormalizeLegacyPlanMode()
	if normalized.Mode == "" && fallback != nil {
		normalized.Mode = fallback.Mode
	}
	if fallback != nil {
		if !normalized.IsBypassPermissionsModeAvailable {
			normalized.IsBypassPermissionsModeAvailable = fallback.IsBypassPermissionsModeAvailable
		}
		if !normalized.IsAutoModeAvailable {
			normalized.IsAutoModeAvailable = fallback.IsAutoModeAvailable
		}
	}
	if normalized.Mode == types.PermissionModeBypass {
		normalized.IsBypassPermissionsModeAvailable = true
	}
	return normalized
}

func permissionContextFromToolContext(base *types.PermissionContext, toolCtx contract.ToolUseContext) *types.PermissionContext {
	current := normalizePermissionContext(base, nil)
	if current == nil {
		current = &types.PermissionContext{}
	}
	current.Mode = toolCtx.PermissionMode
	current.ExecutionMode = toolCtx.ExecutionMode
	current.PrePlanMode = toolCtx.PrePlanMode
	current.IsBypassPermissionsModeAvailable = toolCtx.IsBypassPermissionsModeAvailable
	current.IsAutoModeAvailable = toolCtx.IsAutoModeAvailable
	current.NormalizeLegacyPlanMode()
	return current
}

// MutableState is the per-turn working set owned by the query loop.
// It is intentionally mutable and short-lived: each Run starts from canonical
// SessionState, mutates this structure across iterations, then folds the final
// result back into SessionState via AdvanceTurn.
type MutableState struct {
	// Messages are the conversation messages
	Messages []types.Message

	// ToolUses are all tool uses executed
	ToolUses []types.ToolUseContent

	// ToolResults are all tool results
	ToolResults []tool.CallResult

	// DiscoveredDeferred tracks deferred tools discovered during the current run.
	DiscoveredDeferred []string

	// Usage is token usage information
	Usage *types.TokenUsage

	// UsageAnchor is the size of the conversation as the provider last counted it (see compact.UsageAnchor). Reset by
	// compaction, which rewrites the messages it described.
	UsageAnchor *compact.UsageAnchor

	// StopReason is why the loop stopped
	StopReason string

	// Compacted indicates if compaction occurred
	Compacted bool

	// CompactPreTokens and CompactPostTokens carry the token counts from the
	// most recent compaction this turn (zero unless Compacted is true) - see
	// maybeAutoCompact. Threaded through RunResult/SessionResponse so a host
	// can show a before/after "saved N tokens" toast instead of compaction
	// happening completely silently.
	CompactPreTokens  int
	CompactPostTokens int

	// PreviousResponseID and PreviousResponseMessageCount enable a provider
	// that supports server-side conversation continuation (currently:
	// Codex's Responses API previous_response_id) to send only the messages
	// appended since the last successful same-provider response, instead of
	// resending the whole growing Messages list on every internal iteration
	// of a single turn's tool-use loop (up to MaxIterations). Populated in
	// Loop.recordPreviousResponse after each successful model call;
	// invalidated (reset to zero) the moment compaction changes Messages or
	// a different provider answers, so a stale/mismatched reference can
	// never be sent - worst case on invalidation is just falling back to
	// the existing full-resend behavior. Scoped to a single Loop.Run call
	// (one turn): MutableState is rebuilt fresh per turn, so there is no
	// cross-turn or cross-restart persistence to keep consistent.
	PreviousResponseID           string
	PreviousResponseMessageCount int

	// Iterations is the number of iterations performed
	Iterations int

	// TurnCount is the turn count
	TurnCount int

	// MaxOutputTokensRecoveryCount is recovery attempt count
	MaxOutputTokensRecoveryCount int

	// HasAttemptedReactiveCompact indicates if reactive compact was tried
	HasAttemptedReactiveCompact bool

	// MaxOutputTokensOverride is the override for max tokens
	MaxOutputTokensOverride int

	// ContinuationNudgeCount is continuation nudge count
	ContinuationNudgeCount int

	// TaskNudgeCount counts the reminders sent this turn because the session's
	// tracked task list still had unfinished items. Capped by
	// LoopConfig.TaskNudgeLimit.
	TaskNudgeCount int

	// TaskNudgeKey is the open-task list as of the last reminder, so a model
	// that ignores one is not asked twice about the same unchanged list.
	TaskNudgeKey string

	// Transition is the previous transition (undefined on first iteration)
	Transition Transition

	// StopHookActive indicates if a stop hook is active
	StopHookActive *bool

	// AutoCompactFailureCount tracks consecutive auto-compact failures.
	AutoCompactFailureCount int

	// TotalTurnTokens accumulates API usage across iterations of the turn.
	TotalTurnTokens int

	// BudgetContinuationCount tracks how many budget-based continuations fired.
	BudgetContinuationCount int

	// LastBudgetCheckTokens is the previous total token count seen by budget logic.
	LastBudgetCheckTokens int

	// LastBudgetDelta stores the last token delta for diminishing-returns checks.
	LastBudgetDelta int

	// RecoveryContext tracks details needed to properly resume after interruption
	RecoveryContext *RecoveryContext

	// CurrentStage is the prompt.ExecutionStage detected at the start of the last
	// iteration. Used to avoid redundant system-prompt rebuilds.
	CurrentStage prompt.ExecutionStage

	// PermissionContext is the evolving permission context owned by the loop
	// during this turn. Initialized from RunRequest at turn start; updated after
	// each tool execution batch. Session reads it back via RunResult.
	PermissionContext *types.PermissionContext

	// PermissionMode mirrors PermissionContext.Mode for fast access without a
	// nil check on every iteration.
	PermissionMode types.PermissionMode
}

// NewMutableState creates the short-lived per-turn state for one loop run.
func NewMutableState(initialMessages []types.Message) *MutableState {
	return &MutableState{
		Messages:                     append([]types.Message(nil), initialMessages...),
		ToolUses:                     make([]types.ToolUseContent, 0),
		ToolResults:                  make([]tool.CallResult, 0),
		DiscoveredDeferred:           make([]string, 0),
		TurnCount:                    1,
		StopHookActive:               nil,
		Transition:                   nil,
		TotalTurnTokens:              0,
		BudgetContinuationCount:      0,
		LastBudgetCheckTokens:        0,
		LastBudgetDelta:              0,
		Iterations:                   0,
		MaxOutputTokensRecoveryCount: 0,
		HasAttemptedReactiveCompact:  false,
		MaxOutputTokensOverride:      0,
		ContinuationNudgeCount:       0,
		TaskNudgeCount:               0,
		TaskNudgeKey:                 "",
		Compacted:                    false,
		Usage:                        nil,
		StopReason:                   "",
		RecoveryContext:              nil,
		PermissionContext:            nil,
		PermissionMode:               "",
	}
}

// Clone creates a deep copy of the state
func (s *MutableState) Clone() *MutableState {
	// Copy messages
	messages := make([]types.Message, len(s.Messages))
	copy(messages, s.Messages)

	// Copy tool uses
	toolUses := make([]types.ToolUseContent, len(s.ToolUses))
	copy(toolUses, s.ToolUses)

	// Copy tool results
	toolResults := make([]tool.CallResult, len(s.ToolResults))
	copy(toolResults, s.ToolResults)

	return &MutableState{
		Messages:                     messages,
		ToolUses:                     toolUses,
		ToolResults:                  toolResults,
		DiscoveredDeferred:           append([]string(nil), s.DiscoveredDeferred...),
		Usage:                        s.Usage,
		StopReason:                   s.StopReason,
		Compacted:                    s.Compacted,
		Iterations:                   s.Iterations,
		TurnCount:                    s.TurnCount,
		MaxOutputTokensRecoveryCount: s.MaxOutputTokensRecoveryCount,
		HasAttemptedReactiveCompact:  s.HasAttemptedReactiveCompact,
		MaxOutputTokensOverride:      s.MaxOutputTokensOverride,
		ContinuationNudgeCount:       s.ContinuationNudgeCount,
		TaskNudgeCount:               s.TaskNudgeCount,
		TaskNudgeKey:                 s.TaskNudgeKey,
		Transition:                   s.Transition,
		StopHookActive:               s.StopHookActive,
		TotalTurnTokens:              s.TotalTurnTokens,
		BudgetContinuationCount:      s.BudgetContinuationCount,
		LastBudgetCheckTokens:        s.LastBudgetCheckTokens,
		LastBudgetDelta:              s.LastBudgetDelta,
		RecoveryContext:              s.RecoveryContext,
		CurrentStage:                 s.CurrentStage,
		PermissionContext:            clonePermissionContext(s.PermissionContext),
		PermissionMode:               s.PermissionMode,
	}
}

// RecoveryContext tracks details needed to properly resume after interruption.
type RecoveryContext struct {
	LastTransitionReason string
	LastRecoveryType     RecoveryType
	LastStopReason       string
	CompactionSnapshot   *CompactionSnapshot
	TurnProgress         *TurnProgress
}

// CompactionSnapshot captures the state of compaction at interruption.
type CompactionSnapshot struct {
	PreCompactionTokenCount  int
	PostCompactionTokenCount int
	FirstPreservedMessageID  types.MessageID
	LastPreservedMessageID   types.MessageID
	PreservedTailHash        string
	BoundaryVersion          int
}

// TurnProgress captures the turn execution state at interruption.
type TurnProgress struct {
	IterationsCompleted    int
	LastAssistantMessageID types.MessageID
	PendingToolUses        []types.ToolUseContent
	PendingToolResults     []tool.CallResult
	TotalTokensUsed        int
}
