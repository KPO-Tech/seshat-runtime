package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/monitoring"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// codexJar is a process-global cookie jar for chatgpt.com Cloudflare cookies
// (cf_clearance, __cf_bm, __cflb, _cfuvid). Keeping it alive across requests
// lets the CF cookie flow work without re-triggering the bot challenge.
var (
	codexJarOnce sync.Once
	codexJar     http.CookieJar
)

// getCodexHTTPClient returns the shared streaming client (see
// newStreamingHTTPClient below) with the Cloudflare cookie jar attached.
// This used to construct its own client with Timeout: 120*time.Second,
// which - unlike every other provider - covers the entire request lifetime
// including reading the streamed response body. A reasoning model doing
// heavy tool use routinely takes longer than 120s to finish streaming a
// single turn; the cutoff killed the request mid-stream, and that failure
// (types.ErrCodeAPIResponse, not ErrCodeAPITimeout) was then treated as
// non-recoverable, so the turn died outright with no retry.
func getCodexHTTPClient() *http.Client {
	codexJarOnce.Do(func() {
		if jar, err := cookiejar.New(nil); err == nil {
			codexJar = jar
		}
	})
	client := newStreamingHTTPClient()
	client.Jar = codexJar
	return client
}

// defaultResponseHeaderTimeout bounds how long we wait for the first response
// byte (headers) from a remote API before giving up on the connection.
const defaultResponseHeaderTimeout = 60 * time.Second

// ollamaResponseHeaderTimeout is longer than the default: unlike a remote API
// that's already warm and serving, a local Ollama request can legitimately
// block behind loading a large model into memory (disk read, or CPU-only
// inference startup) before it emits a single byte, which routinely exceeds
// 60s on constrained hardware.
const ollamaResponseHeaderTimeout = 5 * time.Minute

// zaiResponseHeaderTimeout is longer than the default: Z.ai can take more than
// 60s to emit response headers on reasoning-heavy or tool-augmented requests,
// while still streaming normally once the first byte arrives.
const zaiResponseHeaderTimeout = 3 * time.Minute

// newStreamingHTTPClient returns an http.Client suitable for long-running LLM
// streaming responses. The key difference from a standard client is that
// http.Client.Timeout is NOT set — that field applies to the entire request
// lifetime including reading the response body, which would cut off any stream
// that takes longer than the timeout (e.g. complex multi-step tasks).
//
// Instead we apply transport-level timeouts only to the connection setup phase:
//   - TLSHandshakeTimeout: timeout for TLS negotiation
//   - ResponseHeaderTimeout: timeout to receive the first response byte (headers)
//
// The stream body itself is bounded only by context cancellation (user ctrl+c,
// session close, or the engine's own cancellation logic).
func newStreamingHTTPClient() *http.Client {
	return newStreamingHTTPClientWithHeaderTimeout(defaultResponseHeaderTimeout)
}

func newStreamingHTTPClientWithHeaderTimeout(responseHeaderTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: responseHeaderTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          20,
			IdleConnTimeout:       90 * time.Second,
		},
		// No Timeout here — streaming responses are unbounded in duration.
	}
}

// Client represents an API client
type Client struct {
	// apiKey is the API key
	apiKey string

	// baseURL is the base URL for the API
	baseURL string

	// httpClient is the HTTP client to use
	httpClient *http.Client

	// retryConfig is the retry configuration
	retryConfig types.RetryConfig

	// provider is the API provider
	provider types.APIProvider

	// providerConfig holds the provider-specific configuration
	providerConfig *Config

	// circuitBreaker provides fault tolerance
	circuitBreaker *CircuitBreaker

	// monitoring provides centralized metrics and logging
	monitoring *monitoring.System

	// adapter encapsulates the provider-specific wire format (request body,
	// endpoint, auth headers, response decode, streaming). It replaces the
	// per-provider switch statements that used to live in this file.
	adapter providerAdapter
}

// resolveAdapter returns the provider wire-format adapter, falling back to a
// provider-derived adapter if the client was constructed without one (defensive
// against struct-literal construction paths outside the standard constructors).
func (c *Client) resolveAdapter() providerAdapter {
	if c.adapter != nil {
		return c.adapter
	}
	return adapterForProvider(c.provider)
}

// httpClientForProvider returns the streaming http.Client appropriate for the
// given provider: Codex needs the Cloudflare cookie jar, Ollama and Z.ai need
// longer ResponseHeaderTimeout values for slow first-byte responses, everything
// else gets the default streaming client.
func httpClientForProvider(provider types.APIProvider) *http.Client {
	switch provider {
	case types.APIProviderCodex:
		return getCodexHTTPClient()
	case types.APIProviderOllama:
		return newStreamingHTTPClientWithHeaderTimeout(ollamaResponseHeaderTimeout)
	case types.APIProviderZAi:
		return newStreamingHTTPClientWithHeaderTimeout(zaiResponseHeaderTimeout)
	default:
		return newStreamingHTTPClient()
	}
}

// NewClient creates a new API client
func NewClient(apiKey string, providerType types.APIProvider) *Client {
	// Create provider config for model resolution
	providerConfig := GetProviderConfig(providerType)
	if providerConfig == nil {
		providerConfig = &Config{Provider: providerType}
	}
	providerConfig.APIKey = apiKey

	httpClient := httpClientForProvider(providerType)

	return &Client{
		apiKey:         apiKey,
		baseURL:        providerConfig.GetBaseURL(),
		provider:       providerType,
		providerConfig: providerConfig,
		httpClient:     httpClient,
		retryConfig:    types.DefaultRetryConfig(),
		adapter:        adapterForProvider(providerType),
	}
}

// NewClientWithConfig creates a new API client with provider-specific configuration
func NewClientWithConfig(apiKey string, config *Config) *Client {
	return newClientWithConfig(apiKey, config)
}

// NewFallbackClient builds a provider client from the provider-local credential
// sources so the engine can follow configured inter-provider fallback chains.
func NewFallbackClient(ctx context.Context, provider types.APIProvider) (*Client, error) {
	config := GetProviderConfig(provider)
	if config == nil {
		return nil, fmt.Errorf("provider config not found for %q", provider)
	}

	apiKey, err := getAPIKeyForProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	config.APIKey = apiKey

	if err := ValidateProviderConfig(config); err != nil {
		return nil, err
	}

	return newClientWithConfig(apiKey, config), nil
}

func newClientWithConfig(apiKey string, config *Config) *Client {
	baseURL := config.GetBaseURL()
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	httpClient := httpClientForProvider(config.Provider)

	return &Client{
		apiKey:         apiKey,
		baseURL:        baseURL,
		httpClient:     httpClient,
		retryConfig:    types.DefaultRetryConfig(),
		provider:       config.Provider,
		providerConfig: config,
		adapter:        adapterForProvider(config.Provider),
	}
}

// SetRetryConfig sets the retry configuration
func (c *Client) SetRetryConfig(config types.RetryConfig) {
	c.retryConfig = config
}

// SetMonitoring sets the monitoring system
func (c *Client) SetMonitoring(monitoringSys *monitoring.System) {
	c.monitoring = monitoringSys
}

// SetHTTPClient sets the HTTP client
func (c *Client) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// Config returns the provider configuration currently bound to the client.
func (c *Client) Config() *Config {
	return c.providerConfig
}

// resolveRequestModel rewrites req.Model.Model through the provider's
// ModelAliasMapping (e.g. "default" -> "gpt-5.6-sol") if one is configured.
// This must run before the request is used for anything - wire request
// building, response model attribution, monitoring - so callers apply it
// once at the top of each public entry point rather than deep inside the
// send path, where a by-value req copy wouldn't propagate the resolved
// name back to the caller's own local req (used for response decoding).
//
// Previously nothing called Config.ResolveModel for the actual outgoing
// request: it was only ever invoked inside GetEndpoint, and only mattered
// for providers whose endpoint URL embeds the model (Gemini). Every
// OpenAI-compatible provider and Codex put the model in the JSON body via
// ModelIdentifier.ProviderModelName(), which has no access to alias
// mapping (see its own "TODO: wire up provider config for model
// resolution" comment) - so a raw alias like "default" was sent to the
// real API verbatim and rejected. Confirmed live against a real Codex
// account: {"detail":"The 'default' model is not supported..."}.
func (c *Client) resolveRequestModel(req types.APIRequest) types.APIRequest {
	if c.providerConfig != nil {
		req.Model.Model = c.providerConfig.ResolveModel(req.Model.Model)
	}
	return req
}

// CreateMessage sends a non-streaming message creation request
func (c *Client) CreateMessage(ctx context.Context, req types.APIRequest) (*types.APIResponse, error) {
	req = c.resolveRequestModel(req)
	start := time.Now()

	// Record API request in monitoring
	if c.monitoring != nil {
		c.monitoring.RecordAPIRequest()
	}

	// Z.ai/GLM and Codex only support streaming
	if c.provider == types.APIProviderZAi || c.provider == types.APIProviderCodex {
		req.Stream = true
	} else {
		req.Stream = false
	}
	resp, err := c.sendMessageWithRetry(ctx, req)
	duration := time.Since(start)

	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		// Record API failure in monitoring
		if c.monitoring != nil {
			c.monitoring.RecordAPIFailure(err, duration)
		}
		return nil, err
	}
	defer resp.Body.Close()

	// Check status code first - if error, read error body before trying to decode
	if resp.StatusCode != http.StatusOK {
		// Record API failure in monitoring
		if c.monitoring != nil {
			c.monitoring.RecordAPIFailure(fmt.Errorf("HTTP status %d", resp.StatusCode), duration)
		}
		return nil, c.handleErrorResponse(resp, nil)
	}

	apiResp, err := c.decodeProviderResponse(resp.Body, req.Model)
	if err != nil {
		// Record API failure in monitoring
		if c.monitoring != nil {
			c.monitoring.RecordAPIFailure(err, duration)
		}
		return nil, types.WrapError(types.ErrCodeAPIResponse, "failed to decode response", err)
	}

	// Record API success in monitoring
	if c.monitoring != nil {
		c.monitoring.RecordAPISuccess(duration)
	}

	apiResp.StopReason = types.NormalizeStopReason(apiResp.StopReason, apiResp.Content)

	if c.monitoring != nil {
		c.monitoring.RecordCacheStats(
			apiResp.Usage.CacheReadInputTokens,
			apiResp.Usage.CacheCreationInputTokens,
		)
	}

	return &apiResp, nil
}

// CreateMessageStreamResult consumes the provider stream and returns a canonical aggregated response.
func (c *Client) CreateMessageStreamResult(ctx context.Context, req types.APIRequest) (*types.APIStreamResult, error) {
	return c.CreateMessageStreamResultWithCallback(ctx, req, nil)
}

// CreateMessageStreamResultWithCallback consumes the provider stream, emits
// normalized chunks to the callback, and returns a canonical aggregated response.
func (c *Client) CreateMessageStreamResultWithCallback(ctx context.Context, req types.APIRequest, onChunk func(types.APIResponseChunk)) (*types.APIStreamResult, error) {
	req = c.resolveRequestModel(req)
	req.Stream = true
	return c.resolveAdapter().createStreamResult(c, ctx, req, wrapThinkTagFilter(onChunk))
}

// createAnthropicStreamResult consumes an Anthropic SSE stream and returns the
// canonical aggregated response. It is the default streaming path (anthropic,
// and any provider without its own adapter) used by anthropicAdapter.
func (c *Client) createAnthropicStreamResult(ctx context.Context, req types.APIRequest, onChunk func(types.APIResponseChunk)) (*types.APIStreamResult, error) {
	chunks, err := c.CreateMessageStream(ctx, req)
	if err != nil {
		return nil, err
	}

	accumulator := &types.APIChunkAccumulator{}
	collected := make([]types.APIResponseChunk, 0)
	for chunk := range chunks {
		if chunk.Type == types.APIChunkTypeError {
			if chunk.Error != nil {
				return nil, chunk.Error
			}
			return nil, types.NewError(types.ErrCodeAPIResponse, "stream ended with unknown error")
		}
		emitStreamChunk(onChunk, chunk)
		collected = append(collected, chunk)
		accumulator.AddChunk(chunk)
	}

	response := accumulator.Build(req.Model, "stream-response")
	response.StopReason = types.NormalizeStopReason(response.StopReason, response.Content)

	if c.monitoring != nil {
		c.monitoring.RecordCacheStats(
			response.Usage.CacheReadInputTokens,
			response.Usage.CacheCreationInputTokens,
		)
	}

	return &types.APIStreamResult{Response: response, Chunks: collected}, nil
}

func emitStreamChunk(onChunk func(types.APIResponseChunk), chunk types.APIResponseChunk) {
	if onChunk == nil {
		return
	}
	onChunk(chunk)
}

// CreateMessageStream sends a streaming message creation request
func (c *Client) CreateMessageStream(ctx context.Context, req types.APIRequest) (<-chan types.APIResponseChunk, error) {
	req = c.resolveRequestModel(req)
	// Force streaming for this method
	req.Stream = true

	// Send request
	resp, err := c.sendStreamRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, c.handleErrorResponse(resp, nil)
	}

	// Create channel for chunks
	chunkChan := make(chan types.APIResponseChunk, 10)

	// Start streaming in goroutine
	go c.streamAnthropicResponse(ctx, resp, chunkChan)

	return chunkChan, nil
}

// sendMessage sends a message request
func (c *Client) sendMessage(ctx context.Context, req types.APIRequest) (*http.Response, error) {
	// Build request body
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, types.WrapError(types.ErrCodeAPIRequest, "failed to build request body", err)
	}

	// Create HTTP request
	endpoint := c.buildRequestEndpoint(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, types.WrapError(types.ErrCodeAPIRequest, "failed to create HTTP request", err)
	}

	// Set headers
	c.setRequestHeaders(httpReq, req)

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, types.WrapError(types.ErrCodeAPIRequest, "failed to send request", err)
	}

	return resp, nil
}

// Helper methods

func (c *Client) buildRequestEndpoint(req types.APIRequest) string {
	return c.resolveAdapter().requestEndpoint(c, req)
}

func (c *Client) buildModelEndpoint(model string) string {
	if c.providerConfig != nil {
		endpoint := c.providerConfig.GetEndpoint(model)
		if endpoint != "" {
			return endpoint
		}
	}
	return c.resolveAdapter().modelEndpoint(c, model)
}

func (c *Client) setRequestHeaders(req *http.Request, apiReq types.APIRequest) {
	req.Header.Set("Content-Type", "application/json")

	// Provider-specific auth/version headers are owned by the adapter.
	c.resolveAdapter().applyAuthHeaders(c, req)

	for key, value := range extractCustomHeaders(apiReq.Metadata) {
		if !isSafeMetadataHeader(key) {
			continue
		}
		req.Header.Set(key, value)
	}
}

func extractCustomHeaders(metadata map[string]any) map[string]string {
	if metadata == nil {
		return nil
	}
	if customHeaders, ok := metadata["headers"].(map[string]string); ok {
		return customHeaders
	}
	rawHeaders, ok := metadata["headers"].(map[string]any)
	if !ok {
		return nil
	}
	headers := make(map[string]string, len(rawHeaders))
	for key, rawValue := range rawHeaders {
		value, ok := rawValue.(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		headers[key] = value
	}
	return headers
}

// anthropicToolsWithCacheControl returns a copy of tools with cache_control
// set on the last entry for Anthropic-compatible providers. Anthropic caches
// all content up to the last cache_control breakpoint, so marking the final
// tool definition causes the entire tool list to be cached across turns.
// For other providers the input slice is returned as-is (no copy, no mutation).
func anthropicToolsWithCacheControl(tools []types.APIToolDefinition, provider types.APIProvider) []types.APIToolDefinition {
	if len(tools) == 0 {
		return tools
	}
	if provider != types.APIProviderAnthropic {
		return tools
	}
	out := make([]types.APIToolDefinition, len(tools))
	copy(out, tools)
	out[len(out)-1].CacheControl = types.NewEphemeralPromptCacheControl()
	return out
}

// anthropicMessagesWithCacheControl returns a copy of messages with
// cache_control set on the last cacheable content block of the last message,
// for Anthropic-compatible providers. Anthropic caches all content up to the
// last cache_control breakpoint, so marking the tail of the growing
// conversation/tool-result history — not just the system prompt and tool
// definitions, which were the only things previously ever cached — lets a
// long agentic loop reuse the cached prefix on every subsequent turn instead
// of reprocessing the whole accumulated history from scratch each time.
//
// Only TextContent and ToolResultContent carry a CacheControl field (the two
// block types that realistically end a request: the first user turn, or a
// tool-result turn after the model calls tools). If the last message's last
// block is neither, this is a no-op for that call — still correct, just not
// cached until the tail naturally becomes cacheable again.
// For other providers the input slice is returned as-is (no copy, no mutation).
func anthropicMessagesWithCacheControl(messages []types.Message, provider types.APIProvider) []types.Message {
	if len(messages) == 0 {
		return messages
	}
	if provider != types.APIProviderAnthropic {
		return messages
	}

	lastMsgIdx := len(messages) - 1
	lastMsg := messages[lastMsgIdx]
	if len(lastMsg.Content) == 0 {
		return messages
	}
	lastBlockIdx := len(lastMsg.Content) - 1

	var cachedBlock types.ContentBlock
	switch block := lastMsg.Content[lastBlockIdx].(type) {
	case types.TextContent:
		block.CacheControl = types.NewEphemeralPromptCacheControl()
		cachedBlock = block
	case types.ToolResultContent:
		block.CacheControl = types.NewEphemeralPromptCacheControl()
		cachedBlock = block
	default:
		return messages
	}

	outMessages := make([]types.Message, len(messages))
	copy(outMessages, messages)

	outContent := make([]types.ContentBlock, len(lastMsg.Content))
	copy(outContent, lastMsg.Content)
	outContent[lastBlockIdx] = cachedBlock

	lastMsg.Content = outContent
	outMessages[lastMsgIdx] = lastMsg
	return outMessages
}

func isSafeMetadataHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "content-type", "authorization", "x-api-key", "api-key", "anthropic-version":
		return false
	default:
		return key != ""
	}
}

func (c *Client) buildRequestBody(req types.APIRequest) (io.Reader, error) {
	return c.resolveAdapter().buildRequestBody(c, req)
}

// buildAnthropicRequestBody serializes an APIRequest into the Anthropic Messages
// wire format. It is the default request builder (anthropic and any provider
// without its own adapter) used by anthropicAdapter.
func (c *Client) buildAnthropicRequestBody(req types.APIRequest) (io.Reader, error) {
	body := map[string]any{
		"model":      req.Model.ProviderModelName(),
		"max_tokens": req.MaxTokens,
		"messages":   anthropicMessagesWithCacheControl(req.Messages, c.provider),
	}

	// Anthropic can consume structured system prompt blocks, preserving
	// cache_control breakpoints. Other providers that go through this builder
	// fall back to the flattened prompt string so the runtime keeps one
	// canonical prompt representation while their transport stays simple.
	if len(req.SystemPromptBlocks) > 0 && c.provider == types.APIProviderAnthropic {
		body["system"] = req.SystemPromptBlocks
	} else if req.SystemPrompt != "" {
		body["system"] = req.SystemPrompt
	} else if len(req.SystemPromptBlocks) > 0 {
		body["system"] = types.FlattenSystemPromptBlocks(req.SystemPromptBlocks)
	}

	if len(req.Tools) > 0 {
		body["tools"] = anthropicToolsWithCacheControl(req.Tools, c.provider)
	}

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopK != nil {
		body["top_k"] = *req.TopK
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop_sequences"] = req.StopSequences
	}
	if req.Stream {
		body["stream"] = true
	}
	// Anthropic has no native structured-output API: inject the schema constraint
	// as a system message suffix so the model knows the required JSON shape.
	if req.OutputSchema != nil {
		hint := req.OutputSchema.SystemPromptHint()
		if existing, ok := body["system"].(string); ok {
			body["system"] = existing + "\n\n" + hint
		} else {
			body["system"] = hint
		}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (c *Client) decodeProviderResponse(body io.Reader, model types.ModelIdentifier) (types.APIResponse, error) {
	return c.resolveAdapter().decodeResponse(c, body, model)
}

// decodeAnthropicResponse decodes an Anthropic Messages JSON response into the
// canonical APIResponse. It is the default decoder (anthropic and any provider
// without its own adapter) used by anthropicAdapter.
func decodeAnthropicResponse(body io.Reader, _ types.ModelIdentifier) (types.APIResponse, error) {
	var apiResp types.APIResponse
	if err := json.NewDecoder(body).Decode(&apiResp); err != nil {
		return types.APIResponse{}, err
	}
	return apiResp, nil
}

func (c *Client) handleErrorResponse(resp *http.Response, apiResp *types.APIResponse) error {
	// Read error body
	body, _ := io.ReadAll(resp.Body)

	// Parse error
	var errResp struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	_ = json.Unmarshal(body, &errResp)

	// Determine error code
	var errCode types.ErrorCode
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		errCode = types.ErrCodeAPIAuth
	case http.StatusTooManyRequests:
		errCode = types.ErrCodeAPIRateLimit
	case http.StatusBadRequest:
		errCode = types.ErrCodeAPIInvalid
	case http.StatusServiceUnavailable, // 503 — provider down or overloaded
		http.StatusGatewayTimeout,      // 504
		http.StatusBadGateway,          // 502
		http.StatusInternalServerError, // 500
		http.StatusRequestTimeout:      // 408
		errCode = types.ErrCodeAPITimeout
	default:
		if resp.StatusCode == 529 {
			// 529 is Anthropic's non-standard "overloaded" status
			errCode = types.ErrCodeAPITimeout
		} else {
			errCode = types.ErrCodeAPIRequest
		}
	}

	// Create error message
	message := errResp.Error.Message
	if message == "" {
		message = string(body)
	}

	return types.NewError(errCode, message)
}
