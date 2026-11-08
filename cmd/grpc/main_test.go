package main

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
	appconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeStreamingSession struct {
	id              sdk.SessionID
	toolNames       []string
	unregistered    []string
	closeCalls      int
	responseChunkFn func(sdk.ResponseChunk)
	runtimeEventFn  func(sdk.RuntimeEvent)
	submitMessageFn func(ctx context.Context, content string) (*sdk.SessionResponse, error)
}

func (s *fakeStreamingSession) SetResponseChunkFn(fn func(sdk.ResponseChunk)) {
	s.responseChunkFn = fn
}

func (s *fakeStreamingSession) SetRuntimeEventFn(fn func(sdk.RuntimeEvent)) {
	s.runtimeEventFn = fn
}

func (s *fakeStreamingSession) SubmitMessage(ctx context.Context, content string) (*sdk.SessionResponse, error) {
	if s.submitMessageFn == nil {
		return &sdk.SessionResponse{}, nil
	}
	return s.submitMessageFn(ctx, content)
}

func (s *fakeStreamingSession) GetID() sdk.SessionID {
	return s.id
}

func (s *fakeStreamingSession) GetToolNames() []string {
	return append([]string(nil), s.toolNames...)
}

func (s *fakeStreamingSession) UnregisterTool(name string) error {
	s.unregistered = append(s.unregistered, name)
	return nil
}

func (s *fakeStreamingSession) Close() error {
	s.closeCalls++
	return nil
}

type fakeSDKClient struct {
	mu            sync.Mutex
	createSession grpcSDKSession
	createErr     error
	loadSessions  map[sdk.SessionID]grpcSDKSession
	loadErr       error
	createCalls   int
	loadCalls     []sdk.SessionID
	closeCalls    int
}

func (c *fakeSDKClient) CreateSession(ctx context.Context) (grpcSDKSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++
	if c.createErr != nil {
		return nil, c.createErr
	}
	return c.createSession, nil
}

func (c *fakeSDKClient) LoadSession(ctx context.Context, sessionID sdk.SessionID) (grpcSDKSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadCalls = append(c.loadCalls, sessionID)
	if c.loadErr != nil {
		return nil, c.loadErr
	}
	session, ok := c.loadSessions[sessionID]
	if !ok {
		return nil, status.Error(codes.NotFound, "missing session")
	}
	return session, nil
}

func (c *fakeSDKClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCalls++
	return nil
}

type fakeQueryStreamServer struct {
	ctx       context.Context
	responses []*pb.QueryResponse
}

func (s *fakeQueryStreamServer) SetHeader(metadata.MD) error { return nil }

func (s *fakeQueryStreamServer) SendHeader(metadata.MD) error { return nil }

func (s *fakeQueryStreamServer) SetTrailer(metadata.MD) {}

func (s *fakeQueryStreamServer) Context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func (s *fakeQueryStreamServer) Send(resp *pb.QueryResponse) error {
	s.responses = append(s.responses, resp)
	return nil
}

func (s *fakeQueryStreamServer) SendMsg(m any) error {
	resp, ok := m.(*pb.QueryResponse)
	if !ok {
		return status.Errorf(codes.Internal, "unexpected message type %T", m)
	}
	return s.Send(resp)
}

func (s *fakeQueryStreamServer) RecvMsg(any) error { return nil }

func (s *fakeQueryStreamServer) responsesCopy() []*pb.QueryResponse {
	return append([]*pb.QueryResponse(nil), s.responses...)
}

func TestStreamQuerySessionEmitsChunksRuntimeEventsAndFinalResponse(t *testing.T) {
	session := &fakeStreamingSession{
		id: sdk.SessionID("sess-99"),
	}
	session.submitMessageFn = func(ctx context.Context, content string) (*sdk.SessionResponse, error) {
		if content != "hello" {
			t.Fatalf("expected prompt hello, got %q", content)
		}
		if session.responseChunkFn != nil {
			session.responseChunkFn(sdk.ResponseChunk{
				Type:      types.APIChunkTypeContentBlockDelta,
				DeltaType: "text_delta",
				Delta:     "hel",
			})
		}
		if session.runtimeEventFn != nil {
			session.runtimeEventFn(sdk.RuntimeEvent{
				Type:       sdk.RuntimeEventTypeTurnStarted,
				SessionID:  sdk.SessionID("sess-99"),
				TurnID:     "turn-1",
				TurnNumber: 1,
				Timestamp:  time.Date(2026, 5, 9, 10, 11, 12, 0, time.UTC),
			})
			session.runtimeEventFn(sdk.RuntimeEvent{
				Type:       sdk.RuntimeEventTypeToolProgress,
				SessionID:  sdk.SessionID("sess-99"),
				TurnID:     "turn-1",
				TurnNumber: 1,
				ToolProgress: &sdk.ToolProgress{
					ToolName:        "bash",
					Stage:           sdk.ToolProgressStageRunning,
					Message:         "calling tool",
					PercentComplete: 66,
				},
			})
		}
		if session.responseChunkFn != nil {
			session.responseChunkFn(sdk.ResponseChunk{
				Type:      types.APIChunkTypeContentBlockDelta,
				DeltaType: "text_delta",
				Delta:     "lo",
			})
		}
		return &sdk.SessionResponse{
			StopReason: types.StopReasonEndTurn,
			TurnNumber: 1,
			IsComplete: true,
			Usage:      &types.TokenUsage{InputTokens: 10, OutputTokens: 5},
			Messages: []types.Message{
				types.AssistantMessage("msg-1", []types.ContentBlock{types.TextContent{Text: "hello"}}),
			},
		}, nil
	}

	var sent []*pb.QueryResponse
	err := streamQuerySession(context.Background(), &pb.QueryRequest{
		Prompt: "hello",
		Model:  "anthropic:claude-test",
	}, "anthropic:claude-test", session, func(resp *pb.QueryResponse) error {
		sent = append(sent, resp)
		return nil
	})
	if err != nil {
		t.Fatalf("streamQuerySession failed: %v", err)
	}
	if len(sent) != 5 {
		t.Fatalf("expected 5 streamed messages, got %d", len(sent))
	}

	if sent[0].Content != "hel" || sent[0].ConversationId != "" {
		t.Fatalf("expected first message to be raw chunk, got %#v", sent[0])
	}
	if sent[0].ItemType != queryResponseItemTypeChunk || sent[0].Chunk == nil || sent[0].Chunk.Delta != "hel" {
		t.Fatalf("expected first message to carry typed chunk payload, got %#v", sent[0])
	}
	if sent[1].ItemType != queryResponseItemTypeRuntimeEvent || sent[1].RuntimeEvent == nil || sent[1].RuntimeEvent.Type != string(sdk.RuntimeEventTypeTurnStarted) {
		t.Fatalf("expected typed turn.started runtime event, got %#v", sent[1])
	}
	if sent[2].ItemType != queryResponseItemTypeRuntimeEvent || sent[2].RuntimeEvent == nil || sent[2].RuntimeEvent.Type != string(sdk.RuntimeEventTypeToolProgress) {
		t.Fatalf("expected typed tool.progress runtime event, got %#v", sent[2])
	}
	if sent[3].Content != "lo" || sent[3].ConversationId != "" {
		t.Fatalf("expected fourth message to be raw chunk, got %#v", sent[3])
	}
	if sent[3].ItemType != queryResponseItemTypeChunk || sent[3].Chunk == nil || sent[3].Chunk.Delta != "lo" {
		t.Fatalf("expected fourth message to carry typed chunk payload, got %#v", sent[3])
	}
	if sent[2].RuntimeEvent.ToolName != "bash" || sent[2].RuntimeEvent.ToolStage != string(sdk.ToolProgressStageRunning) {
		t.Fatalf("expected typed bash tool progress payload, got %#v", sent[2].RuntimeEvent)
	}

	final := sent[4]
	if final.ConversationId != "sess-99" {
		t.Fatalf("expected final conversation id sess-99, got %q", final.ConversationId)
	}
	if final.Content != "hello" {
		t.Fatalf("expected final content hello, got %q", final.Content)
	}
	if final.ItemType != queryResponseItemTypeFinal {
		t.Fatalf("expected final item type %q, got %q", queryResponseItemTypeFinal, final.ItemType)
	}
	if final.TokenUsage == nil || final.TokenUsage.InputTokens != 10 || final.TokenUsage.OutputTokens != 5 {
		t.Fatalf("expected final token usage, got %#v", final.TokenUsage)
	}
}

func TestApplyRequestedToolsFiltersToRequestedSet(t *testing.T) {
	session := &fakeStreamingSession{
		toolNames: []string{"bash", "read", "write"},
	}

	if err := applyRequestedTools(session, []string{"read", "bash"}); err != nil {
		t.Fatalf("applyRequestedTools failed: %v", err)
	}

	if len(session.unregistered) != 1 || session.unregistered[0] != "write" {
		t.Fatalf("expected write to be unregistered, got %#v", session.unregistered)
	}
}

func TestApplyRequestedToolsRejectsUnknownTools(t *testing.T) {
	session := &fakeStreamingSession{
		toolNames: []string{"bash", "read"},
	}

	err := applyRequestedTools(session, []string{"read", "search"})
	if err == nil {
		t.Fatal("expected applyRequestedTools to reject unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tools requested: search") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLatestResponseTextReturnsLatestAssistantOnly(t *testing.T) {
	messages := []types.Message{
		types.AssistantMessage("msg-1", []types.ContentBlock{types.TextContent{Text: "first"}}),
		types.UserMessage("msg-2", "ignored"),
		types.AssistantMessage("msg-3", []types.ContentBlock{types.TextContent{Text: "second"}}),
	}

	if got := latestResponseText(messages); got != "second" {
		t.Fatalf("expected latest assistant text, got %q", got)
	}
}

func TestHealthCheckReportsVersionAndElapsedUptime(t *testing.T) {
	server := NewSeshatServer(appconfig.DefaultConfig())
	server.version = "test-version"
	server.startedAt = time.Now().Add(-3 * time.Second)

	resp, err := server.HealthCheck(context.Background(), &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
	if resp.Version != "test-version" {
		t.Fatalf("expected version test-version, got %q", resp.Version)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected ok health, got %q", resp.Status)
	}
	if resp.Uptime == "" || resp.Uptime == "0s" {
		t.Fatalf("expected non-zero uptime, got %q", resp.Uptime)
	}
}

func TestQueryLoadsSessionFiltersToolsAndClosesResources(t *testing.T) {
	session := &fakeStreamingSession{
		id:        sdk.SessionID("sess-42"),
		toolNames: []string{"bash", "read"},
	}
	session.submitMessageFn = func(ctx context.Context, content string) (*sdk.SessionResponse, error) {
		if content != "resume" {
			t.Fatalf("expected resume prompt, got %q", content)
		}
		return &sdk.SessionResponse{
			StopReason: types.StopReasonEndTurn,
			IsComplete: true,
			Usage:      &types.TokenUsage{InputTokens: 3, OutputTokens: 4},
			Messages: []types.Message{
				types.AssistantMessage("msg-1", []types.ContentBlock{types.TextContent{Text: "loaded reply"}}),
			},
		}, nil
	}

	fakeClient := &fakeSDKClient{
		loadSessions: map[sdk.SessionID]grpcSDKSession{
			session.id: session,
		},
	}

	server := NewSeshatServer(appconfig.DefaultConfig())
	server.defaultModel = "anthropic:default-test"
	server.clientFactory = func(req *pb.QueryRequest) (grpcSDKClient, error) {
		return fakeClient, nil
	}

	resp, err := server.Query(context.Background(), &pb.QueryRequest{
		Prompt:    "resume",
		ContextId: session.id.String(),
		Tools:     []string{"read"},
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if resp.Content != "loaded reply" {
		t.Fatalf("expected loaded reply, got %q", resp.Content)
	}
	if resp.Model != "anthropic:default-test" {
		t.Fatalf("expected resolved default model, got %q", resp.Model)
	}
	if resp.ConversationId != session.id.String() {
		t.Fatalf("expected conversation id %q, got %q", session.id, resp.ConversationId)
	}
	if resp.ItemType != queryResponseItemTypeFinal {
		t.Fatalf("expected final item type, got %q", resp.ItemType)
	}
	if resp.TokenUsage == nil || resp.TokenUsage.InputTokens != 3 || resp.TokenUsage.OutputTokens != 4 || resp.TokenUsage.TotalTokens != 7 {
		t.Fatalf("expected unary usage totals, got %#v", resp.TokenUsage)
	}
	if !resp.Stopped {
		t.Fatal("expected stopped=true for end_turn")
	}
	if len(fakeClient.loadCalls) != 1 || fakeClient.loadCalls[0] != session.id {
		t.Fatalf("expected one load call for %q, got %#v", session.id, fakeClient.loadCalls)
	}
	if fakeClient.createCalls != 0 {
		t.Fatalf("expected no create calls, got %d", fakeClient.createCalls)
	}
	if len(session.unregistered) != 1 || session.unregistered[0] != "bash" {
		t.Fatalf("expected bash to be filtered out, got %#v", session.unregistered)
	}
	if session.closeCalls != 1 {
		t.Fatalf("expected loaded session to be closed once, got %d", session.closeCalls)
	}
	if fakeClient.closeCalls != 1 {
		t.Fatalf("expected client close once, got %d", fakeClient.closeCalls)
	}
}

func TestQueryReturnsInvalidArgumentForUnknownTool(t *testing.T) {
	session := &fakeStreamingSession{
		id:        sdk.SessionID("sess-invalid"),
		toolNames: []string{"bash", "read"},
	}
	fakeClient := &fakeSDKClient{
		createSession: session,
	}

	server := NewSeshatServer(appconfig.DefaultConfig())
	server.clientFactory = func(req *pb.QueryRequest) (grpcSDKClient, error) {
		return fakeClient, nil
	}

	_, err := server.Query(context.Background(), &pb.QueryRequest{
		Prompt: "hello",
		Tools:  []string{"search"},
	})
	if err == nil {
		t.Fatal("expected Query to fail for unknown tool")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if session.closeCalls != 1 {
		t.Fatalf("expected rejected session to be closed once, got %d", session.closeCalls)
	}
	if fakeClient.closeCalls != 1 {
		t.Fatalf("expected client close once, got %d", fakeClient.closeCalls)
	}
}

func TestQueryStreamEmitsProtocolMessagesWithResolvedModel(t *testing.T) {
	session := &fakeStreamingSession{
		id: sdk.SessionID("sess-stream"),
	}
	session.submitMessageFn = func(ctx context.Context, content string) (*sdk.SessionResponse, error) {
		if content != "hello" {
			t.Fatalf("expected hello prompt, got %q", content)
		}
		if session.responseChunkFn != nil {
			session.responseChunkFn(sdk.ResponseChunk{
				Type:      types.APIChunkTypeContentBlockDelta,
				DeltaType: "text_delta",
				Delta:     "hel",
			})
		}
		if session.runtimeEventFn != nil {
			session.runtimeEventFn(sdk.RuntimeEvent{
				Type:       sdk.RuntimeEventTypeToolProgress,
				SessionID:  session.id,
				TurnID:     "turn-1",
				TurnNumber: 1,
				ToolProgress: &sdk.ToolProgress{
					ToolName:        "bash",
					Stage:           sdk.ToolProgressStageRunning,
					Message:         "running",
					PercentComplete: 50,
				},
			})
		}
		if session.responseChunkFn != nil {
			session.responseChunkFn(sdk.ResponseChunk{
				Type:      types.APIChunkTypeContentBlockDelta,
				DeltaType: "text_delta",
				Delta:     "lo",
			})
		}
		return &sdk.SessionResponse{
			StopReason: types.StopReasonEndTurn,
			IsComplete: true,
			Usage:      &types.TokenUsage{InputTokens: 5, OutputTokens: 2},
			Messages: []types.Message{
				types.AssistantMessage("msg-1", []types.ContentBlock{types.TextContent{Text: "hello"}}),
			},
		}, nil
	}

	fakeClient := &fakeSDKClient{
		createSession: session,
	}

	server := NewSeshatServer(appconfig.DefaultConfig())
	server.defaultModel = "anthropic:default-stream"
	server.clientFactory = func(req *pb.QueryRequest) (grpcSDKClient, error) {
		return fakeClient, nil
	}

	stream := &fakeQueryStreamServer{}
	err := server.QueryStream(&pb.QueryRequest{
		Prompt: "hello",
	}, stream)
	if err != nil {
		t.Fatalf("QueryStream failed: %v", err)
	}

	responses := stream.responsesCopy()

	if len(responses) != 4 {
		t.Fatalf("expected 4 streamed responses, got %d", len(responses))
	}
	for i, resp := range responses {
		if resp.Model != "anthropic:default-stream" {
			t.Fatalf("expected resolved model on response %d, got %q", i, resp.Model)
		}
	}
	if responses[0].ItemType != queryResponseItemTypeChunk || responses[0].Chunk == nil || responses[0].Chunk.Delta != "hel" {
		t.Fatalf("expected first streamed chunk, got %#v", responses[0])
	}
	if responses[1].ItemType != queryResponseItemTypeRuntimeEvent || responses[1].RuntimeEvent == nil || responses[1].RuntimeEvent.ToolName != "bash" {
		t.Fatalf("expected typed runtime event, got %#v", responses[1])
	}
	if responses[2].ItemType != queryResponseItemTypeChunk || responses[2].Chunk == nil || responses[2].Chunk.Delta != "lo" {
		t.Fatalf("expected second streamed chunk, got %#v", responses[2])
	}
	final := responses[3]
	if final.ItemType != queryResponseItemTypeFinal {
		t.Fatalf("expected final item type, got %q", final.ItemType)
	}
	if final.Content != "hello" {
		t.Fatalf("expected final content hello, got %q", final.Content)
	}
	if final.ConversationId != session.id.String() {
		t.Fatalf("expected conversation id %q, got %q", session.id, final.ConversationId)
	}
	if final.TokenUsage == nil || final.TokenUsage.TotalTokens != 7 {
		t.Fatalf("expected final usage totals, got %#v", final.TokenUsage)
	}
	if session.closeCalls != 1 {
		t.Fatalf("expected created session to be closed once, got %d", session.closeCalls)
	}
	if fakeClient.createCalls != 1 {
		t.Fatalf("expected create session once, got %d", fakeClient.createCalls)
	}
	if fakeClient.closeCalls != 1 {
		t.Fatalf("expected client close once, got %d", fakeClient.closeCalls)
	}
}

func TestBuildSDKClientConfigUsesExplicitAPIKeyWithoutHostProviderSetup(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("CODEX_ACCESS_TOKEN", "")

	cfg, err := buildSDKClientConfig(appconfig.Config{}, &pb.QueryRequest{
		Model:  "codex:gpt-5.3-codex",
		ApiKey: "oauth-bearer-token",
	})
	if err != nil {
		t.Fatalf("buildSDKClientConfig: %v", err)
	}
	if cfg.APIKey != "oauth-bearer-token" {
		t.Fatalf("expected explicit api key to win, got %q", cfg.APIKey)
	}
	if cfg.Model.Provider != sdk.APIProviderCodex {
		t.Fatalf("expected codex provider, got %q", cfg.Model.Provider)
	}
}

func TestMaxConcurrentRPCsFromEnvCannotWrapAroundTheServerLimit(t *testing.T) {
	// A number above what the server takes (an unsigned 32-bit one) wrapped around to a small value or to none.
	t.Setenv("SESHAT_GRPC_MAX_CONCURRENT_RPCS", "5000000000")
	cfg := loadGRPCConfigFromEnv()
	if cfg.MaxConcurrentRPCs <= 0 || int64(cfg.MaxConcurrentRPCs) > math.MaxInt32 {
		t.Fatalf("MaxConcurrentRPCs = %d, want a value in 1..%d", cfg.MaxConcurrentRPCs, math.MaxInt32)
	}
	if uint32(cfg.MaxConcurrentRPCs) != uint32(math.MaxInt32) {
		t.Fatalf("the value does not survive the conversion the server makes: %d", cfg.MaxConcurrentRPCs)
	}
}
