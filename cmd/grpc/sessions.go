package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	internaltypes "github.com/KPO-Tech/seshat-runtime/internal/types"
	appconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// defaultInteractionTimeout is how long the server waits for a client to answer
// a permission request or a question before it denies or cancels it, so a turn
// never hangs forever on a disconnected host.
const defaultInteractionTimeout = 5 * time.Minute

const (
	permissionModeBypass      = "bypass"
	permissionModeAuto        = "auto"
	permissionModeAcceptEdits = "acceptEdits"
	permissionModeOnRequest   = "onRequest"
	permissionModeNever       = "never"
	permissionModeGranular    = "granular"
)

// interactionAnswer is what a client decided about one pending request.
type interactionAnswer struct {
	// value is what a permission decision or a prompt answer carries.
	value any
	// cancelled is true when the user dismissed the request.
	cancelled bool
	// reason explains a denial to the model.
	reason string
	// unexpected marks an answer that did not arrive from a client.
	unexpected bool
}

// sessionHandle is one live conversation: its own SDK client (so two sessions
// never share a model, a credential or a permission mode), its engine session,
// its event hub and the decisions waiting for a client.
type sessionHandle struct {
	id      string
	model   string
	client  *sdk.Client
	session *sdk.Session
	hub     *eventHub

	// interactionTimeout bounds how long a prompt or a permission waits.
	interactionTimeout time.Duration

	// pending holds the requests waiting for an answer, by request id.
	pendingMu sync.Mutex
	pending   map[string]chan interactionAnswer
	nextReq   atomic.Uint64

	// turnMu keeps one turn at a time in a session, which is what the engine
	// expects: a second SubmitMessage would interleave two loops.
	turnMu sync.Mutex

	// lastEvent is the Unix nanosecond of the newest published event, read by
	// the idle watchdog.
	lastEvent atomic.Int64

	// turnID is the turn the engine is running, learned from its events, so a
	// request emitted between two of them still names its turn.
	turnIDMu sync.Mutex
	turnID   string

	// closed is set by close, so a late RPC fails instead of touching a closed
	// engine.
	closed atomic.Bool
}

func newSessionHandle(interactionTimeout time.Duration, client *sdk.Client, session *sdk.Session, model string) *sessionHandle {
	if interactionTimeout <= 0 {
		interactionTimeout = defaultInteractionTimeout
	}
	handle := &sessionHandle{
		id:                 session.GetID().String(),
		model:              model,
		client:             client,
		session:            session,
		hub:                newEventHub(),
		interactionTimeout: interactionTimeout,
		pending:            make(map[string]chan interactionAnswer),
	}
	session.SetResponseChunkFn(handle.publishChunk)
	session.SetRuntimeEventFn(handle.publishRuntimeEvent)
	return handle
}

// publish numbers an event on the hub and remembers it happened, which the
// idle watchdog reads.
func (h *sessionHandle) publish(event *pb.SessionEvent) *pb.SessionEvent {
	if event != nil {
		event.SessionId = h.id
		if event.TurnId == "" {
			h.turnIDMu.Lock()
			event.TurnId = h.turnID
			h.turnIDMu.Unlock()
		}
	}
	published := h.hub.publish(event)
	h.lastEvent.Store(time.Now().UnixNano())
	return published
}

// publishChunk sends one piece of a streamed response to the subscribers.
func (h *sessionHandle) publishChunk(chunk sdk.ResponseChunk) {
	if chunk.Delta == "" && chunk.PartialJSON == "" && chunk.ContentBlock == nil {
		return
	}
	h.publish(&pb.SessionEvent{
		Type: string(internaltypes.RuntimeEventTypeResponseChunk),
		Payload: &pb.SessionEvent_Chunk{
			Chunk: chunkToProto(&chunk),
		},
	})
}

// publishRuntimeEvent translates one engine event and sends it on.
func (h *sessionHandle) publishRuntimeEvent(event sdk.RuntimeEvent) {
	if event.TurnID != "" {
		h.turnIDMu.Lock()
		h.turnID = string(event.TurnID)
		h.turnIDMu.Unlock()
	}
	h.publish(sessionEventFromRuntime(event, h.id, h.model))
}

// promptBridge returns the function the engine calls when it needs a decision.
// It publishes the request with an id a client can answer, then blocks the tool
// call until RespondPermission or RespondPrompt arrives, the turn is cancelled,
// or the interaction timeout expires.
func (h *sessionHandle) promptBridge() internaltypes.PromptFn {
	return func(ctx context.Context, request internaltypes.PromptRequest) (internaltypes.PromptResponse, error) {
		if h.closed.Load() {
			return internaltypes.PromptResponse{Cancelled: true}, nil
		}

		requestID := fmt.Sprintf("%s-r%d", h.id, h.nextReq.Add(1))
		answers := make(chan interactionAnswer, 1)
		h.pendingMu.Lock()
		h.pending[requestID] = answers
		h.pendingMu.Unlock()
		defer func() {
			h.pendingMu.Lock()
			delete(h.pending, requestID)
			h.pendingMu.Unlock()
		}()

		expiresAt := time.Now().Add(h.interactionTimeout)
		if isPermissionPrompt(request) {
			h.publish(&pb.SessionEvent{
				Type:    string(internaltypes.RuntimeEventTypeToolPermissionRequired),
				Payload: &pb.SessionEvent_PermissionRequest{PermissionRequest: permissionRequestFromPrompt(requestID, request, expiresAt)},
			})
		} else {
			h.publish(&pb.SessionEvent{
				Type:    string(internaltypes.RuntimeEventTypePromptRequired),
				Payload: &pb.SessionEvent_PromptRequest{PromptRequest: promptRequestFromPrompt(requestID, request, expiresAt)},
			})
		}

		timer := time.NewTimer(h.interactionTimeout)
		defer timer.Stop()

		select {
		case answer := <-answers:
			return promptResponseFromAnswer(request, answer), nil
		case <-timer.C:
			return internaltypes.PromptResponse{
				Cancelled: true,
				Metadata:  map[string]any{"reason": "the client did not answer in time"},
			}, nil
		case <-ctx.Done():
			// A cancelled turn denies the tool call: the safe reading of a host
			// that went away is that it did not consent.
			return internaltypes.PromptResponse{
				Cancelled: true,
				Metadata:  map[string]any{"reason": "the turn was cancelled before the request was answered"},
			}, nil
		}
	}
}

// isPermissionPrompt separates a tool approval from a question a tool asks.
func isPermissionPrompt(request internaltypes.PromptRequest) bool {
	if request.Type != internaltypes.PromptTypeConfirm {
		return false
	}
	if request.Metadata == nil {
		return false
	}
	_, ok := request.Metadata["tool_name"]
	return ok
}

// promptResponseFromAnswer turns a decision into what the engine reads: true
// allows the call, the string "always" also remembers it for the session, and
// anything else denies it.
func promptResponseFromAnswer(request internaltypes.PromptRequest, answer interactionAnswer) internaltypes.PromptResponse {
	if answer.cancelled {
		return internaltypes.PromptResponse{Cancelled: true, Metadata: denyMetadata(answer.reason)}
	}
	if isPermissionPrompt(request) {
		decision, _ := answer.value.(string)
		switch decision {
		case "allow":
			return internaltypes.PromptResponse{Value: true}
		case "allow_session":
			return internaltypes.PromptResponse{Value: "always"}
		default:
			return internaltypes.PromptResponse{Value: false, Metadata: denyMetadata(answer.reason)}
		}
	}
	return internaltypes.PromptResponse{Value: answer.value, Metadata: answer.reasonMetadata()}
}

func denyMetadata(reason string) map[string]any {
	if reason == "" {
		reason = "denied by the client"
	}
	return map[string]any{"reason": reason}
}

func (a interactionAnswer) reasonMetadata() map[string]any {
	if a.reason == "" {
		return nil
	}
	return map[string]any{"reason": a.reason}
}

// resolve answers the pending request called requestID. It reports whether such
// a request was waiting.
func (h *sessionHandle) resolve(requestID string, answer interactionAnswer) bool {
	h.pendingMu.Lock()
	answers, ok := h.pending[requestID]
	if ok {
		delete(h.pending, requestID)
	}
	h.pendingMu.Unlock()
	if !ok {
		return false
	}
	select {
	case answers <- answer:
	default:
	}
	return true
}

// cancelPending releases every request of this session, which unblocks a turn
// that is waiting on a host that will not answer.
func (h *sessionHandle) cancelPending() {
	h.pendingMu.Lock()
	pending := h.pending
	h.pending = make(map[string]chan interactionAnswer)
	h.pendingMu.Unlock()
	for _, answers := range pending {
		select {
		case answers <- interactionAnswer{cancelled: true, reason: "the session was closed"}:
		default:
		}
	}
}

// tryStartTurn takes the turn slot of the session, so a second turn is refused
// instead of interleaving with the first.
func (h *sessionHandle) tryStartTurn() bool {
	return h.turnMu.TryLock()
}

// endTurn releases the turn slot.
func (h *sessionHandle) endTurn() {
	h.turnMu.Unlock()
}

// publishTurnFailed closes the turn with its error, so a stream sees why it
// ended instead of just stopping.
func (h *sessionHandle) publishTurnFailed(err error) {
	h.publish(&pb.SessionEvent{
		Type: string(internaltypes.RuntimeEventTypeTurnFailed),
		Payload: &pb.SessionEvent_TurnFailed{TurnFailed: &pb.TurnFailed{
			Code:    statusCodeName(err),
			Message: err.Error(),
		}},
	})
}

// runTurn runs one turn of the session, the turn slot having been taken by the
// caller, and publishes its outcome.
func (h *sessionHandle) runTurn(ctx context.Context, text string, images []*pb.ImageBlock, idle time.Duration) (*pb.TurnResult, error) {
	if h.closed.Load() {
		err := status.Error(codes.FailedPrecondition, "the session is closed")
		h.publishTurnFailed(err)
		return nil, err
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	turnCtx = internaltypes.WithPromptFn(turnCtx, h.promptBridge())

	h.lastEvent.Store(time.Now().UnixNano())
	if idle > 0 {
		go h.watchIdle(turnCtx, cancel, idle)
	}

	var (
		response *sdk.SessionResponse
		err      error
	)
	if len(images) > 0 {
		response, err = h.session.SubmitMessageWithContent(turnCtx, text, imageContents(images))
	} else {
		response, err = h.session.SubmitMessage(turnCtx, text)
	}
	if err != nil {
		h.publishTurnFailed(err)
		return nil, err
	}

	result := turnResultFrom(response, h.id)
	h.publish(&pb.SessionEvent{
		Type:       string(internaltypes.RuntimeEventTypeTurnCompleted),
		TurnNumber: int32(result.GetTurnNumber()),
		Payload:    &pb.SessionEvent_TurnCompleted{TurnCompleted: &pb.TurnCompleted{Result: result}},
	})
	return result, nil
}

// watchIdle cancels a turn that has produced nothing for too long, so a stream
// the client stopped reading cannot hold the session forever.
func (h *sessionHandle) watchIdle(ctx context.Context, cancel context.CancelFunc, idle time.Duration) {
	interval := idle / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := time.Unix(0, h.lastEvent.Load())
			if time.Since(last) > idle {
				cancel()
				return
			}
		}
	}
}

// statusCodeName names the failure for a client, without leaking an internal
// error type.
func statusCodeName(err error) string {
	code := status.Code(err)
	if code == codes.Unknown {
		code = codes.Internal
	}
	return code.String()
}

func imageContents(images []*pb.ImageBlock) []sdk.ImageContent {
	out := make([]sdk.ImageContent, 0, len(images))
	for _, image := range images {
		if image == nil {
			continue
		}
		sourceType := image.GetSourceType()
		if sourceType == "" {
			sourceType = "base64"
		}
		var content internaltypes.ImageContent
		content.Source.Type = sourceType
		content.Source.MediaType = image.GetMediaType()
		content.Source.Data = image.GetData()
		out = append(out, content)
	}
	return out
}

// close ends the session, its subscriptions and its engine.
func (h *sessionHandle) close() error {
	if h.closed.Swap(true) {
		return nil
	}
	h.cancelPending()
	h.hub.close()
	_ = h.session.Close()
	if h.client != nil {
		_ = h.client.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// sessionRegistry owns the live sessions of the server and the catalog client
// the server-list RPCs use.
type sessionRegistry struct {
	host               appconfig.Config
	interactionTimeout time.Duration
	allowStdioMCP      bool
	tokenRequired      bool
	tlsEnabled         bool

	mu       sync.Mutex
	sessions map[string]*sessionHandle

	catalogOnce sync.Once
	catalog     *sdk.Client
	catalogErr  error
}

func newSessionRegistry(host appconfig.Config) *sessionRegistry {
	return &sessionRegistry{
		host:               host,
		interactionTimeout: interactionTimeoutFromEnv(),
		sessions:           make(map[string]*sessionHandle),
	}
}

// interactionTimeoutFromEnv reads how long a decision may wait.
func interactionTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SESHAT_GRPC_INTERACTION_TIMEOUT"))
	if raw == "" {
		return defaultInteractionTimeout
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return defaultInteractionTimeout
	}
	return value
}

// catalogClient returns one client the server-list RPCs share. It is built once:
// listing conversations does not need a model, a credential or a tool.
func (r *sessionRegistry) catalogClient() (*sdk.Client, error) {
	r.catalogOnce.Do(func() {
		cfg, err := buildSDKClientConfig(r.host, &pb.QueryRequest{})
		if err != nil {
			// A server whose provider is not configured can still list, search
			// and delete conversations, and read its memory: those need the
			// store, not a credential. Fall back to the persistence settings
			// alone rather than refusing the whole catalog.
			base := sdk.DefaultClientConfig()
			base.PermissionMode = sdk.PermissionModeNever
			base.PersistSessions = true
			base.SessionSQLitePath = appconfig.EffectiveSessionDBPath(r.host)
			base.WorkingDir = effectiveWorkingDir(r.host)
			base.Model = resolveModelIdentifier(r.host, "")
			cfg = base
		}
		r.catalog, r.catalogErr = sdk.NewClient(cfg)
	})
	return r.catalog, r.catalogErr
}

// sessionConfig builds the client configuration of a new session: the server's
// configuration, with what the request overrides.
func (r *sessionRegistry) sessionConfig(req *pb.CreateSessionRequest) (*sdk.ClientConfig, error) {
	cfg, err := buildSDKClientConfig(r.host, &pb.QueryRequest{
		Model:     req.GetModel(),
		MaxTokens: req.GetMaxTokens(),
		ApiKey:    req.GetApiKey(),
	})
	if err != nil {
		return nil, err
	}

	mode, err := parsePermissionMode(req.GetPermissionMode())
	if err != nil {
		return nil, err
	}
	cfg.PermissionMode = mode

	if workingDir := strings.TrimSpace(req.GetWorkingDir()); workingDir != "" {
		cfg.WorkingDir = workingDir
	}
	if err := applySessionOptions(cfg, req); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applySessionOptions copies the per-session knobs of a CreateSessionRequest
// onto the client configuration, so a client can describe the agent it wants and
// not only name a model. Every knob is optional: zero, or unset for the
// `optional` fields, keeps what the server's configuration says.
func applySessionOptions(cfg *sdk.ClientConfig, req *pb.CreateSessionRequest) error {
	limits := []struct {
		name  string
		value int32
		apply func(int)
	}{
		{"max_iterations", req.GetMaxIterations(), func(v int) { cfg.MaxIterations = v }},
		{"max_turns", req.GetMaxTurns(), func(v int) { cfg.MaxTurns = v }},
		{"turn_token_budget", req.GetTurnTokenBudget(), func(v int) { cfg.TurnTokenBudget = v }},
		{"max_consecutive_denials", req.GetMaxConsecutiveDenials(), func(v int) { cfg.MaxConsecutiveDenials = v }},
	}
	for _, limit := range limits {
		if limit.value < 0 {
			return status.Errorf(codes.InvalidArgument, "%s cannot be negative", limit.name)
		}
		if limit.value > 0 {
			limit.apply(int(limit.value))
		}
	}
	if req.AutoCompact != nil {
		cfg.AutoCompact = req.GetAutoCompact()
	}
	if req.EnableToolSearch != nil {
		cfg.EnableToolSearch = req.GetEnableToolSearch()
	}
	return nil
}

func parsePermissionMode(raw string) (sdk.PermissionMode, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		// A session created over the network is interactive by default: the
		// server asks, through a permission_request, and a client decides.
		return sdk.PermissionModeOnRequest, nil
	}
	switch sdk.PermissionMode(trimmed) {
	case sdk.PermissionModeBypass, sdk.PermissionModeAuto, sdk.PermissionModeAcceptEdits,
		sdk.PermissionModeOnRequest, sdk.PermissionModeNever, sdk.PermissionModeGranular:
		return sdk.PermissionMode(trimmed), nil
	default:
		return "", status.Errorf(codes.InvalidArgument,
			"unknown permission mode %q: use bypass, auto, acceptEdits, onRequest, never or granular", raw)
	}
}

// create opens a conversation and attaches it.
func (r *sessionRegistry) create(ctx context.Context, req *pb.CreateSessionRequest) (*sessionHandle, error) {
	cfg, err := r.sessionConfig(req)
	if err != nil {
		return nil, err
	}

	client, err := sdk.NewClient(cfg)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "create client: %v", err)
	}
	session, err := client.CreateSession(ctx)
	if err != nil {
		_ = client.Close()
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}

	handle := newSessionHandle(r.interactionTimeout, client, session, modelString(cfg.Model))

	if template := req.GetSystemPromptTemplate(); template != "" {
		session.SetSystemPromptTemplate(template)
	}
	if appendText := req.GetAppendSystemPrompt(); appendText != "" {
		session.SetAppendSystemPrompt(appendText)
	}
	if workingDir := strings.TrimSpace(req.GetWorkingDir()); workingDir != "" {
		session.SetWorkingDirectory(workingDir)
	}
	if len(req.GetTools()) > 0 {
		if err := applyRequestedTools(&sdkSessionAdapter{session: session}, req.GetTools()); err != nil {
			_ = handle.close()
			return nil, err
		}
	}

	r.mu.Lock()
	r.sessions[handle.id] = handle
	r.mu.Unlock()
	return handle, nil
}

// attach loads a persisted conversation and keeps it live. It is how a client
// resumes a conversation it did not create in this process.
func (r *sessionRegistry) attach(ctx context.Context, sessionID string) (*sessionHandle, error) {
	cfg, err := buildSDKClientConfig(r.host, &pb.QueryRequest{})
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "load session: %v", err)
	}
	client, err := sdk.NewClient(cfg)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "create client: %v", err)
	}
	session, err := client.LoadSession(ctx, sdk.SessionID(sessionID))
	if err != nil {
		_ = client.Close()
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "closed") {
			return nil, status.Errorf(codes.NotFound, "load session %q: %v", sessionID, err)
		}
		return nil, status.Errorf(codes.FailedPrecondition, "load session %q: %v", sessionID, err)
	}

	handle := newSessionHandle(r.interactionTimeout, client, session, modelString(cfg.Model))
	r.mu.Lock()
	// Another call may have attached it while this one was loading.
	if existing, ok := r.sessions[handle.id]; ok {
		r.mu.Unlock()
		_ = handle.close()
		return existing, nil
	}
	r.sessions[handle.id] = handle
	r.mu.Unlock()
	return handle, nil
}

// get returns the attached session, or nil.
func (r *sessionRegistry) get(sessionID string) *sessionHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID]
}

// handleFor returns the attached session, attaching it from its stored
// transcript when this process does not hold it yet.
func (r *sessionRegistry) handleFor(ctx context.Context, sessionID string) (*sessionHandle, error) {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if handle := r.get(trimmed); handle != nil {
		return handle, nil
	}
	return r.attach(ctx, trimmed)
}

// remove detaches and closes a session.
func (r *sessionRegistry) remove(sessionID string) *sessionHandle {
	r.mu.Lock()
	handle := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	return handle
}

// count is the number of live sessions.
func (r *sessionRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// ids lists the live sessions, sorted, for diagnostics.
func (r *sessionRegistry) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// closeAll ends every session; the server calls it on shutdown.
func (r *sessionRegistry) closeAll() []error {
	r.mu.Lock()
	sessions := r.sessions
	r.sessions = make(map[string]*sessionHandle)
	r.mu.Unlock()

	var errs []error
	for _, handle := range sessions {
		if err := handle.close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.catalog != nil {
		errs = append(errs, r.catalog.Close())
	}
	return errs
}

// modelString renders a model the way the contract spells it: "provider:model".
func modelString(model sdk.ModelIdentifier) string {
	if model.Provider == "" {
		return model.Model
	}
	if model.Model == "" {
		return string(model.Provider)
	}
	return fmt.Sprintf("%s:%s", model.Provider, model.Model)
}

// ---------------------------------------------------------------------------
// Session rendering
// ---------------------------------------------------------------------------

// sessionToProto renders an attached session.
func sessionToProto(handle *sessionHandle) *pb.Session {
	if handle == nil || handle.session == nil {
		return &pb.Session{}
	}
	metadata := handle.session.GetMetadata()
	out := &pb.Session{
		Id:             handle.id,
		Model:          handle.model,
		TurnNumber:     int32(handle.session.GetTurnNumber()),
		TotalTokens:    int64(handle.session.GetTotalTokens()),
		PermissionMode: string(handle.session.GetPermissionMode()),
		ExecutionMode:  string(handle.session.GetExecutionMode()),
		Tools:          handle.session.GetToolNames(),
	}
	if handle.client != nil {
		out.AutoModeAvailable = handle.client.AutoModeAvailable()
	}
	if metadata != nil {
		out.Title = metadata.Title
		out.Status = string(metadata.Status)
		out.CreatedAt = timestampFrom(metadata.CreatedAt)
		out.UpdatedAt = timestampFrom(metadata.UpdatedAt)
		out.WorkingDir = metadata.RootPath
		if metadata.Model != "" {
			out.Model = metadata.Model
		}
		out.Info = sessionMetadataToProto(metadata)
	}
	return out
}

func sessionMetadataToProto(metadata *internaltypes.SessionMetadata) *pb.SessionInfo {
	if metadata == nil {
		return nil
	}
	return &pb.SessionInfo{
		Id:          string(metadata.ID),
		Title:       metadata.Title,
		Status:      string(metadata.Status),
		CreatedAt:   timestampFrom(metadata.CreatedAt),
		UpdatedAt:   timestampFrom(metadata.UpdatedAt),
		TotalTurns:  int32(metadata.TotalTurns),
		TotalTokens: int64(metadata.TotalTokens),
	}
}

// sessionInfoToProto renders a stored conversation summary.
func sessionInfoToProto(info *sdk.SessionInfo) *pb.SessionInfo {
	if info == nil {
		return nil
	}
	out := &pb.SessionInfo{
		Id:          string(info.ID),
		Title:       info.Title,
		Status:      string(info.Status),
		TotalTurns:  int32(info.TotalTurns),
		TotalTokens: int64(info.TotalTokens),
		Preview:     info.Preview,
		Subagent:    info.Subagent,
		ParentId:    string(info.ParentID),
	}
	if info.CreatedAt > 0 {
		out.CreatedAt = timestampFrom(time.UnixMilli(info.CreatedAt))
	}
	if info.UpdatedAt > 0 {
		out.UpdatedAt = timestampFrom(time.UnixMilli(info.UpdatedAt))
	}
	return out
}

// errSessionNotFound is the error a lookup of an unknown session returns.
var errSessionNotFound = errors.New("session not found")
