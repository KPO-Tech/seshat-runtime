package mcp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// ClientStatus represents the lifecycle state of an MCP connection.
type ClientStatus string

const (
	ClientStatusPending   ClientStatus = "pending"
	ClientStatusConnected ClientStatus = "connected"
	ClientStatusNeedsAuth ClientStatus = "needs_auth"
	ClientStatusExpired   ClientStatus = "expired"
	ClientStatusFailed    ClientStatus = "failed"
	ClientStatusClosed    ClientStatus = "closed"
)

const (
	ErrCodeNeedsAuth            = -32001
	ErrCodeSessionExpired       = -32002
	defaultMaxResourceTextBytes = 64 * 1024
)

// ToolNameMode controls how wrapped MCP tool names are exposed.
type ToolNameMode string

const (
	ToolNameModePrefixed   ToolNameMode = "prefixed"
	ToolNameModeUnprefixed ToolNameMode = "unprefixed"
)

// Discovery contains the entities exposed by an MCP server.
type Discovery struct {
	Tools     []Tool     `json:"tools,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
	Prompts   []Prompt   `json:"prompts,omitempty"`
}

// ClientMetadata captures runtime metadata about an MCP client.
type ClientMetadata struct {
	Status            ClientStatus `json:"status"`
	Initialized       bool         `json:"initialized"`
	ToolNameMode      ToolNameMode `json:"tool_name_mode"`
	LastError         string       `json:"last_error,omitempty"`
	LastInitializedAt *time.Time   `json:"last_initialized_at,omitempty"`
	ServerInfo        *ServerInfo  `json:"server_info,omitempty"`
	// ProtocolVersion is the MCP version agreed during initialize handshake.
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

// IntegrationOptions controls how MCP servers are wrapped into the tool registry.
type IntegrationOptions struct {
	ToolNameMode ToolNameMode `json:"tool_name_mode,omitempty"`
}

func normalizeIntegrationOptions(options *IntegrationOptions) *IntegrationOptions {
	if options == nil {
		options = &IntegrationOptions{}
	}
	if options.ToolNameMode == "" {
		options.ToolNameMode = ToolNameModePrefixed
	}
	return options
}

// ToolCallResult is the canonical result of an MCP tool call.
type ToolCallResult struct {
	Raw        map[string]any `json:"raw,omitempty"`
	Text       string         `json:"text,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	Structured map[string]any `json:"structured,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// PromptResult is the canonical result of an MCP prompt fetch.
type PromptResult struct {
	Messages []PromptMessage `json:"messages,omitempty"`
}

// ResourceReadResult is the canonical result of an MCP resource read.
type ResourceReadResult struct {
	Contents []ResourceContent `json:"contents,omitempty"`
}

// ToolWrapperMetadata is attached to wrapped MCP tools.
type ToolWrapperMetadata struct {
	ServerName string       `json:"server_name"`
	ToolName   string       `json:"tool_name"`
	NameMode   ToolNameMode `json:"name_mode"`
}

func prefixedToolName(serverName, toolName string) string {
	return "mcp__" + sanitizeName(serverName) + "__" + sanitizeName(toolName)
}

func exposedToolName(serverName, toolName string, mode ToolNameMode) string {
	if mode == ToolNameModeUnprefixed {
		return sanitizeName(toolName)
	}
	return prefixedToolName(serverName, toolName)
}

func sanitizeName(value string) string {
	result := make([]rune, 0, len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			result = append(result, r)
		case r >= 'A' && r <= 'Z':
			result = append(result, r)
		case r >= '0' && r <= '9':
			result = append(result, r)
		case r == '_' || r == '-' || r == '.':
			result = append(result, r)
		default:
			result = append(result, '_')
		}
	}
	return string(result)
}

func isReadOnlyMCPTool(name string) bool {
	for _, pattern := range []string{"read", "get", "list", "show", "find", "search", "query"} {
		if containsIgnoreCase(name, pattern) {
			return true
		}
	}
	return false
}

func containsIgnoreCase(value string, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	return len(value) >= len(needle) && indexFold(value, needle) >= 0
}

func indexFold(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(substr)], substr) {
			return i
		}
	}
	return -1
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func mapStringAny(value any) map[string]any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func boolValue(value any) bool {
	if b, ok := value.(bool); ok {
		return b
	}
	return false
}

func sliceMapValue(value any) []map[string]any {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}

func sliceStringValue(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func truncateResourceText(value string) string {
	if len(value) <= defaultMaxResourceTextBytes {
		return value
	}
	return value[:defaultMaxResourceTextBytes] + "\n\n[truncated MCP resource output]"
}

func truncateBlob(value string) string {
	if len(value) <= defaultMaxResourceTextBytes {
		return value
	}
	return value[:defaultMaxResourceTextBytes] + "...[truncated blob]"
}

func isAuthError(resp *JSONRPCResponse) bool {
	if resp == nil || resp.Error == nil {
		return false
	}
	if resp.Error.Code == ErrCodeNeedsAuth {
		return true
	}
	return equalFoldASCII(resp.Error.Message, "unauthorized") || containsIgnoreCase(resp.Error.Message, "auth")
}

func isSessionExpiredError(resp *JSONRPCResponse) bool {
	if resp == nil || resp.Error == nil {
		return false
	}
	if resp.Error.Code == ErrCodeSessionExpired {
		return true
	}
	return containsIgnoreCase(resp.Error.Message, "session expired") || containsIgnoreCase(resp.Error.Message, "session not found")
}

func normalizeToolCallResult(result map[string]any) ToolCallResult {
	result = normalizeToolCallResultMap(result)
	canonical := ToolCallResult{Raw: result}
	canonical.IsError = boolValue(result["isError"])
	canonical.Structured = mapStringAny(result["structuredContent"])
	canonical.Meta = mapStringAny(result["_meta"])
	if text := stringValue(result["text"]); text != "" {
		canonical.Text = text
		return canonical
	}
	if content := result["content"]; content != nil {
		switch v := content.(type) {
		case string:
			canonical.Text = truncateResourceText(v)
		case []any:
			parts := make([]string, 0, len(v))
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if text := stringValue(m["text"]); text != "" {
						parts = append(parts, truncateResourceText(text))
					}
				}
			}
			canonical.Text = stringsJoin(parts, "\n")
		}
	}
	return canonical
}

func cloneMetadata(metadata ClientMetadata) ClientMetadata {
	cloned := metadata
	if metadata.LastInitializedAt != nil {
		ts := *metadata.LastInitializedAt
		cloned.LastInitializedAt = &ts
	}
	if metadata.ServerInfo != nil {
		server := *metadata.ServerInfo
		cloned.ServerInfo = &server
	}
	return cloned
}

func applyErrorStatus(metadata *ClientMetadata, resp *JSONRPCResponse) {
	if metadata == nil || resp == nil || resp.Error == nil {
		return
	}
	if isAuthError(resp) {
		metadata.Status = ClientStatusNeedsAuth
		metadata.LastError = resp.Error.Message
		return
	}
	if isSessionExpiredError(resp) {
		metadata.Status = ClientStatusExpired
		metadata.LastError = resp.Error.Message
		return
	}
	metadata.Status = ClientStatusFailed
	metadata.LastError = resp.Error.Message
}

func refreshConnectedStatus(metadata *ClientMetadata) {
	if metadata == nil {
		return
	}
	metadata.Status = ClientStatusConnected
	metadata.LastError = ""
}

func normalizeMCPError(method string, resp *JSONRPCResponse) error {
	if resp == nil || resp.Error == nil {
		return nil
	}
	return fmt.Errorf("%s error: %s", method, resp.Error.Message)
}

func normalizeTransportError(method string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s request failed: %w", method, err)
}

func normalizeToolCallResultMap(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	cloned := make(map[string]any, len(result))
	for k, v := range result {
		cloned[k] = v
	}
	if text, ok := cloned["text"].(string); ok {
		cloned["text"] = truncateResourceText(text)
	}
	return cloned
}

func normalizeResourceContents(contents []ResourceContent) []ResourceContent {
	if len(contents) == 0 {
		return contents
	}
	normalized := make([]ResourceContent, len(contents))
	for i, content := range contents {
		normalized[i] = content
		normalized[i].Text = truncateResourceText(content.Text)
		normalized[i].Blob = truncateBlob(content.Blob)
	}
	return normalized
}

func normalizePromptMessage(message *PromptMessage) *PromptMessage {
	if message == nil {
		return nil
	}
	if text, ok := message.Content.(string); ok {
		message.Content = truncateResourceText(text)
	}
	return message
}

func stringsJoin(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += sep + parts[i]
	}
	return result
}

// TransportType represents the type of MCP transport
type TransportType string

const (
	TransportTypeStdio     TransportType = "stdio"
	TransportTypeHTTP      TransportType = "http"
	TransportTypeSSE       TransportType = "sse"
	TransportTypeWebSocket TransportType = "websocket"
)

// ServerConfig represents configuration for an MCP server
type ServerConfig struct {
	// Name is the server name
	Name string `json:"name"`

	// Command is the command to start the server (for stdio)
	Command string `json:"command,omitempty"`

	// Args are the command arguments (for stdio)
	Args []string `json:"args,omitempty"`

	// URL is the server URL (for http/sse/websocket)
	URL string `json:"url,omitempty"`

	// Transport is the transport type
	Transport TransportType `json:"transport"`

	// Env are environment variables for the server process
	Env map[string]string `json:"env,omitempty"`

	// Timeout is the timeout for operations
	Timeout time.Duration `json:"timeout"`

	// Headers are HTTP headers (for http/sse)
	Headers map[string]string `json:"headers,omitempty"`

	// MaxToolSpecBytes caps one tool's definition (name, description, input schema) for this server. 0 uses
	// DefaultMaxToolSpecBytes, a negative value removes the cap. A tool over its cap is left out.
	MaxToolSpecBytes int `json:"max_tool_spec_bytes,omitempty"`

	// MaxServerSpecBytes caps the definitions of all this server's tools together. 0 uses
	// DefaultMaxServerSpecBytes, a negative value removes the cap. Tools past the total are left out.
	MaxServerSpecBytes int `json:"max_server_spec_bytes,omitempty"`
}

// ProgressCallback is called when an MCP server sends a progress notification
// during a tool call. progress is the ratio of completion (0–1 when total is
// set); total may be nil if the server does not report it.
type ProgressCallback func(progress float64, total *float64, message string)

// Client represents an MCP client
type Client struct {
	// config is the server configuration
	config ServerConfig

	// transport is the transport layer
	transport Transport

	// requestID is the next request ID to use
	requestID int64

	// notificationsHandler handles notifications
	notificationsHandler NotificationHandler

	// methodsHandler handles method calls from the server
	methodsHandler MethodHandler

	// progressHandlers maps progressToken → callback for in-flight tool calls.
	progressHandlers map[string]ProgressCallback
	progressMu       sync.RWMutex

	// mu protects runtime metadata
	mu sync.RWMutex

	// metadata captures client lifecycle and naming mode
	metadata ClientMetadata
}

// Transport represents an MCP transport
type Transport interface {
	// Start starts the transport
	Start(ctx context.Context) error

	// Send sends a request
	Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)

	// SendNotification sends a notification
	SendNotification(ctx context.Context, notification *JSONRPCNotification) error

	// Close closes the transport
	Close() error

	// SetNotificationsHandler sets the handler for incoming notifications
	SetNotificationsHandler(handler NotificationHandler)

	// SetMethodsHandler sets the handler for incoming method calls
	SetMethodsHandler(handler MethodHandler)
}

// JSONRPCRequest represents a JSON-RPC request
type JSONRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC response
type JSONRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Result  map[string]any `json:"result,omitempty"`
	Error   *JSONRPCError  `json:"error,omitempty"`
}

// JSONRPCNotification represents a JSON-RPC notification
type JSONRPCNotification struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// JSONRPCError represents a JSON-RPC error
type JSONRPCError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// NotificationHandler handles incoming notifications
type NotificationHandler func(notification *JSONRPCNotification)

// MethodHandler handles incoming method calls from the server
type MethodHandler func(ctx context.Context, request *JSONRPCRequest) (*JSONRPCResponse, error)

// Tool represents an MCP tool
type Tool struct {
	// Name is the tool name
	Name string `json:"name"`

	// Description explains what the tool does
	Description string `json:"description"`

	// InputSchema is the JSON schema for the tool input
	InputSchema map[string]any `json:"inputSchema"`
}

// Resource represents an MCP resource
type Resource struct {
	// URI is the resource URI
	URI string `json:"uri"`

	// Name is the resource name
	Name string `json:"name"`

	// Description explains the resource
	Description string `json:"description,omitempty"`

	// MimeType is the resource MIME type
	MimeType string `json:"mimeType,omitempty"`
}

// ResourceContent represents the content of a resource
type ResourceContent struct {
	// URI is the resource URI
	URI string `json:"uri"`

	// MimeType is the content type
	MimeType string `json:"mimeType,omitempty"`

	// Text is the text content
	Text string `json:"text,omitempty"`

	// Blob is the binary content (base64)
	Blob string `json:"blob,omitempty"`
}

// Prompt represents an MCP prompt
type Prompt struct {
	// Name is the prompt name
	Name string `json:"name"`

	// Description explains the prompt
	Description string `json:"description,omitempty"`

	// Arguments are the prompt arguments
	Arguments []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument represents a prompt argument
type PromptArgument struct {
	// Name is the argument name
	Name string `json:"name"`

	// Description explains the argument
	Description string `json:"description,omitempty"`

	// Required indicates if the argument is required
	Required bool `json:"required"`
}

// PromptMessage represents a message in a prompt
type PromptMessage struct {
	// Role is the message role
	Role string `json:"role"`

	// Content is the message content
	Content any `json:"content"`
}

// ServerInfo represents information about the MCP server
type ServerInfo struct {
	// Name is the server name
	Name string `json:"name"`

	// Version is the server version
	Version string `json:"version"`

	// ProtocolVersion is the MCP protocol version
	ProtocolVersion string `json:"protocolVersion"`
}

// InitializeResult represents the result of initializing the server
type InitializeResult struct {
	// ProtocolVersion is the MCP protocol version
	ProtocolVersion string `json:"protocolVersion"`

	// ServerInfo contains server information
	ServerInfo ServerInfo `json:"serverInfo"`

	// Capabilities are the server capabilities
	Capabilities ServerCapabilities `json:"capabilities"`
}

// ServerCapabilities represents the server's capabilities
type ServerCapabilities struct {
	// Tools are the tool capabilities
	Tools *ToolsCapability `json:"tools,omitempty"`

	// Resources are the resource capabilities
	Resources *ResourcesCapability `json:"resources,omitempty"`

	// Prompts are the prompt capabilities
	Prompts *PromptsCapability `json:"prompts,omitempty"`
}

// ToolsCapability represents tool capabilities
type ToolsCapability struct {
	// ListChanged indicates if the tools list can change
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability represents resource capabilities
type ResourcesCapability struct {
	// Subscribe indicates if resources can be subscribed to
	Subscribe bool `json:"subscribe,omitempty"`

	// ListChanged indicates if the resources list can change
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability represents prompt capabilities
type PromptsCapability struct {
	// ListChanged indicates if the prompts list can change
	ListChanged bool `json:"listChanged,omitempty"`
}

// StdioTransportConfig represents configuration for stdio transport
type StdioTransportConfig struct {
	// Command is the command to run
	Command string

	// Args are the command arguments
	Args []string

	// Env are environment variables
	Env map[string]string

	// Stdin is the stdin to use (for testing)
	Stdin io.Reader

	// Stdout is the stdout to use (for testing)
	Stdout io.Writer

	// Stderr is the stderr to use (for testing)
	Stderr io.Writer
}

// HTTPTransportConfig represents configuration for HTTP transport
type HTTPTransportConfig struct {
	// URL is the server URL
	URL string

	// Headers are HTTP headers
	Headers map[string]string

	// Timeout is the request timeout
	Timeout time.Duration
}
