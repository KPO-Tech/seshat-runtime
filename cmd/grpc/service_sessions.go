package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
)

// sessionServer is the SessionService: everything inside one conversation.
type sessionServer struct {
	pb.UnimplementedSessionServiceServer
	registry *sessionRegistry
}

// NewSessionServer builds the session service over the server's registry.
func NewSessionServer(registry *sessionRegistry) *sessionServer {
	return &sessionServer{registry: registry}
}

var _ pb.SessionServiceServer = (*sessionServer)(nil)

// idleTimeout is how long a turn may stay silent before it is abandoned: the
// request's own value, or the server's (0 means the engine's stream timeout).
func idleTimeout(req *pb.SubmitMessageRequest) time.Duration {
	seconds := req.GetIdleTimeoutSeconds()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *sessionServer) handle(ctx context.Context, sessionID string) (*sessionHandle, error) {
	if s.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "sessions are not available on this server")
	}
	return s.registry.handleFor(ctx, sessionID)
}

// SubmitMessage runs a turn and returns its result.
func (s *sessionServer) SubmitMessage(ctx context.Context, req *pb.SubmitMessageRequest) (*pb.TurnResult, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetText()) == "" && len(req.GetImages()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}
	if !handle.tryStartTurn() {
		return nil, status.Error(codes.Aborted, "a turn is already running in this session")
	}
	defer handle.endTurn()

	result, turnErr := handle.runTurn(ctx, req.GetText(), req.GetImages(), idleTimeout(req))
	if turnErr != nil {
		return nil, statusTurnError(turnErr)
	}
	return result, nil
}

// SubmitMessageStream runs a turn and streams its events. The stream ends on
// TurnCompleted or TurnFailed, both of which are sent to the client.
func (s *sessionServer) SubmitMessageStream(req *pb.SubmitMessageRequest, stream grpc.ServerStreamingServer[pb.SessionEvent]) error {
	ctx := stream.Context()
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.GetText()) == "" && len(req.GetImages()) == 0 {
		return status.Error(codes.InvalidArgument, "text is required")
	}
	if !handle.tryStartTurn() {
		return status.Error(codes.Aborted, "a turn is already running in this session")
	}

	// Subscribe before the turn starts: a chunk emitted before the subscription
	// would otherwise be lost.
	_, events, cancel := handle.hub.subscribe(handle.hub.lastSequence())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer handle.endTurn()
		_, turnErr := handle.runTurn(ctx, req.GetText(), req.GetImages(), idleTimeout(req))
		done <- turnErr
	}()

	for {
		select {
		case event, opened := <-events:
			if !opened {
				// The session was closed under the turn; the closing event, if
				// any, has already been sent.
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
			if isTurnTerminal(event) {
				return nil
			}
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}

// Subscribe follows a session's events without starting a turn.
func (s *sessionServer) Subscribe(req *pb.SubscribeRequest, stream grpc.ServerStreamingServer[pb.SessionEvent]) error {
	handle, err := s.handle(stream.Context(), req.GetSessionId())
	if err != nil {
		return err
	}

	replay, events, cancel := handle.hub.subscribe(req.GetFromSequence())
	defer cancel()

	for _, event := range replay {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	for {
		select {
		case event, opened := <-events:
			if !opened {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		}
	}
}

// isTurnTerminal reports whether the event closes the turn it belongs to.
func isTurnTerminal(event *pb.SessionEvent) bool {
	switch event.GetPayload().(type) {
	case *pb.SessionEvent_TurnCompleted, *pb.SessionEvent_TurnFailed:
		return true
	default:
		return false
	}
}

func (s *sessionServer) GetMessages(ctx context.Context, req *pb.GetMessagesRequest) (*pb.GetMessagesResponse, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return &pb.GetMessagesResponse{Messages: messagesToProto(handle.session.GetMessages())}, nil
}

func (s *sessionServer) GetHistory(ctx context.Context, req *pb.GetHistoryRequest) (*pb.GetHistoryResponse, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	history, err := handle.session.History()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "history: %v", err)
	}
	return &pb.GetHistoryResponse{
		Archived: messagesToProto(history.Archived),
		Current:  messagesToProto(history.Current),
	}, nil
}

func (s *sessionServer) GetState(ctx context.Context, req *pb.GetStateRequest) (*pb.SessionState, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	state := &pb.SessionState{
		Metadata:       sessionStateMetadata(handle),
		PermissionMode: string(handle.session.GetPermissionMode()),
		ExecutionMode:  string(handle.session.GetExecutionMode()),
		TurnNumber:     int32(handle.session.GetTurnNumber()),
		TotalTokens:    int64(handle.session.GetTotalTokens()),
		Tools:          handle.session.GetToolNames(),
	}
	if handle.client != nil {
		state.AutoModeAvailable = handle.client.AutoModeAvailable()
	}
	return state, nil
}

func (s *sessionServer) Interrupt(ctx context.Context, req *pb.InterruptRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := handle.session.Interrupt(); err != nil {
		return nil, status.Errorf(codes.Internal, "interrupt: %v", err)
	}
	return &pb.Ack{}, nil
}

func (s *sessionServer) Compact(ctx context.Context, req *pb.CompactRequest) (*pb.CompactResponse, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	outcome, err := handle.session.Compact(ctx)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "compact: %v", err)
	}
	return &pb.CompactResponse{
		PreTokens:  int32(outcome.PreTokens),
		PostTokens: int32(outcome.PostTokens),
	}, nil
}

func (s *sessionServer) Close(ctx context.Context, req *pb.CloseSessionRequest) (*pb.Ack, error) {
	if s.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "sessions are not available on this server")
	}
	handle, err := s.registry.handleFor(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	s.registry.remove(handle.id)
	if err := handle.close(); err != nil {
		return nil, status.Errorf(codes.Internal, "close: %v", err)
	}
	return &pb.Ack{}, nil
}

func (s *sessionServer) SetPermissionMode(ctx context.Context, req *pb.SetPermissionModeRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	mode, err := parsePermissionMode(req.GetMode())
	if err != nil {
		return nil, err
	}
	handle.session.SetPermissionMode(mode)
	return &pb.Ack{}, nil
}

func (s *sessionServer) SetWorkingDirectory(ctx context.Context, req *pb.SetWorkingDirectoryRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.GetPath())
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	handle.session.SetWorkingDirectory(path)
	return &pb.Ack{}, nil
}

func (s *sessionServer) SetSystemPrompt(ctx context.Context, req *pb.SetSystemPromptRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if req.GetAppendOnly() {
		handle.session.SetAppendSystemPrompt(req.GetAppend())
	} else {
		handle.session.SetSystemPromptTemplate(req.GetTemplate())
		if req.GetAppend() != "" {
			handle.session.SetAppendSystemPrompt(req.GetAppend())
		}
	}
	return &pb.Ack{}, nil
}

func (s *sessionServer) SetPlanMode(ctx context.Context, req *pb.SetPlanModeRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if req.GetEnabled() {
		handle.session.ForcePlanMode()
	} else {
		handle.session.ClearPlanMode()
	}
	return &pb.Ack{}, nil
}

func (s *sessionServer) ApprovePlan(ctx context.Context, req *pb.ApprovePlanRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	handle.session.ApprovePlan()
	return &pb.Ack{}, nil
}

func (s *sessionServer) ListTools(ctx context.Context, req *pb.ListToolsRequest) (*pb.ListToolsResponse, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}

	enabled := make(map[string]struct{})
	for _, name := range handle.session.GetToolNames() {
		enabled[name] = struct{}{}
	}

	seen := make(map[string]struct{})
	tools := make([]*pb.ToolInfo, 0, len(enabled))
	if handle.client != nil {
		if surface, surfaceErr := handle.client.BuildToolSurface(ctx); surfaceErr == nil && surface != nil {
			for _, definition := range surface.Tools {
				if filter := req.GetCategoryFilter(); filter != "" && !strings.EqualFold(definition.Category, filter) {
					continue
				}
				_, isEnabled := enabled[definition.Name]
				tools = append(tools, &pb.ToolInfo{
					Name:        definition.Name,
					Description: definition.Description,
					Category:    definition.Category,
					Enabled:     isEnabled,
					// A tool the client surface offers but the session does not
					// currently hold was deferred or unregistered.
					Deferred: !isEnabled,
				})
				seen[definition.Name] = struct{}{}
			}
		}
	}
	// A tool registered in this session but absent from the client surface (a
	// custom tool of the session) is still part of its surface.
	for name := range enabled {
		if _, ok := seen[name]; ok {
			continue
		}
		tools = append(tools, &pb.ToolInfo{Name: name, Enabled: true})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	return &pb.ListToolsResponse{Tools: tools, TotalCount: int32(len(tools))}, nil
}

func (s *sessionServer) SetTools(ctx context.Context, req *pb.SetToolsRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if len(req.GetTools()) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"tools is required: a tool unregistered from a session cannot be offered again in it, so an empty list is refused")
	}
	if err := applyRequestedTools(&sdkSessionAdapter{session: handle.session}, req.GetTools()); err != nil {
		return nil, err
	}
	return &pb.Ack{}, nil
}

func (s *sessionServer) RespondPermission(ctx context.Context, req *pb.RespondPermissionRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	decision := strings.ToLower(strings.TrimSpace(req.GetDecision()))
	switch decision {
	case "allow", "allow_session", "deny":
	default:
		return nil, status.Errorf(codes.InvalidArgument,
			"unknown decision %q: use allow, allow_session or deny", req.GetDecision())
	}
	if !handle.resolve(req.GetRequestId(), interactionAnswer{value: decision, reason: req.GetReason()}) {
		return nil, status.Error(codes.NotFound,
			"no permission request is waiting with this id (it may have expired or been answered already)")
	}
	return &pb.Ack{}, nil
}

func (s *sessionServer) RespondPrompt(ctx context.Context, req *pb.RespondPromptRequest) (*pb.Ack, error) {
	handle, err := s.handle(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	answer := interactionAnswer{
		value:     valueFromProto(req.GetValue()),
		cancelled: req.GetCancelled(),
	}
	if !handle.resolve(req.GetRequestId(), answer) {
		return nil, status.Error(codes.NotFound,
			"no prompt request is waiting with this id (it may have expired or been answered already)")
	}
	return &pb.Ack{}, nil
}

// sessionStateMetadata renders the metadata of a live session.
func sessionStateMetadata(handle *sessionHandle) *pb.SessionMetadata {
	metadata := handle.session.GetMetadata()
	if metadata == nil {
		return nil
	}
	out := &pb.SessionMetadata{
		Id:           string(metadata.ID),
		Title:        metadata.Title,
		Status:       string(metadata.Status),
		CreatedAt:    timestampFrom(metadata.CreatedAt),
		UpdatedAt:    timestampFrom(metadata.UpdatedAt),
		RootPath:     metadata.RootPath,
		Model:        metadata.Model,
		TotalTurns:   int32(metadata.TotalTurns),
		TotalTokens:  int64(metadata.TotalTokens),
		MaxTokens:    int32(metadata.MaxTokens),
		CompactCount: int32(metadata.CompactCount),
	}
	if metadata.LastCompactedAt != nil {
		out.LastCompactedAt = timestampFrom(*metadata.LastCompactedAt)
	}
	return out
}

// statusTurnError keeps the gRPC code of a turn that failed, so a client sees
// Cancelled or DeadlineExceeded instead of a generic Internal.
func statusTurnError(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Errorf(codes.Internal, "turn: %v", err)
}
