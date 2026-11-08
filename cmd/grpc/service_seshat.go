package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	appconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	publicmcp "github.com/KPO-Tech/seshat-runtime/pkg/mcp"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// sessionsUnavailable is the error of a session RPC on a server that has no
// registry, which only a test that builds the struct by hand can produce.
var sessionsUnavailable = status.Error(codes.FailedPrecondition, "sessions are not available on this server")

// GetServerInfo says what this server is.
func (s *SeshatServer) GetServerInfo(ctx context.Context, _ *pb.GetServerInfoRequest) (*pb.GetServerInfoResponse, error) {
	info := &pb.GetServerInfoResponse{
		Version:       s.version,
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Model:         s.defaultModel,
		WorkingDir:    s.skillsCWD,
	}

	if s.registry != nil {
		info.ActiveSessions = int32(s.registry.count())
		info.StdioMcpAllowed = s.registry.allowStdioMCP
		info.AuthRequired = s.registry.tokenRequired
		info.TlsEnabled = s.registry.tlsEnabled

		if cfg, err := buildSDKClientConfig(s.hostConfig, &pb.QueryRequest{}); err == nil {
			info.Model = modelString(cfg.Model)
			info.WorkingDir = cfg.WorkingDir
			info.PermissionMode = string(cfg.PermissionMode)
			info.SandboxKind = string(cfg.SandboxKind)
			info.MemoryEnabled = cfg.EnableMemory
			info.HooksEnabled = cfg.EnableHooks
			info.MonitoringEnabled = cfg.EnableMonitoring
			info.ToolSearchEnabled = cfg.EnableToolSearch
			info.SessionPersistenceEnabled = cfg.PersistSessions
		}
	}

	for provider := range providers.AllProvidersInfo() {
		name := string(provider)
		info.ProvidersAvailable = append(info.ProvidersAvailable, name)
		if appconfig.ResolveAPIKey(s.hostConfig, provider) != "" {
			info.ProvidersConfigured = append(info.ProvidersConfigured, name)
		}
	}
	sort.Strings(info.ProvidersAvailable)
	sort.Strings(info.ProvidersConfigured)
	return info, nil
}

// CreateSession opens a conversation and attaches it to this server.
func (s *SeshatServer) CreateSession(ctx context.Context, req *pb.CreateSessionRequest) (*pb.Session, error) {
	if s.registry == nil {
		return nil, sessionsUnavailable
	}
	handle, err := s.registry.create(ctx, req)
	if err != nil {
		return nil, err
	}
	return sessionToProto(handle), nil
}

// ListSessions lists the conversations this server has stored.
func (s *SeshatServer) ListSessions(_ context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	if s.registry == nil {
		return nil, sessionsUnavailable
	}
	client, err := s.registry.catalogClient()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "sessions: %v", err)
	}
	infos, err := client.ListSessions()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "list sessions: %v", err)
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].UpdatedAt > infos[j].UpdatedAt })

	filtered := make([]*pb.SessionInfo, 0, len(infos))
	for _, info := range infos {
		if info.Subagent && !req.GetIncludeSubagent() {
			continue
		}
		filtered = append(filtered, sessionInfoToProto(info))
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := int(req.GetOffset())
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}

	return &pb.ListSessionsResponse{
		Sessions:   filtered[offset:end],
		TotalCount: int32(len(filtered)),
	}, nil
}

// GetSession returns a conversation, attaching it from its stored transcript
// when this process does not hold it yet. Continuing a conversation therefore
// needs nothing but its id.
func (s *SeshatServer) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.Session, error) {
	if s.registry == nil {
		return nil, sessionsUnavailable
	}
	handle, err := s.registry.handleFor(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return sessionToProto(handle), nil
}

// SearchSessions finds conversations whose transcript contains a text.
func (s *SeshatServer) SearchSessions(_ context.Context, req *pb.SearchSessionsRequest) (*pb.SearchSessionsResponse, error) {
	if s.registry == nil {
		return nil, sessionsUnavailable
	}
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}
	client, err := s.registry.catalogClient()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "sessions: %v", err)
	}
	ids, err := client.SearchTranscriptsByContent(query, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search sessions: %v", err)
	}

	response := &pb.SearchSessionsResponse{}
	matched := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		matched[string(id)] = struct{}{}
		response.SessionIds = append(response.SessionIds, string(id))
	}
	if infos, infoErr := client.ListSessions(); infoErr == nil {
		for _, info := range infos {
			if _, ok := matched[string(info.ID)]; ok {
				response.Sessions = append(response.Sessions, sessionInfoToProto(info))
			}
		}
	}
	return response, nil
}

// DeleteSession removes a conversation and what it stored.
func (s *SeshatServer) DeleteSession(ctx context.Context, req *pb.DeleteSessionRequest) (*pb.DeleteSessionResponse, error) {
	if s.registry == nil {
		return nil, sessionsUnavailable
	}
	sessionID := strings.TrimSpace(req.GetSessionId())
	if sessionID == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if handle := s.registry.remove(sessionID); handle != nil {
		_ = handle.close()
	}

	client, err := s.registry.catalogClient()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "sessions: %v", err)
	}
	if err := client.DeleteSession(sdk.SessionID(sessionID)); err != nil {
		return nil, status.Errorf(codes.NotFound, "delete session %q: %v", sessionID, err)
	}
	return &pb.DeleteSessionResponse{Success: true}, nil
}

// ReconnectMCP reopens a configured MCP server.
func (s *SeshatServer) ReconnectMCP(ctx context.Context, req *pb.ReconnectMCPRequest) (*pb.ReconnectMCPResponse, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := publicmcp.ReconnectServer(ctx, s.mcpManager, name, s.skillsCWD); err != nil {
		return &pb.ReconnectMCPResponse{Success: false, Error: err.Error()}, nil
	}
	for _, server := range s.mcpServerStatuses() {
		if server.GetName() == name {
			return &pb.ReconnectMCPResponse{Success: true, Server: server}, nil
		}
	}
	return &pb.ReconnectMCPResponse{Success: true}, nil
}

// mcpServerStatuses renders the MCP servers this server knows.
func (s *SeshatServer) mcpServerStatuses() []*pb.MCPServer {
	if s.mcpManager == nil {
		return nil
	}
	statuses := s.mcpManager.GetServerStatuses()
	servers := make([]*pb.MCPServer, 0, len(statuses))
	for _, st := range statuses {
		server := &pb.MCPServer{
			Name:      st.Name,
			Status:    string(st.Status),
			ToolCount: int32(st.ToolCount),
			LastError: st.LastError,
			Type:      st.Type,
		}
		if !st.ConnectedAt.IsZero() {
			server.Enabled = true
		}
		servers = append(servers, server)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers
}
