package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	coregoal "github.com/KPO-Tech/seshat-runtime/internal/agent/goal"
	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	contextusage "github.com/KPO-Tech/seshat-runtime/internal/tools/special/context_usage"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Session represents an active query session.
type Session struct {
	engine *Engine
	state  *SessionState
	store  SessionStore
	config *Config

	progressCallback      func(types.ToolProgress)
	responseChunkCallback func(types.APIResponseChunk)
	runtimeEventCallback  func(types.RuntimeEvent)

	eventQueue        *execution.EventQueue
	runtimeEventQueue *execution.RuntimeEventQueue

	// Per-session system prompt overrides.
	appendSystemPromptOverride   *string
	systemPromptTemplateOverride *string
	workingDirectoryOverride     *string

	// userID is the authenticated user of the latest turn, kept for what happens when the session closes.
	userID string

	// recalled is the long-term memory block of this session, computed once at the first request so the system prompt
	// stays the same from turn to turn (a changing prompt cannot be cached by the provider).
	recalled       string
	recalledLoaded bool

	// mu guards cancelFn and phase.
	mu sync.Mutex
	// cancelFn cancels the context passed to the current Loop.Run.
	cancelFn context.CancelFunc
	// phase enforces that only one lifecycle-mutating operation
	// (SubmitMessage/SubmitMessageWithContent, Close) runs at a time - two
	// concurrent calls would otherwise both mutate s.state.Messages and
	// persist independently, silently corrupting/dropping one of them.
	// Interrupt is exempt: it only ever cancels whatever IS running.
	phase sessionPhase
}

// sessionPhase is Session's single-flight lifecycle state, guarded by mu.
type sessionPhase int32

const (
	sessionPhaseIdle sessionPhase = iota
	sessionPhaseRunning
	sessionPhaseClosed
)

// NewSession creates a new session.
func (e *Engine) NewSession(ctx context.Context) (*Session, error) {
	sessionID := types.NewSessionID(generateID())
	return e.NewSessionFromState(ctx, sessionID, nil, nil)
}

// NewSessionFromState creates a session from existing state or initializes a new one.
func (e *Engine) NewSessionFromState(
	ctx context.Context,
	sessionID types.SessionID,
	metadata *types.SessionMetadata,
	messages []types.Message,
) (*Session, error) {
	// createdFresh is true when creating a brand-new session:
	// - metadata is nil (classic NewSession path), OR
	// - sessionID is empty (SDK CreateSession path: metadata with Title but no ID).
	// Both cases require an immediate store write so LoadSession works before
	// the first SubmitMessage.
	createdFresh := metadata == nil || sessionID == ""
	if sessionID == "" {
		sessionID = types.NewSessionID(generateID())
	}
	if metadata == nil {
		metadata = &types.SessionMetadata{
			ID:            sessionID,
			Status:        types.SessionStatusActive,
			CreatedAt:     currentTime(),
			UpdatedAt:     currentTime(),
			Model:         e.config.Model.String(),
			TotalTurns:    0,
			TotalTokens:   0,
			MaxTokens:     e.config.MaxTokens,
			SchemaVersion: types.SessionMetadataSchemaVersion,
		}
	} else {
		migrateSessionMetadata(metadata)
		metadata.ID = sessionID
		if metadata.Status == "" {
			metadata.Status = types.SessionStatusActive
		}
		if metadata.UpdatedAt.IsZero() {
			metadata.UpdatedAt = currentTime()
		}
		if metadata.Model == "" {
			metadata.Model = e.config.Model.String()
		}
		if metadata.MaxTokens == 0 {
			metadata.MaxTokens = e.config.MaxTokens
		}
	}
	if metadata.RootPath == "" {
		metadata.RootPath = e.workingDirectory()
	}

	if e.memoryService != nil {
		if _, err := e.memoryService.LoadProject(metadata.RootPath); err != nil {
			slog.Warn("failed to load project memory", "error", err)
		}
		if err := e.memoryService.LoadUser(); err != nil {
			slog.Warn("failed to load user memory", "error", err)
		}
		if err := e.memoryService.LoadCrossSession(); err != nil {
			slog.Warn("failed to load cross-session memory", "error", err)
		}
	}

	surfaceBuilder := tool.NewSurfaceBuilder(e.toolRegistry)
	surfaceProfile := sessionToolSurfaceProfile(metadata)
	var allowedNames []string
	if surfaceProfile == tool.ToolSurfaceProfileMonoRun {
		allowedNames = e.config.MainAgentTools
	}
	tools, err := surfaceBuilder.BuildToolMap(ctx, tool.SurfaceBuildRequest{
		IncludeReadOnly:    true,
		IncludeDestructive: true,
		SurfaceProfile:     surfaceProfile,
		AllowedNames:       allowedNames,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build tool surface: %w", err)
	}

	messageCopy := append([]types.Message(nil), messages...)
	turnNumber := metadata.TotalTurns
	if turnNumber < 0 {
		turnNumber = 0
	}
	turnID := nextTurnID(messageCopy)

	sessionState := NewSessionState(sessionID, turnID, messageCopy, tools, turnNumber, metadata.TotalTokens, metadata)
	sessionState.SetPermissionContext(normalizePermissionContext(sessionState.PermissionContextSnapshot(), e.defaultPermissionContext()))
	if surfaceProfile == tool.ToolSurfaceProfileMonoRun {
		sessionState.DeferredScope = append([]string(nil), e.config.MainAgentDeferredTools...)
	}

	session := &Session{
		engine:            e,
		state:             sessionState,
		store:             e.sessionStore,
		config:            e.config,
		eventQueue:        execution.NewEventQueue(execution.DefaultEventQueueCapacity),
		runtimeEventQueue: execution.NewRuntimeEventQueue(execution.DefaultRuntimeEventQueueCapacity),
	}

	// createdFresh is true when the caller is creating a brand-new session:
	// - metadata was nil (classic NewSession path), OR
	// - sessionID was empty (SDK path that passes pre-built metadata but no ID).
	// In both cases the store entry must be written immediately so that
	// LoadSession can succeed before the first SubmitMessage.
	if createdFresh && e.sessionStore != nil {
		if err := e.sessionStore.SaveSessionState(sessionID, metadata, nil, messageCopy); err != nil {
			return nil, fmt.Errorf("failed to persist new session: %w", err)
		}
	}

	if e.hookExecutor != nil {
		e.hookExecutor.ExecuteAsync(ctx, types.HookEventSessionStart, map[string]any{
			"session_id":    sessionID,
			"created_fresh": createdFresh,
		})
	}

	return session, nil
}

// SubmitMessage submits a plain-text user message to the session.
func (s *Session) SubmitMessage(ctx context.Context, content string) (*SessionResponse, error) {
	msg := types.UserMessage(fmt.Sprintf("msg-%d", len(s.state.Messages)+1), content)
	return s.submitWithMessage(ctx, msg, content)
}

// SubmitMessageWithContent submits a user message that may include image content blocks.
func (s *Session) SubmitMessageWithContent(ctx context.Context, text string, images []types.ImageContent) (*SessionResponse, error) {
	var msg types.Message
	if len(images) > 0 {
		msg = types.UserMessageWithImage(fmt.Sprintf("msg-%d", len(s.state.Messages)+1), text, images...)
	} else {
		msg = types.UserMessage(fmt.Sprintf("msg-%d", len(s.state.Messages)+1), text)
	}
	return s.submitWithMessage(ctx, msg, text)
}

// submitWithMessage is the shared implementation for SubmitMessage and SubmitMessageWithContent.
func (s *Session) submitWithMessage(ctx context.Context, userMsg types.Message, text string) (*SessionResponse, error) {
	if err := s.beginTurn(); err != nil {
		return nil, err
	}
	defer s.endTurn()
	s.rememberUserID(ctx)
	if s.engine != nil && s.engine.hookExecutor != nil {
		defer s.engine.hookExecutor.ExecuteAsync(context.Background(), types.HookEventTurnEnd, map[string]any{
			"session_id": s.state.SessionID,
			"turn_id":    s.state.TurnID,
		})
	}

	if err := s.enforceMaxTurns(); err != nil {
		return nil, err
	}

	if s.engine != nil && s.engine.hookExecutor != nil {
		if result := s.engine.hookExecutor.ExecuteFirst(ctx, types.HookEventTurnStart, map[string]any{
			"session_id": s.state.SessionID,
			"turn_id":    s.state.TurnID,
		}); result != nil && (result.Action == types.HookActionStop || result.Action == types.HookActionDeny) {
			if strings.TrimSpace(result.Message) != "" {
				return nil, fmt.Errorf("turn denied by hook: %s", result.Message)
			}
			return nil, fmt.Errorf("turn denied by hook")
		}
	}

	userMsg.Timestamp = currentTime().UTC()
	userMsg.Metadata = &types.MessageMetadata{
		TurnID: s.state.TurnID.String(),
	}

	previousMessages := s.state.CloneMessages()
	s.state.Messages = append(s.state.Messages, userMsg)
	if s.state.Metadata != nil {
		s.state.Metadata.UpdatedAt = currentTime()
	}
	persistedMessages := s.state.CloneMessages()
	if err := s.persistSessionState(previousMessages); err != nil {
		return nil, fmt.Errorf("failed to persist accepted user input: %w", err)
	}

	// Kick off async title generation as soon as we know this is the
	// session's first message - before running the turn, not after it
	// completes. generateTitleAsync only ever needs the first user
	// message text, never the assistant's response, so there's no reason
	// to make the session sit as "Untitled" through the whole first reply
	// before getting its real name.
	if s.state.Metadata != nil && s.state.Metadata.TotalTurns == 0 && text != "" {
		sid := s.state.SessionID
		go s.engine.generateTitleAsync(sid, text) // #nosec G118 -- the title may arrive after the turn; generateTitleAsync has its own 30 s timeout
	}

	s.rememberUserDirectives(text)
	s.emitRuntimeEvent(types.RuntimeEvent{
		Type:          types.RuntimeEventTypeTurnStarted,
		SessionID:     s.state.SessionID,
		TurnID:        s.state.TurnID,
		TurnNumber:    s.currentTurnNumber(),
		ExecutionMode: s.state.CurrentExecutionMode(),
	})

	// turn.stage existed but nothing ever constructed it (H6 in TODO.md) -
	// emit it for the one real pre-generation setup phase this engine has
	// today (assembling tools, system prompt and context), so a client can
	// show that instead of a silent gap before the first response chunk.
	s.emitRuntimeEvent(types.RuntimeEvent{
		Type:      types.RuntimeEventTypeStage,
		SessionID: s.state.SessionID,
		TurnID:    s.state.TurnID,
		StageEvent: &types.StageRuntimeEvent{
			Stage: "preparing_turn",
			Label: "Preparing the turn",
		},
	})

	apiReq, err := s.buildAPIRequest(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build API request: %w", err)
	}
	runtimeTools := s.state.EffectiveToolSurface(s.engine.toolRegistry)

	permissionContext := s.state.PermissionContextSnapshot()
	loopReq := RunRequest{
		Messages:                s.state.CloneMessages(),
		SystemPrompt:            apiReq.SystemPrompt,
		SystemPromptBlocks:      apiReq.SystemPromptBlocks,
		ProviderTools:           apiReq.Tools,
		Tools:                   runtimeTools,
		ToolRegistry:            s.engine.toolRegistry,
		DeferredScope:           s.state.DeferredScope,
		RefreshSystemPrompt:     s.refreshSystemPrompt,
		AutoDetectStage:         s.config.PromptStage == prompt.StageDefault,
		SessionID:               s.state.SessionID,
		TurnID:                  s.state.TurnID,
		WorkingDirectory:        s.workingDirectory(),
		PermissionMode:          permissionContext.Mode,
		PermissionContext:       permissionContext,
		Model:                   s.config.Model,
		MaxTokens:               s.config.MaxTokens,
		ProgressCallback:        s.handleProgressCallback,
		ResponseChunkCallback:   s.handleResponseChunkCallback,
		EventQueue:              s.eventQueue,
		DenialTracking:          s.state.DenialTrackingState(),
		AutoCompactFailureCount: s.state.AutoCompactFailureCount,
	}

	turnCtx, cancel := context.WithCancel(ctx)
	emitter := func(event types.RuntimeEvent) {
		// What runs inside the turn (a provider retrying, a tool) does not know
		// which session it is in.
		if event.SessionID == "" {
			event.SessionID = s.state.SessionID
		}
		if event.TurnID == "" {
			event.TurnID = s.state.TurnID
		}
		s.emitRuntimeEvent(event)
	}
	turnCtx = context.WithValue(turnCtx, types.RuntimeEventEmitterKey, emitter)
	s.mu.Lock()
	s.cancelFn = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancelFn = nil
		s.mu.Unlock()
		cancel()
	}()

	loopResult := s.engine.loop.Run(turnCtx, loopReq)
	// Persist the auto-compact circuit-breaker count regardless of outcome -
	// including on error, so a failing compaction still counts toward the
	// breaker instead of resetting to zero on the next turn (see RunRequest/
	// RunResult.AutoCompactFailureCount doc comments).
	s.state.AutoCompactFailureCount = loopResult.AutoCompactFailureCount
	if loopResult.Error != nil {
		s.emitRuntimeEvent(types.RuntimeEvent{
			Type:       types.RuntimeEventTypeTurnFailed,
			SessionID:  s.state.SessionID,
			TurnID:     s.state.TurnID,
			TurnNumber: s.currentTurnNumber(),
			Error:      loopResult.Error.Error(),
		})
		if loopResult.RecoveryContext != nil {
			s.state.StoreRecoveryContext(loopResult.RecoveryContext)
		}
		// Do NOT call AdvanceTurn on error: counting an errored loop as a
		// completed turn would increment TurnNumber and prematurely exhaust MaxTurns.
		// The work the turn did is kept, though: see keepInterruptedTurn.
		s.keepInterruptedTurn(loopResult.Messages, persistedMessages, loopResult.Error)
		return nil, fmt.Errorf("query loop failed: %w", loopResult.Error)
	}
	s.state.RegisterDiscoveredDeferredTools(loopResult.DiscoveredDeferred)
	if loopResult.PermissionContext != nil {
		s.state.SetPermissionContext(loopResult.PermissionContext)
	}
	if loopResult.RecoveryContext != nil {
		s.state.StoreRecoveryContext(loopResult.RecoveryContext)
	}

	s.state.AdvanceTurn(loopResult.Usage, loopResult.Messages)
	recordGoalTokenUsage(s.state.SessionID, loopResult.Usage)
	if err := s.persistSessionState(persistedMessages); err != nil {
		return nil, fmt.Errorf("failed to persist session state after turn: %w", err)
	}
	s.rememberToolUsage(loopResult.ToolUses, loopResult.ToolResults)

	response := &SessionResponse{
		Messages:          s.state.CloneMessages(),
		StopReason:        loopResult.StopReason,
		ToolUses:          loopResult.ToolUses,
		ToolResults:       loopResult.ToolResults,
		Usage:             loopResult.Usage,
		TotalTokens:       s.state.TotalTokens,
		TurnNumber:        s.state.TurnNumber,
		Compacted:         loopResult.Compacted,
		CompactPreTokens:  loopResult.CompactPreTokens,
		CompactPostTokens: loopResult.CompactPostTokens,
	}
	s.emitRuntimeEvent(types.RuntimeEvent{
		Type:          types.RuntimeEventTypeTurnCompleted,
		SessionID:     s.state.SessionID,
		TurnID:        s.state.TurnID,
		TurnNumber:    response.TurnNumber,
		StopReason:    response.StopReason,
		ExecutionMode: s.state.CurrentExecutionMode(),
		Usage:         cloneTokenUsage(response.Usage),
	})
	// Compaction otherwise happens completely silently - the token counts
	// were already computed by maybeAutoCompact and would just be discarded.
	// Emitted alongside (not instead of) turn.completed, in-band with the
	// rest of this turn's stream, since - unlike a separate async
	// condense call - compaction here already ran synchronously inside
	// the turn that just completed.
	return response, nil
}

func recordGoalTokenUsage(sessionID types.SessionID, usage *types.TokenUsage) {
	if usage == nil {
		return
	}
	tokens := int64(usage.InputTokens + usage.OutputTokens)
	if tokens <= 0 {
		return
	}
	coregoal.GetDefaultStore().RecordTokenUsage(sessionID.String(), tokens)
}

// beginTurn enforces that submitWithMessage is single-flight: a session
// that's already running a turn, or has been closed, refuses a new one
// instead of letting two calls race on s.state.
func (s *Session) beginTurn() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.phase {
	case sessionPhaseClosed:
		return types.NewError(types.ErrCodeSessionClosed, "session is closed")
	case sessionPhaseRunning:
		return fmt.Errorf("session is busy: a turn is already in progress")
	}
	s.phase = sessionPhaseRunning
	return nil
}

// endTurn releases the single-flight guard beginTurn acquired, unless
// Close() has since moved the session to sessionPhaseClosed - closing must
// win over a turn that was still in flight when it happened.
func (s *Session) endTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == sessionPhaseRunning {
		s.phase = sessionPhaseIdle
	}
}

// Interrupt cancels the current turn and marks the session as interrupted.
// Not gated by beginTurn/endTurn: it targets whatever IS currently running,
// so it must work regardless of the single-flight guard.
func (s *Session) Interrupt() error {
	s.mu.Lock()
	fn := s.cancelFn
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
	s.state.MarkInterrupted()
	messages := s.state.CloneMessages()
	return s.persistSessionState(messages)
}

// Close closes the session, persists memory, and releases the browser
// session. Idempotent (a second call is a no-op) and safe to call while a
// turn is running: it cancels that turn first, before tearing down the
// event queues underneath it.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.phase == sessionPhaseClosed {
		s.mu.Unlock()
		return nil
	}
	fn := s.cancelFn
	s.phase = sessionPhaseClosed
	s.mu.Unlock()
	if fn != nil {
		fn()
	}

	if s.eventQueue != nil {
		s.eventQueue.Close()
	}
	if s.runtimeEventQueue != nil {
		s.runtimeEventQueue.Close()
	}
	s.state.MarkClosed()
	contextusage.Forget(s.state.SessionID)
	messages := s.state.CloneMessages()
	s.extractMemories(messages)

	if s.engine.memoryService != nil {
		s.rememberSessionSummary()
		if err := s.engine.memoryService.SaveProject(s.sessionProjectID()); err != nil {
			slog.Warn("failed to save project memory", "error", err)
		}
		if err := s.engine.memoryService.SaveUser(); err != nil {
			slog.Warn("failed to save user memory", "error", err)
		}
		if err := s.engine.memoryService.SaveCrossSession(); err != nil {
			slog.Warn("failed to save cross-session memory", "error", err)
		}
	}

	if s.engine != nil && s.engine.browserManager != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.engine.browserManager.CloseSession(closeCtx, s.state.SessionID)
		cancel()
	}

	if s.engine != nil && s.engine.hookExecutor != nil {
		s.engine.hookExecutor.ExecuteAsync(context.Background(), types.HookEventSessionEnd, map[string]any{
			"session_id":  s.state.SessionID,
			"turn_number": s.state.TurnNumber,
		})
	}

	return s.persistSessionState(messages)
}

// GetSessionID returns the stable identifier for this session.
func (s *Session) GetSessionID() types.SessionID {
	return s.state.SessionID
}

func (s *Session) enforceMaxTurns() error {
	if s == nil || s.config == nil {
		return nil
	}
	maxTurns := s.config.MaxTurns
	if maxTurns <= 0 {
		return nil
	}
	if s.state.TurnNumber >= maxTurns {
		return fmt.Errorf("session turn limit reached: completed %d turns (max %d)", s.state.TurnNumber, maxTurns)
	}
	return nil
}
