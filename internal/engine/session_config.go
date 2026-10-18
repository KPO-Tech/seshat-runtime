package engine

import (
	"fmt"
	"sort"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/modes"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// --- callbacks ---

// SetProgressCallback wires a host-level observer for tool execution progress.
func (s *Session) SetProgressCallback(fn func(types.ToolProgress)) {
	if s == nil {
		return
	}
	s.progressCallback = fn
}

// SetResponseChunkCallback wires a host-level observer for live model chunks.
func (s *Session) SetResponseChunkCallback(fn func(types.APIResponseChunk)) {
	if s == nil {
		return
	}
	s.responseChunkCallback = fn
}

// SetRuntimeEventCallback wires a host-level observer for structured runtime events.
func (s *Session) SetRuntimeEventCallback(fn func(types.RuntimeEvent)) {
	if s == nil {
		return
	}
	s.runtimeEventCallback = fn
}

// --- queues ---

// GetEventQueue returns the session's streaming event queue.
func (s *Session) GetEventQueue() *execution.EventQueue {
	return s.eventQueue
}

// GetRuntimeEventQueue returns the session's structured runtime event queue.
func (s *Session) GetRuntimeEventQueue() *execution.RuntimeEventQueue {
	return s.runtimeEventQueue
}

// --- tool registration ---

func (s *Session) RegisterTool(t tool.Tool) error {
	def := t.Definition()
	if promptAware, ok := t.(promptAwareTool); ok {
		promptAware.SetPromptFn(s.engine.promptFn)
	}

	if _, exists := s.state.Tools[def.Name]; exists {
		return fmt.Errorf("tool '%s' already registered", def.Name)
	}
	s.state.Tools[def.Name] = t

	for _, alias := range def.Aliases {
		if _, exists := s.state.Tools[alias]; exists {
			return fmt.Errorf("tool alias '%s' conflicts with existing tool", alias)
		}
		s.state.Tools[alias] = t
	}
	return nil
}

func (s *Session) RegisterTools(tools []tool.Tool) error {
	for _, t := range tools {
		if err := s.RegisterTool(t); err != nil {
			return err
		}
	}
	return nil
}

// UnregisterTool removes a tool from the session by name.
func (s *Session) UnregisterTool(name string) error {
	if s == nil || s.state == nil {
		return fmt.Errorf("session is nil")
	}
	if _, ok := s.state.Tools[name]; !ok {
		return types.NewError(types.ErrCodeToolNotFound, fmt.Sprintf("tool %q not found", name))
	}
	delete(s.state.Tools, name)
	return nil
}

// --- metadata accessors ---

func (s *Session) GetMetadata() *types.SessionMetadata {
	return s.state.MetadataSnapshot()
}

func (s *Session) GetPermissionMode() types.PermissionMode {
	return s.state.CurrentPermissionMode()
}

func (s *Session) GetExecutionMode() string {
	return s.state.CurrentExecutionMode()
}

func (s *Session) GetPermissionContext() *types.PermissionContext {
	return s.state.PermissionContextSnapshot()
}

func (s *Session) GetMessages() []types.Message {
	return s.state.CloneMessages()
}

// SessionHistory is the whole conversation of a session: Archived holds the messages compactions took out of what the
// model sees, oldest first, and Current holds the messages the model sees now.
type SessionHistory struct {
	Archived []types.Message `json:"archived"`
	Current  []types.Message `json:"current"`
}

// History returns the whole conversation, including what compactions archived. Without a store that keeps an
// archive, Archived is empty and Current is GetMessages.
func (s *Session) History() (SessionHistory, error) {
	history := SessionHistory{Current: s.state.CloneMessages()}
	loader, ok := s.store.(historyLoader)
	if !ok || s.store == nil {
		return history, nil
	}
	archived, _, err := loader.LoadHistory(s.state.SessionID)
	if err != nil {
		return SessionHistory{}, err
	}
	history.Archived = archived
	return history, nil
}

func (s *Session) GetTurnNumber() int {
	if s == nil || s.state == nil {
		return 0
	}
	return s.state.TurnNumber
}

func (s *Session) GetTotalTokens() int {
	if s == nil || s.state == nil {
		return 0
	}
	return s.state.TotalTokens
}

func (s *Session) GetToolNames() []string {
	if s == nil || s.state == nil || len(s.state.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.state.Tools))
	for name := range s.state.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- setters ---

func (s *Session) SetPermissionMode(mode types.PermissionMode) {
	if s == nil || s.state == nil {
		return
	}
	ctx := s.state.PermissionContextSnapshot()
	if ctx == nil {
		ctx = &types.PermissionContext{}
	}
	ctx.Mode = mode
	s.state.SetPermissionContext(ctx)
}

// ForcePlanMode enters plan mode in the session permission context directly.
// This mirrors what enter_plan_mode.Call does via its ContextModifier,
// allowing a host application to force a session into plan mode without
// waiting for the model to call enter_plan_mode itself. Like ClearPlanMode
// below, this stays a pure state mutation — no runtime event is emitted and
// no plan-file bookkeeping (internal/modes/execution's planCache) is touched,
// since both live behind a context.Context-scoped emitter/session id not
// available to this parameterless setter. Safe to call unconditionally
// (including when already in plan mode, which is a harmless no-op: Mode
// itself is untouched by plan mode, only PrePlanMode/ExecutionMode change).
func (s *Session) ForcePlanMode() {
	if s == nil || s.state == nil {
		return
	}
	ctx := s.state.PermissionContextSnapshot()
	if ctx == nil {
		ctx = &types.PermissionContext{Mode: types.PermissionModeOnRequest}
	}
	ctx.PrePlanMode = ctx.Mode
	ctx.ExecutionMode = string(modes.ExecutionModePlan)
	if ctx.Mode == types.PermissionModeBypass {
		ctx.IsBypassPermissionsModeAvailable = true
	}
	s.state.SetPermissionContext(ctx)
}

// ClearPlanMode exits plan mode in the session permission context, restoring
// the previous permission mode. This mirrors what exit_plan_mode.Call does via
// its ContextModifier, allowing the TUI to pre-exit plan mode on user approval
// without waiting for the model to call exit_plan_mode itself.
func (s *Session) ClearPlanMode() {
	if s == nil || s.state == nil {
		return
	}
	ctx := s.state.PermissionContextSnapshot()
	if ctx == nil {
		return
	}
	restoreMode := ctx.PrePlanMode
	if restoreMode == "" {
		restoreMode = types.PermissionModeOnRequest
	}
	ctx.Mode = restoreMode
	ctx.PrePlanMode = ""
	ctx.ExecutionMode = string(modes.ExecutionModeExecute)
	s.state.SetPermissionContext(ctx)
}

// SetSystemPromptTemplate sets a per-session override that fully replaces the
// default system prompt. Pass an empty string to clear the override.
func (s *Session) SetSystemPromptTemplate(text string) {
	if s == nil {
		return
	}
	if text == "" {
		s.systemPromptTemplateOverride = nil
	} else {
		s.systemPromptTemplateOverride = &text
	}
}

// SetAppendSystemPrompt sets a per-session override for the append system prompt.
func (s *Session) SetAppendSystemPrompt(text string) {
	if s == nil {
		return
	}
	if text == "" {
		s.appendSystemPromptOverride = nil
	} else {
		s.appendSystemPromptOverride = &text
	}
}

// SetWorkingDirectory overrides the working directory for this session's turns.
func (s *Session) SetWorkingDirectory(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "" {
		s.workingDirectoryOverride = nil
	} else {
		s.workingDirectoryOverride = &path
	}
}

func (s *Session) currentTurnNumber() int {
	if s == nil || s.state == nil {
		return 0
	}
	return s.state.TurnNumber + 1
}
