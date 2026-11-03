package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// mcpPreferredVersion is the MCP protocol version we advertise during
// initialization. If the server replies with a different (older) version we
// accept it — that is the standard MCP negotiation behaviour.
// Bump this constant when the MCP spec releases a new stable revision.
const mcpPreferredVersion = "2025-03-26"

// NewClient creates a new MCP client.
func NewClient(config ServerConfig) (*Client, error) {
	var transport Transport
	var err error
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Transport == "" {
		// Infer the transport instead of failing on every config that
		// follows the README/docs examples, none of which set Transport
		// explicitly (H7 in TODO.md).
		switch {
		case config.Command != "":
			config.Transport = TransportTypeStdio
		case config.URL != "":
			config.Transport = TransportTypeHTTP
		}
	}
	switch config.Transport {
	case TransportTypeStdio:
		transport, err = NewStdioTransport(StdioTransportConfig{Command: config.Command, Args: config.Args, Env: config.Env})
	case TransportTypeHTTP, TransportTypeSSE, TransportTypeWebSocket:
		transport, err = NewHTTPTransport(HTTPTransportConfig{URL: config.URL, Headers: config.Headers, Timeout: config.Timeout})
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", config.Transport)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}
	c := &Client{
		config:           config,
		transport:        transport,
		requestID:        0,
		progressHandlers: make(map[string]ProgressCallback),
		metadata: ClientMetadata{
			Status:       ClientStatusPending,
			ToolNameMode: ToolNameModePrefixed,
		},
	}
	// Wire the notification dispatcher so progress notifications are always routed.
	transport.SetNotificationsHandler(c.dispatchNotification)
	// Wire the server-method handler for elicitation and other server-to-client calls.
	transport.SetMethodsHandler(c.handleServerMethod)
	return c, nil
}

// Start starts the underlying transport but intentionally leaves the client in
// the pending state. Seshat only treats the connection as fully ready after the
// explicit MCP initialize handshake succeeds.
func (c *Client) Start(ctx context.Context) error {
	if err := c.transport.Start(ctx); err != nil {
		c.setStatus(ClientStatusFailed, err)
		return err
	}
	c.setStatus(ClientStatusPending, nil)
	return nil
}

// Close closes the MCP client.
func (c *Client) Close() error {
	err := c.transport.Close()
	if err != nil {
		c.setStatus(ClientStatusFailed, err)
		return err
	}
	c.mu.Lock()
	c.metadata.Status = ClientStatusClosed
	c.mu.Unlock()
	return nil
}

func (c *Client) setStatus(status ClientStatus, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metadata.Status = status
	if err != nil {
		c.metadata.LastError = err.Error()
	} else {
		c.metadata.LastError = ""
	}
}

// Metadata returns a snapshot of the client metadata.
func (c *Client) Metadata() ClientMetadata {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneMetadata(c.metadata)
}

// withTimeout scopes each RPC to the server-configured timeout so a slow or
// wedged backend cannot stall the caller indefinitely.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.config.Timeout)
}

// Discovery fetches all entities exposed by the server.
func (c *Client) Discovery(ctx context.Context) (*Discovery, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	resources, err := c.ListResources(ctx)
	if err != nil {
		return nil, err
	}
	prompts, err := c.ListPrompts(ctx)
	if err != nil {
		return nil, err
	}
	return &Discovery{Tools: tools, Resources: resources, Prompts: prompts}, nil
}

// CanonicalToolCall executes an MCP tool and returns the normalized result.
func (c *Client) CanonicalToolCall(ctx context.Context, name string, arguments map[string]any) (*ToolCallResult, error) {
	result, err := c.CallTool(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	canonical := normalizeToolCallResult(result)
	return &canonical, nil
}

// CanonicalReadResource reads a resource and returns the normalized result.
func (c *Client) CanonicalReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	contents, err := c.ReadResource(ctx, uri)
	if err != nil {
		return nil, err
	}
	return &ResourceReadResult{Contents: contents}, nil
}

// CanonicalPrompt fetches a prompt and returns all messages.
func (c *Client) CanonicalPrompt(ctx context.Context, name string, arguments map[string]any) (*PromptResult, error) {
	result, err := c.GetPrompt(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &PromptResult{}, nil
	}
	return &PromptResult{Messages: []PromptMessage{*result}}, nil
}

// SetToolNameMode records how wrapped MCP tool names are exposed.
func (c *Client) SetToolNameMode(mode ToolNameMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode == "" {
		mode = ToolNameModePrefixed
	}
	c.metadata.ToolNameMode = mode
}

// ToolNameMode returns the current exposed name mode.
func (c *Client) ToolNameMode() ToolNameMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metadata.ToolNameMode
}

// Initialize initializes the MCP server connection.
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": mcpPreferredVersion,
			"capabilities": map[string]any{
				"roots": map[string]any{
					"listChanged": true,
				},
				// Advertise elicitation support so servers may send
				// elicitation/create requests to ask the user for input.
				"elicitation": map[string]any{},
			},
			"clientInfo": map[string]any{
				"name":    "seshat-core",
				"version": "1.0.0",
			},
		},
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("initialize request failed: %w", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("initialize", resp)
	}

	result := &InitializeResult{}
	if err := parseMapToStruct(resp.Result, result); err != nil {
		c.setStatus(ClientStatusFailed, err)
		return nil, fmt.Errorf("failed to parse initialize result: %w", err)
	}

	now := time.Now()
	c.mu.Lock()
	c.metadata.Initialized = true
	c.metadata.LastInitializedAt = &now
	c.metadata.ServerInfo = &result.ServerInfo
	// Record the version the server agreed to use. The server is authoritative:
	// if it returns an older version we fall back to it gracefully.
	if result.ProtocolVersion != "" {
		c.metadata.ProtocolVersion = result.ProtocolVersion
	} else {
		c.metadata.ProtocolVersion = mcpPreferredVersion
	}
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	// The protocol asks for this before anything else is sent. A server that missed it says so on the next request, so a
	// failure here does not fail the connection.
	_ = c.transport.SendNotification(ctx, &JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
	return result, nil
}

// ListTools lists available tools from the MCP server.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req := &JSONRPCRequest{JSONRPC: "2.0", ID: c.nextRequestID(), Method: "tools/list"}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.setStatus(ClientStatusFailed, normalizeTransportError("list tools", err))
		return nil, normalizeTransportError("list tools", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("list tools", resp)
	}
	c.mu.Lock()
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	toolsRaw, ok := resp.Result["tools"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid tools list format")
	}
	tools := make([]Tool, 0, len(toolsRaw))
	for _, toolRaw := range toolsRaw {
		toolMap, ok := toolRaw.(map[string]any)
		if !ok {
			continue
		}
		tool := Tool{}
		if err := parseMapToStruct(toolMap, &tool); err != nil {
			continue
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// CallTool calls a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (map[string]any, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.setStatus(ClientStatusFailed, normalizeTransportError("call tool", err))
		return nil, normalizeTransportError("call tool", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("call tool", resp)
	}
	c.mu.Lock()
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	return normalizeToolCallResultMap(resp.Result), nil
}

// ListResources lists available resources from the MCP server.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req := &JSONRPCRequest{JSONRPC: "2.0", ID: c.nextRequestID(), Method: "resources/list"}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("list resources request failed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("list resources error: %s", resp.Error.Message)
	}
	resourcesRaw, ok := resp.Result["resources"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid resources list format")
	}
	resources := make([]Resource, 0, len(resourcesRaw))
	for _, resourceRaw := range resourcesRaw {
		resourceMap, ok := resourceRaw.(map[string]any)
		if !ok {
			continue
		}
		resource := Resource{}
		if err := parseMapToStruct(resourceMap, &resource); err != nil {
			continue
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

// ReadResource reads a resource from the MCP server.
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "resources/read",
		Params:  map[string]any{"uri": uri},
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.setStatus(ClientStatusFailed, normalizeTransportError("read resource", err))
		return nil, normalizeTransportError("read resource", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("read resource", resp)
	}
	c.mu.Lock()
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	contentsRaw, ok := resp.Result["contents"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid resource contents format")
	}
	contents := make([]ResourceContent, 0, len(contentsRaw))
	for _, contentRaw := range contentsRaw {
		contentMap, ok := contentRaw.(map[string]any)
		if !ok {
			continue
		}
		content := ResourceContent{}
		if err := parseMapToStruct(contentMap, &content); err != nil {
			continue
		}
		contents = append(contents, content)
	}
	return normalizeResourceContents(contents), nil
}

// ListPrompts lists available prompts from the MCP server.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req := &JSONRPCRequest{JSONRPC: "2.0", ID: c.nextRequestID(), Method: "prompts/list"}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.setStatus(ClientStatusFailed, normalizeTransportError("list prompts", err))
		return nil, normalizeTransportError("list prompts", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("list prompts", resp)
	}
	c.mu.Lock()
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	promptsRaw, ok := resp.Result["prompts"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid prompts list format")
	}
	prompts := make([]Prompt, 0, len(promptsRaw))
	for _, promptRaw := range promptsRaw {
		promptMap, ok := promptRaw.(map[string]any)
		if !ok {
			continue
		}
		prompt := Prompt{}
		if err := parseMapToStruct(promptMap, &prompt); err != nil {
			continue
		}
		prompts = append(prompts, prompt)
	}
	return prompts, nil
}

// GetPrompt gets a prompt from the MCP server.
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]any) (*PromptMessage, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "prompts/get",
		Params: map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.setStatus(ClientStatusFailed, normalizeTransportError("get prompt", err))
		return nil, normalizeTransportError("get prompt", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("get prompt", resp)
	}
	c.mu.Lock()
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	messagesRaw, ok := resp.Result["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid prompt messages format")
	}
	if len(messagesRaw) == 0 {
		return nil, fmt.Errorf("no messages in prompt")
	}
	messageMap, ok := messagesRaw[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid message format")
	}
	message := &PromptMessage{}
	if err := parseMapToStruct(messageMap, message); err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}
	return normalizePromptMessage(message), nil
}

// SetNotificationsHandler overrides the default notification dispatcher.
// Use this only if you need to intercept ALL notifications; prefer registering
// a per-call progress callback via CallToolWithProgress.
func (c *Client) SetNotificationsHandler(handler NotificationHandler) {
	c.transport.SetNotificationsHandler(handler)
}

// SetMethodsHandler overrides the default server-method handler.
func (c *Client) SetMethodsHandler(handler MethodHandler) {
	c.transport.SetMethodsHandler(handler)
}

// dispatchNotification routes server-to-client notifications to registered handlers.
// Currently handles notifications/progress (MCP spec §3.3).
func (c *Client) dispatchNotification(n *JSONRPCNotification) {
	if n.Method != "notifications/progress" {
		return
	}
	token, _ := n.Params["progressToken"].(string)
	if token == "" {
		return
	}
	c.progressMu.RLock()
	cb, ok := c.progressHandlers[token]
	c.progressMu.RUnlock()
	if !ok {
		return
	}
	progress, _ := n.Params["progress"].(float64)
	var totalPtr *float64
	if t, ok := n.Params["total"].(float64); ok {
		totalPtr = &t
	}
	message, _ := n.Params["message"].(string)
	cb(progress, totalPtr, message)
}

// handleServerMethod handles server-initiated JSON-RPC requests.
// Currently supports elicitation/create (MCP spec — elicitation capability).
func (c *Client) handleServerMethod(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	switch req.Method {
	case "elicitation/create":
		return c.handleElicitation(ctx, req)
	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}, nil
	}
}

// handleElicitation handles an elicitation/create request from the server.
// The server is asking the user to fill out a form; we forward the request via
// the PromptFn stored in the context (set by the SSE query handler).
func (c *Client) handleElicitation(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	message, _ := req.Params["message"].(string)
	// Fall back to a generic message if the server didn't provide one.
	if message == "" {
		message = "The MCP server is requesting input."
	}

	promptFn, _ := ctx.Value(elicitationPromptFnKey{}).(func(message string, schema map[string]any) (map[string]any, bool))
	if promptFn == nil {
		// No handler registered — return cancelled so the server can handle gracefully.
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"action": "cancel"},
		}, nil
	}

	schema, _ := req.Params["requestedSchema"].(map[string]any)
	response, ok := promptFn(message, schema)
	if !ok {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"action": "cancel"},
		}, nil
	}
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"action": "accept", "content": response},
	}, nil
}

// elicitationPromptFnKey is the context key for the elicitation prompt function.
type elicitationPromptFnKey struct{}

// registerProgressHandler registers a callback for a specific progress token.
func (c *Client) registerProgressHandler(token string, cb ProgressCallback) {
	c.progressMu.Lock()
	c.progressHandlers[token] = cb
	c.progressMu.Unlock()
}

// unregisterProgressHandler removes the progress callback for a token.
func (c *Client) unregisterProgressHandler(token string) {
	c.progressMu.Lock()
	delete(c.progressHandlers, token)
	c.progressMu.Unlock()
}

// CallToolWithProgress calls a tool and receives progress notifications via the
// provided callback. If onProgress is nil the call behaves like CallTool.
//
// The server receives a _meta.progressToken so it knows to send progress updates.
func (c *Client) CallToolWithProgress(
	ctx context.Context,
	name string,
	arguments map[string]any,
	onProgress ProgressCallback,
) (map[string]any, error) {
	params := map[string]any{
		"name":      name,
		"arguments": arguments,
	}
	if onProgress != nil {
		token := fmt.Sprintf("seshat-%d", c.nextRequestID())
		params["_meta"] = map[string]any{"progressToken": token}
		c.registerProgressHandler(token, onProgress)
		defer c.unregisterProgressHandler(token)
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "tools/call",
		Params:  params,
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.setStatus(ClientStatusFailed, normalizeTransportError("call tool", err))
		return nil, normalizeTransportError("call tool with progress", err)
	}
	if resp.Error != nil {
		c.mu.Lock()
		applyErrorStatus(&c.metadata, resp)
		c.mu.Unlock()
		return nil, normalizeMCPError("call tool with progress", resp)
	}
	c.mu.Lock()
	refreshConnectedStatus(&c.metadata)
	c.mu.Unlock()
	return normalizeToolCallResultMap(resp.Result), nil
}

// nextRequestID generates the next request ID (thread-safe).
func (c *Client) nextRequestID() int64 {
	return atomic.AddInt64(&c.requestID, 1)
}

// parseMapToStruct converts map[string]any to struct using JSON marshaling.
func parseMapToStruct(m map[string]any, target any) error {
	if m == nil {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to marshal map to JSON: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("failed to unmarshal JSON to target: %w", err)
	}
	return nil
}
