package sdk

import (
	"context"
	"errors"

	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	modeexec "github.com/KPO-Tech/seshat-runtime/internal/modes/execution"
)

// Session represents a multi-turn conversation session.
type Session struct {
	client  *Client
	session *engine.Session
}

// SubmitMessage sends a plain-text user message and runs the turn it starts: the model answers, calls the tools it asks for, and the response (messages, tool uses and results, usage, stop reason) comes back when the turn ends. It streams through the callbacks and queues set on the session, and ctx cancels the turn.
func (s *Session) SubmitMessage(ctx context.Context, content string) (*SessionResponse, error) {
	response, err := s.session.SubmitMessage(ctx, content)
	if err != nil {
		return nil, err
	}
	return &SessionResponse{
		Messages:          response.Messages,
		StopReason:        response.StopReason,
		ToolUses:          response.ToolUses,
		ToolResults:       response.ToolResults,
		Usage:             response.Usage,
		TotalTokens:       response.TotalTokens,
		TurnNumber:        response.TurnNumber,
		IsComplete:        response.IsComplete(),
		Compacted:         response.Compacted,
		CompactPreTokens:  response.CompactPreTokens,
		CompactPostTokens: response.CompactPostTokens,
	}, nil
}

// SubmitMessageWithContent is SubmitMessage for a message that also carries images.
func (s *Session) SubmitMessageWithContent(ctx context.Context, text string, images []ImageContent) (*SessionResponse, error) {
	response, err := s.session.SubmitMessageWithContent(ctx, text, images)
	if err != nil {
		return nil, err
	}
	return &SessionResponse{
		Messages:          response.Messages,
		StopReason:        response.StopReason,
		ToolUses:          response.ToolUses,
		ToolResults:       response.ToolResults,
		Usage:             response.Usage,
		TotalTokens:       response.TotalTokens,
		TurnNumber:        response.TurnNumber,
		IsComplete:        response.IsComplete(),
		Compacted:         response.Compacted,
		CompactPreTokens:  response.CompactPreTokens,
		CompactPostTokens: response.CompactPostTokens,
	}, nil
}

// RegisterTool makes tool available to the model in this session. It returns an error when the tool cannot be registered.
func (s *Session) RegisterTool(tool Tool) error {
	return s.session.RegisterTool(tool)
}

// UnregisterTool removes the tool called name from this session. It does nothing on a nil session.
func (s *Session) UnregisterTool(name string) error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.UnregisterTool(name)
}

// GetID returns the identifier of the session.
func (s *Session) GetID() SessionID {
	return s.session.GetMetadata().ID
}

// GetMessages returns the conversation so far, in order.
func (s *Session) GetMessages() []Message {
	return s.session.GetMessages()
}

// SessionHistory is the whole conversation of a session: Archived holds the messages that compactions took out of what
// the model sees, oldest first, and Current holds the messages the model sees now.
type SessionHistory = engine.SessionHistory

// History returns the whole conversation, including the messages that compactions archived. GetMessages returns only
// what the model sees. Without a session store that keeps an archive, Archived is empty.
func (s *Session) History() (SessionHistory, error) {
	return s.session.History()
}

// Close ends the session: it cancels a turn that is running, persists the memory of the session and releases what it holds (the browser session, the event queues). It is idempotent.
func (s *Session) Close() error {
	return s.session.Close()
}

// Interrupt cancels the turn that is running and marks the session as interrupted. It can be called from another goroutine while SubmitMessage is waiting.
func (s *Session) Interrupt() error {
	return s.session.Interrupt()
}

// GetStatus returns the current status of the session, or an empty status when the session has no metadata.
func (s *Session) GetStatus() SessionStatus {
	metadata := s.session.GetMetadata()
	if metadata == nil {
		return ""
	}
	return metadata.Status
}

// GetMetadata returns the metadata the session keeps about itself, its identifier and status among others.
func (s *Session) GetMetadata() *SessionMetadata {
	return s.session.GetMetadata()
}

// GetPermissionMode returns the permission mode of the session, which decides which tool calls are asked about.
func (s *Session) GetPermissionMode() PermissionMode {
	return s.session.GetPermissionMode()
}

// GetExecutionMode returns the execution mode of the session.
func (s *Session) GetExecutionMode() ExecutionMode {
	return ExecutionMode(s.session.GetExecutionMode())
}

// GetPermissionContext returns the permission context of the session: the mode and the rules its tool calls are checked against.
func (s *Session) GetPermissionContext() *PermissionContext {
	return s.session.GetPermissionContext()
}

// ForcePlanMode enters plan mode in the session context directly, allowing
// the host to force a session into plan mode without waiting for the model
// to call enter_plan_mode itself.
func (s *Session) ForcePlanMode() {
	if s == nil || s.session == nil {
		return
	}
	s.session.ForcePlanMode()
	// Entering starts a new plan cycle: a review left pending from a previous
	// cycle must not stay "awaiting approval" and block exit_plan_mode before
	// this cycle's plan has even been submitted. The engine-level setter cannot
	// reach the plan review state, so it is reset here where the session id is
	// known.
	modeexec.ClearPlanReview(s.session.GetSessionID())
}

// ClearPlanMode exits plan mode in the session context, restoring the previous
// permission mode. This allows the host to pre-exit plan mode on user approval
// without the model needing to call exit_plan_mode itself.
func (s *Session) ClearPlanMode() {
	if s == nil || s.session == nil {
		return
	}
	s.session.ClearPlanMode()
}

// ApprovePlan records the user's acceptance of the plan the model submitted with
// submit_plan, for the given session. exit_plan_mode is gated on that decision,
// and the SDK exposes no review surface of its own, so a host that shows the plan
// to the user calls this when the user accepts it - otherwise the model's
// exit_plan_mode stays refused and the only way out is ClearPlanMode, which
// leaves the mode without recording the decision. It does not itself leave plan
// mode; the model's exit_plan_mode (or ClearPlanMode) applies that change.
//
// It is a package-level function, not only a Session method, because a host that
// persists its sessions reloads them per turn: between turns there is no live
// *Session to call it on, while the review state it settles is process-global
// and keyed by session id - the plan approval a user clicks between two turns
// still has to reach the next turn.
func ApprovePlan(sessionID SessionID) {
	modeexec.MarkPlanApproved(sessionID)
}

// CompactionOutcome is what a manual compaction did: the size of the conversation before and after, in tokens.
type CompactionOutcome struct {
	PreTokens  int `json:"pre_tokens"`
	PostTokens int `json:"post_tokens"`
}

// ErrNothingToCompact is returned by Compact when the conversation is too short to shrink.
var ErrNothingToCompact = engine.ErrNothingToCompact

// ErrCompactionWouldNotShrink is returned by Compact when summarizing would leave the conversation no smaller, as with
// a small conversation. Nothing was changed.
var ErrCompactionWouldNotShrink = engine.ErrCompactionWouldNotShrink

// Compact summarizes the older part of the conversation now, instead of waiting for the automatic threshold, and
// keeps the most recent messages as they are. It refuses while a turn is running. The host receives
// context.compacting and then context.compacted events. It returns an error on a nil session.
func (s *Session) Compact(ctx context.Context) (CompactionOutcome, error) {
	if s == nil || s.session == nil {
		return CompactionOutcome{}, errors.New("nil session")
	}
	out, err := s.session.Compact(ctx)
	return CompactionOutcome{PreTokens: out.PreTokens, PostTokens: out.PostTokens}, err
}

// ApprovePlan records the user's acceptance of the plan this session submitted
// with submit_plan. See the package-level ApprovePlan. It does nothing on a nil
// session.
func (s *Session) ApprovePlan() {
	if s == nil || s.session == nil {
		return
	}
	ApprovePlan(s.session.GetSessionID())
}

// SetPermissionMode changes the permission mode of the session from the next tool call on. It does nothing on a nil session.
func (s *Session) SetPermissionMode(mode PermissionMode) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetPermissionMode(mode)
}

// SetSystemPromptTemplate sets a template that fully replaces the default system prompt of this session. An empty text clears the override. It does nothing on a nil session.
func (s *Session) SetSystemPromptTemplate(text string) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetSystemPromptTemplate(text)
}

// SetAppendSystemPrompt sets text that is appended to the system prompt of this session. It does nothing on a nil session.
func (s *Session) SetAppendSystemPrompt(text string) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetAppendSystemPrompt(text)
}

// SetWorkingDirectory sets the directory the tools of the session work in (files, shell) from now on. It does nothing on a nil session.
func (s *Session) SetWorkingDirectory(path string) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetWorkingDirectory(path)
}

// GetTurnNumber returns the number of turns the session has run, or 0 on a nil session.
func (s *Session) GetTurnNumber() int {
	if s == nil || s.session == nil {
		return 0
	}
	return s.session.GetTurnNumber()
}

// GetTotalTokens returns the tokens the session has used so far, or 0 on a nil session.
func (s *Session) GetTotalTokens() int {
	if s == nil || s.session == nil {
		return 0
	}
	return s.session.GetTotalTokens()
}

// GetToolNames returns the names of the tools available in the session.
func (s *Session) GetToolNames() []string {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.GetToolNames()
}

// SetProgressFn sets the function called with the progress of a running tool. It does nothing on a nil session.
func (s *Session) SetProgressFn(progressFn func(ToolProgress)) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetProgressCallback(progressFn)
}

// SetResponseChunkFn sets the function called with each chunk of the model's response as it streams in. It does nothing on a nil session.
func (s *Session) SetResponseChunkFn(chunkFn func(ResponseChunk)) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetResponseChunkCallback(chunkFn)
}

// SetRuntimeEventFn sets the function called with each structured runtime event of the session. It does nothing on a nil session.
func (s *Session) SetRuntimeEventFn(runtimeEventFn func(RuntimeEvent)) {
	if s == nil || s.session == nil {
		return
	}
	s.session.SetRuntimeEventCallback(runtimeEventFn)
}

// GetEventQueue returns the streaming event queue of the session, which a caller can read instead of setting a callback.
func (s *Session) GetEventQueue() *EventQueue {
	return s.session.GetEventQueue()
}

// GetRuntimeEventQueue returns the structured runtime event queue of the session, which a caller can read instead of setting a callback.
func (s *Session) GetRuntimeEventQueue() *RuntimeEventQueue {
	return s.session.GetRuntimeEventQueue()
}

// newSDKSession wraps an engine session with the client's default callbacks.
func newSDKSession(c *Client, querySession *engine.Session) *Session {
	s := &Session{client: c, session: querySession}
	s.SetProgressFn(c.config.ProgressFn)
	s.SetResponseChunkFn(c.config.ResponseChunkFn)
	s.SetRuntimeEventFn(c.config.RuntimeEventFn)
	return s
}
