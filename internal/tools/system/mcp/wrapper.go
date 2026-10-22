package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const ListMcpResourcesToolName = "mcp_list_resources"
const ReadMcpResourceToolName = "mcp_read_resource"

const (
	// DefaultMaxToolSpecBytes is the most one MCP tool definition may take (name, description and input schema).
	DefaultMaxToolSpecBytes = 8_000
	// DefaultMaxServerSpecBytes is the most the tool definitions of one MCP server may take together.
	DefaultMaxServerSpecBytes = 64_000
)

type Wrapper struct {
	serverName string
	nameMode   ToolNameMode
	config     ServerConfig
	// eager is set when the tools of the server are small enough to be loaded with every request (see EagerServerBytes).
	eager bool

	// mu guards client - every wrapped tool/resource/prompt handler reads it
	// through activeClient, which may swap it for a freshly reconnected one
	// (see activeClient's doc comment). All handlers close over the same
	// *Wrapper, so this is the one place that needs to be race-safe.
	mu     sync.Mutex
	client *Client
}

func NewWrapper(client *Client, config ServerConfig, options *IntegrationOptions) *Wrapper {
	resolved := normalizeIntegrationOptions(options)
	if client != nil {
		client.SetToolNameMode(resolved.ToolNameMode)
	}
	return &Wrapper{client: client, serverName: config.Name, nameMode: resolved.ToolNameMode, config: config}
}

// activeClient returns a client ready to serve the next request, reconnecting
// once first if the current one has failed or closed.
//
// Mirrors OpenHands' MCPToolExecutor.call_tool (openhands-sdk/mcp/tool.py):
// checking connection health right before use and retrying once "prevents a
// single transient error from permanently disabling all MCP tools" for the
// rest of this connection's lifetime - which, since seshat-backend now
// caches the whole sdk.Client (and everything built into it, including this
// MCP connection) for up to 30 minutes, is a much longer window to be stuck
// than the old per-turn-fresh-client world this was originally written
// against. Without this, a server that crashes or drops its connection mid-
// session would stay broken for every turn until the cache entry expires.
//
// The underlying transport can't simply be restarted in place (see
// StdioTransport.Start's "already started" guard), so a genuine reconnect
// means building a brand new *Client from the original ServerConfig - this
// is why Wrapper keeps config around, not just the live client.
func (w *Wrapper) activeClient(ctx context.Context) *Client {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch w.client.Metadata().Status {
	case ClientStatusFailed, ClientStatusClosed, ClientStatusExpired:
		// fall through to reconnect attempt below
	default:
		return w.client
	}

	fresh, err := NewClient(w.config)
	if err != nil {
		log.Printf("[mcp] server %q: reconnect failed (create client): %v", w.serverName, err)
		return w.client
	}
	if err := fresh.Start(ctx); err != nil {
		log.Printf("[mcp] server %q: reconnect failed (start): %v", w.serverName, err)
		return w.client
	}
	if _, err := fresh.Initialize(ctx); err != nil {
		log.Printf("[mcp] server %q: reconnect failed (initialize): %v", w.serverName, err)
		_ = fresh.Close()
		return w.client
	}
	fresh.SetToolNameMode(w.nameMode)

	log.Printf("[mcp] server %q: reconnected after a dropped connection", w.serverName)
	stale := w.client
	w.client = fresh
	go func() { _ = stale.Close() }() // best-effort; never block this call on cleaning up the dead one
	return fresh
}

func (w *Wrapper) WrapTool(mcpTool Tool) (tool.Tool, error) {
	inputSchema := w.buildInputSchema(mcpTool.InputSchema)
	def := tool.Definition{
		Name:               exposedToolName(w.serverName, mcpTool.Name, w.nameMode),
		DisplayName:        mcpTool.Name,
		Description:        mcpTool.Description,
		Category:           "mcp",
		InputSchema:        inputSchema,
		IsReadOnly:         isReadOnlyMCPTool(mcpTool.Name),
		IsDestructive:      false,
		IsConcurrencySafe:  true,
		RequiresPermission: true,
		Metadata: map[string]any{
			"mcp_server":  w.serverName,
			"mcp_tool":    mcpTool.Name,
			"mcp_wrapper": ToolWrapperMetadata{ServerName: w.serverName, ToolName: mcpTool.Name, NameMode: w.nameMode},
		},
	}
	handler := func(ctx context.Context, input tool.CallInput, toolCtx tool.ToolUseContext) (tool.CallResult, error) {
		// Build a progress callback that forwards MCP progress notifications as
		// RuntimeEvents so SSE clients receive live updates during long tool calls.
		var onProgress ProgressCallback
		if emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent)); ok && emitter != nil {
			serverName := w.serverName
			toolDisplayName := mcpTool.Name
			onProgress = func(progress float64, total *float64, message string) {
				percent := progress * 100
				if total != nil && *total > 0 {
					percent = (progress / *total) * 100
				}
				meta := map[string]any{"mcp_server": serverName}
				if total != nil {
					meta["total"] = *total
				}
				emitter(types.RuntimeEvent{
					Type: types.RuntimeEventTypeToolProgress,
					ToolProgress: &types.ToolProgress{
						ToolName:        toolDisplayName,
						Stage:           types.ToolProgressStageRunning,
						Message:         message,
						PercentComplete: percent,
						Metadata:        meta,
					},
				})
			}
		}

		result, err := w.activeClient(ctx).CallToolWithProgress(ctx, mcpTool.Name, input.Parsed, onProgress)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		canonical := normalizeToolCallResult(result)
		resultStr, err := w.formatMCPResult(&canonical)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(resultStr), nil
	}
	aliases := w.generateAliases(mcpTool)
	builtTool, err := tool.NewBuilder(def.Name).
		WithDescription(def.Description).
		WithCategory(def.Category).
		AsMCP().
		WithAlwaysLoad(w.eager).
		WithInputSchema(def.InputSchema).
		WithAliases(aliases...).
		WithHandler(handler).
		Build()
	if err != nil {
		return nil, err
	}
	return builtTool, nil
}

// WrapTools wraps the tools of one server, within the server's size caps (see ServerConfig.MaxToolSpecBytes). A tool
// whose definition is over its cap, or that would take the server past its total, is left out and logged: every
// definition costs context on every request, and one chatty server must not fill the window.
func (w *Wrapper) WrapTools(mcpTools []Tool) ([]tool.Tool, error) {
	perTool := resolveSpecCap(w.config.MaxToolSpecBytes, DefaultMaxToolSpecBytes)
	perServer := resolveSpecCap(w.config.MaxServerSpecBytes, DefaultMaxServerSpecBytes)
	// A small server is loaded with every request instead of being found with tool_search: the search costs a whole round trip to the
	// model (several seconds) each time the person's request needs one of its tools, and the definitions cost less than that in
	// tokens. A server over the budget stays deferred.
	candidate := 0
	for _, mcpTool := range mcpTools {
		if size := toolSpecBytes(mcpTool); (perTool == 0 || size <= perTool) && (perServer == 0 || candidate+size <= perServer) {
			candidate += size
		}
	}
	w.eager = candidate > 0 && candidate <= EagerServerBytes

	wrapped := make([]tool.Tool, 0, len(mcpTools))
	total := 0
	for _, mcpTool := range mcpTools {
		size := toolSpecBytes(mcpTool)
		if perTool > 0 && size > perTool {
			log.Printf("[mcp] server %q: tool %q left out, its definition is %d bytes (cap %d)", w.serverName, mcpTool.Name, size, perTool)
			continue
		}
		if perServer > 0 && total+size > perServer {
			log.Printf("[mcp] server %q: tool %q left out, the server is over its %d byte total", w.serverName, mcpTool.Name, perServer)
			continue
		}
		wrappedTool, err := w.WrapTool(mcpTool)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap tool '%s': %w", mcpTool.Name, err)
		}
		total += size
		wrapped = append(wrapped, wrappedTool)
	}
	return wrapped, nil
}

// EagerServerBytes is the size, in bytes of name, description and schema, under which the tools of one MCP server are loaded with every
// request. About 750 tokens: the cost of the definitions of a server of a few small tools.
const EagerServerBytes = 3000

// toolSpecBytes is the size of what the model is sent for a tool: its name, description and input schema.
func toolSpecBytes(t Tool) int {
	raw, _ := json.Marshal(t.InputSchema)
	return len(t.Name) + len(t.Description) + len(raw)
}

// resolveSpecCap maps a configured cap to a limit: 0 means the default, a negative value means no limit.
func resolveSpecCap(configured, def int) int {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return def
	default:
		return configured
	}
}

func (w *Wrapper) formatMCPResult(result *ToolCallResult) (string, error) {
	if result == nil {
		return "", nil
	}
	if result.Text != "" {
		return result.Text, nil
	}
	if result.Structured != nil {
		return fmt.Sprintf("%v", result.Structured), nil
	}
	if result.Raw != nil {
		return fmt.Sprintf("%v", result.Raw), nil
	}
	return "", nil
}

// buildInputSchema converts the schema an MCP server sent for a tool. The server's value already is a whole JSON
// schema (type, properties, required), so it is converted as is; an object type is assumed when the server omits it.
func (w *Wrapper) buildInputSchema(mcpSchema map[string]any) schema.JSONSchema {
	if len(mcpSchema) == 0 {
		return schema.JSONSchema{Type: "object"}
	}
	converted := schema.FromMap(mcpSchema)
	if converted.Type == "" {
		converted.Type = "object"
	}
	return converted
}

func (w *Wrapper) generateAliases(mcpTool Tool) []string {
	aliases := []string{mcpTool.Name, fmt.Sprintf("%s.%s", w.serverName, mcpTool.Name)}
	if w.nameMode == ToolNameModeUnprefixed {
		aliases = append(aliases, prefixedToolName(w.serverName, mcpTool.Name))
	}
	return aliases
}

func (w *Wrapper) WrapResource(resource Resource) (tool.Tool, error) {
	def := tool.Definition{
		Name:               w.prefixResourceName(resource.URI),
		DisplayName:        resource.Name,
		Description:        fmt.Sprintf("Read MCP resource: %s", resource.Description),
		Category:           "mcp_resource",
		InputSchema:        schema.FromMap(map[string]any{"type": "object", "properties": map[string]any{"uri": map[string]any{"type": "string", "description": "Resource URI", "default": resource.URI}}}),
		IsReadOnly:         true,
		IsDestructive:      false,
		IsConcurrencySafe:  true,
		RequiresPermission: true,
		Metadata: map[string]any{
			"mcp_server":   w.serverName,
			"mcp_resource": resource.URI,
			"mime_type":    resource.MimeType,
		},
	}
	handler := func(ctx context.Context, input tool.CallInput, toolCtx tool.ToolUseContext) (tool.CallResult, error) {
		uri := resource.URI
		if input.Parsed != nil {
			if u, ok := input.Parsed["uri"].(string); ok {
				uri = u
			}
		}
		readResult, err := w.activeClient(ctx).CanonicalReadResource(ctx, uri)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(w.formatResourceContents(readResult.Contents)), nil
	}
	builtTool, err := tool.NewBuilder(def.Name).
		WithDescription(def.Description).
		WithCategory(def.Category).
		AsMCP().
		WithInputSchema(def.InputSchema).
		ReadOnly().
		WithHandler(handler).
		Build()
	if err != nil {
		return nil, err
	}
	return builtTool, nil
}

func (w *Wrapper) WrapResources(resources []Resource) ([]tool.Tool, error) {
	wrapped := make([]tool.Tool, 0, len(resources))
	for _, resource := range resources {
		wrappedResource, err := w.WrapResource(resource)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap resource '%s': %w", resource.URI, err)
		}
		wrapped = append(wrapped, wrappedResource)
	}
	return wrapped, nil
}

func (w *Wrapper) formatResourceContents(contents []ResourceContent) string {
	result := ""
	for _, content := range contents {
		if content.Text != "" {
			result += content.Text
		} else if content.Blob != "" {
			result += fmt.Sprintf("[Binary data: %d bytes]", len(content.Blob))
		}
		if content.MimeType != "" {
			result += fmt.Sprintf("\n(MIME type: %s)", content.MimeType)
		}
		result += "\n\n"
	}
	return result
}

func (w *Wrapper) prefixResourceName(uri string) string {
	return fmt.Sprintf("mcp_%s_resource_%s", sanitizeName(w.serverName), sanitizeName(uri))
}

func (w *Wrapper) WrapPrompt(prompt Prompt) (tool.Tool, error) {
	def := tool.Definition{
		Name:               w.prefixPromptName(prompt.Name),
		DisplayName:        prompt.Name,
		Description:        fmt.Sprintf("Get MCP prompt: %s", prompt.Description),
		Category:           "mcp_prompt",
		InputSchema:        w.buildPromptInputSchema(prompt),
		IsReadOnly:         true,
		IsDestructive:      false,
		IsConcurrencySafe:  true,
		RequiresPermission: true,
		Metadata: map[string]any{
			"mcp_server": w.serverName,
			"mcp_prompt": prompt.Name,
		},
	}
	handler := func(ctx context.Context, input tool.CallInput, toolCtx tool.ToolUseContext) (tool.CallResult, error) {
		arguments := make(map[string]any)
		if input.Parsed != nil {
			arguments = input.Parsed
		}
		promptResult, err := w.activeClient(ctx).CanonicalPrompt(ctx, prompt.Name, arguments)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		if len(promptResult.Messages) == 0 {
			return tool.NewTextResult(""), nil
		}
		message := promptResult.Messages[0]
		return tool.NewTextResult(fmt.Sprintf("[%s] %v", message.Role, message.Content)), nil
	}
	builtTool, err := tool.NewBuilder(def.Name).
		WithDescription(def.Description).
		WithCategory(def.Category).
		AsMCP().
		WithInputSchema(def.InputSchema).
		ReadOnly().
		WithHandler(handler).
		Build()
	if err != nil {
		return nil, err
	}
	return builtTool, nil
}

func (w *Wrapper) buildPromptInputSchema(prompt Prompt) schema.JSONSchema {
	properties := make(map[string]any)
	required := make([]string, 0)
	for _, arg := range prompt.Arguments {
		properties[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	return schema.FromMap(map[string]any{"type": "object", "properties": properties, "required": required})
}

func (w *Wrapper) prefixPromptName(name string) string {
	return fmt.Sprintf("mcp_%s_prompt_%s", sanitizeName(w.serverName), sanitizeName(name))
}

func (w *Wrapper) WrapListResourcesTool() (tool.Tool, error) {
	def := tool.Definition{
		Name:              ListMcpResourcesToolName,
		DisplayName:       "ListMcpResources",
		Description:       "List resources exposed by the MCP server",
		Category:          "mcp_resource",
		InputSchema:       schema.FromMap(map[string]any{"type": "object"}),
		IsReadOnly:        true,
		IsConcurrencySafe: true,
	}
	handler := func(ctx context.Context, input tool.CallInput, toolCtx tool.ToolUseContext) (tool.CallResult, error) {
		resources, err := w.activeClient(ctx).ListResources(ctx)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(fmt.Sprintf("%v", resources)), nil
	}
	builtTool, err := tool.NewBuilder(def.Name).
		WithDescription(def.Description).
		WithCategory(def.Category).
		WithInputSchema(def.InputSchema).
		ReadOnly().
		WithHandler(handler).
		Build()
	if err != nil {
		return nil, err
	}
	return builtTool, nil
}

func (w *Wrapper) WrapReadResourceTool() (tool.Tool, error) {
	def := tool.Definition{
		Name:              ReadMcpResourceToolName,
		DisplayName:       "ReadMcpResource",
		Description:       "Read a resource exposed by the MCP server",
		Category:          "mcp_resource",
		InputSchema:       schema.FromMap(map[string]any{"type": "object", "properties": map[string]any{"uri": map[string]any{"type": "string"}}, "required": []string{"uri"}}),
		IsReadOnly:        true,
		IsConcurrencySafe: true,
	}
	handler := func(ctx context.Context, input tool.CallInput, toolCtx tool.ToolUseContext) (tool.CallResult, error) {
		uri, _ := input.Parsed["uri"].(string)
		readResult, err := w.activeClient(ctx).CanonicalReadResource(ctx, uri)
		if err != nil {
			return tool.NewErrorResult(err), nil
		}
		return tool.NewTextResult(w.formatResourceContents(readResult.Contents)), nil
	}
	builtTool, err := tool.NewBuilder(def.Name).
		WithDescription(def.Description).
		WithCategory(def.Category).
		WithInputSchema(def.InputSchema).
		ReadOnly().
		WithHandler(handler).
		Build()
	if err != nil {
		return nil, err
	}
	return builtTool, nil
}

func (w *Wrapper) WrapAll(ctx context.Context) ([]tool.Tool, error) {
	allTools := make([]tool.Tool, 0)
	mcpTools, err := w.activeClient(ctx).ListTools(ctx)
	if err == nil && len(mcpTools) > 0 {
		wrappedTools, err := w.WrapTools(mcpTools)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap tools: %w", err)
		}
		allTools = append(allTools, wrappedTools...)
	}
	resources, err := w.activeClient(ctx).ListResources(ctx)
	if err == nil && len(resources) > 0 {
		wrappedResources, err := w.WrapResources(resources)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap resources: %w", err)
		}
		allTools = append(allTools, wrappedResources...)
	}
	prompts, err := w.activeClient(ctx).ListPrompts(ctx)
	if err == nil && len(prompts) > 0 {
		for _, prompt := range prompts {
			wrappedPrompt, err := w.WrapPrompt(prompt)
			if err != nil {
				return nil, fmt.Errorf("failed to wrap prompt '%s': %w", prompt.Name, err)
			}
			allTools = append(allTools, wrappedPrompt)
		}
	}
	listResourcesTool, err := w.WrapListResourcesTool()
	if err == nil {
		allTools = append(allTools, listResourcesTool)
	}
	readResourceTool, err := w.WrapReadResourceTool()
	if err == nil {
		allTools = append(allTools, readResourceTool)
	}
	return allTools, nil
}
