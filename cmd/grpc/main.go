package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	internaltypes "github.com/KPO-Tech/seshat-runtime/internal/types"
	appconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	publicmcp "github.com/KPO-Tech/seshat-runtime/pkg/mcp"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	publicskills "github.com/KPO-Tech/seshat-runtime/pkg/skills"
)

// GRPCConfig holds server configuration.
type GRPCConfig struct {
	// Host is the address the server listens on: the loopback address unless SESHAT_GRPC_HOST says otherwise.
	Host              string
	Port              int
	MaxConcurrentRPCs int
	KeepaliveTime     time.Duration
	EnableReflection  bool

	// AuthToken, when set, is required of every call but HealthCheck as "authorization: Bearer <token>".
	AuthToken string
	// TLSCertFile and TLSKeyFile turn TLS on when both are set.
	TLSCertFile string
	TLSKeyFile  string
	// AllowInsecureRemote accepts a listen address that is not the loopback with no AuthToken.
	AllowInsecureRemote bool
	// AllowStdioMCP lets ConnectMCP start a stdio MCP server, that is a command line given by the caller.
	AllowStdioMCP bool
}

var defaultGRPCConfig = GRPCConfig{
	Host:              "127.0.0.1",
	Port:              50051,
	MaxConcurrentRPCs: 10,
	KeepaliveTime:     30 * time.Second,
	EnableReflection:  false,
}

type grpcSDKSession interface {
	SetResponseChunkFn(func(sdk.ResponseChunk))
	SetRuntimeEventFn(func(sdk.RuntimeEvent))
	SubmitMessage(ctx context.Context, content string) (*sdk.SessionResponse, error)
	GetID() sdk.SessionID
	GetToolNames() []string
	UnregisterTool(name string) error
	Close() error
}

type grpcSDKClient interface {
	CreateSession(ctx context.Context) (grpcSDKSession, error)
	LoadSession(ctx context.Context, sessionID sdk.SessionID) (grpcSDKSession, error)
	Close() error
}

type sdkClientAdapter struct {
	client *sdk.Client
}

func (c *sdkClientAdapter) CreateSession(ctx context.Context) (grpcSDKSession, error) {
	session, err := c.client.CreateSession(ctx)
	if err != nil {
		return nil, err
	}
	return &sdkSessionAdapter{session: session}, nil
}

func (c *sdkClientAdapter) LoadSession(ctx context.Context, sessionID sdk.SessionID) (grpcSDKSession, error) {
	session, err := c.client.LoadSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &sdkSessionAdapter{session: session}, nil
}

func (c *sdkClientAdapter) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

type sdkSessionAdapter struct {
	session *sdk.Session
}

func (s *sdkSessionAdapter) SetResponseChunkFn(fn func(sdk.ResponseChunk)) {
	s.session.SetResponseChunkFn(fn)
}

func (s *sdkSessionAdapter) SetRuntimeEventFn(fn func(sdk.RuntimeEvent)) {
	s.session.SetRuntimeEventFn(fn)
}

func (s *sdkSessionAdapter) SubmitMessage(ctx context.Context, content string) (*sdk.SessionResponse, error) {
	return s.session.SubmitMessage(ctx, content)
}

func (s *sdkSessionAdapter) GetID() sdk.SessionID {
	return s.session.GetID()
}

func (s *sdkSessionAdapter) GetToolNames() []string {
	return s.session.GetToolNames()
}

func (s *sdkSessionAdapter) UnregisterTool(name string) error {
	return s.session.UnregisterTool(name)
}

func (s *sdkSessionAdapter) Close() error {
	return s.session.Close()
}

// SeshatServer implements pb.SeshatServiceServer using the real engine packages.
type SeshatServer struct {
	pb.UnimplementedSeshatServiceServer
	startedAt     time.Time
	version       string
	defaultModel  string
	skillsCWD     string
	mcpManager    *publicmcp.Manager
	clientFactory func(*pb.QueryRequest) (grpcSDKClient, error)
	// allowStdioMCP: see GRPCConfig.AllowStdioMCP.
	allowStdioMCP bool
}

var _ pb.SeshatServiceServer = (*SeshatServer)(nil)

func NewSeshatServer(hostConfig appconfig.Config) *SeshatServer {
	cwd := strings.TrimSpace(hostConfig.Cwd)
	if cwd == "" {
		if resolved, err := os.Getwd(); err == nil && resolved != "" {
			cwd = resolved
		} else {
			cwd = "."
		}
	}
	return &SeshatServer{
		startedAt:     time.Now().UTC(),
		version:       grpcServerVersion(),
		defaultModel:  strings.TrimSpace(hostConfig.Model),
		skillsCWD:     cwd,
		mcpManager:    publicmcp.GlobalManager(),
		clientFactory: newSDKClientFactory(hostConfig),
	}
}

const (
	queryResponseItemTypeChunk        = "chunk"
	queryResponseItemTypeRuntimeEvent = "runtime_event"
	queryResponseItemTypeFinal        = "final"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[gRPC] shutting down…")
		cancel()
	}()

	hostConfig, err := appconfig.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gRPC] warning config: %v\n", err)
	}

	storage.SetConfig(storage.Config{
		Provider:          storage.ProviderType(hostConfig.StorageProvider),
		LocalPath:         appconfig.EffectiveStorageLocalPath(hostConfig),
		S3Endpoint:        hostConfig.S3Endpoint,
		S3Bucket:          hostConfig.S3Bucket,
		S3AccessKeyID:     hostConfig.S3AccessKeyID,
		S3SecretAccessKey: hostConfig.S3SecretAccessKey,
		S3Region:          hostConfig.S3Region,
		S3KeyPrefix:       hostConfig.S3KeyPrefix,
	})
	if err := storage.HealthCheck(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[gRPC] warning storage: %v\n", err)
	}

	cfg := loadGRPCConfigFromEnv()
	if err := validateListenConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[gRPC] %v\n", err)
		os.Exit(1)
	}
	securityOpts, err := serverOptions(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gRPC] %v\n", err)
		os.Exit(1)
	}
	if cfg.AuthToken != "" && cfg.TLSCertFile == "" && !isLoopbackHost(cfg.Host) {
		fmt.Fprintln(os.Stderr, "[gRPC] warning: the token travels in clear text; set SESHAT_GRPC_TLS_CERT and SESHAT_GRPC_TLS_KEY")
	}
	if cfg.AuthToken == "" && !isLoopbackHost(cfg.Host) {
		fmt.Fprintf(os.Stderr, "[gRPC] warning: listening on %s with no authentication (SESHAT_GRPC_ALLOW_INSECURE_REMOTE)\n", cfg.Host)
	}
	listener, err := net.Listen("tcp", listenAddress(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gRPC] listen: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()

	grpcServer := grpc.NewServer(append([]grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(cfg.MaxConcurrentRPCs)),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime: cfg.KeepaliveTime,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge: cfg.KeepaliveTime * 2,
		}),
	}, securityOpts...)...)

	seshatServer := NewSeshatServer(hostConfig)
	seshatServer.allowStdioMCP = cfg.AllowStdioMCP
	pb.RegisterSeshatServiceServer(grpcServer, seshatServer)
	if cfg.EnableReflection {
		reflection.Register(grpcServer)
	}

	go func() {
		fmt.Printf("[gRPC] listening on %s\n", listener.Addr())
		if err := grpcServer.Serve(listener); err != nil {
			fmt.Fprintf(os.Stderr, "[gRPC] serve: %v\n", err)
			cancel()
		}
	}()

	<-ctx.Done()
	grpcServer.GracefulStop()
	fmt.Println("[gRPC] stopped")
}

// ---------------------------------------------------------------------------
// Query — single-turn, non-streaming
// ---------------------------------------------------------------------------

func (s *SeshatServer) Query(ctx context.Context, req *pb.QueryRequest) (*pb.QueryResponse, error) {
	if req.Prompt == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt is required")
	}

	client, err := s.clientFactory(req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create client: %v", err)
	}
	defer client.Close()

	session, err := loadOrCreateSession(ctx, client, req)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	resp, err := session.SubmitMessage(ctx, req.Prompt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query: %v", err)
	}

	pbResp := &pb.QueryResponse{
		Content:        latestResponseText(resp.Messages),
		Model:          s.responseModel(req),
		ConversationId: session.GetID().String(),
		Stopped:        resp.StopReason == "end_turn",
		ItemType:       queryResponseItemTypeFinal,
	}
	if resp.Usage != nil {
		pbResp.TokenUsage = &pb.TokenUsage{
			InputTokens:  int64(resp.Usage.InputTokens),
			OutputTokens: int64(resp.Usage.OutputTokens),
			TotalTokens:  int64(resp.Usage.InputTokens + resp.Usage.OutputTokens),
		}
	}
	return pbResp, nil
}

// ---------------------------------------------------------------------------
// QueryStream — server-side streaming
// ---------------------------------------------------------------------------

func (s *SeshatServer) QueryStream(req *pb.QueryRequest, stream grpc.ServerStreamingServer[pb.QueryResponse]) error {
	if req.Prompt == "" {
		return status.Error(codes.InvalidArgument, "prompt is required")
	}

	client, err := s.clientFactory(req)
	if err != nil {
		return status.Errorf(codes.Internal, "create client: %v", err)
	}
	defer client.Close()

	session, err := loadOrCreateSession(stream.Context(), client, req)
	if err != nil {
		return err
	}
	defer session.Close()

	return streamQuerySession(stream.Context(), req, s.responseModel(req), session, stream.Send)
}

type grpcStreamingSession interface {
	SetResponseChunkFn(func(sdk.ResponseChunk))
	SetRuntimeEventFn(func(sdk.RuntimeEvent))
	SubmitMessage(ctx context.Context, content string) (*sdk.SessionResponse, error)
	GetID() sdk.SessionID
	GetToolNames() []string
	UnregisterTool(name string) error
	Close() error
}

func streamQuerySession(
	ctx context.Context,
	req *pb.QueryRequest,
	model string,
	session grpcStreamingSession,
	send func(*pb.QueryResponse) error,
) error {
	if session == nil {
		return status.Error(codes.Internal, "session not available")
	}

	var (
		sendMu  sync.Mutex
		sendErr error
	)
	safeSend := func(resp *pb.QueryResponse) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendErr != nil {
			return sendErr
		}
		if err := send(resp); err != nil {
			sendErr = err
			return err
		}
		return nil
	}

	// Accumulate content via the chunk callback, then send a single response per
	// assistant message. Full token-by-token streaming would require a lower-level
	// integration; this gives callers real incremental results turn-by-turn.
	session.SetResponseChunkFn(func(chunk sdk.ResponseChunk) {
		if chunk.Delta == "" {
			return
		}
		_ = safeSend(queryResponseFromChunk(model, chunk))
	})
	defer session.SetResponseChunkFn(nil)

	session.SetRuntimeEventFn(func(event sdk.RuntimeEvent) {
		_ = safeSend(queryResponseFromRuntimeEvent(model, event))
	})
	defer session.SetRuntimeEventFn(nil)

	result, err := session.SubmitMessage(ctx, req.Prompt)
	if err != nil {
		return status.Errorf(codes.Internal, "submit: %v", err)
	}
	if sendErr != nil {
		return sendErr
	}

	// Send final message with usage.
	text := latestResponseText(result.Messages)
	final := &pb.QueryResponse{
		Content:        text,
		Model:          model,
		ConversationId: session.GetID().String(),
		Stopped:        result.StopReason == "end_turn",
		ItemType:       queryResponseItemTypeFinal,
	}
	if result.Usage != nil {
		final.TokenUsage = &pb.TokenUsage{
			InputTokens:  int64(result.Usage.InputTokens),
			OutputTokens: int64(result.Usage.OutputTokens),
			TotalTokens:  int64(result.Usage.InputTokens + result.Usage.OutputTokens),
		}
	}
	return safeSend(final)
}

func queryResponseFromChunk(model string, chunk sdk.ResponseChunk) *pb.QueryResponse {
	return &pb.QueryResponse{
		Content:  chunk.Delta,
		Model:    model,
		ItemType: queryResponseItemTypeChunk,
		Chunk: &pb.ChunkDelta{
			Type:      string(chunk.Type),
			DeltaType: chunk.DeltaType,
			Delta:     chunk.Delta,
		},
	}
}

func queryResponseFromRuntimeEvent(model string, event sdk.RuntimeEvent) *pb.QueryResponse {
	return &pb.QueryResponse{
		Model:        model,
		ItemType:     queryResponseItemTypeRuntimeEvent,
		RuntimeEvent: runtimeEventToProto(event),
	}
}

func runtimeEventToProto(event sdk.RuntimeEvent) *pb.RuntimeEvent {
	rt := &pb.RuntimeEvent{
		Type:       string(event.Type),
		SessionId:  event.SessionID.String(),
		TurnId:     string(event.TurnID),
		TurnNumber: int32(event.TurnNumber),
		StopReason: event.StopReason,
		Error:      event.Error,
	}
	if !event.Timestamp.IsZero() {
		rt.Timestamp = event.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	if event.Usage != nil {
		rt.TokenUsage = &pb.TokenUsage{
			InputTokens:  int64(event.Usage.InputTokens),
			OutputTokens: int64(event.Usage.OutputTokens),
			TotalTokens:  int64(event.Usage.InputTokens + event.Usage.OutputTokens),
		}
	}
	if event.Chunk != nil {
		rt.Chunk = &pb.ChunkDelta{
			Type:      string(event.Chunk.Type),
			DeltaType: event.Chunk.DeltaType,
			Delta:     event.Chunk.Delta,
		}
	}
	if event.ToolProgress != nil {
		rt.ToolName = event.ToolProgress.ToolName
		rt.ToolStage = string(event.ToolProgress.Stage)
		rt.ToolMessage = event.ToolProgress.Message
		rt.ToolPercentComplete = float32(event.ToolProgress.PercentComplete)
	}
	return rt
}

// ---------------------------------------------------------------------------
// Skills
// ---------------------------------------------------------------------------

func (s *SeshatServer) ListSkills(ctx context.Context, req *pb.ListSkillsRequest) (*pb.ListSkillsResponse, error) {
	skills, err := publicskills.All(s.skillsCWD)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list skills: %v", err)
	}

	filter := strings.ToLower(req.SourceFilter)
	pbSkills := make([]*pb.Skill, 0, len(skills))
	for _, sk := range skills {
		src := strings.ToLower(string(sk.Source))
		if filter != "" && filter != "all" && src != filter {
			continue
		}
		pbSkills = append(pbSkills, skillToProto(sk))
	}

	return &pb.ListSkillsResponse{
		Skills:     pbSkills,
		TotalCount: int32(len(pbSkills)),
	}, nil
}

func (s *SeshatServer) GetSkillDetails(ctx context.Context, req *pb.GetSkillDetailsRequest) (*pb.GetSkillDetailsResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	skills, err := publicskills.All(s.skillsCWD)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list skills: %v", err)
	}

	for _, sk := range skills {
		if sk.Name == req.Name {
			return &pb.GetSkillDetailsResponse{
				Skill: skillToProto(sk),
			}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "skill %q not found", req.Name)
}

// ---------------------------------------------------------------------------
// MCP
// ---------------------------------------------------------------------------

func (s *SeshatServer) ListMCP(ctx context.Context, _ *pb.ListMCPRequest) (*pb.ListMCPResponse, error) {
	statuses := s.mcpManager.GetServerStatuses()
	servers := make([]*pb.MCPServer, 0, len(statuses))
	for _, st := range statuses {
		servers = append(servers, &pb.MCPServer{
			Name:      st.Name,
			Status:    string(st.Status),
			ToolCount: int32(st.ToolCount),
			LastError: st.LastError,
		})
	}
	return &pb.ListMCPResponse{
		Servers:    servers,
		TotalCount: int32(len(servers)),
	}, nil
}

func (s *SeshatServer) ConnectMCP(ctx context.Context, req *pb.ConnectMCPRequest) (*pb.ConnectMCPResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	serverType := publicmcp.ServerTypeStdio
	switch strings.ToLower(req.Type) {
	case "http":
		serverType = publicmcp.ServerTypeHTTP
	case "sse":
		serverType = publicmcp.ServerTypeSSE
	case "ws", "websocket":
		serverType = publicmcp.ServerTypeWebSocket
	}

	if serverType == publicmcp.ServerTypeStdio && !s.allowStdioMCP {
		return nil, status.Error(codes.PermissionDenied,
			"a stdio MCP server is a command line to run on this host: ConnectMCP refuses it unless the server is started with SESHAT_GRPC_ALLOW_STDIO_MCP=true")
	}

	cfg := publicmcp.McpServerConfig{
		Type:    serverType,
		Command: req.Command,
		Args:    req.Args,
		URL:     req.Url,
		Env:     req.Env,
		Timeout: int(req.Timeout),
	}

	if err := publicmcp.AddServer(req.Name, cfg, publicmcp.ScopeUser); err != nil {
		return &pb.ConnectMCPResponse{Success: false, Error: err.Error()}, nil
	}

	if err := publicmcp.ReconnectServer(ctx, s.mcpManager, req.Name, s.skillsCWD); err != nil {
		return &pb.ConnectMCPResponse{Success: false, Error: err.Error()}, nil
	}

	return &pb.ConnectMCPResponse{Success: true}, nil
}

func (s *SeshatServer) DisconnectMCP(ctx context.Context, req *pb.DisconnectMCPRequest) (*pb.DisconnectMCPResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := s.mcpManager.Disconnect(req.Name); err != nil {
		return &pb.DisconnectMCPResponse{Success: false, Error: err.Error()}, nil
	}
	return &pb.DisconnectMCPResponse{Success: true}, nil
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

func (s *SeshatServer) GetModels(_ context.Context, _ *pb.GetModelsRequest) (*pb.GetModelsResponse, error) {
	all := providers.AllProvidersInfo()
	var models []*pb.ModelInfo
	for provider, info := range all {
		for _, m := range info.Models {
			models = append(models, &pb.ModelInfo{
				Id:        m.Identifier,
				Name:      m.Description,
				Provider:  string(provider),
				MaxTokens: int32(m.MaxOutput),
			})
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider == models[j].Provider {
			return models[i].Id < models[j].Id
		}
		return models[i].Provider < models[j].Provider
	})
	return &pb.GetModelsResponse{Models: models}, nil
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func (s *SeshatServer) HealthCheck(_ context.Context, _ *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	return &pb.HealthCheckResponse{
		Status:  "ok",
		Version: s.version,
		Uptime:  time.Since(s.startedAt).Round(time.Second).String(),
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func loadGRPCConfigFromEnv() GRPCConfig {
	cfg := defaultGRPCConfig
	if raw := strings.TrimSpace(os.Getenv("SESHAT_GRPC_PORT")); raw != "" {
		if port, err := strconv.Atoi(raw); err == nil && port > 0 {
			cfg.Port = port
		}
	}
	if raw := strings.TrimSpace(os.Getenv("SESHAT_GRPC_MAX_CONCURRENT_RPCS")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			// The server takes an unsigned 32-bit number: a larger value would wrap around to a small one, or to none.
			cfg.MaxConcurrentRPCs = min(value, math.MaxInt32)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("SESHAT_GRPC_KEEPALIVE_TIME")); raw != "" {
		if value, err := time.ParseDuration(raw); err == nil && value > 0 {
			cfg.KeepaliveTime = value
		}
	}
	if raw := strings.TrimSpace(os.Getenv("SESHAT_GRPC_ENABLE_REFLECTION")); raw != "" {
		cfg.EnableReflection = envTrue(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("SESHAT_GRPC_HOST")); raw != "" {
		cfg.Host = raw
	}
	cfg.AuthToken = strings.TrimSpace(os.Getenv("SESHAT_GRPC_AUTH_TOKEN"))
	cfg.TLSCertFile = strings.TrimSpace(os.Getenv("SESHAT_GRPC_TLS_CERT"))
	cfg.TLSKeyFile = strings.TrimSpace(os.Getenv("SESHAT_GRPC_TLS_KEY"))
	cfg.AllowInsecureRemote = envTrue(os.Getenv("SESHAT_GRPC_ALLOW_INSECURE_REMOTE"))
	cfg.AllowStdioMCP = envTrue(os.Getenv("SESHAT_GRPC_ALLOW_STDIO_MCP"))
	return cfg
}

func envTrue(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.EqualFold(raw, "1") || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
}

func grpcServerVersion() string {
	if value := strings.TrimSpace(os.Getenv("SESHAT_VERSION")); value != "" {
		return value
	}
	return "dev"
}

func newSDKClientFactory(hostConfig appconfig.Config) func(*pb.QueryRequest) (grpcSDKClient, error) {
	return func(req *pb.QueryRequest) (grpcSDKClient, error) {
		cfg, err := buildSDKClientConfig(hostConfig, req)
		if err != nil {
			return nil, err
		}
		client, err := sdk.NewClient(cfg)
		if err != nil {
			return nil, err
		}
		return &sdkClientAdapter{client: client}, nil
	}
}

func buildSDKClientConfig(hostConfig appconfig.Config, req *pb.QueryRequest) (*sdk.ClientConfig, error) {
	cfg := sdk.DefaultClientConfig()
	cfg.PermissionMode = sdk.PermissionModeNever
	cfg.AutoCompact = true
	cfg.PersistSessions = true
	cfg.SessionSQLitePath = appconfig.EffectiveSessionDBPath(hostConfig)
	cfg.WorkingDir = effectiveWorkingDir(hostConfig)
	cfg.BrowserRemoteControlURL = strings.TrimSpace(hostConfig.BrowserRemoteControlURL)
	cfg.BrowserExecutablePath = strings.TrimSpace(hostConfig.BrowserExecutablePath)
	cfg.StorageGCEnabled = hostConfig.StorageGCEnabled
	cfg.StorageGCInterval = parseDurationOrDefault(hostConfig.StorageGCInterval, time.Hour)
	cfg.StorageGCLimit = hostConfig.StorageGCLimit
	cfg.StorageGCNamespaces = splitCommaList(hostConfig.StorageGCNamespaces)
	cfg.Model = resolveModelIdentifier(hostConfig, resolvedModelString(req))
	if req.GetMaxTokens() > 0 {
		cfg.MaxTokens = int(req.GetMaxTokens())
	} else if hostConfig.MaxTokens > 0 {
		cfg.MaxTokens = hostConfig.MaxTokens
	}
	explicitAPIKey := strings.TrimSpace(req.GetApiKey())
	if explicitAPIKey != "" {
		cfg.APIKey = explicitAPIKey
	} else {
		cfg.APIKey = appconfig.ResolveAPIKey(hostConfig, cfg.Model.Provider)
		if err := appconfig.ValidateProviderSetup(hostConfig, cfg.Model.Provider); err != nil {
			return nil, err
		}
	}
	if hasStorageConfig(hostConfig) {
		cfg.StorageConfig = &sdk.StorageConfig{
			Provider:          sdk.StorageProviderType(hostConfig.StorageProvider),
			LocalPath:         appconfig.EffectiveStorageLocalPath(hostConfig),
			S3Endpoint:        hostConfig.S3Endpoint,
			S3Bucket:          hostConfig.S3Bucket,
			S3AccessKeyID:     hostConfig.S3AccessKeyID,
			S3SecretAccessKey: hostConfig.S3SecretAccessKey,
			S3Region:          hostConfig.S3Region,
			S3KeyPrefix:       hostConfig.S3KeyPrefix,
		}
	}
	return cfg, nil
}

func effectiveWorkingDir(hostConfig appconfig.Config) string {
	if cwd := strings.TrimSpace(hostConfig.Cwd); cwd != "" {
		return cwd
	}
	return "."
}

func resolvedModelString(req *pb.QueryRequest) string {
	if req == nil {
		return ""
	}
	return strings.TrimSpace(req.GetModel())
}

func (s *SeshatServer) responseModel(req *pb.QueryRequest) string {
	if model := effectiveResponseModel(req); model != "" {
		return model
	}
	return s.defaultModel
}

func effectiveResponseModel(req *pb.QueryRequest) string {
	return resolvedModelString(req)
}

func resolveModelIdentifier(hostConfig appconfig.Config, raw string) sdk.ModelIdentifier {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = strings.TrimSpace(hostConfig.Model)
	}
	if value == "" {
		return sdk.DefaultClientConfig().Model
	}
	if appconfig.HasExplicitProviderPrefix(value) {
		return appconfig.ParseModelIdentifier(value)
	}

	provider := appconfig.DetectProviderFromModel(value)
	if provider == "" {
		if hostModel := strings.TrimSpace(hostConfig.Model); hostModel != "" {
			provider = appconfig.ParseModelIdentifier(hostModel).Provider
		}
	}
	if provider == "" {
		_, provider = appconfig.EffectiveAPIKeyAndProvider(hostConfig)
	}
	if provider == "" {
		provider = sdk.DefaultClientConfig().Model.Provider
	}
	return sdk.ModelIdentifier{Provider: provider, Model: value}
}

func hasStorageConfig(hostConfig appconfig.Config) bool {
	return strings.TrimSpace(hostConfig.StorageProvider) != "" ||
		strings.TrimSpace(hostConfig.StorageLocalPath) != "" ||
		strings.TrimSpace(hostConfig.S3Endpoint) != "" ||
		strings.TrimSpace(hostConfig.S3Bucket) != ""
}

func parseDurationOrDefault(raw string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func splitCommaList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func loadOrCreateSession(ctx context.Context, client grpcSDKClient, req *pb.QueryRequest) (grpcSDKSession, error) {
	if client == nil {
		return nil, status.Error(codes.Internal, "client not available")
	}
	var session grpcSDKSession
	var err error
	if req != nil && strings.TrimSpace(req.GetContextId()) != "" {
		session, err = client.LoadSession(ctx, sdk.SessionID(strings.TrimSpace(req.GetContextId())))
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "load session: %v", err)
		}
	} else {
		session, err = client.CreateSession(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "create session: %v", err)
		}
	}
	if err := applyRequestedTools(session, req.GetTools()); err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, err
	}
	return session, nil
}

func applyRequestedTools(session grpcSDKSession, requested []string) error {
	if session == nil || len(requested) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		allowed[trimmed] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil
	}

	current := session.GetToolNames()
	currentSet := make(map[string]struct{}, len(current))
	for _, name := range current {
		currentSet[name] = struct{}{}
	}
	missing := make([]string, 0)
	for name := range allowed {
		if _, ok := currentSet[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return status.Errorf(codes.InvalidArgument, "unknown tools requested: %s", strings.Join(missing, ", "))
	}

	for _, name := range current {
		if _, ok := allowed[name]; ok {
			continue
		}
		if err := session.UnregisterTool(name); err != nil {
			return status.Errorf(codes.Internal, "disable tool %q: %v", name, err)
		}
	}
	return nil
}

func latestResponseText(messages []internaltypes.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != internaltypes.RoleAssistant {
			continue
		}
		var text strings.Builder
		for _, block := range messages[i].Content {
			if t, ok := block.(internaltypes.TextContent); ok {
				text.WriteString(t.Text)
			}
		}
		if text.Len() > 0 {
			return text.String()
		}
	}
	return ""
}

func skillToProto(sk publicskills.Skill) *pb.Skill {
	return &pb.Skill{
		Name:         sk.Name,
		Description:  sk.Description,
		Source:       string(sk.Source),
		WhenToUse:    sk.WhenToUse,
		AllowedTools: sk.AllowedTools,
	}
}
