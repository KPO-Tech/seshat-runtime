// Package workspace implements the Workspace interface backed by pkg/sdk.
//
// SeshatWorkspace is the active implementation (Option A — SDK-only path).
// All LLM traffic flows through pkg/sdk.Client; the Fantasy-based agent
// package (internal/seshattui/agent/) is NOT used here.
package workspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/KPO-Tech/seshat-runtime/internal/modes/execution"
	internalproviders "github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/builtin"
	worktreePkg "github.com/KPO-Tech/seshat-runtime/internal/tools/special/worktree"
	tasktool "github.com/KPO-Tech/seshat-runtime/internal/tools/task"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"github.com/KPO-Tech/seshat-runtime/tui/cmd/cli/appdir"
	tuiTools "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/agent/tools"
	mcptools "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/agent/tools/mcp"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/csync"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/history"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/lsp"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/message"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/oauth"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/permission"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/planreview"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/pubsub"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/session"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/skills"
	"github.com/google/uuid"
)

type subAgentStreamState struct {
	reasoning string
	content   string
}

type askUserSurveyState struct {
	Answers map[string]string
}

// Verify compile-time that SeshatWorkspace implements Workspace.
var _ Workspace = (*SeshatWorkspace)(nil)

// SeshatWorkspace adapts the Seshat SDK to the Workspace interface.
// It keeps an in-memory message store and publishes pubsub events that the
// seshattui UI consumes to render the conversation.
type SeshatWorkspace struct {
	// Subagent streaming state
	subAgentStreams   map[string]*subAgentStreamState
	subAgentStreamsMu sync.Mutex

	// SDK layer
	client  *sdk.Client
	workDir string
	model   string // "provider:model" string

	// Active session
	sessMu  sync.Mutex
	session *sdk.Session

	modeMu        sync.RWMutex
	liveExecModes map[string]string

	// In-memory stores (sessionID → records)
	msgMu    sync.RWMutex
	msgStore map[string][]message.Message // complete message list per session

	sessBroker    *pubsub.Broker[session.Session]
	msgBroker     *pubsub.Broker[message.Message]
	permBroker    *pubsub.Broker[permission.PermissionRequest]
	planBroker    *pubsub.Broker[planreview.Submission]
	askUserBroker *pubsub.Broker[tuiTools.AskUserRequest]
	planStore     *memPlanStore
	sessStore     map[string]session.Session
	sessionsMu    sync.RWMutex

	// tea.Program for Subscribe
	programMu sync.Mutex
	program   *tea.Program

	// streaming state (single active assistant message)
	streamMu           sync.Mutex
	streamMsg          *message.Message // the in-progress assistant message for the current model response
	streamSess         string           // session ID of the streaming message
	streamResponseDone bool             // set after message_stop; the next model chunk starts a new assistant message
	streamToolUseID    string           // tool_use block currently being streamed by the LLM
	streamToolUseName  string
	streamToolInputBuf string // accumulated input_json_delta for the current block

	// debounce for streaming updates (33 ms)
	debounce *msgDebounce

	// busy flag
	busy atomic.Bool

	// cancel for the active submit goroutine
	submitMu     sync.Mutex
	submitCancel context.CancelFunc

	// Config (lazily built, mutex-guarded)
	cfgMu    sync.Mutex
	cfg      *config.Config
	mcpCfg   config.MCPs
	mcpStore *config.ConfigStore

	// Permission: allow-all skip flag
	permSkip atomic.Bool

	// pendingPerms maps PermissionRequest.ID → resolution channel.
	// PromptFn blocks on the channel; Grant/Deny send the response.
	pendingPerms sync.Map // map[string]chan sdk.PromptResponse

	// pendingAskUser maps AskUserRequest.ID → resolution channel.
	// PromptFn (ask_user path) blocks on the channel; AnswerAskUser sends the response.
	pendingAskUser sync.Map // map[string]chan types.PromptResponse

	// askUserSurveyAnswers buffers final wizard answers by tool use ID so the
	// ask_user tool can keep calling promptFn sequentially without re-showing UI.
	askUserSurveyAnswers sync.Map // map[string]askUserSurveyState

	// Provider registry — populated by DetectProviders() at startup.
	providerKeys     sync.Map // providerID → apiKey string (empty string = no key needed)
	providerBaseURLs sync.Map // providerID → editable/test base URL override
	ollamaMu         sync.RWMutex
	ollamaModels     []catwalk.Model

	// Options stored for client rebuilds on model/provider switch.
	sqlitePath string
	permMode   sdk.PermissionMode
	monitoring *sdk.MonitoringSystem

	imageGeneration config.ImageGenerationConfig
	textToSpeech    config.TextToSpeechConfig
	speechToText    config.SpeechToTextConfig
}

// ─── Constructor ──────────────────────────────────────────────────────────────

// NewSeshatWorkspace creates a workspace backed by the given SDK client.
// modelStr is the "provider:model" string shown in the UI header.
func NewSeshatWorkspace(client *sdk.Client, workDir, modelStr string) *SeshatWorkspace {
	w := &SeshatWorkspace{
		client:          client,
		workDir:         workDir,
		model:           modelStr,
		subAgentStreams: make(map[string]*subAgentStreamState),
		msgStore:        make(map[string][]message.Message),
		sessStore:       make(map[string]session.Session),
		liveExecModes:   make(map[string]string),
		sessBroker:      pubsub.NewBroker[session.Session](),
		msgBroker:       pubsub.NewBroker[message.Message](),
		permBroker:      pubsub.NewBroker[permission.PermissionRequest](),
		planBroker:      pubsub.NewBroker[planreview.Submission](),
		askUserBroker:   pubsub.NewBroker[tuiTools.AskUserRequest](),
		planStore:       newMemPlanStore(),
	}
	w.debounce = newMsgDebounce(33*time.Millisecond, func(msg message.Message, sessID string) {
		w.publishMsg(pubsub.UpdatedEvent, sessID, msg)
	})
	return w
}

// PlanStore returns the workspace's plan persistence backend. Use this to
// inject the same store into the initial sdk.ClientConfig created outside
// the workspace (e.g. cmd/cli/tui_seshat.go).
func (w *SeshatWorkspace) PlanStore() sdk.PlanStore {
	return w.planStore
}

// ─── Subscribe / event fan-out ────────────────────────────────────────────────

func (w *SeshatWorkspace) Subscribe(p *tea.Program) {
	w.programMu.Lock()
	w.program = p
	w.programMu.Unlock()

	ctx := context.Background()

	// Fan out session events.
	go func() {
		ch := w.sessBroker.Subscribe(ctx)
		for ev := range ch {
			p.Send(ev)
		}
	}()

	// Fan out message events.
	go func() {
		ch := w.msgBroker.Subscribe(ctx)
		for ev := range ch {
			p.Send(ev)
		}
	}()

	// Fan out permission events.
	go func() {
		ch := w.permBroker.Subscribe(ctx)
		for ev := range ch {
			p.Send(ev)
		}
	}()

	// Fan out plan review events.
	go func() {
		ch := w.planBroker.Subscribe(ctx)
		for ev := range ch {
			p.Send(ev)
		}
	}()

	// Fan out ask_user_question events.
	go func() {
		ch := w.askUserBroker.Subscribe(ctx)
		for ev := range ch {
			p.Send(ev)
		}
	}()

	// Fan out MCP state-change events so the UI reacts in real time.
	go func() {
		ch := mcptools.SubscribeEvents(ctx)
		for ev := range ch {
			p.Send(ev)
		}
	}()
}

func (w *SeshatWorkspace) Shutdown() {
	w.sessBroker.Shutdown()
	w.msgBroker.Shutdown()
	w.permBroker.Shutdown()
	w.planBroker.Shutdown()
	w.askUserBroker.Shutdown()
	if w.client != nil {
		_ = w.client.Close()
	}
}

func (w *SeshatWorkspace) ExecutionMode() string {
	w.sessMu.Lock()
	defer w.sessMu.Unlock()
	if w.session == nil {
		return string(sdk.ExecutionModeExecute)
	}
	if mode := w.liveExecutionMode(string(w.session.GetID())); mode != "" {
		return mode
	}
	return string(w.session.GetExecutionMode())
}

// SetPlanMode enters or leaves plan mode on the live session at the user's own
// request. It is the user's way into plan mode - besides starting with
// --mode plan - and it is what makes plan mode a collaboration surface for
// designing a plan rather than a gate the agent opens by itself: the agent's
// own enter_plan_mode is deliberately permission-free, so without this the user
// had no way to ask for the collaboration.
//
// Both directions go through the SDK's public ForcePlanMode/ClearPlanMode,
// which is the same state change enter_plan_mode.Call and exit_plan_mode.Call
// make through their ContextModifiers. No approval is involved in either
// direction: the user asked for it.
func (w *SeshatWorkspace) SetPlanMode(on bool) error {
	w.sessMu.Lock()
	sess := w.session
	w.sessMu.Unlock()
	if sess == nil {
		return fmt.Errorf("no active session")
	}

	mode := string(sdk.ExecutionModeExecute)
	if on {
		sess.ForcePlanMode()
		mode = string(sdk.ExecutionModePlan)
	} else {
		sess.ClearPlanMode()
	}
	// Mirror what a RuntimeEventTypeExecutionModeChanged event does for the
	// model-driven transitions, so ExecutionMode() reports the user's switch
	// immediately instead of waiting for the next runtime event.
	w.setLiveExecutionMode(string(sess.GetID()), mode)
	return nil
}

func (w *SeshatWorkspace) WorktreePath() string {
	w.sessMu.Lock()
	sess := w.session
	w.sessMu.Unlock()
	if sess == nil {
		return ""
	}
	wt := worktreePkg.GetSession(types.SessionID(sess.GetID()))
	if wt == nil {
		return ""
	}
	return wt.WorktreePath
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) CreateSession(ctx context.Context, title string) (session.Session, error) {
	sess, err := w.client.CreateSession(ctx)
	if err != nil {
		return session.Session{}, fmt.Errorf("create session: %w", err)
	}
	id := string(sess.GetID())
	// Errors are intentionally swallowed — the DB is the authoritative store
	// and a missing subdirectory only causes a deferred tool-level error.
	_ = appdir.EnsureSessionDir(id)
	now := time.Now().UnixMilli()
	s := session.Session{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	w.sessionsMu.Lock()
	w.sessStore[id] = s
	w.sessionsMu.Unlock()

	w.sessMu.Lock()
	w.session = sess
	w.sessMu.Unlock()

	w.sessBroker.Publish(pubsub.CreatedEvent, s)
	return s, nil
}

func (w *SeshatWorkspace) GetSession(_ context.Context, sessionID string) (session.Session, error) {
	w.sessionsMu.RLock()
	s, ok := w.sessStore[sessionID]
	w.sessionsMu.RUnlock()
	if ok {
		return s, nil
	}
	return session.Session{}, fmt.Errorf("session %q not found", sessionID)
}

func (w *SeshatWorkspace) ListSessions(_ context.Context) ([]session.Session, error) {
	infos, err := w.client.ListSessions()
	if err != nil {
		return nil, err
	}
	result := conversationSessions(infos, time.Now())
	w.sessionsMu.Lock()
	for _, s := range result {
		w.sessStore[s.ID] = s
	}
	w.sessionsMu.Unlock()
	return result, nil
}

func (w *SeshatWorkspace) SaveSession(_ context.Context, sess session.Session) (session.Session, error) {
	w.sessionsMu.Lock()
	w.sessStore[sess.ID] = sess
	w.sessionsMu.Unlock()
	w.sessBroker.Publish(pubsub.UpdatedEvent, sess)
	return sess, nil
}

func (w *SeshatWorkspace) DeleteSession(_ context.Context, sessionID string) error {
	w.sessMu.Lock()
	active := w.session != nil && string(w.session.GetID()) == sessionID
	var sdkSess *sdk.Session
	if active {
		sdkSess = w.session
		w.session = nil
	}
	w.sessMu.Unlock()

	if sdkSess != nil {
		_ = sdkSess.Interrupt()
		_ = sdkSess.Close()
	}
	// The sessions this one launched are not listed, so they could not be deleted
	// on their own any more: they go with it. Best effort, the conversation is what
	// the user asked to delete.
	if infos, listErr := w.client.ListSessions(); listErr == nil {
		for _, child := range descendantSessionIDs(infos, sessionID) {
			_ = w.client.DeleteSession(sdk.SessionID(child))
		}
	}
	if err := w.client.DeleteSession(sdk.SessionID(sessionID)); err != nil {
		return err
	}

	w.sessionsMu.Lock()
	s := w.sessStore[sessionID]
	delete(w.sessStore, sessionID)
	w.sessionsMu.Unlock()

	w.msgMu.Lock()
	delete(w.msgStore, sessionID)
	w.msgMu.Unlock()

	w.sessBroker.Publish(pubsub.DeletedEvent, s)
	// Remove workspaces/{id}/ from disk; DB cascade already cleaned up all records.
	appdir.DeleteSessionDir(sessionID)
	return nil
}

func (w *SeshatWorkspace) SetCurrentSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	// Fast path: session is already active.
	w.sessMu.Lock()
	if w.session != nil && string(w.session.GetID()) == sessionID {
		w.sessMu.Unlock()
		return nil
	}
	w.sessMu.Unlock()

	sess, err := w.client.LoadSession(ctx, sdk.SessionID(sessionID))
	if err != nil {
		return fmt.Errorf("activate session %q: %w", sessionID, err)
	}
	w.LoadSessionMessages(sessionID, sess.GetMessages())
	w.syncSessionTodos(sessionID)
	_ = appdir.EnsureSessionDir(sessionID)
	w.sessMu.Lock()
	w.session = sess
	w.sessMu.Unlock()
	return nil
}

// Agent tool session IDs use a simple "msgID:toolID" encoding.
func (w *SeshatWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return messageID + ":" + toolCallID
}

func (w *SeshatWorkspace) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	parts := strings.SplitN(sessionID, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ─── Messages ─────────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) ListMessages(_ context.Context, sessionID string) ([]message.Message, error) {
	w.msgMu.RLock()
	msgs := make([]message.Message, len(w.msgStore[sessionID]))
	copy(msgs, w.msgStore[sessionID])
	w.msgMu.RUnlock()
	return msgs, nil
}

func (w *SeshatWorkspace) ListUserMessages(_ context.Context, sessionID string) ([]message.Message, error) {
	w.msgMu.RLock()
	all := w.msgStore[sessionID]
	var result []message.Message
	for _, m := range all {
		if m.Role == message.User {
			result = append(result, m)
		}
	}
	w.msgMu.RUnlock()
	return result, nil
}

func (w *SeshatWorkspace) ListAllUserMessages(_ context.Context) ([]message.Message, error) {
	w.msgMu.RLock()
	var result []message.Message
	for _, msgs := range w.msgStore {
		for _, m := range msgs {
			if m.Role == message.User {
				result = append(result, m)
			}
		}
	}
	w.msgMu.RUnlock()
	return result, nil
}

// ─── Agent ────────────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) AgentRun(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) error {
	w.sessMu.Lock()
	sess := w.session
	w.sessMu.Unlock()

	if sess == nil || string(sess.GetID()) != sessionID {
		return fmt.Errorf("session %q not active", sessionID)
	}
	if w.busy.Load() {
		return fmt.Errorf("agent busy")
	}
	w.busy.Store(true)

	persistedAttachments, err := persistEphemeralSessionAttachments(sessionID, attachments)
	if err != nil {
		w.busy.Store(false)
		return fmt.Errorf("persist attachments: %w", err)
	}

	submittedPrompt := message.PromptWithTextAttachments(prompt, persistedAttachments)
	images := imageContentsFromAttachments(persistedAttachments)

	// Record the user message.
	now := time.Now().UnixMilli()
	userMsg := newUserMessage(sessionID, prompt, persistedAttachments, now)
	w.appendMsg(sessionID, userMsg)
	w.msgBroker.Publish(pubsub.CreatedEvent, userMsg)

	// Create the assistant message placeholder for the first model response.
	asstMsg := w.newStreamingAssistantMessage(sessionID)

	w.streamMu.Lock()
	w.streamMsg = &asstMsg
	w.streamSess = sessionID
	w.streamResponseDone = false
	w.streamMu.Unlock()

	submitCtx, cancel := context.WithCancel(ctx)
	w.submitMu.Lock()
	w.submitCancel = cancel
	w.submitMu.Unlock()

	go func() {
		defer func() {
			w.submitMu.Lock()
			w.submitCancel = nil
			w.submitMu.Unlock()
			cancel()
			w.busy.Store(false)
			w.debounce.forceFlush()
		}()

		// Wait for MCP servers to finish initializing before the first turn
		// so the engine already has the tools registered. Bounded to avoid
		// blocking the turn indefinitely if a server is slow.
		if w.mcpStore != nil {
			waitCtx, waitCancel := context.WithTimeout(submitCtx, 5*time.Second)
			_ = mcptools.WaitForInit(waitCtx)
			waitCancel()
		}

		var (
			resp *sdk.SessionResponse
			err  error
		)
		if len(images) > 0 {
			resp, err = sess.SubmitMessageWithContent(submitCtx, submittedPrompt, images)
		} else {
			resp, err = sess.SubmitMessage(submitCtx, submittedPrompt)
		}

		w.streamMu.Lock()
		cur := w.streamMsg
		w.streamMsg = nil
		w.streamSess = ""
		w.streamMu.Unlock()

		if cur == nil {
			return
		}

		finishReason := message.FinishReasonEndTurn
		if err != nil {
			if submitCtx.Err() != nil {
				finishReason = message.FinishReasonCanceled
			} else {
				finishReason = message.FinishReasonError
			}
		} else if resp != nil {
			switch resp.StopReason {
			case "max_tokens":
				finishReason = message.FinishReasonMaxTokens
			case "tool_use":
				finishReason = message.FinishReasonToolUse
			}
		}

		// Mark any tool calls that never received an execution result as
		// finished so their spinners stop (e.g. truncated JSON stream).
		// Inject a synthetic error result so the tool shows an ERROR icon.
		errMsg := "aborted"
		if finishReason == message.FinishReasonError {
			errMsg = "stream error"
		}
		for i, part := range cur.Parts {
			if tc, ok := part.(message.ToolCall); ok && !tc.Finished {
				cur.Parts[i] = message.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input, Finished: true}
				cur.Parts = append(cur.Parts, message.ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    errMsg,
					IsError:    true,
				})
			}
		}

		finish := message.Finish{
			Reason: finishReason,
			Time:   time.Now().UnixMilli(),
		}
		if finishReason == message.FinishReasonError {
			finish.Message, finish.Details = finishErrorText(err)
		}
		cur.Parts = append(cur.Parts, finish)
		cur.UpdatedAt = time.Now().UnixMilli()
		w.updateMsg(sessionID, *cur)
		w.msgBroker.Publish(pubsub.UpdatedEvent, *cur)

		// Update session record.
		w.sessionsMu.Lock()
		if s, ok := w.sessStore[sessionID]; ok {
			s.UpdatedAt = time.Now().UnixMilli()
			if resp != nil && resp.Usage != nil {
				s.PromptTokens += int64(resp.Usage.InputTokens)
				s.CompletionTokens += int64(resp.Usage.OutputTokens)
			}
			w.sessStore[sessionID] = s
			w.sessBroker.Publish(pubsub.UpdatedEvent, s)
		}
		w.sessionsMu.Unlock()
	}()
	return nil
}

const textAttachmentSystemInfo = "\n<system_info>The files below have been attached by the user, consider them in your response</system_info>\n"

var textAttachmentBlockRE = regexp.MustCompile(`(?s)<file(?: path='([^']*)')?>\n\n(.*?)\n</file>\n?`)
var ephemeralPasteNameRE = regexp.MustCompile(`^paste_\d+\.[A-Za-z0-9]+$`)

func persistEphemeralSessionAttachments(sessionID string, attachments []message.Attachment) ([]message.Attachment, error) {
	persisted := make([]message.Attachment, len(attachments))
	copy(persisted, attachments)
	for i, attachment := range persisted {
		if !isEphemeralPasteAttachment(attachment) {
			continue
		}
		persistedPath, err := persistSessionPasteAttachment(sessionID, attachment)
		if err != nil {
			return nil, err
		}
		persisted[i].FilePath = persistedPath
	}
	return persisted, nil
}

func isEphemeralPasteAttachment(attachment message.Attachment) bool {
	if attachment.FileName == "" || attachment.FilePath == "" {
		return false
	}
	if attachment.FilePath != attachment.FileName {
		return false
	}
	return ephemeralPasteNameRE.MatchString(filepath.Base(attachment.FileName))
}

func persistSessionPasteAttachment(sessionID string, attachment message.Attachment) (string, error) {
	dir := sessionPasteDirForAttachment(sessionID, attachment)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	filename := filepath.Base(attachment.FileName)
	path, err := nextAvailablePath(dir, filename)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, attachment.Content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func sessionPasteDirForAttachment(sessionID string, attachment message.Attachment) string {
	switch {
	case attachment.IsImage():
		return runtimepath.SessionPastesImagesDir("", sessionID)
	case attachment.IsText() || strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.MimeType)), "application/json"):
		return runtimepath.SessionPastesTextDir("", sessionID)
	default:
		return runtimepath.SessionPastesOtherDir("", sessionID)
	}
}

func nextAvailablePath(dir, filename string) (string, error) {
	clean := filepath.Base(filename)
	if clean == "." || clean == string(filepath.Separator) || clean == "" {
		clean = "paste.bin"
	}
	candidate := filepath.Join(dir, clean)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate, nil
	} else if err != nil {
		return "", err
	}
	ext := filepath.Ext(clean)
	base := strings.TrimSuffix(clean, ext)
	for i := 2; ; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

func splitPromptTextAttachments(text string) (string, []message.BinaryContent) {
	prompt, tail, found := strings.Cut(text, textAttachmentSystemInfo)
	if !found {
		return text, nil
	}
	matches := textAttachmentBlockRE.FindAllStringSubmatch(tail, -1)
	if len(matches) == 0 {
		return text, nil
	}
	attachments := make([]message.BinaryContent, 0, len(matches))
	for idx, match := range matches {
		path := strings.TrimSpace(match[1])
		data := []byte(match[2])
		mimeType := detectAttachmentMime(path, data)
		if path == "" {
			path = syntheticAttachmentName(idx+1, mimeType)
		}
		attachments = append(attachments, message.BinaryContent{Path: path, MIMEType: mimeType, Data: data})
	}
	return strings.TrimSpace(prompt), attachments
}

func detectAttachmentMime(path string, data []byte) string {
	if ext := filepath.Ext(path); ext != "" {
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return mimeType
		}
	}
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data[:min(512, len(data))])
}

func syntheticAttachmentName(index int, mimeType string) string {
	ext := ".bin"
	if exts, err := mime.ExtensionsByType(strings.TrimSpace(mimeType)); err == nil && len(exts) > 0 {
		ext = exts[0]
	}
	return fmt.Sprintf("attached_file_%d%s", index, ext)
}

func binaryFromSDKImage(block sdk.ImageContent, index int) (message.BinaryContent, bool) {
	data, err := base64.StdEncoding.DecodeString(block.Source.Data)
	if err != nil {
		return message.BinaryContent{}, false
	}
	return message.BinaryContent{
		Path:     fmt.Sprintf("attached_image_%d%s", index, extensionForMIME(block.Source.MediaType, ".png")),
		MIMEType: block.Source.MediaType,
		Data:     data,
	}, true
}

func extensionForMIME(mimeType, fallback string) string {
	exts, err := mime.ExtensionsByType(strings.TrimSpace(mimeType))
	if err != nil || len(exts) == 0 {
		return fallback
	}
	return exts[0]
}

func newUserMessage(sessionID, prompt string, attachments []message.Attachment, now int64) message.Message {
	parts := []message.ContentPart{message.TextContent{Text: prompt}}
	for _, attachment := range attachments {
		path := attachment.FilePath
		if path == "" {
			path = attachment.FileName
		}
		parts = append(parts, message.BinaryContent{
			Path:     path,
			MIMEType: attachment.MimeType,
			Data:     slices.Clone(attachment.Content),
		})
	}
	return message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      message.User,
		Parts:     parts,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func imageContentsFromAttachments(attachments []message.Attachment) []types.ImageContent {
	images := make([]types.ImageContent, 0)
	for _, attachment := range attachments {
		if !attachment.IsImage() {
			continue
		}
		img := types.ImageContent{}
		img.Source.Type = "base64"
		img.Source.MediaType = attachment.MimeType
		img.Source.Data = base64.StdEncoding.EncodeToString(attachment.Content)
		images = append(images, img)
	}
	return images
}

func (w *SeshatWorkspace) AgentCancel(sessionID string) {
	w.submitMu.Lock()
	cancel := w.submitCancel
	w.submitMu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.sessMu.Lock()
	sess := w.session
	w.sessMu.Unlock()
	if sess != nil && string(sess.GetID()) == sessionID {
		_ = sess.Interrupt()
	}
	w.busy.Store(false)
}

func (w *SeshatWorkspace) AgentIsBusy() bool { return w.busy.Load() }
func (w *SeshatWorkspace) AgentIsSessionBusy(sessionID string) bool {
	if !w.busy.Load() {
		return false
	}
	w.sessMu.Lock()
	defer w.sessMu.Unlock()
	return w.session != nil && string(w.session.GetID()) == sessionID
}

func (w *SeshatWorkspace) AgentIsReady() bool                               { return w.client != nil }
func (w *SeshatWorkspace) AgentSummarize(_ context.Context, _ string) error { return nil }
func (w *SeshatWorkspace) InitCoderAgent(_ context.Context) error           { return nil }

// ApprovePlan exits plan mode immediately on behalf of the agent — identical
// in effect to the agent calling exit_plan_mode — and returns the plan content
// so the TUI can include it in the approval message. This lets ctrl+y approval
// skip the agent's verbose "plan approved!" preamble entirely.
func (w *SeshatWorkspace) ApprovePlan(sessionID string) (string, error) {
	// Record the user's decision first and unconditionally. It is a fact about
	// the plan, not a consequence of the mode change below: exit_plan_mode opens
	// on this record, and a session this workspace cannot drive must still see the
	// approval it was given - otherwise the "Proceed" sent down the fallback path
	// would promise the model an exit it is refused.
	execution.MarkPlanApproved(types.SessionID(sessionID))

	w.sessMu.Lock()
	sess := w.session
	w.sessMu.Unlock()

	if sess == nil || string(sess.GetID()) != sessionID {
		return "", fmt.Errorf("session %q not active", sessionID)
	}

	// Mirror what exit_plan_mode.Call does: clear the execution mode from the
	// engine's session context so future tool calls see execute mode.
	sess.ClearPlanMode()

	// Update the disk state and the TUI's live display.
	execution.ExitPlanMode(types.SessionID(sessionID))
	w.setLiveExecutionMode(sessionID, "")
	w.publishSessionRefresh(sessionID)

	// Return the plan content so the TUI can embed it in the approval message.
	content, err := execution.GetPlan(types.SessionID(sessionID), nil)
	if err != nil {
		return "", fmt.Errorf("read plan: %w", err)
	}
	return content, nil
}

// UpdateAgentModel rebuilds the SDK client with the current w.model string.
// Called by the TUI after the user selects a new model.
func (w *SeshatWorkspace) UpdateAgentModel(ctx context.Context) error {
	provider, modelID := w.splitModel()
	apiKey := w.resolveAPIKey(provider)

	provCfg := internalproviders.GetProviderConfig(types.APIProvider(provider))
	if provCfg == nil {
		provCfg = &internalproviders.Config{Provider: types.APIProvider(provider)}
	}
	provCfg.APIKey = apiKey
	if baseURL := sdkProviderBaseURL(provider, w.resolveProviderBaseURL(provider)); baseURL != "" {
		provCfg.BaseURL = baseURL
	}

	enableMonitoring := w.monitoring != nil
	newClient, err := sdk.NewClient(&sdk.ClientConfig{
		APIKey:                   apiKey,
		Model:                    sdk.ModelIdentifier{Provider: sdk.APIProvider(provider), Model: modelID},
		PermissionMode:           w.permMode,
		AutoCompact:              true,
		PersistSessions:          true,
		SessionSQLitePath:        w.sqlitePath,
		PromptFn:                 w.PromptFn,
		ProgressFn:               w.OnProgress,
		ResponseChunkFn:          w.OnChunk,
		RuntimeEventFn:           w.OnRuntimeEvent,
		OnSessionTitled:          w.OnSessionTitled,
		TitleGenerator:           localTitleGenerator(),
		WorkingDir:               w.workDir,
		ProviderConfig:           provCfg,
		ImageGeneration:          w.currentImageGenerationConfig(),
		TextToSpeech:             w.currentTextToSpeechConfig(),
		SpeechToText:             w.currentSpeechToTextConfig(),
		EnableMonitoring:         enableMonitoring,
		Monitoring:               w.monitoring,
		PlanStore:                w.planStore,
		TrustProjectInstructions: config.IsTrustedProjectFile,
		MainAgentTools:           builtin.GeneralAgentToolNames(),
	})
	if err != nil {
		return fmt.Errorf("rebuild SDK client: %w", err)
	}

	if w.client != nil {
		w.client.Close()
	}
	w.SetSDKClient(newClient)
	return nil
}

func (w *SeshatWorkspace) AgentModel() AgentModel {
	provider, modelID := w.splitModel()
	return AgentModel{
		CatwalkCfg: catwalk.Model{
			ID:            modelID,
			Name:          modelID,
			ContextWindow: 200000,
			CanReason:     strings.Contains(strings.ToLower(modelID), "think") || strings.Contains(strings.ToLower(modelID), "o3") || strings.Contains(strings.ToLower(modelID), "o1"),
		},
		ModelCfg: config.SelectedModel{
			Model:    modelID,
			Provider: provider,
		},
	}
}

func (w *SeshatWorkspace) GetDefaultSmallModel(_ string) config.SelectedModel {
	provider, modelID := w.splitModel()
	return config.SelectedModel{Model: modelID, Provider: provider}
}

func (w *SeshatWorkspace) splitModel() (provider, model string) {
	if parts := strings.SplitN(w.model, ":", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "anthropic", w.model
}

// ─── Permissions ──────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) PermissionGrant(perm permission.PermissionRequest) bool {
	w.permBroker.Publish(pubsub.UpdatedEvent, perm)
	return w.resolvePermission(perm.ID, sdk.PromptResponse{Value: true})
}

func (w *SeshatWorkspace) PermissionGrantPersistent(perm permission.PermissionRequest) bool {
	w.permBroker.Publish(pubsub.UpdatedEvent, perm)
	return w.resolvePermission(perm.ID, sdk.PromptResponse{Value: "always"})
}

func (w *SeshatWorkspace) PermissionDeny(perm permission.PermissionRequest) bool {
	w.permBroker.Publish(pubsub.UpdatedEvent, perm)
	return w.resolvePermission(perm.ID, sdk.PromptResponse{Cancelled: true})
}

// resolvePermission sends a response on the pending channel for the given ID.
// Returns false if no pending request was found (already resolved or unknown).
func (w *SeshatWorkspace) resolvePermission(id string, resp sdk.PromptResponse) bool {
	if ch, ok := w.pendingPerms.LoadAndDelete(id); ok {
		ch.(chan sdk.PromptResponse) <- resp
		return true
	}
	return false
}

func (w *SeshatWorkspace) PermissionSkipRequests() bool        { return w.permSkip.Load() }
func (w *SeshatWorkspace) PermissionSetSkipRequests(skip bool) { w.permSkip.Store(skip) }

// ─── File Tracker (no-op) ─────────────────────────────────────────────────────

func (w *SeshatWorkspace) FileTrackerRecordRead(_ context.Context, _, _ string) {}
func (w *SeshatWorkspace) FileTrackerLastReadTime(_ context.Context, _, _ string) time.Time {
	return time.Time{}
}
func (w *SeshatWorkspace) FileTrackerListReadFiles(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

// ─── History (no-op) ─────────────────────────────────────────────────────────

func (w *SeshatWorkspace) ListSessionHistory(_ context.Context, _ string) ([]history.File, error) {
	return nil, nil
}

// ─── LSP (no-op) ─────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) LSPStart(_ context.Context, _ string)   {}
func (w *SeshatWorkspace) LSPStopAll(_ context.Context)           {}
func (w *SeshatWorkspace) LSPGetStates() map[string]LSPClientInfo { return nil }
func (w *SeshatWorkspace) LSPGetDiagnosticCounts(_ string) lsp.DiagnosticCounts {
	return lsp.DiagnosticCounts{}
}

// ─── Config ───────────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) Config() *config.Config {
	w.cfgMu.Lock()
	defer w.cfgMu.Unlock()
	if w.cfg != nil {
		return w.cfg
	}

	provider, modelID := w.splitModel()
	var cfg config.Config
	if w.mcpStore != nil && w.mcpStore.Config() != nil {
		cfg = *w.mcpStore.Config()
	}
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	if cfg.Options.TUI == nil {
		cfg.Options.TUI = &config.TUIOptions{}
	}
	if len(cfg.Models) == 0 {
		cfg.Models = make(map[config.SelectedModelType]config.SelectedModel)
	}
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Model: modelID, Provider: provider}
	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
		cfg.Models[config.SelectedModelTypeSmall] = config.SelectedModel{Model: modelID, Provider: provider}
	}
	if len(w.mcpCfg) > 0 {
		cfg.MCP = w.mcpCfg
	}

	providers := csync.NewMap[string, config.ProviderConfig]()
	if cfg.Providers != nil {
		for pid, pc := range cfg.Providers.Seq2() {
			providers.Set(pid, pc)
		}
	}

	providerIDs := map[string]struct{}{provider: {}}
	if cfg.Providers != nil {
		for pc := range cfg.Providers.Seq() {
			providerIDs[pc.ID] = struct{}{}
		}
	}
	w.providerKeys.Range(func(k, _ any) bool {
		providerIDs[k.(string)] = struct{}{}
		return true
	})
	w.providerBaseURLs.Range(func(k, _ any) bool {
		providerIDs[k.(string)] = struct{}{}
		return true
	})

	for pid := range providerIDs {
		pc, _ := providers.Get(pid)
		apiKey := w.resolveAPIKey(pid)
		if apiKey != "" {
			pc.APIKey = apiKey
		}
		baseURL := w.resolveProviderBaseURL(pid)
		if baseURL != "" {
			pc.BaseURL = baseURL
		}
		pc.ID = pid
		if pc.Name == "" {
			pc.Name = displayNameFor(pid)
		}
		if pc.Type == "" {
			pc.Type = catwalkTypeFor(pid)
		}
		if pid == "ollama" {
			w.ollamaMu.RLock()
			pc.Models = append([]catwalk.Model(nil), w.ollamaModels...)
			w.ollamaMu.RUnlock()
		}
		if pid == provider && len(pc.Models) == 0 {
			pc.Models = []catwalk.Model{{ID: modelID, Name: modelID, ContextWindow: 200000}}
		}
		providers.Set(pid, pc)
	}
	cfg.Providers = providers

	if cfg.ImageGeneration != nil {
		w.imageGeneration = *cfg.ImageGeneration
	}
	if cfg.TextToSpeech != nil {
		w.textToSpeech = *cfg.TextToSpeech
	}
	if cfg.SpeechToText != nil {
		w.speechToText = *cfg.SpeechToText
	}
	cfg.ImageGeneration = cloneImageGenerationConfig(w.imageGeneration)
	cfg.TextToSpeech = cloneTextToSpeechConfig(w.textToSpeech)
	cfg.SpeechToText = cloneSpeechToTextConfig(w.speechToText)

	w.cfg = &cfg
	return w.cfg
}

// SetMCPConfig stores the MCP configuration loaded from seshat.json so it is
// included in the config returned by Config() and shown in the Settings panel.
func (w *SeshatWorkspace) SetMCPConfig(mcps config.MCPs) {
	w.cfgMu.Lock()
	w.mcpCfg = mcps
	w.cfg = nil // invalidate cached config so next Config() call picks up MCPs
	w.cfgMu.Unlock()
}

// SetMCPStore stores the full config store used for MCP operations (resource
// reads, prompt fetches, tool refresh, Docker MCP, enable/disable).
func (w *SeshatWorkspace) SetMCPStore(store *config.ConfigStore) {
	w.cfgMu.Lock()
	w.mcpStore = store
	w.cfgMu.Unlock()
}

func (w *SeshatWorkspace) WorkingDir() string { return w.workDir }

// LongTermMemory implements Workspace.
func (w *SeshatWorkspace) LongTermMemory() sdk.LongTermMemory { return w.client.LongTermMemory() }

func (w *SeshatWorkspace) Resolver() config.VariableResolver {
	return config.IdentityResolver()
}

func cloneImageGenerationConfig(src config.ImageGenerationConfig) *config.ImageGenerationConfig {
	if strings.TrimSpace(src.Provider) == "" && strings.TrimSpace(src.Model) == "" {
		return nil
	}
	cp := src
	return &cp
}

func cloneTextToSpeechConfig(src config.TextToSpeechConfig) *config.TextToSpeechConfig {
	if strings.TrimSpace(src.Provider) == "" && strings.TrimSpace(src.Model) == "" &&
		strings.TrimSpace(src.Voice) == "" && strings.TrimSpace(src.Format) == "" {
		return nil
	}
	cp := src
	return &cp
}

func cloneSpeechToTextConfig(src config.SpeechToTextConfig) *config.SpeechToTextConfig {
	if strings.TrimSpace(src.Provider) == "" && strings.TrimSpace(src.Model) == "" && strings.TrimSpace(src.Language) == "" {
		return nil
	}
	cp := src
	return &cp
}

func (w *SeshatWorkspace) currentImageGenerationConfig() *sdk.ImageGenerationConfig {
	cfg := w.Config()
	if cfg == nil || cfg.ImageGeneration == nil {
		return nil
	}
	providerID := strings.TrimSpace(cfg.ImageGeneration.Provider)
	if providerID == "" {
		return nil
	}
	return &sdk.ImageGenerationConfig{
		Provider: providerID,
		Model:    strings.TrimSpace(cfg.ImageGeneration.Model),
		APIKey:   w.resolveAPIKey(providerID),
		BaseURL:  w.resolveProviderBaseURL(providerID),
	}
}

func (w *SeshatWorkspace) currentTextToSpeechConfig() *sdk.TextToSpeechConfig {
	cfg := w.Config()
	if cfg == nil || cfg.TextToSpeech == nil {
		return nil
	}
	providerID := strings.TrimSpace(cfg.TextToSpeech.Provider)
	if providerID == "" {
		return nil
	}
	return &sdk.TextToSpeechConfig{
		Provider: providerID,
		Model:    strings.TrimSpace(cfg.TextToSpeech.Model),
		Voice:    strings.TrimSpace(cfg.TextToSpeech.Voice),
		Format:   strings.TrimSpace(cfg.TextToSpeech.Format),
		APIKey:   w.resolveAPIKey(providerID),
		BaseURL:  w.resolveProviderBaseURL(providerID),
	}
}

func (w *SeshatWorkspace) currentSpeechToTextConfig() *sdk.SpeechToTextConfig {
	cfg := w.Config()
	if cfg == nil || cfg.SpeechToText == nil {
		return nil
	}
	providerID := strings.TrimSpace(cfg.SpeechToText.Provider)
	if providerID == "" {
		return nil
	}
	return &sdk.SpeechToTextConfig{
		Provider: providerID,
		Model:    strings.TrimSpace(cfg.SpeechToText.Model),
		Language: strings.TrimSpace(cfg.SpeechToText.Language),
		APIKey:   w.resolveAPIKey(providerID),
		BaseURL:  w.resolveProviderBaseURL(providerID),
	}
}

// ─── Config mutations (stubs — UI writes, we ignore) ─────────────────────────

// credKeyModel is the credentials-DB key under which the selected model is kept;
// cmd/cli reads it back at startup (loadCredsIntoConfig) when no model is set
// elsewhere, so it must stay the same string as there.
const credKeyModel = "model"

// UpdatePreferredModel records the model the user picked. Only the large model
// is the one the session runs on: the interface also sets a small model right
// after a large one is chosen (for titles and summaries), and that call must not
// replace it. The choice is written to the credentials DB so the next start
// opens on it instead of on the provider default.
func (w *SeshatWorkspace) UpdatePreferredModel(_ config.Scope, modelType config.SelectedModelType, m config.SelectedModel) error {
	if modelType != config.SelectedModelTypeLarge {
		return nil
	}
	if strings.TrimSpace(m.Provider) == "" || strings.TrimSpace(m.Model) == "" {
		return nil
	}
	w.model = m.Provider + ":" + m.Model
	w.persistCredential(credKeyModel, w.model)
	w.cfgMu.Lock()
	w.cfg = nil // invalidate cached config
	w.cfgMu.Unlock()
	return nil
}

func (w *SeshatWorkspace) SetCompactMode(_ config.Scope, _ bool) error { return nil }
func (w *SeshatWorkspace) SetConfigField(scope config.Scope, key string, value any) error {
	stringValue := strings.TrimSpace(fmt.Sprint(value))
	persistConfigField := func() error {
		if w.mcpStore == nil {
			return nil
		}
		return w.mcpStore.SetConfigField(scope, key, value)
	}
	invalidateConfig := func() {
		w.cfgMu.Lock()
		w.cfg = nil
		w.cfgMu.Unlock()
	}

	switch {
	case strings.HasPrefix(key, "providers.") && strings.HasSuffix(key, ".base_url"):
		providerID := strings.TrimSuffix(strings.TrimPrefix(key, "providers."), ".base_url")
		if stringValue == "" {
			stringValue = defaultProviderBaseURL(providerID)
		}
		w.providerBaseURLs.Store(providerID, stringValue)
		if providerID == "ollama" {
			_ = os.Setenv("OLLAMA_HOST", stringValue)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if models := fetchOllamaModels(ctx, stringValue); len(models) > 0 {
				w.ollamaMu.Lock()
				w.ollamaModels = models
				w.ollamaMu.Unlock()
				w.providerKeys.Store("ollama", "")
			}
		}
		invalidateConfig()
		go w.persistProviderBaseURL(providerID, stringValue)
		return nil
	case key == "web_search_provider":
		if stringValue == "" {
			stringValue = "auto"
		}
		_ = os.Setenv("WEB_SEARCH_PROVIDER", stringValue)
		go w.persistCredential("setting:web_search_provider", stringValue)
		return nil
	case strings.HasPrefix(key, "web_search.") && strings.HasSuffix(key, ".api_key"):
		providerID := strings.TrimSuffix(strings.TrimPrefix(key, "web_search."), ".api_key")
		if envVar := webSearchAPIKeyEnvVar(providerID); envVar != "" {
			if stringValue == "" {
				_ = os.Unsetenv(envVar)
			} else {
				_ = os.Setenv(envVar, stringValue)
			}
			go w.persistCredential("web_search_api_key:"+providerID, stringValue)
		}
		return nil
	case strings.HasPrefix(key, "web_search.") && strings.HasSuffix(key, ".base_url"):
		providerID := strings.TrimSuffix(strings.TrimPrefix(key, "web_search."), ".base_url")
		if envVar := webSearchBaseURLEnvVar(providerID); envVar != "" {
			if stringValue == "" {
				_ = os.Unsetenv(envVar)
			} else {
				_ = os.Setenv(envVar, stringValue)
			}
			go w.persistCredential("web_search_base_url:"+providerID, stringValue)
		}
		return nil
	case key == "image_generation.provider":
		w.imageGeneration.Provider = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "image_generation.model":
		w.imageGeneration.Model = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "text_to_speech.provider":
		w.textToSpeech.Provider = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "text_to_speech.model":
		w.textToSpeech.Model = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "text_to_speech.voice":
		w.textToSpeech.Voice = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "text_to_speech.format":
		w.textToSpeech.Format = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "speech_to_text.provider":
		w.speechToText.Provider = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "speech_to_text.model":
		w.speechToText.Model = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case key == "speech_to_text.language":
		w.speechToText.Language = stringValue
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	case strings.HasPrefix(key, "options."):
		if err := persistConfigField(); err != nil {
			return err
		}
		invalidateConfig()
		return nil
	default:
		return nil
	}
}

// SetProviderAPIKey persists the API key for a provider and marks it as
// configured in the TUI's in-memory config (so isConfigured() returns true).
func (w *SeshatWorkspace) SetProviderAPIKey(_ config.Scope, providerID string, value any) error {
	apiKey := ""
	switch v := value.(type) {
	case string:
		apiKey = v
	case *oauth.Token:
		if v != nil {
			apiKey = v.AccessToken
		}
	}

	w.providerKeys.Store(providerID, apiKey)

	cfg := w.Config()
	existing, _ := cfg.Providers.Get(providerID)
	existing.ID = providerID
	existing.Name = displayNameFor(providerID)
	existing.APIKey = apiKey
	existing.Type = catwalkTypeFor(providerID)
	if existing.BaseURL == "" {
		existing.BaseURL = w.resolveProviderBaseURL(providerID)
	}
	cfg.Providers.Set(providerID, existing)

	go w.persistProviderAPIKey(providerID, apiKey)
	return nil
}
func (w *SeshatWorkspace) RemoveConfigField(_ config.Scope, _ string) error { return nil }
func (w *SeshatWorkspace) ImportCopilot() (*oauth.Token, bool)              { return nil, false }
func (w *SeshatWorkspace) RefreshOAuthToken(_ context.Context, _ config.Scope, _ string) error {
	return nil
}

// ─── Project lifecycle (stubs) ────────────────────────────────────────────────

func (w *SeshatWorkspace) ProjectNeedsInitialization() (bool, error) { return false, nil }
func (w *SeshatWorkspace) MarkProjectInitialized() error             { return nil }
func (w *SeshatWorkspace) InitializePrompt() (string, error)         { return "", nil }
func (w *SeshatWorkspace) ListTools(ctx context.Context) ([]ToolInfo, error) {
	if w.client == nil {
		return nil, nil
	}
	surface, err := w.client.BuildToolSurface(ctx)
	if err != nil {
		return nil, err
	}
	tools := make([]ToolInfo, 0, len(surface.Tools))
	for _, def := range surface.Tools {
		tools = append(tools, ToolInfo{
			Name:        def.Name,
			Description: def.Description,
			Category:    def.Category,
		})
	}
	return tools, nil
}

// discoverActiveSkills runs the same skill discovery ListSkills and ReadSkill
// both need, and returns the active skills alongside the resolved skill
// paths (for labelling). Factored out so ReadSkill (previously a stub - see
// H8 in TODO.md) discovers the exact same skill set ListSkills shows in the
// catalog, instead of drifting out of sync with a second implementation.
func (w *SeshatWorkspace) discoverActiveSkills() (active []*skills.Skill, resolvedPaths []string) {
	// w.Config() returns a lazily-synthesized config (provider/model/MCP
	// state only) whose Options is never populated with the defaulted
	// SkillsPaths/DisabledSkills. The fully-loaded config — with
	// runtimepath-aware defaults applied via setDefaults — lives in
	// w.mcpStore, so prefer that when available.
	var cfg *config.Config
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store != nil {
		cfg = store.Config()
	} else {
		cfg = w.Config()
	}
	var skillsPaths []string
	var disabledSkills []string
	if cfg != nil && cfg.Options != nil {
		skillsPaths = cfg.Options.SkillsPaths
		disabledSkills = cfg.Options.DisabledSkills
	}
	resolver := w.Resolver()
	discCfg := skills.DiscoveryConfig{
		SkillsPaths:    skillsPaths,
		DisabledSkills: disabledSkills,
		WorkingDir:     w.workDir,
		Resolver:       resolver.ResolveValue,
	}
	_, active, _ = skills.DiscoverFromConfig(discCfg)
	return active, discCfg.ResolvePaths()
}

func (w *SeshatWorkspace) ListSkills(_ context.Context) ([]skills.CatalogEntry, error) {
	active, resolvedPaths := w.discoverActiveSkills()
	return skills.Catalog(active, resolvedPaths, w.workDir), nil
}

func (w *SeshatWorkspace) ReadSkill(_ context.Context, skillID string) ([]byte, skills.SkillReadResult, error) {
	active, resolvedPaths := w.discoverActiveSkills()
	return skills.ReadContent(active, resolvedPaths, w.workDir, skillID)
}

// ─── MCP ──────────────────────────────────────────────────────────────────────

func (w *SeshatWorkspace) MCPGetStates() map[string]mcptools.ClientInfo {
	return mcptools.GetStates()
}

func (w *SeshatWorkspace) MCPRefreshPrompts(ctx context.Context, name string) {
	mcptools.RefreshPrompts(ctx, name)
}

func (w *SeshatWorkspace) MCPRefreshResources(ctx context.Context, name string) {
	mcptools.RefreshResources(ctx, name)
}

func (w *SeshatWorkspace) RefreshMCPTools(ctx context.Context, name string) {
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return
	}
	mcptools.RefreshTools(ctx, store, name)
}

func (w *SeshatWorkspace) ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error) {
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return nil, nil
	}
	raw, err := mcptools.ReadResource(ctx, store, name, uri)
	if err != nil {
		return nil, err
	}
	out := make([]MCPResourceContents, 0, len(raw))
	for _, c := range raw {
		out = append(out, MCPResourceContents{
			URI:      c.URI,
			MIMEType: c.MIMEType,
			Text:     c.Text,
			Blob:     c.Blob,
		})
	}
	return out, nil
}

func (w *SeshatWorkspace) GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return "", nil
	}
	messages, err := mcptools.GetPromptMessages(ctx, store, clientID, promptID, args)
	if err != nil {
		return "", err
	}
	return strings.Join(messages, " "), nil
}

func (w *SeshatWorkspace) EnableDockerMCP(ctx context.Context) error {
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return fmt.Errorf("MCP config store not initialized")
	}
	if err := store.EnableDockerMCP(); err != nil {
		return err
	}
	return mcptools.InitializeSingle(ctx, config.DockerMCPName, store)
}

func (w *SeshatWorkspace) DisableDockerMCP() error {
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return nil
	}
	_ = mcptools.DisableSingle(store, config.DockerMCPName)
	return store.DisableDockerMCP()
}

func (w *SeshatWorkspace) EnableMCPServer(ctx context.Context, name string) error {
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return fmt.Errorf("MCP config store not initialized")
	}
	return mcptools.InitializeSingle(ctx, name, store)
}

func (w *SeshatWorkspace) DisableMCPServer(name string) error {
	w.cfgMu.Lock()
	store := w.mcpStore
	w.cfgMu.Unlock()
	if store == nil {
		return nil
	}
	return mcptools.DisableSingle(store, name)
}

// ─── SDK callback receivers (called from SDK event callbacks) ─────────────────

// HandleChunk processes a streaming text delta and updates the in-progress message.
func (w *SeshatWorkspace) HandleChunk(delta string, isThinking bool) {
	w.streamMu.Lock()
	cur := w.streamMsg
	sessID := w.streamSess
	w.streamMu.Unlock()
	if cur == nil {
		return
	}

	if isThinking {
		cur.AppendReasoningContent(delta)
	} else {
		cur.AppendContent(delta)
	}
	cur.UpdatedAt = time.Now().UnixMilli()
	w.updateMsg(sessID, *cur)
	w.debounce.update(*cur, sessID)
}

// HandleToolProgress updates the in-progress tool call within the message.
// It extracts the full tool input and result content from p.Metadata so that
// tool renderers receive the correct parameters and output during live streaming.
func (w *SeshatWorkspace) HandleToolProgress(p sdk.ToolProgress) {
	w.streamMu.Lock()
	cur := w.streamMsg
	sessID := w.streamSess
	w.streamMu.Unlock()
	if cur == nil {
		return
	}

	toolUseID := p.ToolUseID
	toolName := p.ToolName
	status := string(p.Stage)

	// Serialize tool_input from metadata into JSON for the ToolCall.Input field.
	// This is populated by the execution layer before the tool runs, so it is
	// available on the first pending/running event.
	inputJSON := toolInputFromMetadata(p.Metadata)

	switch status {
	case "running", "pending":
		found := false
		for i, part := range cur.Parts {
			if tc, ok := part.(message.ToolCall); ok && tc.ID == toolUseID {
				input := tc.Input
				if inputJSON != "" {
					input = inputJSON
				}
				cur.Parts[i] = message.ToolCall{ID: toolUseID, Name: toolName, Input: input, Finished: false}
				found = true
				break
			}
		}
		if !found {
			cur.Parts = append(cur.Parts, message.ToolCall{ID: toolUseID, Name: toolName, Input: inputJSON, Finished: false})
		}

	case "completed", "done":
		for i, part := range cur.Parts {
			if tc, ok := part.(message.ToolCall); ok && tc.ID == toolUseID {
				input := tc.Input
				if inputJSON != "" {
					input = inputJSON
				}
				cur.Parts[i] = message.ToolCall{ID: toolUseID, Name: toolName, Input: input, Finished: true}
				break
			}
		}
		// Extract actual tool output from metadata["content"]; fall back to the
		// human-readable message only when no content was provided.
		content := p.Message
		if c, ok := p.Metadata["content"].(string); ok && c != "" {
			content = c
		}
		cur.Parts = append(cur.Parts, message.ToolResult{
			ToolCallID: toolUseID,
			Name:       toolName,
			Content:    content,
			Metadata:   toolResultMetadataJSON(p.Metadata),
		})

	case "failed", "error":
		for i, part := range cur.Parts {
			if tc, ok := part.(message.ToolCall); ok && tc.ID == toolUseID {
				input := tc.Input
				if inputJSON != "" {
					input = inputJSON
				}
				cur.Parts[i] = message.ToolCall{ID: toolUseID, Name: toolName, Input: input, Finished: true}
				break
			}
		}
		content := p.Message
		if c, ok := p.Metadata["content"].(string); ok && c != "" {
			content = c
		}
		cur.Parts = append(cur.Parts, message.ToolResult{
			ToolCallID: toolUseID,
			Name:       toolName,
			Content:    content,
			IsError:    true,
		})
	}

	cur.UpdatedAt = time.Now().UnixMilli()
	w.updateMsg(sessID, *cur)
	w.debounce.update(*cur, sessID)
}

// toolInputFromMetadata extracts the tool_input from ToolProgress.Metadata and
// returns it as a JSON string. Returns "" when no input is present.
func toolInputFromMetadata(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	toolInput, ok := meta["tool_input"]
	if !ok || toolInput == nil {
		return ""
	}
	data, err := json.Marshal(toolInput)
	if err != nil {
		return ""
	}
	return string(data)
}

// toolResultMetadataJSON builds a JSON string from ToolProgress.Metadata for use
// as ToolResult.Metadata. It excludes internal bookkeeping keys (tool_name,
// tool_input, content) so only tool-specific fields (exit_code, output, diff,
// shell_id, etc.) are included — matching the format written by the SDK's
// session storage and read by the tool renderers.
func toolResultMetadataJSON(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	skip := map[string]bool{"tool_name": true, "tool_input": true, "content": true}
	filtered := make(map[string]any, len(meta))
	for k, v := range meta {
		if !skip[k] {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		return ""
	}
	return string(data)
}

// HandlePermissionRequest emits a permission request to the seshattui UI.
func (w *SeshatWorkspace) HandlePermissionRequest(req permission.PermissionRequest) {
	w.permBroker.Publish(pubsub.CreatedEvent, req)
}

// LoadSessionMessages populates the message store from the SDK session history
// (call after LoadSession completes).
func (w *SeshatWorkspace) LoadSessionMessages(sessionID string, sdkMsgs []sdk.Message) {
	msgs := convertSDKMessages(sessionID, sdkMsgs)
	w.msgMu.Lock()
	w.msgStore[sessionID] = msgs
	w.msgMu.Unlock()
}

// ─── SDK glue — called from newClient() callbacks ────────────────────────────

// SetSDKClient wires the SDK client and registers unique TUI tools.
func (w *SeshatWorkspace) SetSDKClient(client *sdk.Client) {
	w.client = client
	if w.client == nil {
		return
	}
	logFile := w.logFilePath()
	for _, t := range []sdk.Tool{
		tuiTools.NewSeshatLogsTool(logFile),
		tuiTools.NewDiagnosticsTool(nil),
		tuiTools.NewLSPRestartTool(nil),
		tuiTools.NewReferencesTool(nil),
	} {
		if err := w.client.RegisterTool(t); err != nil {
			slog.Debug("Failed to register TUI tool", "tool", t.Definition().Name, "error", err)
		}
	}
}

// logFilePath returns the path to the seshat log file, derived from config.
func (w *SeshatWorkspace) logFilePath() string {
	cfg := w.Config()
	dataDir := ""
	if cfg.Options != nil {
		dataDir = cfg.Options.DataDirectory
	}
	if dataDir == "" {
		if cacheDir, err := os.UserCacheDir(); err == nil {
			dataDir = filepath.Join(cacheDir, "seshat")
		} else {
			dataDir = os.TempDir()
		}
	}
	return filepath.Join(dataDir, "logs", "seshat.log")
}

// OnChunk is the sdk.ResponseChunk callback — translates to HandleChunk.
func (w *SeshatWorkspace) OnChunk(chunk sdk.ResponseChunk) {
	switch chunk.Type {
	case sdk.ResponseChunkTypeContentBlockStart, sdk.ResponseChunkTypeContentBlockDelta, sdk.ResponseChunkTypeContentBlockStop:
		w.rotateStreamingAssistantMessageIfNeeded()
	}

	switch chunk.Type {
	case sdk.ResponseChunkTypeContentBlockStart:
		// When a tool_use block starts, immediately create a pending ToolCall
		// so the item appears in the chat with a spinner before execution begins.
		if toolUse, ok := chunk.ContentBlock.(sdk.ToolUseContent); ok {
			w.streamMu.Lock()
			w.streamToolUseID = toolUse.ID
			w.streamToolUseName = toolUse.Name
			w.streamToolInputBuf = ""
			cur := w.streamMsg
			sessID := w.streamSess
			w.streamMu.Unlock()
			if cur != nil {
				cur.Parts = append(cur.Parts, message.ToolCall{
					ID:       toolUse.ID,
					Name:     toolUse.Name,
					Finished: false,
				})
				cur.UpdatedAt = time.Now().UnixMilli()
				w.updateMsg(sessID, *cur)
				w.debounce.update(*cur, sessID)
			}
		} else {
			// Text or thinking block: clear tool tracking.
			w.streamMu.Lock()
			w.streamToolUseID = ""
			w.streamToolInputBuf = ""
			w.streamMu.Unlock()
		}

	case sdk.ResponseChunkTypeContentBlockDelta:
		switch chunk.DeltaType {
		case "text_delta", "":
			w.HandleChunk(chunk.Delta, false)
		case "thinking_delta":
			w.HandleChunk(chunk.Delta, true)
		case "input_json_delta":
			w.handleToolInputDelta(chunk.PartialJSON)
		}

	case sdk.ResponseChunkTypeContentBlockStop:
		// Clear block tracking on stop and stamp FinishedAt on any open
		// thinking block. FinishThinking is idempotent so calling it here
		// for non-thinking blocks (text, tool_use) is a safe no-op.
		w.streamMu.Lock()
		cur := w.streamMsg
		sessID := w.streamSess
		w.streamToolUseID = ""
		w.streamToolInputBuf = ""
		w.streamMu.Unlock()
		if cur != nil {
			cur.FinishThinking()
			w.debounce.update(*cur, sessID)
		}

	case sdk.ResponseChunkTypeMessageStop:
		// Safety net: stamp FinishedAt before the final flush so the TUI
		// receives the correct thinking duration in the forceFlush payload.
		w.streamMu.Lock()
		cur := w.streamMsg
		sessID := w.streamSess
		w.streamResponseDone = true
		w.streamToolUseID = ""
		w.streamToolUseName = ""
		w.streamToolInputBuf = ""
		w.streamMu.Unlock()
		if cur != nil {
			cur.FinishThinking()
			w.debounce.update(*cur, sessID)
		}
		w.debounce.forceFlush()
	}
}

// handleToolInputDelta accumulates a partial JSON fragment for the current
// tool_use block and updates the ToolCall.Input in the streaming message.
func (w *SeshatWorkspace) handleToolInputDelta(partialJSON string) {
	w.streamMu.Lock()
	toolUseID := w.streamToolUseID
	toolName := w.streamToolUseName
	w.streamToolInputBuf += partialJSON
	input := w.streamToolInputBuf
	cur := w.streamMsg
	sessID := w.streamSess
	w.streamMu.Unlock()

	if cur == nil || toolUseID == "" {
		return
	}

	for i, part := range cur.Parts {
		if tc, ok := part.(message.ToolCall); ok && tc.ID == toolUseID {
			cur.Parts[i] = message.ToolCall{ID: toolUseID, Name: toolName, Input: input, Finished: false}
			cur.UpdatedAt = time.Now().UnixMilli()
			w.updateMsg(sessID, *cur)
			w.debounce.update(*cur, sessID)
			return
		}
	}
}

// OnProgress is the sdk.ToolProgress callback.
func (w *SeshatWorkspace) OnProgress(p sdk.ToolProgress) {
	w.HandleToolProgress(p)
}

// OnRuntimeEvent forwards structured runtime events that need dedicated TUI
// surfaces outside the normal transcript.
func (w *SeshatWorkspace) OnRuntimeEvent(ev sdk.RuntimeEvent) {
	// Sub-agent tool progress: forward nested tool calls to the live tail.
	if ev.AgentToolUseID != "" && ev.ToolProgress != nil {
		w.handleSubAgentToolProgress(ev.AgentToolUseID, ev.ToolProgress)
	}

	// Sub-agent response chunks: forward streaming reasoning and text.
	if ev.AgentToolUseID != "" && ev.Type == sdk.RuntimeEventTypeResponseChunk && ev.Chunk != nil {
		w.handleSubAgentResponseChunk(ev.AgentToolUseID, ev.Chunk)
	}

	if ev.AgentToolUseID == "" && w.trackProviderRetry(ev) {
		return
	}

	switch ev.Type {
	case sdk.RuntimeEventTypeTaskChanged:
		sessionID := string(ev.SessionID)
		if sessionID == "" {
			return
		}
		w.syncSessionTodos(sessionID)
		w.publishSessionRefresh(sessionID)
	case sdk.RuntimeEventTypeExecutionModeChanged, sdk.RuntimeEventTypeTurnStarted, sdk.RuntimeEventTypeTurnCompleted:
		sessionID := string(ev.SessionID)
		if sessionID == "" {
			return
		}
		if ev.ExecutionMode != "" {
			w.setLiveExecutionMode(sessionID, ev.ExecutionMode)
		}
		w.publishSessionRefresh(sessionID)
	case sdk.RuntimeEventTypePlanSubmitted:
		if ev.PlanEvent == nil {
			return
		}
		submission := planreview.Submission{
			SessionID: string(ev.SessionID),
			PlanID:    ev.PlanEvent.PlanID,
			Slug:      ev.PlanEvent.Slug,
			Filename:  ev.PlanEvent.Filename,
			Status:    ev.PlanEvent.Status,
			Version:   ev.PlanEvent.Version,
			Content:   ev.PlanEvent.Content,
		}
		w.planBroker.PublishMustDeliver(context.Background(), pubsub.CreatedEvent, submission)
	}
}

// handleSubAgentToolProgress synthesises a child-session message from a
// sub-agent ToolProgress event and publishes it to msgBroker so that
// handleChildSessionMessage in ui.go can update the agent's live-tail tree.
func (w *SeshatWorkspace) handleSubAgentToolProgress(agentToolUseID string, tp *sdk.ToolProgress) {
	if tp.ToolUseID == "" || tp.ToolName == "" {
		return
	}

	// ":<agentToolUseID>" passes ParseAgentToolSessionID (splits on ":") and
	// yields toolCallID = agentToolUseID so the agent item can be looked up.
	childSessionID := ":" + agentToolUseID

	var parts []message.ContentPart

	switch tp.Stage {
	case sdk.ToolProgressStageRunning:
		var inputStr string
		if inp, ok := tp.Metadata["tool_input"]; ok {
			if data, err := json.Marshal(inp); err == nil {
				inputStr = string(data)
			}
		}
		parts = []message.ContentPart{
			message.ToolCall{
				ID:       tp.ToolUseID,
				Name:     tp.ToolName,
				Input:    inputStr,
				Finished: false,
			},
		}

	case sdk.ToolProgressStageCompleted:
		var content string
		if c, ok := tp.Metadata["content"]; ok {
			content, _ = c.(string)
		}
		parts = []message.ContentPart{
			message.ToolCall{
				ID:       tp.ToolUseID,
				Name:     tp.ToolName,
				Finished: true,
			},
			message.ToolResult{
				ToolCallID: tp.ToolUseID,
				Name:       tp.ToolName,
				Content:    content,
			},
		}

	case sdk.ToolProgressStageFailed:
		content := tp.Message
		if c, ok := tp.Metadata["content"]; ok {
			if s, _ := c.(string); s != "" {
				content = s
			}
		}
		parts = []message.ContentPart{
			message.ToolCall{
				ID:       tp.ToolUseID,
				Name:     tp.ToolName,
				Finished: true,
			},
			message.ToolResult{
				ToolCallID: tp.ToolUseID,
				Name:       tp.ToolName,
				Content:    content,
				IsError:    true,
			},
		}

	default:
		return
	}

	msg := message.Message{
		ID:        "sub-" + tp.ToolUseID,
		SessionID: childSessionID,
		Parts:     parts,
	}
	w.msgBroker.Publish(pubsub.UpdatedEvent, msg)
}

func (w *SeshatWorkspace) handleSubAgentResponseChunk(agentToolUseID string, chunk *sdk.ResponseChunk) {
	if chunk == nil {
		return
	}

	w.subAgentStreamsMu.Lock()
	state, ok := w.subAgentStreams[agentToolUseID]
	if !ok {
		state = &subAgentStreamState{}
		w.subAgentStreams[agentToolUseID] = state
	}

	switch chunk.Type {
	case sdk.ResponseChunkTypeContentBlockStart:
		// Reset text and thinking buffers when starting a content block
		state.reasoning = ""
		state.content = ""
	case sdk.ResponseChunkTypeContentBlockDelta:
		switch chunk.DeltaType {
		case "text_delta", "":
			state.content += chunk.Delta
		case "thinking_delta":
			state.reasoning += chunk.Delta
		}
	}
	reasoning := state.reasoning
	content := state.content
	w.subAgentStreamsMu.Unlock()

	childSessionID := ":" + agentToolUseID
	var parts []message.ContentPart
	if reasoning != "" {
		parts = append(parts, message.ReasoningContent{Thinking: reasoning})
	}
	if content != "" {
		parts = append(parts, message.TextContent{Text: content})
	}

	msg := message.Message{
		ID:        agentToolUseID + "_streaming",
		SessionID: childSessionID,
		Parts:     parts,
	}
	w.msgBroker.Publish(pubsub.UpdatedEvent, msg)
}

// OnSessionTitled updates the session title in our local store.
func (w *SeshatWorkspace) liveExecutionMode(sessionID string) string {
	w.modeMu.RLock()
	defer w.modeMu.RUnlock()
	return w.liveExecModes[sessionID]
}

func (w *SeshatWorkspace) setLiveExecutionMode(sessionID, mode string) {
	w.modeMu.Lock()
	defer w.modeMu.Unlock()
	if mode == "" {
		delete(w.liveExecModes, sessionID)
		return
	}
	w.liveExecModes[sessionID] = mode
}

func (w *SeshatWorkspace) syncSessionTodos(sessionID string) {
	tasks, err := tasktool.GlobalTaskStore().ListTasks(context.Background(), sessionID)
	if err != nil {
		return
	}
	todos := make([]session.Todo, 0, len(tasks))
	for _, task := range tasks {
		var status session.TodoStatus
		switch task.Status {
		case tasktool.TaskStatusCompleted:
			status = session.TodoStatusCompleted
		case tasktool.TaskStatusInProgress:
			status = session.TodoStatusInProgress
		default:
			status = session.TodoStatusPending
		}
		todos = append(todos, session.Todo{ID: task.ID, Content: task.Subject, Description: task.Description, Status: status, ActiveForm: task.ActiveForm, Owner: task.Owner})
	}
	w.sessionsMu.Lock()
	if s, ok := w.sessStore[sessionID]; ok {
		s.Todos = todos
		w.sessStore[sessionID] = s
	}
	w.sessionsMu.Unlock()
}

func (w *SeshatWorkspace) publishSessionRefresh(sessionID string) {
	w.sessionsMu.RLock()
	s, ok := w.sessStore[sessionID]
	w.sessionsMu.RUnlock()
	if !ok {
		return
	}
	w.sessBroker.Publish(pubsub.UpdatedEvent, s)
}

func (w *SeshatWorkspace) OnSessionTitled(id sdk.SessionID, title string) {
	sessID := string(id)
	w.sessionsMu.Lock()
	if s, ok := w.sessStore[sessID]; ok {
		s.Title = title
		s.UpdatedAt = time.Now().UnixMilli()
		w.sessStore[sessID] = s
		w.sessBroker.Publish(pubsub.UpdatedEvent, s)
	}
	w.sessionsMu.Unlock()
}

// buildToolPermissionParams constructs the typed permission params struct for a tool
// from its raw input map. Returns nil for tools with no typed params.
func buildToolPermissionParams(toolName string, input map[string]any) any {
	if input == nil {
		return nil
	}
	str := func(key string) string {
		s, _ := input[key].(string)
		return s
	}
	intVal := func(key string) int {
		switch v := input[key].(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
		return 0
	}
	notebookCells := func() []tuiTools.NotebookCellPreview {
		rawCells, ok := input["cells"].([]any)
		if !ok || len(rawCells) == 0 {
			return nil
		}
		cells := make([]tuiTools.NotebookCellPreview, 0, len(rawCells))
		for _, raw := range rawCells {
			cellMap, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			cell := tuiTools.NotebookCellPreview{}
			if cellType, ok := cellMap["cell_type"].(string); ok {
				cell.CellType = cellType
			}
			if source, ok := cellMap["source"].(string); ok {
				cell.Source = source
			}
			cells = append(cells, cell)
		}
		return cells
	}

	switch toolName {
	case tuiTools.WriteToolName:
		filePath := str("file_path")
		var oldContent string
		if filePath != "" {
			if data, err := os.ReadFile(filePath); err == nil {
				oldContent = string(data)
			}
		}
		return tuiTools.WritePermissionsParams{
			FilePath:   filePath,
			OldContent: oldContent,
			NewContent: str("content"),
		}
	case tuiTools.EditToolName:
		return tuiTools.EditPermissionsParams{
			FilePath:   str("file_path"),
			OldContent: str("old_string"),
			NewContent: str("new_string"),
		}
	case tuiTools.MultiEditToolName:
		return tuiTools.MultiEditPermissionsParams{
			FilePath: str("file_path"),
		}
	case tuiTools.BashToolName:
		return tuiTools.BashPermissionsParams{
			Command:     str("command"),
			Description: str("description"),
		}
	case tuiTools.ViewToolName:
		return tuiTools.ViewPermissionsParams{
			FilePath: str("file_path"),
			Offset:   intVal("offset"),
			Limit:    intVal("limit"),
		}
	case tuiTools.LSToolName:
		return tuiTools.LSPermissionsParams{
			Path: str("path"),
		}
	case tuiTools.DownloadToolName:
		return tuiTools.DownloadPermissionsParams{
			URL:      str("url"),
			FilePath: str("file_path"),
			Timeout:  intVal("timeout"),
		}
	case tuiTools.FetchToolName:
		return tuiTools.FetchPermissionsParams{
			URL: str("url"),
		}
	case tuiTools.AgenticFetchToolName:
		return tuiTools.AgenticFetchPermissionsParams{
			URL:    str("url"),
			Prompt: str("prompt"),
		}
	case tuiTools.NotebookEditToolName:
		notebookPath := str("notebook_path")
		var oldContent string
		if notebookPath != "" {
			if data, err := os.ReadFile(notebookPath); err == nil {
				oldContent = string(data)
			}
		}
		return tuiTools.NotebookEditPermissionsParams{
			NotebookPath: notebookPath,
			CellID:       str("cell_id"),
			CellType:     str("cell_type"),
			EditMode:     str("edit_mode"),
			OldContent:   oldContent,
			NewSource:    str("new_source"),
		}
	case tuiTools.NotebookCreateToolName:
		kernel := str("kernel")
		if kernel == "" {
			kernel = "python3"
		}
		language := str("language")
		if language == "" {
			language = "python"
		}
		cells := notebookCells()
		return tuiTools.NotebookCreatePermissionsParams{
			NotebookPath: str("notebook_path"),
			Kernel:       kernel,
			Language:     language,
			CellCount:    len(cells),
			Cells:        cells,
		}
	case tuiTools.NotebookWriteToolName:
		notebookPath := str("notebook_path")
		var oldContent string
		if notebookPath != "" {
			if data, err := os.ReadFile(notebookPath); err == nil {
				oldContent = string(data)
			}
		}
		kernel := str("kernel")
		if kernel == "" {
			kernel = "python3"
		}
		language := str("language")
		if language == "" {
			language = "python"
		}
		cells := notebookCells()
		return tuiTools.NotebookWritePermissionsParams{
			NotebookPath: notebookPath,
			Kernel:       kernel,
			Language:     language,
			CellCount:    len(cells),
			Cells:        cells,
			OldContent:   oldContent,
		}
	}
	return nil
}

// PromptFn blocks the SDK agent goroutine until the UI resolves the prompt.
// ask_user_question prompts route to askUserBroker; all others go to permBroker.
func (w *SeshatWorkspace) PromptFn(ctx context.Context, req sdk.PromptRequest) (sdk.PromptResponse, error) {
	toolName, _ := req.Metadata["tool_name"].(string)
	toolUseID, _ := req.Metadata["tool_use_id"].(string)

	// Route interactive ask_user_question prompts (Choice/Text) to the dedicated
	// bubble. PromptTypeConfirm is a permission check — fall through to permBroker.
	if toolName == tuiTools.AskUserToolName &&
		(req.Type == types.PromptTypeChoice || req.Type == types.PromptTypeText) {
		return w.promptAskUser(ctx, req, toolUseID)
	}

	// Yolo mode: auto-allow permission dialogs without showing them.
	if w.permSkip.Load() {
		return sdk.PromptResponse{Value: true}, nil
	}

	workDir, _ := req.Metadata["working_directory"].(string)
	if workDir == "" {
		workDir = w.workDir
	}
	toolInput, _ := req.Metadata["tool_input"].(map[string]any)

	params := buildToolPermissionParams(toolName, toolInput)

	// Use the actual target file/directory path for display when available,
	// falling back to the working directory if no file path is present.
	displayPath := workDir
	if toolInput != nil {
		for _, key := range []string{"file_path", "notebook_path", "path", "url"} {
			if v, ok := toolInput[key].(string); ok && v != "" {
				displayPath = v
				break
			}
		}
	}

	permID := uuid.New().String()
	permReq := permission.PermissionRequest{
		ID:          permID,
		ToolCallID:  toolUseID,
		ToolName:    toolName,
		Description: req.Message,
		Action:      string(req.Type),
		Path:        displayPath,
		Params:      params,
	}

	// Register the resolution channel before publishing (avoids race with fast UI).
	ch := make(chan sdk.PromptResponse, 1)
	w.pendingPerms.Store(permID, ch)

	// Show the permission dialog.
	w.permBroker.Publish(pubsub.CreatedEvent, permReq)

	// Block until the UI resolves or the context is cancelled.
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		w.pendingPerms.Delete(permID)
		return sdk.PromptResponse{Cancelled: true}, ctx.Err()
	}
}

// promptAskUser handles ask_user_question prompts by publishing to askUserBroker
// and blocking until the user answers (or context is cancelled).
func (w *SeshatWorkspace) promptAskUser(ctx context.Context, req sdk.PromptRequest, toolUseID string) (sdk.PromptResponse, error) {
	askReq, questionIndex, isSurvey, err := buildAskUserRequest(req, toolUseID)
	if err != nil {
		return sdk.PromptResponse{Cancelled: true}, err
	}
	if isSurvey && questionIndex > 0 {
		if resp, ok, err := w.consumeAskUserSurveyAnswer(toolUseID, askReq, questionIndex); ok || err != nil {
			return resp, err
		}
	}

	id := uuid.New().String()
	askReq.ID = id

	ch := make(chan sdk.PromptResponse, 1)
	w.pendingAskUser.Store(id, ch)
	w.askUserBroker.Publish(pubsub.CreatedEvent, askReq)

	select {
	case resp := <-ch:
		if isSurvey && len(askReq.Questions) > 1 {
			answers, err := decodeAskUserSurveyAnswers(fmt.Sprintf("%v", resp.Value))
			if err != nil {
				return sdk.PromptResponse{Cancelled: true}, err
			}
			w.askUserSurveyAnswers.Store(toolUseID, askUserSurveyState{Answers: answers})
			bufferedResp, ok, err := w.consumeAskUserSurveyAnswer(toolUseID, askReq, questionIndex)
			if err != nil {
				return sdk.PromptResponse{Cancelled: true}, err
			}
			if !ok {
				return sdk.PromptResponse{Cancelled: true}, fmt.Errorf("missing buffered ask_user survey answer")
			}
			return bufferedResp, nil
		}
		return resp, nil
	case <-ctx.Done():
		w.pendingAskUser.Delete(id)
		return sdk.PromptResponse{Cancelled: true}, ctx.Err()
	}
}

func buildAskUserRequest(req sdk.PromptRequest, toolUseID string) (tuiTools.AskUserRequest, int, bool, error) {
	header, _ := req.Metadata["header"].(string)
	multiSelect, _ := req.Metadata["multiSelect"].(bool)
	questionIndex := 0
	if rawIndex, ok := req.Metadata["survey_question_index"]; ok {
		switch value := rawIndex.(type) {
		case int:
			questionIndex = value
		case float64:
			questionIndex = int(value)
		}
	}

	askReq := tuiTools.AskUserRequest{
		ToolCallID:   toolUseID,
		Question:     req.Message,
		Header:       header,
		MultiSelect:  multiSelect,
		IsCustomText: req.Type == types.PromptTypeText,
	}
	for _, opt := range req.Options {
		askReq.Options = append(askReq.Options, tuiTools.AskUserOption{
			Label:       opt.Label,
			Value:       fmt.Sprintf("%v", opt.Value),
			Description: opt.Description,
		})
	}

	rawSurvey, _ := req.Metadata["survey_questions_json"].(string)
	if strings.TrimSpace(rawSurvey) == "" {
		askReq.Questions = []tuiTools.AskUserQuestion{{
			Question:    askReq.Question,
			Header:      askReq.Header,
			Options:     append([]tuiTools.AskUserOption(nil), askReq.Options...),
			MultiSelect: askReq.MultiSelect,
		}}
		return askReq, questionIndex, false, nil
	}

	var surveyQuestions []tuiTools.AskUserQuestion
	if err := json.Unmarshal([]byte(rawSurvey), &surveyQuestions); err != nil {
		return tuiTools.AskUserRequest{}, 0, false, fmt.Errorf("parse ask_user survey metadata: %w", err)
	}
	if len(surveyQuestions) == 0 {
		return askReq, questionIndex, false, nil
	}
	askReq.Questions = normalizeAskUserSurveyQuestions(surveyQuestions)
	return askReq, questionIndex, true, nil
}

func normalizeAskUserSurveyQuestions(questions []tuiTools.AskUserQuestion) []tuiTools.AskUserQuestion {
	for qIndex := range questions {
		hasOther := false
		for optIndex := range questions[qIndex].Options {
			if strings.TrimSpace(questions[qIndex].Options[optIndex].Value) == "" {
				questions[qIndex].Options[optIndex].Value = questions[qIndex].Options[optIndex].Label
			}
			if questions[qIndex].Options[optIndex].Value == "__other__" || strings.EqualFold(questions[qIndex].Options[optIndex].Label, "Other") {
				hasOther = true
			}
		}
		if !hasOther {
			questions[qIndex].Options = append(questions[qIndex].Options, tuiTools.AskUserOption{
				Label:       "Other",
				Value:       "__other__",
				Description: "Provide custom input",
			})
		}
	}
	return questions
}

func decodeAskUserSurveyAnswers(raw string) (map[string]string, error) {
	answers := make(map[string]string)
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &answers); err != nil {
		return nil, fmt.Errorf("decode ask_user survey answers: %w", err)
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("decode ask_user survey answers: empty response")
	}
	return answers, nil
}

func (w *SeshatWorkspace) consumeAskUserSurveyAnswer(toolUseID string, askReq tuiTools.AskUserRequest, questionIndex int) (sdk.PromptResponse, bool, error) {
	if questionIndex < 0 || questionIndex >= len(askReq.Questions) {
		return sdk.PromptResponse{}, false, fmt.Errorf("ask_user survey question index %d out of range", questionIndex)
	}
	stored, ok := w.askUserSurveyAnswers.Load(toolUseID)
	if !ok {
		return sdk.PromptResponse{}, false, nil
	}
	state, ok := stored.(askUserSurveyState)
	if !ok {
		w.askUserSurveyAnswers.Delete(toolUseID)
		return sdk.PromptResponse{}, false, fmt.Errorf("invalid ask_user survey state")
	}
	question := askReq.Questions[questionIndex].Question
	answer, ok := state.Answers[question]
	if !ok {
		return sdk.PromptResponse{}, false, fmt.Errorf("missing ask_user survey answer for %q", question)
	}
	if questionIndex == len(askReq.Questions)-1 {
		w.askUserSurveyAnswers.Delete(toolUseID)
	}
	return sdk.PromptResponse{Value: answer}, true, nil
}

// AnswerAskUser resolves a pending ask_user_question prompt.
func (w *SeshatWorkspace) AnswerAskUser(id, value string) bool {
	v, ok := w.pendingAskUser.LoadAndDelete(id)
	if !ok {
		return false
	}
	ch := v.(chan sdk.PromptResponse)
	ch <- sdk.PromptResponse{Value: value}
	return true
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (w *SeshatWorkspace) appendMsg(sessionID string, msg message.Message) {
	w.msgMu.Lock()
	w.msgStore[sessionID] = append(w.msgStore[sessionID], msg)
	w.msgMu.Unlock()
}

func (w *SeshatWorkspace) newStreamingAssistantMessage(sessionID string) message.Message {
	now := time.Now().UnixMilli()
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      message.Assistant,
		Parts:     []message.ContentPart{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	w.appendMsg(sessionID, msg)
	w.msgBroker.Publish(pubsub.CreatedEvent, msg)
	return msg
}

func (w *SeshatWorkspace) rotateStreamingAssistantMessageIfNeeded() {
	w.streamMu.Lock()
	if !w.streamResponseDone || w.streamSess == "" {
		w.streamMu.Unlock()
		return
	}
	sessionID := w.streamSess
	w.streamMu.Unlock()

	msg := w.newStreamingAssistantMessage(sessionID)

	w.streamMu.Lock()
	w.streamMsg = &msg
	w.streamResponseDone = false
	w.streamToolUseID = ""
	w.streamToolUseName = ""
	w.streamToolInputBuf = ""
	w.streamMu.Unlock()
}

func (w *SeshatWorkspace) updateMsg(sessionID string, msg message.Message) {
	w.msgMu.Lock()
	msgs := w.msgStore[sessionID]
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].ID == msg.ID {
			msgs[i] = msg
			break
		}
	}
	w.msgMu.Unlock()
}

func (w *SeshatWorkspace) publishMsg(evt pubsub.EventType, _ string, msg message.Message) {
	w.msgBroker.Publish(evt, msg)
}

// convertSDKMessages converts the SDK message history to seshattui message types.
func convertSDKMessages(sessionID string, sdkMsgs []sdk.Message) []message.Message {
	// Pre-build tool result map keyed by tool_use_id.
	type resultEntry struct {
		content  string
		isError  bool
		metadata string
	}
	resultMap := make(map[string]resultEntry)
	for _, m := range sdkMsgs {
		if m.Role != sdk.RoleUser {
			continue
		}
		for _, block := range m.Content {
			if tr, ok := block.(sdk.ToolResultContent); ok {
				meta := ""
				if tr.Metadata != nil {
					if data, err := json.Marshal(tr.Metadata); err == nil {
						meta = string(data)
					}
				}
				resultMap[tr.ToolUseID] = resultEntry{
					content:  tr.Content,
					isError:  tr.IsError,
					metadata: meta,
				}
			}
		}
	}

	var out []message.Message
	for _, m := range sdkMsgs {
		ts := m.Timestamp.UnixMilli()
		if ts <= 0 {
			ts = time.Now().UnixMilli()
		}

		msg := message.Message{
			ID:        string(m.ID),
			SessionID: sessionID,
			CreatedAt: ts,
			UpdatedAt: ts,
		}

		switch m.Role {
		case sdk.RoleUser:
			msg.Role = message.User
			imageIndex := 0
			for _, block := range m.Content {
				switch b := block.(type) {
				case sdk.TextContent:
					promptText, attachments := splitPromptTextAttachments(b.Text)
					if promptText != "" || len(attachments) == 0 {
						msg.Parts = append(msg.Parts, message.TextContent{Text: promptText})
					}
					for _, attachment := range attachments {
						msg.Parts = append(msg.Parts, attachment)
					}
				case sdk.ImageContent:
					imageIndex++
					if attachment, ok := binaryFromSDKImage(b, imageIndex); ok {
						msg.Parts = append(msg.Parts, attachment)
					}
				}
			}
		case sdk.RoleAssistant:
			msg.Role = message.Assistant
			// Append content blocks in their natural order, then append Finish last.
			for _, block := range m.Content {
				switch b := block.(type) {
				case sdk.TextContent:
					msg.Parts = append(msg.Parts, message.TextContent{Text: b.Text})
				case sdk.ThinkingContent:
					msg.Parts = append(msg.Parts, message.ReasoningContent{Thinking: b.Thinking})
				case sdk.ToolUseContent:
					inputJSON, _ := json.Marshal(b.Input)
					tc := message.ToolCall{
						ID:       b.ID,
						Name:     b.Name,
						Input:    string(inputJSON),
						Finished: true,
					}
					msg.Parts = append(msg.Parts, tc)
					if r, ok := resultMap[b.ID]; ok {
						msg.Parts = append(msg.Parts, message.ToolResult{
							ToolCallID: b.ID,
							Name:       b.Name,
							Content:    r.content,
							Metadata:   r.metadata,
							IsError:    r.isError,
						})
					}
				}
			}
			if m.Metadata != nil && m.Metadata.StopReason != "" {
				finishReason := sdkStopToFinish(m.Metadata.StopReason)
				msg.Parts = append(msg.Parts, message.Finish{
					Reason: finishReason,
					Time:   ts,
				})
			}
		}

		if len(msg.Parts) > 0 {
			out = append(out, msg)
		}
	}
	return out
}

// prependPart inserts a part before any existing Finish parts (so text/tools
// appear before the finish marker in the parts slice).
func prependPart(parts []message.ContentPart, p message.ContentPart) []message.ContentPart {
	// Find the first Finish part index.
	for i, part := range parts {
		if _, ok := part.(message.Finish); ok {
			// Insert before it.
			result := make([]message.ContentPart, 0, len(parts)+1)
			result = append(result, parts[:i]...)
			result = append(result, p)
			result = append(result, parts[i:]...)
			return result
		}
	}
	return append(parts, p)
}

func sdkStopToFinish(stopReason string) message.FinishReason {
	switch stopReason {
	case "end_turn", "stop":
		return message.FinishReasonEndTurn
	case "max_tokens":
		return message.FinishReasonMaxTokens
	case "tool_use":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonEndTurn
	}
}

// ─── msgDebounce — batches streaming pubsub updates at a fixed interval ───────

type msgDebounce struct {
	mu         sync.Mutex
	latestMsg  message.Message
	latestSess string
	dirty      bool
	timer      *time.Timer
	delay      time.Duration
	flush      func(message.Message, string)
}

func newMsgDebounce(delay time.Duration, flush func(message.Message, string)) *msgDebounce {
	return &msgDebounce{delay: delay, flush: flush}
}

func (d *msgDebounce) update(msg message.Message, sessID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.latestMsg = msg
	d.latestSess = sessID
	d.dirty = true
	if d.timer == nil {
		d.timer = time.AfterFunc(d.delay, d.tick)
	}
}

func (d *msgDebounce) tick() {
	d.mu.Lock()
	if !d.dirty {
		d.timer = nil
		d.mu.Unlock()
		return
	}
	msg := d.latestMsg
	sess := d.latestSess
	d.dirty = false
	d.timer = nil
	d.mu.Unlock()
	d.flush(msg, sess)
}

func (d *msgDebounce) forceFlush() {
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if !d.dirty {
		d.mu.Unlock()
		return
	}
	msg := d.latestMsg
	sess := d.latestSess
	d.dirty = false
	d.mu.Unlock()
	d.flush(msg, sess)
}

// finishErrorText turns the error that ended a turn into the title and the
// details of the ERROR block in the chat. Without them the block is an empty
// red badge and the user cannot tell a provider timeout from a bug.
func finishErrorText(err error) (title, details string) {
	if err == nil {
		return "The turn ended with an error", ""
	}
	details = strings.TrimSpace(err.Error())
	title = details
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	if title == "" {
		title = "The turn ended with an error"
	}
	return title, details
}

// trackProviderRetry keeps the live notice of a session in step with the
// runtime: it is set while the runtime waits to retry a request the provider
// refused, and cleared once the turn moves again. It reports whether it
// handled the event, which is so for provider.retry only.
func (w *SeshatWorkspace) trackProviderRetry(ev sdk.RuntimeEvent) bool {
	sessionID := string(ev.SessionID)
	if sessionID == "" {
		return false
	}
	switch ev.Type {
	case sdk.RuntimeEventTypeProviderRetry:
		if ev.ProviderRetryEvent != nil {
			w.setSessionNotice(sessionID, providerRetryNotice(ev.ProviderRetryEvent))
		}
		return true
	case sdk.RuntimeEventTypeResponseChunk, sdk.RuntimeEventTypeTurnStarted,
		sdk.RuntimeEventTypeTurnCompleted, sdk.RuntimeEventTypeTurnFailed:
		w.setSessionNotice(sessionID, "")
	}
	return false
}

// providerRetryNotice words a provider.retry event for the status line.
func providerRetryNotice(ev *sdk.ProviderRetryRuntimeEvent) string {
	wait := time.Duration(ev.WaitMs) * time.Millisecond
	return fmt.Sprintf("%s, retrying in %s (attempt %d/%d)", ev.Reason, formatNoticeWait(wait), ev.Attempt, ev.MaxAttempts)
}

// formatNoticeWait renders a wait as "800ms", "12s" or "1m30s".
func formatNoticeWait(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int((d+500*time.Millisecond)/time.Second))
	default:
		seconds := int((d + 500*time.Millisecond) / time.Second)
		return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
	}
}

// setSessionNotice sets the live notice of a session and tells the UI. A notice
// that is already the one shown is not published again: response chunks arrive
// by the hundred and each one clears a notice that is already empty.
func (w *SeshatWorkspace) setSessionNotice(sessionID, notice string) {
	w.sessionsMu.Lock()
	defer w.sessionsMu.Unlock()
	s, ok := w.sessStore[sessionID]
	if !ok || s.Notice == notice {
		return
	}
	s.Notice = notice
	w.sessStore[sessionID] = s
	w.sessBroker.Publish(pubsub.UpdatedEvent, s)
}
