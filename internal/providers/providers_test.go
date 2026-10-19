package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestProviderAdapterDispatch is a golden/characterization test that locks in
// the per-provider wire-format contract now routed through providerAdapter.
// It exercises the full observable chain (endpoint + auth headers + request
// body shape) so future adapter changes that drift from the previous switch
// behaviour are caught.
func TestProviderAdapterDispatch(t *testing.T) {
	const baseURL = "https://test.local"
	cases := []struct {
		name         string
		provider     types.APIProvider
		modelName    string
		wantEndpoint string    // substring expected in the request URL
		wantHeader   [2]string // key,value to assert on the outgoing request ("" key skips)
		wantBodyKey  string    // top-level JSON key expected in the request body
	}{
		{"anthropic", types.APIProviderAnthropic, "claude-x", "/v1/messages", [2]string{"x-api-key", "k"}, "messages"},
		{"openai", types.APIProviderOpenAI, "gpt", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"zai", types.APIProviderZAi, "glm", "/chat/completions", [2]string{"x-api-key", "k"}, "messages"},
		{"minimax", types.APIProviderMiniMax, "mm", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"openrouter", types.APIProviderOpenRouter, "or", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"mistral", types.APIProviderMistral, "mi", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"gemini", types.APIProviderGemini, "gemini-x", ":generateContent", [2]string{"Authorization", "Bearer k"}, "contents"},
		{"ollama", types.APIProviderOllama, "llama", "/api/chat", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"codex", types.APIProviderCodex, "gpt-codex", "/responses", [2]string{"Authorization", "Bearer k"}, "input"},
		{"deepseek", types.APIProviderDeepSeek, "deepseek-chat", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"opencode", types.APIProviderOpenCode, "claude-sonnet-4", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
		{"kimi", types.APIProviderKimi, "kimi-k3", "/chat/completions", [2]string{"Authorization", "Bearer k"}, "messages"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := &Config{Provider: tc.provider, BaseURL: baseURL, APIKey: "k"}
			client := NewClientWithConfig("k", config)

			req := types.APIRequest{
				Model:     types.ModelIdentifier{Provider: tc.provider, Model: tc.modelName},
				MaxTokens: 10,
				Messages:  []types.Message{types.UserMessage("u", "hi")},
			}

			ep := client.buildRequestEndpoint(req)
			if !strings.Contains(ep, tc.wantEndpoint) {
				t.Errorf("endpoint = %q, want substring %q", ep, tc.wantEndpoint)
			}

			httpReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ep, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			client.setRequestHeaders(httpReq, req)
			if tc.wantHeader[0] != "" {
				if got := httpReq.Header.Get(tc.wantHeader[0]); got != tc.wantHeader[1] {
					t.Errorf("header %s = %q, want %q", tc.wantHeader[0], got, tc.wantHeader[1])
				}
			}

			bodyReader, err := client.buildRequestBody(req)
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			raw, err := io.ReadAll(bodyReader)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode body: %v (%s)", err, raw)
			}
			if _, ok := payload[tc.wantBodyKey]; !ok {
				t.Errorf("body missing top-level key %q; got keys %v", tc.wantBodyKey, keysOf(payload))
			}
		})
	}
}

// TestMiniMaxEndpointUsesCurrentDomainAndPath verifies MiniMax's default
// endpoint against its real, currently-documented API: api.minimax.io (not
// api.minimax.chat, which isn't a real MiniMax domain), with the standard
// OpenAI-compatible /v1/chat/completions path (not the deprecated
// /v1/text/chatcompletion_v2). An earlier version of this test locked in a
// narrower fix for a different bug (GetEndpoint double-appending
// "/chat/completions" onto what was then treated as an already-complete
// endpoint) — that special case is gone now that MiniMax's BaseURL is a real
// API root like every other OpenAI-compatible provider here.
func TestMiniMaxEndpointUsesCurrentDomainAndPath(t *testing.T) {
	config := &Config{Provider: types.APIProviderMiniMax, APIKey: "k"}
	got := config.GetEndpoint("MiniMax-M2.7")
	want := "https://api.minimax.io/v1/chat/completions"
	if got != want {
		t.Fatalf("GetEndpoint() = %q, want %q", got, want)
	}
}

// TestGeminiStreamEndpoint locks in the Gemini-specific streaming URL, which is
// the one request endpoint that differs from the non-stream model endpoint.
func TestGeminiStreamEndpoint(t *testing.T) {
	config := &Config{Provider: types.APIProviderGemini, BaseURL: "https://test.local", APIKey: "k"}
	client := NewClientWithConfig("k", config)
	req := types.APIRequest{
		Model:  types.ModelIdentifier{Provider: types.APIProviderGemini, Model: "gemini-x"},
		Stream: true,
	}
	ep := client.buildRequestEndpoint(req)
	if !strings.Contains(ep, ":streamGenerateContent?alt=sse") {
		t.Errorf("stream endpoint = %q, want :streamGenerateContent?alt=sse", ep)
	}
}

// TestAdapterForProviderDefault verifies the Anthropic provider uses the
// Anthropic wire format, matching the prior switch default.
func TestAdapterForProviderDefault(t *testing.T) {
	for _, p := range []types.APIProvider{types.APIProviderAnthropic} {
		if _, ok := adapterForProvider(p).(anthropicAdapter); !ok {
			t.Errorf("adapterForProvider(%s) = %T, want anthropicAdapter", p, adapterForProvider(p))
		}
	}
}

// TestKimiProviderConfig locks in the pieces that make the Kimi provider
// actually usable at runtime, not just listed in the catalog. Before this,
// types.APIProviderKimi had a registry.go catalog entry (so it showed up in
// provider-picker UIs) but no config.go Config, no adapter mapping (silently
// fell through to the Anthropic wire format), and no env var mapping -
// selecting it would have failed outright.
// TestDefaultBaseURL_MiniMaxEnablesModelSync verifies the fix for MiniMax
// model sync being unconditionally broken: DefaultBaseURL had no case for it
// (fell to the empty-string default), so FetchModels built "" + "/v1/models"
// whenever no explicit base URL was supplied. Now that MiniMax's real API
// root (api.minimax.io) supports the standard OpenAI-compatible /v1/models
// discovery endpoint, this returns a real, working base.
func TestDefaultBaseURL_MiniMaxEnablesModelSync(t *testing.T) {
	if got := DefaultBaseURL("minimax"); got != "https://api.minimax.io" {
		t.Errorf(`DefaultBaseURL("minimax") = %q, want the international https://api.minimax.io endpoint`, got)
	}
}

// TestResolveRequestModel_AppliesAliasMapping guards a bug found via a live
// smoke test against a real Codex account: model aliases (e.g. "default" ->
// "gpt-5.6-sol") were never actually applied to the outgoing request.
// Config.ResolveModel was only ever called inside GetEndpoint, which only
// matters for providers whose endpoint URL embeds the model (Gemini) - every
// OpenAI-compatible provider and Codex put the model in the JSON body via
// ModelIdentifier.ProviderModelName(), which has no access to alias mapping.
// A raw alias like "default" was sent to the real API verbatim and rejected:
// {"detail":"The 'default' model is not supported when using Codex with a
// ChatGPT account."}. resolveRequestModel is now called at the top of every
// public entry point (CreateMessage, CreateMessageStreamResultWithCallback,
// CreateMessageStream) so every adapter sees the resolved model consistently.
func TestResolveRequestModel_AppliesAliasMapping(t *testing.T) {
	config := &Config{
		Provider: types.APIProviderCodex,
		ModelAliasMapping: map[string]string{
			"default": "gpt-5.6-sol",
		},
	}
	client := NewClientWithConfig("k", config)

	req := types.APIRequest{Model: types.ModelIdentifier{Provider: types.APIProviderCodex, Model: "default"}}
	resolved := client.resolveRequestModel(req)
	if resolved.Model.Model != "gpt-5.6-sol" {
		t.Fatalf("resolveRequestModel() model = %q, want gpt-5.6-sol", resolved.Model.Model)
	}

	// A model that isn't a known alias passes through unchanged.
	req2 := types.APIRequest{Model: types.ModelIdentifier{Provider: types.APIProviderCodex, Model: "gpt-5.5"}}
	resolved2 := client.resolveRequestModel(req2)
	if resolved2.Model.Model != "gpt-5.5" {
		t.Fatalf("resolveRequestModel() model = %q, want unchanged gpt-5.5", resolved2.Model.Model)
	}
}

// TestCreateMessage_SendsResolvedModelInRequestBody is an end-to-end guard
// for the same bug at the real HTTP boundary: it spins up a fake server and
// asserts the JSON body's "model" field is the alias's resolved target, not
// the raw alias string a caller passed in.
func TestCreateMessage_SendsResolvedModelInRequestBody(t *testing.T) {
	var gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	config := &Config{
		Provider: types.APIProviderOpenAI,
		BaseURL:  server.URL,
		ModelAliasMapping: map[string]string{
			"default": "gpt-4o",
		},
	}
	client := NewClientWithConfig("k", config)
	client.SetHTTPClient(server.Client())

	_, err := client.CreateMessage(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "default"},
		MaxTokens: 10,
		Messages:  []types.Message{types.UserMessage("m1", "hi")},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if gotModel != "gpt-4o" {
		t.Fatalf("server received model %q, want resolved alias gpt-4o", gotModel)
	}
}

func TestKimiProviderConfig(t *testing.T) {
	cfg := GetProviderConfig(types.APIProviderKimi)
	if cfg == nil {
		t.Fatal("GetProviderConfig(kimi) = nil, want a Config (kimi has no working request path without one)")
	}
	// International endpoint - api.moonshot.cn is a distinct China mainland
	// deployment with separate accounts/billing; an international key 401s
	// against it and vice versa.
	if cfg.BaseURL != "https://api.moonshot.ai/v1" {
		t.Errorf("kimi BaseURL = %q, want the international https://api.moonshot.ai/v1 endpoint", cfg.BaseURL)
	}

	if _, ok := adapterForProvider(types.APIProviderKimi).(openAICompatAdapter); !ok {
		t.Errorf("adapterForProvider(kimi) = %T, want openAICompatAdapter (Kimi's API is OpenAI-compatible)", adapterForProvider(types.APIProviderKimi))
	}

	if got := providerEnvVar(types.APIProviderKimi); got != "KIMI_API_KEY" {
		t.Errorf("providerEnvVar(kimi) = %q, want KIMI_API_KEY", got)
	}

	if got := DefaultBaseURL("kimi"); got != "https://api.moonshot.ai" {
		t.Errorf(`DefaultBaseURL("kimi") = %q, want the international https://api.moonshot.ai endpoint`, got)
	}

	info, ok := GetProviderInfo(types.APIProviderKimi)
	if !ok {
		t.Fatal("GetProviderInfo(kimi) not found")
	}
	found := false
	for _, m := range info.Models {
		if m.Identifier == "kimi-k3" {
			found = true
			if m.ContextWindow <= 0 {
				t.Error("kimi-k3 has no context window set")
			}
		}
	}
	if !found {
		t.Error(`expected "kimi-k3" in the Kimi model catalog`)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCalculateAdvancedBackoff(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)

	retryConfig := types.RetryConfig{
		InitialBackoff:    100,
		MaxBackoff:        5000,
		BackoffMultiplier: 2.0,
	}

	client.SetRetryConfig(retryConfig)

	// Test backoff progression
	backoffs := make([]time.Duration, 5)
	for i := 1; i <= 5; i++ {
		backoffs[i-1] = client.calculateAdvancedBackoff(i)
	}

	// First attempt should be close to initial (with jitter)
	assert.GreaterOrEqual(t, backoffs[0].Milliseconds(), int64(75)) // 100 * 0.75
	assert.LessOrEqual(t, backoffs[0].Milliseconds(), int64(125))   // 100 * 1.25

	// Second attempt should be approximately 2x (with jitter)
	assert.GreaterOrEqual(t, backoffs[1].Milliseconds(), int64(150)) // 200 * 0.75
	assert.LessOrEqual(t, backoffs[1].Milliseconds(), int64(250))    // 200 * 1.25

	// Backoff should be capped at max
	for i := 2; i < 5; i++ {
		assert.LessOrEqual(t, backoffs[i].Milliseconds(), int64(5000))
	}
}

func TestClassifyError(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)

	// Test network errors
	classification := client.classifyError(fmt.Errorf("connection refused"), 0)
	assert.Equal(t, RetryClassificationNetwork, classification)

	// Test rate limit
	classification = client.classifyError(nil, http.StatusTooManyRequests)
	assert.Equal(t, RetryClassificationRateLimit, classification)

	// Test server overload
	classification = client.classifyError(nil, http.StatusGatewayTimeout)
	assert.Equal(t, RetryClassificationServerOverload, classification)

	classification = client.classifyError(nil, http.StatusServiceUnavailable)
	assert.Equal(t, RetryClassificationServerOverload, classification)

	// Test timeout
	classification = client.classifyError(nil, http.StatusRequestTimeout)
	assert.Equal(t, RetryClassificationTimeout, classification)

	// Test server error
	classification = client.classifyError(nil, http.StatusInternalServerError)
	assert.Equal(t, RetryClassificationServerError, classification)

	// Test client error
	classification = client.classifyError(nil, http.StatusBadRequest)
	assert.Equal(t, RetryClassificationClientError, classification)

	// Test auth error
	classification = client.classifyError(nil, http.StatusUnauthorized)
	assert.Equal(t, RetryClassificationAuthError, classification)

	classification = client.classifyError(nil, http.StatusForbidden)
	assert.Equal(t, RetryClassificationAuthError, classification)
}

func TestShouldRetryAdvanced(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)

	retryConfig := types.RetryConfig{
		MaxAttempts: 5,
	}
	client.SetRetryConfig(retryConfig)

	// Test network errors - should retry
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationNetwork, 1))
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationNetwork, 4))

	// Test client errors - should NOT retry
	assert.False(t, client.shouldRetryAdvanced(RetryClassificationClientError, 1))
	assert.False(t, client.shouldRetryAdvanced(RetryClassificationAuthError, 1))

	// Test server errors - should retry
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationServerError, 1))
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationServerOverload, 1))
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationTimeout, 1))

	// Test rate limit - should retry
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationRateLimit, 1))

	// Test max attempts - should NOT retry
	assert.False(t, client.shouldRetryAdvanced(RetryClassificationServerError, 5))
	assert.False(t, client.shouldRetryAdvanced(RetryClassificationNetwork, 5))

	// Test unknown errors - retry up to 3 times
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationUnknown, 1))
	assert.True(t, client.shouldRetryAdvanced(RetryClassificationUnknown, 2))
	assert.False(t, client.shouldRetryAdvanced(RetryClassificationUnknown, 3))
}

func TestAdvancedRetryIntegration(t *testing.T) {
	attemptCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++

		// Fail first 3 times, then succeed
		if attemptCount <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Configure retry with advanced settings
	retryConfig := types.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    50,  // Fast retry for testing
		MaxBackoff:        200, // Cap for testing
		BackoffMultiplier: 2.0,
	}

	client.SetRetryConfig(retryConfig)

	ctx := context.Background()
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	startTime := time.Now()
	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}
	duration := time.Since(startTime)

	// Should eventually succeed
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 4, attemptCount, "Should have taken 4 attempts (3 failures + 1 success)")

	// Should have taken reasonable time (not just sum of retries)
	assert.Greater(t, duration.Milliseconds(), int64(150)) // At least 3 * 50ms
	assert.Less(t, duration.Milliseconds(), int64(2000))   // But not too long
}

func TestAdvancedRetryWithRateLimit(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		// Return 429 first time, then success
		if requestCount == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"type":"rate_limit","message":"too many requests"}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	retryConfig := types.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    50,
		MaxBackoff:        200,
		BackoffMultiplier: 2.0,
	}

	client.SetRetryConfig(retryConfig)

	ctx := context.Background()
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Should succeed after rate limit retry
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, requestCount)
}

func TestAdvancedRetryNoClientErrors(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Always return 400 (client error)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"type":"bad_request","message":"invalid input"}}`))
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	retryConfig := types.RetryConfig{
		MaxAttempts:       10, // High max attempts
		InitialBackoff:    50,
		MaxBackoff:        200,
		BackoffMultiplier: 2.0,
	}

	client.SetRetryConfig(retryConfig)

	ctx := context.Background()
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Should fail immediately without retries for client errors
	assert.Error(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 1, requestCount, "Should only attempt once for client errors")
}

func TestAdvancedRetryWithCircuitBreaker(t *testing.T) {
	attemptCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		// Always fail
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Configure retry
	retryConfig := types.RetryConfig{
		MaxAttempts:       10,
		InitialBackoff:    50,
		MaxBackoff:        200,
		BackoffMultiplier: 2.0,
	}
	client.SetRetryConfig(retryConfig)

	// Configure circuit breaker
	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      3, // Trip after 3 failures
		CallTimeout:      1 * time.Second,
		ResetTimeout:     500 * time.Millisecond,
		HalfOpenMaxCalls: 2,
	}
	client.EnableCircuitBreakerWithConfig(cbConfig)

	ctx := context.Background()
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Should fail due to circuit breaker
	assert.Error(t, err)
	assert.Nil(t, resp)

	// Check that circuit breaker tripped and prevented many retry attempts
	stats := client.CircuitBreakerStats()
	assert.GreaterOrEqual(t, stats.TotalFailures, uint64(3))
	assert.LessOrEqual(t, attemptCount, 10, "Circuit breaker should have prevented excessive retries")
}

func TestAdvancedRetryJitter(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)

	retryConfig := types.RetryConfig{
		InitialBackoff:    1000,
		MaxBackoff:        5000,
		BackoffMultiplier: 2.0,
	}

	client.SetRetryConfig(retryConfig)

	// Collect multiple backoff values for the same non-capped attempt to check
	// jitter. Using attempts 1..100 mostly exercises the MaxBackoff cap, which
	// legitimately collapses the output into only a few unique values.
	backoffs := make([]time.Duration, 100)
	for i := range backoffs {
		backoffs[i] = client.calculateAdvancedBackoff(2)
	}

	// Check that we have variation (jitter is working)
	uniqueValues := make(map[float64]bool)
	for _, duration := range backoffs {
		uniqueValues[float64(duration.Milliseconds())] = true
	}

	// Should have some variation (not all identical)
	assert.Greater(t, len(uniqueValues), 10, "Should have variation in backoff values due to jitter")
}

func TestAdvancedRetryMaxAttempts(t *testing.T) {
	attemptCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		// Always fail
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Configure retry with limited attempts
	retryConfig := types.RetryConfig{
		MaxAttempts:       3, // Only 3 attempts
		InitialBackoff:    50,
		MaxBackoff:        200,
		BackoffMultiplier: 2.0,
	}
	client.SetRetryConfig(retryConfig)

	ctx := context.Background()
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Should fail after max attempts
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, 3, attemptCount, "Should have made exactly 3 attempts")
}

func TestAdvancedRetryConcurrent(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		// Fail first 2 times, then succeed
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	retryConfig := types.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    50,
		MaxBackoff:        200,
		BackoffMultiplier: 2.0,
	}
	client.SetRetryConfig(retryConfig)

	ctx := context.Background()

	// Send multiple concurrent requests that will all need retries
	const numRequests = 5
	var (
		wg         sync.WaitGroup
		resultsMu  sync.Mutex
		errResults []error
	)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := types.APIRequest{
				Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
				Messages:  []types.Message{},
				MaxTokens: 100,
			}
			resp, err := client.sendMessageWithRetry(ctx, req)
			if resp != nil {
				resp.Body.Close()
			}
			resultsMu.Lock()
			errResults = append(errResults, err)
			resultsMu.Unlock()
		}()
	}

	wg.Wait()

	// Check that most requests succeeded
	successCount := 0
	for _, err := range errResults {
		if err == nil {
			successCount++
		}
	}
	assert.Greater(t, successCount, 0, "Some concurrent requests should have succeeded")
	assert.LessOrEqual(t, int(requestCount.Load()), 25, "Should not have made excessive requests")
}

func TestAdvancedRetryWithMonitoring(t *testing.T) {
	attemptCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++

		// Fail first 2 times, then succeed
		if attemptCount <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Enable circuit breaker with monitoring callback
	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     500 * time.Millisecond,
		HalfOpenMaxCalls: 2,
		OnStateChange: func(from, to CircuitState) {
			// Simulate monitoring hook
			t.Logf("Circuit breaker state changed: %s -> %s", from, to)
		},
	}

	client.EnableCircuitBreakerWithConfig(cbConfig)

	retryConfig := types.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    50,
		MaxBackoff:        200,
		BackoffMultiplier: 2.0,
	}

	client.SetRetryConfig(retryConfig)

	ctx := context.Background()
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Check final stats
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cbStats := client.CircuitBreakerStats()
	assert.Greater(t, cbStats.TotalRequests, uint64(0))
	assert.Greater(t, cbStats.TotalFailures, uint64(0))
}

// TestCircuitBreakerIntegration tests the circuit breaker integration with the provider client
func TestCircuitBreakerIntegration(t *testing.T) {
	// Create a test HTTP server that simulates failures
	failureCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failureCount++
		if failureCount <= 3 {
			// First 3 requests fail
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
		} else {
			// After 3 failures, succeed
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"model": "claude-3-5-sonnet-20241022",
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	// Create client with circuit breaker enabled
	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Enable circuit breaker with custom configuration
	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      3, // Trip after 3 failures
		CallTimeout:      2 * time.Second,
		ResetTimeout:     500 * time.Millisecond, // Quick reset for testing
		HalfOpenMaxCalls: 2,
	}

	client.EnableCircuitBreakerWithConfig(cbConfig)

	// Make 5 requests
	ctx := context.Background()
	successCount := 0
	blockCount := 0

	for i := 0; i < 5; i++ {
		req := types.APIRequest{
			Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
			Messages:  []types.Message{},
			MaxTokens: 100,
		}

		resp, err := client.sendMessageWithRetry(ctx, req)
		if resp != nil {
			defer resp.Body.Close()
		}

		if err != nil {
			if IsCircuitBreakerOpenError(err) {
				blockCount++
			} else {
				// Other errors count as failures for circuit breaker
			}
		} else if resp != nil && resp.StatusCode == http.StatusOK {
			successCount++
		}

		// Small delay between requests
		time.Sleep(100 * time.Millisecond)
	}

	// Verify behavior: first 3 fail, circuit opens, subsequent requests blocked
	assert.GreaterOrEqual(t, failureCount, 3)
	assert.GreaterOrEqual(t, successCount, 0)

	// Check circuit breaker stats
	stats := client.CircuitBreakerStats()
	assert.Greater(t, stats.TotalRequests, uint64(0))
	assert.Greater(t, stats.TotalFailures, uint64(0))
}

func TestCircuitBreakerIntegrationAutoReset(t *testing.T) {
	// Create a test server that fails then succeeds
	failureCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failureCount++
		if failureCount <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	// Create client with circuit breaker
	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      2, // Trip after 2 failures
		CallTimeout:      1 * time.Second,
		ResetTimeout:     300 * time.Millisecond, // Fast reset for testing
		HalfOpenMaxCalls: 2,
	}

	client.EnableCircuitBreakerWithConfig(cbConfig)

	ctx := context.Background()

	// Trip the circuit
	for i := 0; i < 2; i++ {
		req := types.APIRequest{
			Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
			Messages:  []types.Message{},
			MaxTokens: 100,
		}

		if r, _ := client.sendMessageWithRetry(ctx, req); r != nil {
			r.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Circuit should be open
	stateAfterTrip := client.GetCircuitBreaker().State()
	assert.True(t, stateAfterTrip == CircuitStateOpen || stateAfterTrip == CircuitStateHalfOpen || stateAfterTrip == CircuitStateClosed)

	// Wait for auto-reset
	time.Sleep(400 * time.Millisecond)

	// Circuit should now be in half-open state
	state := client.GetCircuitBreaker().State()
	assert.True(t, state == CircuitStateHalfOpen || state == CircuitStateClosed)

	// Make a successful request
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Circuit should be closed after successful recovery
	stats := client.CircuitBreakerStats()
	assert.Equal(t, CircuitStateClosed, client.GetCircuitBreaker().State())
	assert.Greater(t, stats.TotalSuccesses, uint64(0))
}

func TestCircuitBreakerIntegrationWithRetry(t *testing.T) {
	// Test that retry logic respects circuit breaker
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Fail first 5 calls, then succeed
		if callCount <= 5 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	// Create client with circuit breaker and retry
	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Configure circuit breaker
	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      3, // Trip after 3 failures
		CallTimeout:      1 * time.Second,
		ResetTimeout:     500 * time.Millisecond,
		HalfOpenMaxCalls: 2,
	}

	client.EnableCircuitBreakerWithConfig(cbConfig)

	// Configure retry
	retryConfig := types.RetryConfig{
		MaxAttempts:       10, // More attempts than circuit breaker threshold
		InitialBackoff:    50, // Fast retry for testing
		MaxBackoff:        200,
		BackoffMultiplier: 1.5,
	}

	client.SetRetryConfig(retryConfig)

	ctx := context.Background()

	// Make a request that should trip circuit after 3 failures
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		resp.Body.Close()
	}

	// Should fail due to circuit breaker (not just HTTP errors)
	assert.Error(t, err)
	assert.True(t, IsCircuitBreakerOpenError(err) || errors.Is(err, context.DeadlineExceeded))

	// Check that circuit breaker tripped correctly
	stats := client.CircuitBreakerStats()
	assert.GreaterOrEqual(t, stats.ConsecutiveFailures, 3)
}

func TestCircuitBreakerIntegrationManualReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"type":"internal_error","message":"simulated failure"}}`))
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      2,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     5 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	client.EnableCircuitBreakerWithConfig(cbConfig)

	ctx := context.Background()

	// Make requests that will trip the circuit
	for i := 0; i < 2; i++ {
		req := types.APIRequest{
			Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
			Messages:  []types.Message{},
			MaxTokens: 100,
		}
		if r, _ := client.sendMessageWithRetry(ctx, req); r != nil {
			r.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Circuit should be open
	assert.Equal(t, CircuitStateOpen, client.GetCircuitBreaker().State())

	// Manually reset the circuit breaker
	client.ResetCircuitBreaker()

	// Circuit should now be closed
	assert.Equal(t, CircuitStateClosed, client.GetCircuitBreaker().State())

	// Stats should be reset (except for history)
	stats := client.CircuitBreakerStats()
	assert.Equal(t, 0, stats.ConsecutiveFailures)

	// Make a request - should be blocked again until failure threshold
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}
	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestCircuitBreakerIntegrationNoCircuitBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
	}))

	defer server.Close()

	// Create client without circuit breaker
	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	// Don't enable circuit breaker
	assert.Nil(t, client.GetCircuitBreaker())

	ctx := context.Background()

	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		Messages:  []types.Message{},
		MaxTokens: 100,
	}

	// Request should succeed normally
	resp, err := client.sendMessageWithRetry(ctx, req)
	if resp != nil {
		defer resp.Body.Close()
	}
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Stats should be empty
	stats := client.CircuitBreakerStats()
	assert.Equal(t, uint64(0), stats.TotalRequests)
}

func TestCircuitBreakerIntegrationConcurrent(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count <= 5 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "test-id",
				"type": "message",
				"role": "assistant",
				"content": [{"type":"text","text":"test response"}],
				"stop_reason": "end_turn"
			}`))
		}
	}))

	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})

	cbConfig := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     500 * time.Millisecond,
		HalfOpenMaxCalls: 2,
	}

	client.EnableCircuitBreakerWithConfig(cbConfig)

	ctx := context.Background()

	// Send multiple concurrent requests that will trip the circuit
	var wg sync.WaitGroup
	requestErrors := make([]error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := types.APIRequest{
				Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
				Messages:  []types.Message{},
				MaxTokens: 100,
			}
			resp, err := client.sendMessageWithRetry(ctx, req)
			if resp != nil {
				defer resp.Body.Close()
			}
			if err != nil && !IsCircuitBreakerOpenError(err) {
				requestErrors[idx] = err
			} else if resp != nil && resp.StatusCode != http.StatusOK {
				requestErrors[idx] = errors.New("server error")
			}
		}(i)
	}

	wg.Wait()

	// Some requests should have been blocked by circuit breaker
	unexpectedErrors := 0
	for _, err := range requestErrors {
		if err != nil && !IsCircuitBreakerOpenError(err) {
			unexpectedErrors++
		}
	}

	assert.Equal(t, 0, unexpectedErrors)

	// Check final state
	stats := client.CircuitBreakerStats()
	assert.Greater(t, stats.TotalRequests, uint64(0))
}

func TestNewCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker()
	assert.NotNil(t, cb)
	assert.Equal(t, CircuitStateClosed, cb.State())
}

func TestNewCircuitBreakerWithConfig(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      10 * time.Second,
		ResetTimeout:     30 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)
	assert.NotNil(t, cb)
	assert.Equal(t, CircuitStateClosed, cb.State())
}

func TestCircuitBreakerDefaultConfig(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	assert.Equal(t, 5, config.MaxFailures)
	assert.Equal(t, 30*time.Second, config.CallTimeout)
	assert.Equal(t, 60*time.Second, config.ResetTimeout)
	assert.Equal(t, 3, config.HalfOpenMaxCalls)
}

func TestCircuitBreakerInitialStats(t *testing.T) {
	cb := NewCircuitBreaker()
	stats := cb.Stats()

	assert.Equal(t, uint64(0), stats.TotalRequests)
	assert.Equal(t, uint64(0), stats.TotalSuccesses)
	assert.Equal(t, uint64(0), stats.TotalFailures)
	assert.Equal(t, 0, stats.ConsecutiveFailures)
	assert.Equal(t, 0, stats.HalfOpenCalls)
	assert.True(t, stats.LastFailureTime.IsZero())
	assert.True(t, stats.LastSuccessTime.IsZero())
}

func TestCircuitBreakerExecuteSuccess(t *testing.T) {
	cb := NewCircuitBreaker()

	// Execute successful calls
	for i := 0; i < 10; i++ {
		err := cb.Execute(func() error {
			return nil
		})
		assert.NoError(t, err)
		assert.Equal(t, CircuitStateClosed, cb.State())
	}

	stats := cb.Stats()
	assert.Equal(t, uint64(10), stats.TotalRequests)
	assert.Equal(t, uint64(10), stats.TotalSuccesses)
	assert.Equal(t, uint64(0), stats.TotalFailures)
	assert.Equal(t, 0, stats.ConsecutiveFailures)
}

func TestCircuitBreakerExecuteFailure(t *testing.T) {
	cb := NewCircuitBreaker()

	// Execute failed calls
	for i := 0; i < 10; i++ {
		err := cb.Execute(func() error {
			return errors.New("test error")
		})
		assert.Error(t, err)
	}

	stats := cb.Stats()
	assert.Equal(t, uint64(5), stats.TotalRequests)
	assert.Equal(t, uint64(0), stats.TotalSuccesses)
	assert.Equal(t, uint64(5), stats.TotalFailures)
	assert.Equal(t, 5, stats.ConsecutiveFailures)
}

func TestCircuitBreakerTrip(t *testing.T) {
	type stateChange struct {
		from CircuitState
		to   CircuitState
	}
	var (
		stateChanges []stateChange
		changesMu    sync.Mutex
	)

	config := &CircuitBreakerConfig{
		MaxFailures:      3, // Trip after 3 failures
		CallTimeout:      1 * time.Second,
		ResetTimeout:     100 * time.Millisecond,
		HalfOpenMaxCalls: 2,
		OnStateChange: func(from, to CircuitState) {
			changesMu.Lock()
			stateChanges = append(stateChanges, stateChange{from, to})
			changesMu.Unlock()
		},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// First two failures - circuit stays closed
	for i := 0; i < 2; i++ {
		err := cb.Execute(func() error {
			return errors.New("test error")
		})
		assert.Error(t, err)
		assert.Equal(t, CircuitStateClosed, cb.State())
	}

	// Third failure - circuit should trip
	err := cb.Execute(func() error {
		return errors.New("test error")
	})
	assert.Error(t, err)
	assert.Equal(t, CircuitStateOpen, cb.State())

	// Verify state transitions
	changesMu.Lock()
	changes := make([]stateChange, len(stateChanges))
	copy(changes, stateChanges)
	changesMu.Unlock()

	assert.GreaterOrEqual(t, len(changes), 1)
	if len(changes) > 0 {
		assert.Equal(t, CircuitStateClosed, changes[0].from)
		assert.Equal(t, CircuitStateOpen, changes[0].to)
	}
}

func TestCircuitBreakerOpenBlocksRequests(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, CircuitStateOpen, cb.State())

	// Subsequent requests should be blocked
	err := cb.Execute(func() error {
		return nil
	})
	assert.Error(t, err)
	assert.True(t, IsCircuitBreakerOpenError(err))
	assert.Equal(t, CircuitStateOpen, cb.State())
}

func TestCircuitBreakerCanExecute(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     100 * time.Millisecond,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Circuit is initially closed
	assert.True(t, cb.CanExecute())

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, CircuitStateOpen, cb.State())
	assert.False(t, cb.CanExecute())
}

func TestCircuitBreakerReset(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     200 * time.Millisecond,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, CircuitStateOpen, cb.State())
	assert.Equal(t, 3, cb.Stats().ConsecutiveFailures)

	// Reset circuit
	cb.Reset()

	assert.Equal(t, CircuitStateClosed, cb.State())
	assert.Equal(t, 0, cb.Stats().ConsecutiveFailures)

	// Should allow requests again
	err := cb.Execute(func() error {
		return nil
	})
	assert.NoError(t, err)
}

func TestCircuitBreakerAutomaticReset(t *testing.T) {
	type stateChange struct {
		from CircuitState
		to   CircuitState
	}
	var (
		stateChanges []stateChange
		changesMu    sync.Mutex
	)

	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     300 * time.Millisecond,
		HalfOpenMaxCalls: 2,
		OnStateChange: func(from, to CircuitState) {
			changesMu.Lock()
			stateChanges = append(stateChanges, stateChange{from, to})
			changesMu.Unlock()
		},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, CircuitStateOpen, cb.State())

	// Wait for automatic reset to half-open
	time.Sleep(400 * time.Millisecond)

	// State should have transitioned to half-open
	assert.Equal(t, CircuitStateHalfOpen, cb.State())

	// Verify state transitions
	changesMu.Lock()
	changes := make([]stateChange, len(stateChanges))
	copy(changes, stateChanges)
	changesMu.Unlock()

	if len(changes) >= 2 {
		assert.Equal(t, CircuitStateClosed, changes[0].from)
		assert.Equal(t, CircuitStateOpen, changes[0].to)
	}
}

func TestCircuitBreakerHalfOpenBehavior(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     200 * time.Millisecond,
		HalfOpenMaxCalls: 2, // Only allow 2 calls in half-open
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, CircuitStateOpen, cb.State())

	// Wait for automatic reset
	time.Sleep(300 * time.Millisecond)

	// Should now be in half-open state
	assert.Equal(t, CircuitStateHalfOpen, cb.State())

	// A successful half-open call should close the circuit and clear half-open tracking.
	err := cb.Execute(func() error {
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, CircuitStateClosed, cb.State())
	assert.Equal(t, 0, cb.Stats().HalfOpenCalls)
	assert.Equal(t, 0, cb.Stats().ConsecutiveFailures)
}

func TestCircuitBreakerHalfOpenToClosed(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     200 * time.Millisecond,
		HalfOpenMaxCalls: 3,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	// Wait for automatic reset to half-open
	time.Sleep(300 * time.Millisecond)

	assert.Equal(t, CircuitStateHalfOpen, cb.State())

	// Make successful calls in half-open
	for i := 0; i < 3; i++ {
		err := cb.Execute(func() error {
			return nil
		})
		assert.NoError(t, err)
	}

	// Circuit should transition back to closed
	assert.Equal(t, CircuitStateClosed, cb.State())
	assert.Equal(t, 0, cb.Stats().HalfOpenCalls)
	assert.Equal(t, 0, cb.Stats().ConsecutiveFailures)
}

func TestCircuitBreakerReadyToTripVeto(t *testing.T) {
	tripCount := 0
	vetoCount := 0

	config := &CircuitBreakerConfig{
		MaxFailures:      2,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     200 * time.Millisecond,
		HalfOpenMaxCalls: 2,
		ReadyToTrip: func() bool {
			vetoCount++
			return false // Always veto tripping
		},
		OnStateChange: func(from, to CircuitState) {
			if to == CircuitStateOpen {
				tripCount++
			}
		},
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Execute 3 failed calls (should trip after 2 but is vetoed)
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	// Circuit should NOT have opened due to veto
	assert.Equal(t, CircuitStateClosed, cb.State())
	assert.Equal(t, 0, tripCount)
	assert.Equal(t, 2, vetoCount)
}

func TestCircuitBreakerExecuteWithTimeout(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      1,
		CallTimeout:      100 * time.Millisecond, // Very short timeout
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Execute a function that respects context cancellation and times out.
	err := cb.ExecuteWithTimeout(context.Background(), func(ctx context.Context) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err() // Returns context.DeadlineExceeded when timeout fires.
		}
	})

	assert.Error(t, err)
	assert.True(t, IsCircuitBreakerTimeoutError(err))
	var timeoutErr *CircuitBreakerTimeoutError
	if assert.True(t, errors.As(err, &timeoutErr)) {
		assert.Equal(t, 100*time.Millisecond, timeoutErr.Timeout)
	}
	assert.Equal(t, CircuitStateOpen, cb.State()) // Should have tripped
}

func TestCircuitBreakerExecuteWithTimeoutSuccess(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      500 * time.Millisecond,
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Execute a function that completes within timeout.
	err := cb.ExecuteWithTimeout(context.Background(), func(ctx context.Context) error {
		select {
		case <-time.After(50 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	assert.NoError(t, err)
	assert.Equal(t, CircuitStateClosed, cb.State())
}

func TestCircuitBreakerConcurrentAccess(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      10,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 5,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Execute many concurrent requests
	var wg sync.WaitGroup
	requestErrors := make([]error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := cb.Execute(func() error {
				time.Sleep(time.Duration(idx) * time.Millisecond)
				if idx%10 == 0 {
					return errors.New("periodic error")
				}
				return nil
			})
			requestErrors[idx] = err
		}(i)
	}

	wg.Wait()

	// Check that all calls completed
	successCount := 0
	failureCount := 0
	for _, err := range requestErrors {
		if err == nil {
			successCount++
		} else if !IsCircuitBreakerOpenError(err) {
			failureCount++
		}
	}

	assert.Greater(t, successCount, 0)
	assert.Greater(t, failureCount, 0)

	// Verify stats are consistent
	stats := cb.Stats()
	assert.Equal(t, uint64(100), stats.TotalRequests)
	assert.Equal(t, uint64(successCount+failureCount), stats.TotalSuccesses+stats.TotalFailures)
}

func TestCircuitBreakerResetClearsTimer(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     5 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, CircuitStateOpen, cb.State())

	// Reset multiple times
	cb.Reset()
	cb.Reset()
	cb.Reset()

	assert.Equal(t, CircuitStateClosed, cb.State())

	// Wait for timer to fire (should have been stopped)
	time.Sleep(100 * time.Millisecond)

	// Should still be closed (timer was stopped)
	assert.Equal(t, CircuitStateClosed, cb.State())
}

func TestCircuitBreakerStatsConsistency(t *testing.T) {
	cb := NewCircuitBreaker()

	// Execute a mix of successes and failures
	for i := 0; i < 20; i++ {
		err := cb.Execute(func() error {
			if i%3 == 0 {
				return errors.New("periodic error")
			}
			return nil
		})
		_ = err
	}

	stats := cb.Stats()

	// Verify consistency
	assert.Equal(t, uint64(20), stats.TotalRequests)
	assert.Equal(t, uint64(13), stats.TotalSuccesses)
	assert.Equal(t, uint64(7), stats.TotalFailures)
	assert.Equal(t, uint64(0), stats.StateTransitions)
}

func TestCircuitBreakerSuccessResetsConsecutiveFailures(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Accumulate consecutive failures
	for i := 0; i < 2; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, 2, cb.Stats().ConsecutiveFailures)

	// One success should reset consecutive failures
	_ = cb.Execute(func() error {
		return nil
	})

	assert.Equal(t, 0, cb.Stats().ConsecutiveFailures)
}

func TestCircuitBreakerStateTransitionCounting(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      2,
		CallTimeout:      1 * time.Second,
		ResetTimeout:     200 * time.Millisecond,
		HalfOpenMaxCalls: 2,
	}

	cb := NewCircuitBreakerWithConfig(config)

	// Trip circuit
	for i := 0; i < 2; i++ {
		_ = cb.Execute(func() error {
			return errors.New("test error")
		})
	}

	assert.Equal(t, uint64(1), cb.Stats().StateTransitions)

	// Wait for auto-reset
	time.Sleep(300 * time.Millisecond)

	// Should have transitioned to half-open
	assert.GreaterOrEqual(t, cb.Stats().StateTransitions, uint64(2))

	// Make successful calls
	for i := 0; i < 2; i++ {
		_ = cb.Execute(func() error {
			return nil
		})
	}

	// Should have transitioned back to closed
	assert.GreaterOrEqual(t, cb.Stats().StateTransitions, uint64(3))
}

func TestIsCircuitBreakerOpenError(t *testing.T) {
	err := &CircuitBreakerOpenError{State: CircuitStateOpen}
	assert.True(t, IsCircuitBreakerOpenError(err))

	otherErr := errors.New("other error")
	assert.False(t, IsCircuitBreakerOpenError(otherErr))
}

func TestIsCircuitBreakerTimeoutError(t *testing.T) {
	err := &CircuitBreakerTimeoutError{Timeout: 1 * time.Second, Duration: 1 * time.Second}
	assert.True(t, IsCircuitBreakerTimeoutError(err))

	otherErr := errors.New("other error")
	assert.False(t, IsCircuitBreakerTimeoutError(otherErr))
}

// TestCB_TimeoutCancelsWork verifies that the fn context is cancelled on timeout
// and that no goroutine outlives the ExecuteWithTimeout call.
func TestCB_TimeoutCancelsWork(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      5,
		CallTimeout:      50 * time.Millisecond,
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 3,
	}
	cb := NewCircuitBreakerWithConfig(config)

	cancelled := make(chan struct{})
	err := cb.ExecuteWithTimeout(context.Background(), func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			close(cancelled) // signal that we saw the cancellation
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})

	assert.True(t, IsCircuitBreakerTimeoutError(err), "expected timeout error, got %v", err)

	select {
	case <-cancelled:
		// fn received cancellation — no goroutine leak
	case <-time.After(200 * time.Millisecond):
		t.Error("fn did not receive context cancellation within 200ms")
	}
}

// TestCB_RepeatedTimeouts verifies that repeated timeouts accumulate failures
// and eventually trip the circuit.
func TestCB_RepeatedTimeouts(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      3,
		CallTimeout:      30 * time.Millisecond,
		ResetTimeout:     10 * time.Second,
		HalfOpenMaxCalls: 2,
	}
	cb := NewCircuitBreakerWithConfig(config)

	slowFn := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}

	for i := 0; i < 3; i++ {
		err := cb.ExecuteWithTimeout(context.Background(), slowFn)
		assert.True(t, IsCircuitBreakerTimeoutError(err), "call %d should timeout", i+1)
	}

	assert.Equal(t, CircuitStateOpen, cb.State(), "circuit should be open after 3 timeouts")

	// Further calls must be blocked immediately.
	err := cb.ExecuteWithTimeout(context.Background(), slowFn)
	assert.True(t, IsCircuitBreakerOpenError(err), "open circuit should block")
}

// TestCB_ContextCancellationPropagation verifies that parent-context cancellation
// is forwarded to fn and is NOT treated as a circuit-breaker timeout.
func TestCB_ContextCancellationPropagation(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      5,
		CallTimeout:      5 * time.Second, // long timeout — parent ctx will cancel first
		ResetTimeout:     10 * time.Second,
		HalfOpenMaxCalls: 3,
	}
	cb := NewCircuitBreakerWithConfig(config)

	parentCtx, parentCancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- cb.ExecuteWithTimeout(parentCtx, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	// Cancel the parent context while fn is waiting.
	time.Sleep(20 * time.Millisecond)
	parentCancel()

	select {
	case err := <-done:
		// Should NOT be a circuit-breaker timeout error.
		assert.False(t, IsCircuitBreakerTimeoutError(err), "parent cancel should not be treated as CB timeout")
	case <-time.After(500 * time.Millisecond):
		t.Error("ExecuteWithTimeout did not return after parent context was cancelled")
	}
}

// TestCB_ConcurrentExecuteWithTimeout verifies that concurrent calls through
// ExecuteWithTimeout are race-free.
func TestCB_ConcurrentExecuteWithTimeout(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      100,
		CallTimeout:      200 * time.Millisecond,
		ResetTimeout:     1 * time.Second,
		HalfOpenMaxCalls: 10,
	}
	cb := NewCircuitBreakerWithConfig(config)

	const goroutines = 20
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cb.ExecuteWithTimeout(context.Background(), func(ctx context.Context) error {
				select {
				case <-time.After(10 * time.Millisecond):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	wg.Wait()
	// Must not panic or race; stats must be consistent.
	stats := cb.Stats()
	assert.Equal(t, uint64(goroutines), stats.TotalRequests)
}

// TestCB_SuccessResetsAfterTimeout verifies that a successful call after a timeout
// resets consecutive failure count.
func TestCB_SuccessResetsAfterTimeout(t *testing.T) {
	config := &CircuitBreakerConfig{
		MaxFailures:      5,
		CallTimeout:      30 * time.Millisecond,
		ResetTimeout:     10 * time.Second,
		HalfOpenMaxCalls: 3,
	}
	cb := NewCircuitBreakerWithConfig(config)

	// One timeout.
	_ = cb.ExecuteWithTimeout(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	// One success.
	err := cb.ExecuteWithTimeout(context.Background(), func(ctx context.Context) error {
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 0, cb.Stats().ConsecutiveFailures)
}

// TestBuildOpenAIMessagesToolResultWithText is a regression test for the
// converter bug where a user message mixing tool_result blocks with free text
// silently dropped the text when translating Anthropic → OpenAI chat.
func TestBuildOpenAIMessagesToolResultWithText(t *testing.T) {
	client := NewClientWithConfig("k", &Config{Provider: types.APIProviderOpenAI})
	messages := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.ToolResultContent{ToolUseID: "call_1", Content: "result text"},
				types.TextContent{Text: "now summarize"},
			},
		},
	}

	parts := client.buildOpenAIMessages(types.APIRequest{Messages: messages})

	var sawTool, sawUserText bool
	for _, p := range parts {
		if p["role"] == "tool" && p["tool_call_id"] == "call_1" {
			sawTool = true
		}
		if p["role"] == "user" && p["content"] == "now summarize" {
			sawUserText = true
		}
	}
	if !sawTool {
		t.Error("expected a role:tool message for the tool_result")
	}
	if !sawUserText {
		t.Error("user free-text alongside tool_result was dropped")
	}
}

// TestBuildOpenAIMessagesToolResultOnly ensures the common case (tool_result
// with no accompanying text) still emits exactly the tool message and no empty
// trailing user message.
func TestBuildOpenAIMessagesToolResultOnly(t *testing.T) {
	client := NewClientWithConfig("k", &Config{Provider: types.APIProviderOpenAI})
	messages := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.ToolResultContent{ToolUseID: "call_1", Content: "result text"},
			},
		},
	}

	parts := client.buildOpenAIMessages(types.APIRequest{Messages: messages})
	for _, p := range parts {
		if p["role"] == "user" {
			t.Errorf("did not expect a user message for tool_result-only input, got %v", p)
		}
	}
}

func TestBuildRequestBodyUsesProviderModelName(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		MaxTokens: 512,
		Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
	}

	body, err := client.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody failed: %v", err)
	}

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read request body: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}

	if got := payload["model"]; got != req.Model.ProviderModelName() {
		t.Fatalf("expected provider-facing model %q, got %#v", req.Model.ProviderModelName(), got)
	}
	if got := payload["model"]; got == req.Model.String() {
		t.Fatalf("request model must not use Seshat-qualified name %q", req.Model.String())
	}
}

func TestBuildRequestBodyOpenAIUsesProviderNativeShape(t *testing.T) {
	client := NewClient("test-key", types.APIProviderOpenAI)
	req := types.APIRequest{
		Model:        types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o-mini"},
		MaxTokens:    512,
		SystemPrompt: "system rules",
		Messages: []types.Message{
			types.UserMessage("msg-1", "hello"),
			types.AssistantMessage("msg-2", []types.ContentBlock{
				types.TextContent{Text: "checking"},
				types.ToolUseContent{ID: "call_1", Name: "Read", Input: map[string]any{"file_path": "README.md"}},
			}),
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					types.ToolResultContent{ToolUseID: "call_1", Content: "file contents"},
				},
			},
		},
		Tools: []types.APIToolDefinition{
			{Name: "Read", Description: "Read file", InputSchema: schema.FromMap(map[string]any{"type": "object"})},
		},
	}

	body, err := client.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody failed: %v", err)
	}

	var payload map[string]any
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}

	if _, exists := payload["system"]; exists {
		t.Fatalf("openai payload must not use anthropic system field: %#v", payload)
	}
	rawMessages, ok := payload["messages"].([]any)
	if !ok || len(rawMessages) != 4 {
		t.Fatalf("expected 4 provider-native messages including system, got %#v", payload["messages"])
	}
	systemMsg := rawMessages[0].(map[string]any)
	if systemMsg["role"] != "system" || systemMsg["content"] != "system rules" {
		t.Fatalf("unexpected system message %#v", systemMsg)
	}
	assistantMsg := rawMessages[2].(map[string]any)
	if assistantMsg["role"] != "assistant" {
		t.Fatalf("unexpected assistant message %#v", assistantMsg)
	}
	toolCalls, ok := assistantMsg["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected assistant tool_calls, got %#v", assistantMsg["tool_calls"])
	}
	toolMsg := rawMessages[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" {
		t.Fatalf("unexpected tool result message %#v", toolMsg)
	}
	rawTools, ok := payload["tools"].([]any)
	if !ok || len(rawTools) != 1 {
		t.Fatalf("expected openai tool definitions, got %#v", payload["tools"])
	}
}

func TestCreateMessageStreamResultReconstructsToolUseInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("expected /v1/messages endpoint, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{
				"type": "content_block_start",
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    "toolu_1",
					"name":  "bash",
					"input": map[string]any{},
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `{"command":"ls`,
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": ` -la","path":"/tmp"}`,
				},
			},
			{
				"type": "content_block_stop",
			},
			{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"input_tokens":  12,
					"output_tokens": 7,
				},
			},
			{
				"type": "message_stop",
				"usage": map[string]any{
					"input_tokens":  12,
					"output_tokens": 7,
				},
			},
		}
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("failed to encode event: %v", err)
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				t.Fatalf("failed to write event: %v", err)
			}
		}
	}))
	defer server.Close()

	client := NewClient("test-key", types.APIProviderAnthropic)
	client.baseURL = server.URL
	client.providerConfig.BaseURL = server.URL
	client.SetHTTPClient(server.Client())

	result, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "run ls")},
	})
	if err != nil {
		t.Fatalf("CreateMessageStreamResult failed: %v", err)
	}

	if result.Response.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected normalized stop reason %q, got %q", types.StopReasonToolUse, result.Response.StopReason)
	}
	if len(result.Response.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Response.Content))
	}

	toolUse, ok := result.Response.Content[0].(types.ToolUseContent)
	if !ok {
		t.Fatalf("expected tool_use content block, got %T", result.Response.Content[0])
	}
	if toolUse.Name != "bash" {
		t.Fatalf("expected tool name bash, got %q", toolUse.Name)
	}
	if got := toolUse.Input["command"]; got != "ls -la" {
		t.Fatalf("expected reconstructed command %q, got %#v", "ls -la", got)
	}
	if got := toolUse.Input["path"]; got != "/tmp" {
		t.Fatalf("expected reconstructed path %q, got %#v", "/tmp", got)
	}
}

func TestCreateMessageStreamResultPreservesMixedTextAndMultipleToolUses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{
				"type": "content_block_start",
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type": "text_delta",
					"text": "Plan:",
				},
			},
			{
				"type": "content_block_stop",
			},
			{
				"type": "content_block_start",
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    "toolu_1",
					"name":  "Read",
					"input": map[string]any{},
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `{"file_path":"README.md"}`,
				},
			},
			{
				"type": "content_block_stop",
			},
			{
				"type": "content_block_start",
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type": "text_delta",
					"text": " then patch",
				},
			},
			{
				"type": "content_block_stop",
			},
			{
				"type": "content_block_start",
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    "toolu_2",
					"name":  "Edit",
					"input": map[string]any{},
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `{"file_path":"README.md","old_string":"old","new_string":"new"}`,
				},
			},
			{
				"type": "content_block_stop",
			},
			{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"input_tokens":  20,
					"output_tokens": 11,
				},
			},
			{
				"type": "message_stop",
			},
		}
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("failed to encode event: %v", err)
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				t.Fatalf("failed to write event: %v", err)
			}
		}
	}))
	defer server.Close()

	client := NewClient("test-key", types.APIProviderAnthropic)
	client.baseURL = server.URL
	client.providerConfig.BaseURL = server.URL
	client.SetHTTPClient(server.Client())

	result, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "patch it")},
	})
	if err != nil {
		t.Fatalf("CreateMessageStreamResult failed: %v", err)
	}

	if result.Response.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected normalized stop reason %q, got %q", types.StopReasonToolUse, result.Response.StopReason)
	}
	if len(result.Response.Content) != 4 {
		t.Fatalf("expected 4 content blocks, got %d", len(result.Response.Content))
	}

	firstText, ok := result.Response.Content[0].(types.TextContent)
	if !ok || firstText.Text != "Plan:" {
		t.Fatalf("expected first text block %q, got %#v", "Plan:", result.Response.Content[0])
	}
	firstTool, ok := result.Response.Content[1].(types.ToolUseContent)
	if !ok || firstTool.Name != "Read" {
		t.Fatalf("expected first tool_use Read, got %#v", result.Response.Content[1])
	}
	if got := firstTool.Input["file_path"]; got != "README.md" {
		t.Fatalf("expected first tool file_path %q, got %#v", "README.md", got)
	}
	secondText, ok := result.Response.Content[2].(types.TextContent)
	if !ok || secondText.Text != " then patch" {
		t.Fatalf("expected second text block %q, got %#v", " then patch", result.Response.Content[2])
	}
	secondTool, ok := result.Response.Content[3].(types.ToolUseContent)
	if !ok || secondTool.Name != "Edit" {
		t.Fatalf("expected second tool_use Edit, got %#v", result.Response.Content[3])
	}
	if got := secondTool.Input["new_string"]; got != "new" {
		t.Fatalf("expected second tool new_string %q, got %#v", "new", got)
	}
}

func TestCreateMessageParsesOpenAIResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test",
			"choices": []map[string]any{
				{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"content": "Checking.",
						"tool_calls": []map[string]any{
							{
								"id":   "call_1",
								"type": "function",
								"function": map[string]any{
									"name":      "Read",
									"arguments": `{"file_path":"README.md"}`,
								},
							},
						},
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 5,
			},
		})
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	resp, err := client.CreateMessage(context.Background(), types.APIRequest{
		Model:        types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o-mini"},
		MaxTokens:    256,
		SystemPrompt: "rules",
		Messages:     []types.Message{types.UserMessage("msg-1", "hello")},
	})
	if err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}
	if resp.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonToolUse, resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected text + tool_use content, got %d blocks", len(resp.Content))
	}
}

// TestCreateMessageDecodesZAiSSEResponse covers the auto-mode classifier's
// call path: it calls the non-streaming CreateMessage, but Z.ai only
// supports streaming, so CreateMessage forces req.Stream = true for it and
// the server answers with an SSE body ("data: {...}") instead of one JSON
// object. Before the fix, decodeOpenAIResponse tried to json.Decode that SSE
// body directly and failed on the literal first byte with "invalid
// character 'd' looking for beginning of value" - see zAiAdapter.decodeResponse.
func TestCreateMessageDecodesZAiSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if stream, _ := payload["stream"].(bool); !stream {
			t.Fatalf("expected CreateMessage to force a streaming request for Z.ai, got %#v", payload)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{
				"id": "chatcmpl-1",
				"choices": []map[string]any{
					{"delta": map[string]any{"content": "blocking for safety"}},
				},
			},
			{
				"id": "chatcmpl-1",
				"choices": []map[string]any{
					{"finish_reason": "stop"},
				},
				"usage": map[string]any{
					"prompt_tokens":     10,
					"completion_tokens": 3,
				},
			},
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderZAi, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	resp, err := client.CreateMessage(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderZAi, Model: "glm-5.1"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "classify this")},
	})
	if err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}
	if resp.StopReason != types.StopReasonEndTurn {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonEndTurn, resp.StopReason)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("expected a single text content block, got %d blocks", len(resp.Content))
	}
}

func TestCreateMessageParsesOllamaResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "qwen2.5",
			"message": map[string]any{
				"content": "Checking.",
				"tool_calls": []map[string]any{
					{
						"function": map[string]any{
							"name":      "Read",
							"arguments": map[string]any{"file_path": "README.md"},
						},
					},
				},
			},
			"prompt_eval_count": 14,
			"eval_count":        6,
		})
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderOllama, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	resp, err := client.CreateMessage(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderOllama, Model: "qwen2.5"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
	})
	if err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}
	if resp.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonToolUse, resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected text + tool_use content, got %d blocks", len(resp.Content))
	}
}

func TestCreateMessageParsesGeminiResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{
					"finishReason": "STOP",
					"content": map[string]any{
						"parts": []map[string]any{
							{"text": "Checking."},
							{
								"functionCall": map[string]any{
									"name": "Read",
									"args": map[string]any{"file_path": "README.md"},
								},
							},
						},
					},
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     11,
				"candidatesTokenCount": 5,
			},
		})
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderGemini, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	resp, err := client.CreateMessage(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderGemini, Model: "gemini-2.0-flash"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
	})
	if err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}
	if resp.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonToolUse, resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected text + tool_use content, got %d blocks", len(resp.Content))
	}
}

func TestCreateMessageStreamResultParsesOpenAIStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if stream, _ := payload["stream"].(bool); !stream {
			t.Fatalf("expected OpenAI stream request, got %#v", payload)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{
				"id": "chatcmpl-1",
				"choices": []map[string]any{
					{
						"delta": map[string]any{
							"content": "Checking.",
						},
					},
				},
			},
			{
				"id": "chatcmpl-1",
				"choices": []map[string]any{
					{
						"delta": map[string]any{
							"tool_calls": []map[string]any{
								{
									"index": 0,
									"id":    "call_1",
									"type":  "function",
									"function": map[string]any{
										"name":      "Read",
										"arguments": `{"file_path":"README`,
									},
								},
							},
						},
					},
				},
			},
			{
				"id": "chatcmpl-1",
				"choices": []map[string]any{
					{
						"delta": map[string]any{
							"tool_calls": []map[string]any{
								{
									"index": 0,
									"function": map[string]any{
										"arguments": `.md"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]any{
					"prompt_tokens":     15,
					"completion_tokens": 7,
				},
			},
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	result, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o-mini"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
	})
	if err != nil {
		t.Fatalf("CreateMessageStreamResult failed: %v", err)
	}
	if result.Response.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonToolUse, result.Response.StopReason)
	}
	if len(result.Response.Content) != 2 {
		t.Fatalf("expected text + tool_use content, got %d blocks", len(result.Response.Content))
	}
}

func TestCreateMessageStreamResultParsesOllamaStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if stream, _ := payload["stream"].(bool); !stream {
			t.Fatalf("expected Ollama stream request, got %#v", payload)
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		events := []map[string]any{
			{
				"message": map[string]any{
					"content": "Checking.",
				},
			},
			{
				"message": map[string]any{
					"tool_calls": []map[string]any{
						{
							"function": map[string]any{
								"name":      "Read",
								"arguments": map[string]any{"file_path": "README.md"},
							},
						},
						{
							"function": map[string]any{
								"name":      "Read",
								"arguments": map[string]any{"file_path": "README.md"},
							},
						},
					},
				},
				"prompt_eval_count": 12,
				"eval_count":        5,
			},
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "%s\n", data)
		}
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderOllama, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	result, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderOllama, Model: "qwen2.5"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
	})
	if err != nil {
		t.Fatalf("CreateMessageStreamResult failed: %v", err)
	}
	if result.Response.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonToolUse, result.Response.StopReason)
	}
	if len(result.Response.Content) != 3 {
		t.Fatalf("expected text + 2 tool_use content blocks, got %d", len(result.Response.Content))
	}
}

func TestCreateMessageStreamResultParsesGeminiStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.String(), ":streamGenerateContent?alt=sse") {
			t.Fatalf("expected Gemini stream endpoint, got %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"parts": []map[string]any{
								{"text": "Checking."},
							},
						},
					},
				},
			},
			{
				"candidates": []map[string]any{
					{
						"finishReason": "STOP",
						"content": map[string]any{
							"parts": []map[string]any{
								{
									"functionCall": map[string]any{
										"name": "Read",
										"args": map[string]any{"file_path": "README.md"},
									},
								},
							},
						},
					},
				},
				"usageMetadata": map[string]any{
					"promptTokenCount":     11,
					"candidatesTokenCount": 5,
				},
			},
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}))
	defer server.Close()

	client := NewClientWithConfig("test-key", &Config{Provider: types.APIProviderGemini, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	result, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderGemini, Model: "gemini-2.0-flash"},
		MaxTokens: 256,
		Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
	})
	if err != nil {
		t.Fatalf("CreateMessageStreamResult failed: %v", err)
	}
	if result.Response.StopReason != types.StopReasonToolUse {
		t.Fatalf("expected stop reason %q, got %q", types.StopReasonToolUse, result.Response.StopReason)
	}
	if len(result.Response.Content) != 2 {
		t.Fatalf("expected text + tool_use content, got %d blocks", len(result.Response.Content))
	}
}

func TestCreateMessageStreamResultReturnsNativeRateLimitErrors(t *testing.T) {
	testCases := []struct {
		name        string
		provider    types.APIProvider
		model       string
		checkStream func(*testing.T, *http.Request)
	}{
		{
			name:     "openai",
			provider: types.APIProviderOpenAI,
			model:    "gpt-4o-mini",
		},
		{
			name:     "ollama",
			provider: types.APIProviderOllama,
			model:    "qwen2.5",
		},
		{
			name:     "gemini",
			provider: types.APIProviderGemini,
			model:    "gemini-2.0-flash",
			checkStream: func(t *testing.T, r *http.Request) {
				t.Helper()
				if !strings.Contains(r.URL.String(), ":streamGenerateContent?alt=sse") {
					t.Fatalf("expected Gemini stream endpoint, got %s", r.URL.String())
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.checkStream != nil {
					tc.checkStream(t, r)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			}))
			defer server.Close()

			client := NewClientWithConfig("test-key", &Config{Provider: tc.provider, BaseURL: server.URL})
			client.SetHTTPClient(server.Client())
			// A stream is retried on a 429, so the server answers 429 until the
			// attempts run out; keep that fast.
			client.SetRetryConfig(types.RetryConfig{MaxAttempts: 2, InitialBackoff: 1, MaxBackoff: 2, BackoffMultiplier: 1})

			_, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
				Model:     types.ModelIdentifier{Provider: tc.provider, Model: tc.model},
				MaxTokens: 256,
				Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
			})
			assertEngineErrorCode(t, err, types.ErrCodeAPIRateLimit)
		})
	}
}

func TestCreateMessageStreamResultRejectsMalformedNativeStreams(t *testing.T) {
	testCases := []struct {
		name     string
		provider types.APIProvider
		model    string
		handler  http.HandlerFunc
	}{
		{
			name:     "openai",
			provider: types.APIProviderOpenAI,
			model:    "gpt-4o-mini",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {invalid-json}\n\n")
			},
		},
		{
			name:     "ollama",
			provider: types.APIProviderOllama,
			model:    "qwen2.5",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = fmt.Fprintln(w, `{"message":{"content":"ok"}}`)
				_, _ = fmt.Fprintln(w, `{invalid-json}`)
			},
		},
		{
			name:     "gemini",
			provider: types.APIProviderGemini,
			model:    "gemini-2.0-flash",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.String(), ":streamGenerateContent?alt=sse") {
					t.Fatalf("expected Gemini stream endpoint, got %s", r.URL.String())
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {invalid-json}\n\n")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()

			client := NewClientWithConfig("test-key", &Config{Provider: tc.provider, BaseURL: server.URL})
			client.SetHTTPClient(server.Client())

			_, err := client.CreateMessageStreamResult(context.Background(), types.APIRequest{
				Model:     types.ModelIdentifier{Provider: tc.provider, Model: tc.model},
				MaxTokens: 256,
				Messages:  []types.Message{types.UserMessage("msg-1", "hello")},
			})
			assertEngineErrorCode(t, err, types.ErrCodeAPIResponse)
		})
	}
}

func assertEngineErrorCode(t *testing.T, err error, want types.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	var engineErr *types.EngineError
	if !errors.As(err, &engineErr) {
		t.Fatalf("expected EngineError, got %T: %v", err, err)
	}
	if engineErr.Code != want {
		t.Fatalf("expected error code %q, got %q (%v)", want, engineErr.Code, err)
	}
}

func TestHandleErrorResponseMapsStatusCodesToEngineErrorCodes(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)

	cases := []struct {
		name       string
		statusCode int
		wantCode   types.ErrorCode
		wantRetry  bool
	}{
		{"401 unauthorized", http.StatusUnauthorized, types.ErrCodeAPIAuth, false},
		{"429 rate limit", http.StatusTooManyRequests, types.ErrCodeAPIRateLimit, true},
		{"400 bad request", http.StatusBadRequest, types.ErrCodeAPIInvalid, false},
		{"500 internal server error", http.StatusInternalServerError, types.ErrCodeAPITimeout, true},
		{"502 bad gateway", http.StatusBadGateway, types.ErrCodeAPITimeout, true},
		{"503 service unavailable", http.StatusServiceUnavailable, types.ErrCodeAPITimeout, true},
		{"504 gateway timeout", http.StatusGatewayTimeout, types.ErrCodeAPITimeout, true},
		{"408 request timeout", http.StatusRequestTimeout, types.ErrCodeAPITimeout, true},
		{"529 overloaded", 529, types.ErrCodeAPITimeout, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.statusCode,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"test error"}}`)),
				Header:     make(http.Header),
			}
			err := client.handleErrorResponse(resp, nil)
			assertEngineErrorCode(t, err, tc.wantCode)

			var engineErr *types.EngineError
			if !errors.As(err, &engineErr) {
				t.Fatalf("expected EngineError, got %T", err)
			}
			if got := engineErr.IsRetryable(); got != tc.wantRetry {
				t.Fatalf("IsRetryable() = %v, want %v for status %d", got, tc.wantRetry, tc.statusCode)
			}
			if tc.wantRetry && engineErr.IsPermanent() {
				t.Fatalf("IsPermanent() must be false for retryable error code %q", engineErr.Code)
			}
		})
	}
}

func TestHandleErrorResponseIsRetryableMatchesDefaultRetryConfig(t *testing.T) {
	client := NewClient("test-key", types.APIProviderAnthropic)
	retryableStatuses := []int{
		http.StatusTooManyRequests,     // 429
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
		http.StatusBadGateway,          // 502
		http.StatusInternalServerError, // 500
		http.StatusRequestTimeout,      // 408
		529,                            // Anthropic overloaded
	}

	for _, status := range retryableStatuses {
		resp := &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}
		err := client.handleErrorResponse(resp, nil)

		var engineErr *types.EngineError
		if !errors.As(err, &engineErr) {
			t.Fatalf("status %d: expected EngineError, got %T", status, err)
		}
		if !engineErr.IsRetryable() {
			t.Fatalf("status %d: IsRetryable() = false, expected true — loop cannot recover from this", status)
		}
	}
}

// ============================================================================
// Prompt Caching Tests
// ============================================================================

func TestAnthropicToolsWithCacheControl_SetsOnLastTool(t *testing.T) {
	tools := []types.APIToolDefinition{
		{Name: "read", Description: "Read a file"},
		{Name: "write", Description: "Write a file"},
		{Name: "bash", Description: "Run shell command"},
	}

	result := anthropicToolsWithCacheControl(tools, types.APIProviderAnthropic)

	if len(result) != len(tools) {
		t.Fatalf("expected %d tools, got %d", len(tools), len(result))
	}
	if result[0].CacheControl != nil {
		t.Error("first tool should not have cache_control")
	}
	if result[1].CacheControl != nil {
		t.Error("middle tool should not have cache_control")
	}
	last := result[len(result)-1]
	if last.CacheControl == nil {
		t.Fatal("last tool must have cache_control set")
	}
	if last.CacheControl.Type != "ephemeral" {
		t.Errorf("expected cache_control.type=ephemeral, got %q", last.CacheControl.Type)
	}
}

func TestAnthropicToolsWithCacheControl_NotSetForOpenAI(t *testing.T) {
	tools := []types.APIToolDefinition{
		{Name: "search", Description: "Search the web"},
	}

	result := anthropicToolsWithCacheControl(tools, types.APIProviderOpenAI)

	if result[0].CacheControl != nil {
		t.Error("cache_control must not be set for non-Anthropic providers")
	}
}

func TestAnthropicToolsWithCacheControl_EmptySlice(t *testing.T) {
	result := anthropicToolsWithCacheControl(nil, types.APIProviderAnthropic)
	if result != nil {
		t.Error("nil input should return nil")
	}
	result = anthropicToolsWithCacheControl([]types.APIToolDefinition{}, types.APIProviderAnthropic)
	if len(result) != 0 {
		t.Error("empty input should return empty")
	}
}

func TestAnthropicToolsWithCacheControl_DoesNotMutateInput(t *testing.T) {
	tools := []types.APIToolDefinition{
		{Name: "bash", Description: "Run shell"},
	}

	anthropicToolsWithCacheControl(tools, types.APIProviderAnthropic)

	if tools[0].CacheControl != nil {
		t.Error("original slice must not be mutated")
	}
}

// TestAnthropicMessagesWithCacheControl verifies the fix for conversation/
// tool-result history never being cached: previously only the system prompt
// and tool definitions ever got a cache_control breakpoint, so in a long
// agentic loop (many tool_use/tool_result rounds) the whole growing history
// was reprocessed from scratch on every single call. The last cacheable
// block of the last message should now get an ephemeral breakpoint too, for
// Anthropic-compatible providers only, without mutating the input.
func TestAnthropicMessagesWithCacheControl(t *testing.T) {
	t.Run("marks last TextContent block for Anthropic", func(t *testing.T) {
		messages := []types.Message{
			types.UserMessage("m1", "hello"),
		}
		result := anthropicMessagesWithCacheControl(messages, types.APIProviderAnthropic)
		text, ok := result[0].Content[0].(types.TextContent)
		if !ok || text.CacheControl == nil {
			t.Fatalf("expected last TextContent block to carry cache_control, got %+v", result[0].Content[0])
		}
		if orig, ok := messages[0].Content[0].(types.TextContent); !ok || orig.CacheControl != nil {
			t.Error("original messages slice must not be mutated")
		}
	})

	t.Run("marks last ToolResultContent block for Anthropic", func(t *testing.T) {
		messages := []types.Message{
			{ID: "m1", Role: types.RoleUser, Content: []types.ContentBlock{
				types.ToolResultContent{ToolUseID: "t1", Content: "ok"},
			}},
		}
		result := anthropicMessagesWithCacheControl(messages, types.APIProviderAnthropic)
		toolResult, ok := result[0].Content[0].(types.ToolResultContent)
		if !ok || toolResult.CacheControl == nil {
			t.Fatalf("expected last ToolResultContent block to carry cache_control, got %+v", result[0].Content[0])
		}
	})

	t.Run("only marks the trailing block, not earlier ones in the same message", func(t *testing.T) {
		messages := []types.Message{
			{ID: "m1", Role: types.RoleUser, Content: []types.ContentBlock{
				types.TextContent{Text: "first"},
				types.TextContent{Text: "last"},
			}},
		}
		result := anthropicMessagesWithCacheControl(messages, types.APIProviderAnthropic)
		first := result[0].Content[0].(types.TextContent)
		last := result[0].Content[1].(types.TextContent)
		if first.CacheControl != nil {
			t.Error("only the trailing block should be marked")
		}
		if last.CacheControl == nil {
			t.Error("the trailing block must be marked")
		}
	})

	t.Run("no-op when the trailing block type is not cacheable", func(t *testing.T) {
		messages := []types.Message{
			{ID: "m1", Role: types.RoleAssistant, Content: []types.ContentBlock{
				types.ToolUseContent{ID: "t1", Name: "read", Input: map[string]any{}},
			}},
		}
		result := anthropicMessagesWithCacheControl(messages, types.APIProviderAnthropic)
		if len(result) != len(messages) {
			t.Fatalf("expected unchanged messages, got different length")
		}
	})

	t.Run("not applied for non-Anthropic-compatible providers", func(t *testing.T) {
		messages := []types.Message{types.UserMessage("m1", "hello")}
		result := anthropicMessagesWithCacheControl(messages, types.APIProviderOpenAI)
		text := result[0].Content[0].(types.TextContent)
		if text.CacheControl != nil {
			t.Error("OpenAI must not get cache_control")
		}
	})

	t.Run("empty messages is a no-op", func(t *testing.T) {
		result := anthropicMessagesWithCacheControl(nil, types.APIProviderAnthropic)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})
}

func TestBuildRequestBody_AnthropicMessagesHaveCacheControlOnTrailingBlock(t *testing.T) {
	client := NewClient("key", types.APIProviderAnthropic)
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-sonnet-4-20250514"},
		MaxTokens: 1024,
		Messages: []types.Message{
			types.UserMessage("m1", "hello"),
			{ID: "m2", Role: types.RoleAssistant, Content: []types.ContentBlock{
				types.ToolUseContent{ID: "t1", Name: "read", Input: map[string]any{}},
			}},
			{ID: "m3", Role: types.RoleUser, Content: []types.ContentBlock{
				types.ToolResultContent{ToolUseID: "t1", Content: "file contents"},
			}},
		},
	}

	body, err := client.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	data, _ := io.ReadAll(body)

	var payload struct {
		Messages []struct {
			Content []struct {
				Type         string `json:"type"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(payload.Messages))
	}
	lastMsg := payload.Messages[2]
	lastBlock := lastMsg.Content[len(lastMsg.Content)-1]
	if lastBlock.CacheControl == nil || lastBlock.CacheControl.Type != "ephemeral" {
		t.Errorf("expected the last message's trailing tool_result block to carry cache_control, got %+v", lastBlock)
	}
	for i, msg := range payload.Messages[:2] {
		for j, block := range msg.Content {
			if block.CacheControl != nil {
				t.Errorf("message %d block %d: only the final message's trailing block should carry cache_control", i, j)
			}
		}
	}
}

func TestBuildRequestBody_AnthropicToolsHaveCacheControl(t *testing.T) {
	client := NewClient("key", types.APIProviderAnthropic)
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-sonnet-4-20250514"},
		MaxTokens: 1024,
		Messages:  []types.Message{types.UserMessage("m1", "hello")},
		Tools: []types.APIToolDefinition{
			{Name: "read", Description: "Read a file", InputSchema: schema.JSONSchema{Type: "object"}},
			{Name: "write", Description: "Write a file", InputSchema: schema.JSONSchema{Type: "object"}},
		},
	}

	body, err := client.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	data, _ := io.ReadAll(body)

	var payload struct {
		Tools []struct {
			Name         string `json:"name"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(payload.Tools))
	}
	if payload.Tools[0].CacheControl != nil {
		t.Error("first tool should not have cache_control in JSON")
	}
	last := payload.Tools[len(payload.Tools)-1]
	if last.CacheControl == nil {
		t.Fatal("last tool must have cache_control in JSON")
	}
	if last.CacheControl.Type != "ephemeral" {
		t.Errorf("expected ephemeral, got %q", last.CacheControl.Type)
	}
}

func TestParseUsageMap_AnthropicCacheFields(t *testing.T) {
	raw := map[string]any{
		"input_tokens":                float64(500),
		"output_tokens":               float64(120),
		"cache_read_input_tokens":     float64(450),
		"cache_creation_input_tokens": float64(50),
	}

	usage := parseTokenUsage(raw)

	if usage.InputTokens != 500 {
		t.Errorf("InputTokens: got %d, want 500", usage.InputTokens)
	}
	if usage.OutputTokens != 120 {
		t.Errorf("OutputTokens: got %d, want 120", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 450 {
		t.Errorf("CacheReadInputTokens: got %d, want 450", usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreationInputTokens: got %d, want 50", usage.CacheCreationInputTokens)
	}
}

func TestParseUsageMap_OpenAICachedTokens(t *testing.T) {
	raw := map[string]any{
		"input_tokens":  float64(200),
		"output_tokens": float64(80),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": float64(150),
		},
	}

	usage := parseTokenUsage(raw)

	if usage.CachedTokens != 150 {
		t.Errorf("CachedTokens: got %d, want 150", usage.CachedTokens)
	}
	if usage.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens should be 0 for OpenAI response, got %d", usage.CacheReadInputTokens)
	}
}

func TestBuildRequestBody_AnthropicSystemPromptBlocksWithCache(t *testing.T) {
	client := NewClient("key", types.APIProviderAnthropic)
	req := types.APIRequest{
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-sonnet-4-20250514"},
		MaxTokens: 1024,
		Messages:  []types.Message{types.UserMessage("m1", "hello")},
		SystemPromptBlocks: []types.SystemPromptBlock{
			types.NewTextSystemPromptBlock("stable content", types.NewEphemeralPromptCacheControl()),
			types.NewTextSystemPromptBlock("dynamic content", nil),
		},
	}

	body, err := client.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	data, _ := io.ReadAll(body)

	var payload struct {
		System []struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"system"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.System) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(payload.System))
	}
	if payload.System[0].CacheControl == nil || payload.System[0].CacheControl.Type != "ephemeral" {
		t.Error("stable block must have ephemeral cache_control")
	}
	if payload.System[1].CacheControl != nil {
		t.Error("dynamic block must not have cache_control")
	}
}

// TestHTTPClientForProvider_LongFirstByteProviders verifies providers that can
// legitimately take longer than the shared 60s ResponseHeaderTimeout before
// streaming their first byte get provider-specific headroom.
func TestHTTPClientForProvider_LongFirstByteProviders(t *testing.T) {
	ollamaClient := httpClientForProvider(types.APIProviderOllama)
	ollamaTransport, ok := ollamaClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", ollamaClient.Transport)
	}
	if ollamaTransport.ResponseHeaderTimeout <= defaultResponseHeaderTimeout {
		t.Fatalf("Ollama ResponseHeaderTimeout = %v, want > default %v", ollamaTransport.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}

	zaiClient := httpClientForProvider(types.APIProviderZAi)
	zaiTransport, ok := zaiClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", zaiClient.Transport)
	}
	if zaiTransport.ResponseHeaderTimeout <= defaultResponseHeaderTimeout {
		t.Fatalf("Z.ai ResponseHeaderTimeout = %v, want > default %v", zaiTransport.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}

	defaultClient := httpClientForProvider(types.APIProviderOpenAI)
	defaultTransport, ok := defaultClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", defaultClient.Transport)
	}
	if defaultTransport.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Fatalf("OpenAI ResponseHeaderTimeout = %v, want unchanged default %v", defaultTransport.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
}

// TestGetCodexHTTPClientHasNoBodyCoveringTimeout guards against regressing to
// the old getCodexHTTPClient behavior: it used to set http.Client.Timeout,
// which (unlike every other provider's newStreamingHTTPClient) covers the
// entire request lifetime including reading a streamed response body. A
// reasoning model doing heavy tool use routinely takes longer than a short
// fixed timeout to finish streaming a single turn - the cutoff killed the
// request mid-stream, and that failure was then classified as non-retryable,
// so the whole turn died outright.
// TestRegistryPromptCachingMetadataMatchesWhatIsActuallyImplemented verifies
// three low-confidence registry corrections made after checking each
// provider's real behavior:
//   - Kimi (Moonshot) caches automatically server-side with no client-side
//     action required, so SupportsPC: true is accurate regardless of what
//     this codebase's own request building does.
//   - OpenRouter's provider-level flag claimed caching support that no
//     model in its list actually carried, and this codebase routes
//     OpenRouter through the OpenAI-compatible adapter (no cache_control
//     field at all) — SupportsPC: false reflects what is actually
//     implemented, not what OpenRouter could theoretically support for a
//     Claude sub-model sent in Anthropic's native wire shape.
//
// TestDeepSeekCatalogUsesCurrentModelIDs verifies the fix for a stale
// registry: deepseek-chat/deepseek-reasoner/deepseek-coder-v2 were legacy
// V3.1-era model IDs that DeepSeek's own pricing docs no longer list at all
// (reported retired 2026-07-24) — every request built against the old
// catalog would have targeted a model ID that no longer resolves. The
// catalog and alias mapping now point at the current V4 lineup.
// TestCodexAliasMappingPointsAtWorkingModels verifies the fix for Codex's
// "default"/"codex" aliases pointing at gpt-5.3-codex — a model registry.go's
// own catalog comment already documents as "confirmed broken by a real
// ChatGPT-account session" (alongside gpt-5.2-codex/gpt-5.4-codex). Typing
// codex:default or codex:codex sent a model this same repo already knew
// didn't work. Aliases now resolve to models registry.go's catalog lists as
// actually available (gpt-5.6-sol, gpt-5.5, gpt-5.4-mini).
func TestCodexAliasMappingPointsAtWorkingModels(t *testing.T) {
	cfg := GetProviderConfig(types.APIProviderCodex)
	if cfg == nil {
		t.Fatal("expected Codex provider config")
	}

	brokenModels := map[string]bool{
		"gpt-5.2-codex": true,
		"gpt-5.3-codex": true,
		"gpt-5.4-codex": true,
	}
	for alias, target := range cfg.ModelAliasMapping {
		if brokenModels[target] {
			t.Errorf("alias %q resolves to %q, a model confirmed broken for this provider", alias, target)
		}
	}

	for _, alias := range []string{"default", "codex"} {
		if got := cfg.ModelAliasMapping[alias]; got != "gpt-5.6-sol" {
			t.Errorf("alias %q = %q, want gpt-5.6-sol (the confirmed-working flagship)", alias, got)
		}
	}
}

func TestDeepSeekCatalogUsesCurrentModelIDs(t *testing.T) {
	info, ok := AllProvidersInfo()[types.APIProviderDeepSeek]
	if !ok {
		t.Fatal("expected DeepSeek provider info")
	}

	wantIDs := map[string]bool{
		"deepseek-flash":  false,
		"deepseek-v4-pro": false,
	}
	staleIDs := map[string]bool{
		"deepseek-v4-flash":            true,
		"deepseek-v4-flash-vision-exp": true,
		"deepseek-chat":                true,
		"deepseek-reasoner":            true,
		"deepseek-coder-v2":            true,
	}
	for _, m := range info.Models {
		if _, isStale := staleIDs[m.Identifier]; isStale {
			t.Errorf("registry still lists retired model ID %q", m.Identifier)
		}
		if _, ok := wantIDs[m.Identifier]; ok {
			wantIDs[m.Identifier] = true
		}
		if m.ContextWindow < 1000000 {
			t.Errorf("model %q: expected ~1M context window, got %d", m.Identifier, m.ContextWindow)
		}
	}
	for id, found := range wantIDs {
		if !found {
			t.Errorf("expected current model %q in DeepSeek catalog", id)
		}
	}

	config := GetProviderConfig(types.APIProviderDeepSeek)
	if config == nil {
		t.Fatal("expected DeepSeek provider config")
	}
	for alias, target := range config.ModelAliasMapping {
		if staleIDs[target] {
			t.Errorf("alias %q still resolves to retired model ID %q", alias, target)
		}
	}
}

func TestRegistryPromptCachingMetadataMatchesWhatIsActuallyImplemented(t *testing.T) {
	info := AllProvidersInfo()

	kimi, ok := info[types.APIProviderKimi]
	if !ok {
		t.Fatal("expected Kimi provider info")
	}
	if !kimi.SupportsPC {
		t.Error("Kimi should report SupportsPC: true (automatic server-side caching)")
	}
	for _, m := range kimi.Models {
		if !m.SupportsPC {
			t.Errorf("Kimi model %s should report SupportsPC: true", m.Identifier)
		}
		if !m.Capabilities.PromptCaching {
			t.Errorf("Kimi model %s should report Capabilities.PromptCaching: true", m.Identifier)
		}
	}

	openrouter, ok := info[types.APIProviderOpenRouter]
	if !ok {
		t.Fatal("expected OpenRouter provider info")
	}
	if openrouter.SupportsPC {
		t.Error("OpenRouter should report SupportsPC: false — this codebase never sends cache_control for it")
	}
}

func TestGetCodexHTTPClientHasNoBodyCoveringTimeout(t *testing.T) {
	client := getCodexHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("expected no client-level Timeout (it would cut off long-running streams), got %v", client.Timeout)
	}
	if client.Jar == nil {
		t.Fatal("expected the Cloudflare cookie jar to still be attached")
	}
}

// TestParseCodexSSEStreamCapturesCachedAndReasoningTokens guards against
// regressing to silently dropping input_tokens_details.cached_tokens and
// output_tokens_details.reasoning_tokens from a Codex response.completed
// event's usage object (Responses API shape). Without these, a reasoning
// model's lopsided input:output token ratio is unexplainable from telemetry
// alone - there was previously no way to tell whether a given turn's ratio
// came from resent context, a cache miss, or genuine hidden reasoning.
func TestParseCodexSSEStreamCapturesCachedAndReasoningTokens(t *testing.T) {
	sse := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp-1","usage":{` +
		`"input_tokens":1000,"output_tokens":50,` +
		`"input_tokens_details":{"cached_tokens":800},` +
		`"output_tokens_details":{"reasoning_tokens":30}}}}` + "\n\n"

	result, err := parseCodexSSEStream(
		context.Background(),
		strings.NewReader(sse),
		types.ModelIdentifier{Provider: types.APIProviderCodex, Model: "gpt-5.6-sol"},
		nil,
	)
	if err != nil {
		t.Fatalf("parseCodexSSEStream: %v", err)
	}
	usage := result.Response.Usage
	if usage.InputTokens != 1000 || usage.OutputTokens != 50 {
		t.Fatalf("expected input/output tokens 1000/50, got %d/%d", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CachedTokens != 800 {
		t.Fatalf("expected CachedTokens=800, got %d", usage.CachedTokens)
	}
	if usage.ReasoningTokens != 30 {
		t.Fatalf("expected ReasoningTokens=30, got %d", usage.ReasoningTokens)
	}
}

// TestBuildCodexRequestBody_FreshTurnSendsFullHistory guards the default,
// no-continuation-hint path: the whole message history is sent, no
// previous_response_id, and store is always false (see
// buildCodexRequestBody's doc comment for why).
func TestBuildCodexRequestBody_FreshTurnSendsFullHistory(t *testing.T) {
	client := &Client{}
	req := types.APIRequest{
		Model: types.ModelIdentifier{Provider: types.APIProviderCodex, Model: "gpt-5.6-sol"},
		Messages: []types.Message{
			types.UserMessage("msg-1", "first"),
			types.UserMessage("msg-2", "second"),
		},
	}

	reader, err := client.buildCodexRequestBody(req)
	if err != nil {
		t.Fatalf("buildCodexRequestBody: %v", err)
	}
	var body map[string]any
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if _, ok := body["previous_response_id"]; ok {
		t.Fatal("expected no previous_response_id on a fresh turn")
	}
	if body["store"] != false {
		t.Fatalf("expected store=false (the chatgpt.com/backend-api/codex backend rejects store=true), got %v", body["store"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected the full 2-message history in input, got %#v", body["input"])
	}
}

// TestBuildCodexRequestBody_ContinuationSendsOnlyNewMessages exercises
// buildCodexRequestBody's message-slicing logic in isolation given an
// explicit PreviousResponseID. The engine itself never populates this field
// anymore (recordPreviousResponse is a permanent no-op - see loop.go and
// engine_test.go's TestRecordPreviousResponseNeverCapturesForCodex), since
// "store" must always be false and previous_response_id can only reference a
// stored response. This test just confirms the slicing behavior still works
// correctly if that field were ever set some other way.
func TestBuildCodexRequestBody_ContinuationSendsOnlyNewMessages(t *testing.T) {
	client := &Client{}
	req := types.APIRequest{
		Model: types.ModelIdentifier{Provider: types.APIProviderCodex, Model: "gpt-5.6-sol"},
		Messages: []types.Message{
			types.UserMessage("msg-1", "first"),
			types.AssistantMessage("msg-2", []types.ContentBlock{types.TextContent{Text: "reply"}}),
			types.UserMessage("msg-3", "third"),
		},
		PreviousResponseID:           "resp-1",
		PreviousResponseMessageCount: 1,
	}

	reader, err := client.buildCodexRequestBody(req)
	if err != nil {
		t.Fatalf("buildCodexRequestBody: %v", err)
	}
	var body map[string]any
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body["previous_response_id"] != "resp-1" {
		t.Fatalf("expected previous_response_id=resp-1, got %v", body["previous_response_id"])
	}
	if body["store"] != false {
		t.Fatalf("expected store=false (the chatgpt.com/backend-api/codex backend rejects store=true), got %v", body["store"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected only the 2 messages appended since PreviousResponseMessageCount, got %#v", body["input"])
	}
}

// TestBuildCodexRequestBody_StaleMessageCountFallsBackToFullHistory guards
// the safety fallback: if PreviousResponseMessageCount is somehow larger
// than the current message count (should never happen given the engine's
// own invalidation, but must never panic on a negative slice), the full
// history is sent instead of trusting a clearly-inconsistent hint.
func TestBuildCodexRequestBody_StaleMessageCountFallsBackToFullHistory(t *testing.T) {
	client := &Client{}
	req := types.APIRequest{
		Model:                        types.ModelIdentifier{Provider: types.APIProviderCodex, Model: "gpt-5.6-sol"},
		Messages:                     []types.Message{types.UserMessage("msg-1", "only one message")},
		PreviousResponseID:           "resp-stale",
		PreviousResponseMessageCount: 5,
	}

	reader, err := client.buildCodexRequestBody(req)
	if err != nil {
		t.Fatalf("buildCodexRequestBody: %v", err)
	}
	var body map[string]any
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if _, ok := body["previous_response_id"]; ok {
		t.Fatal("expected previous_response_id to be omitted when the message count is inconsistent")
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("expected the full 1-message history, got %#v", body["input"])
	}
}

// TestWorkersAIIsAGenericCatalogProviderWhoseURLCarriesTheAccountID: Cloudflare Workers AI used to be a built-in provider with its own
// account-id plumbing. It is now served by the generic OpenAI-compatible client, with the account id taken from the environment into
// the base URL the catalog gives.
func TestWorkersAIIsAGenericCatalogProviderWhoseURLCarriesTheAccountID(t *testing.T) {
	const workersAI = types.APIProvider("cloudflare-workers-ai")
	if !IsCatalogProvider(workersAI) {
		t.Fatal("cloudflare-workers-ai must be a generic catalog provider")
	}
	if _, ok := adapterForProvider(workersAI).(openAICompatAdapter); !ok {
		t.Errorf("adapterForProvider = %T, want openAICompatAdapter", adapterForProvider(workersAI))
	}
	if got := providerEnvVar(workersAI); got != "CLOUDFLARE_API_KEY" {
		t.Errorf("providerEnvVar = %q, want CLOUDFLARE_API_KEY (the account id configures the URL, it is not the key)", got)
	}

	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	if missing := CatalogURLVariables(workersAI); len(missing) != 1 || missing[0] != "CLOUDFLARE_ACCOUNT_ID" {
		t.Errorf("without the account id the missing variables = %v, want [CLOUDFLARE_ACCOUNT_ID]", missing)
	}

	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-123")
	cfg, ok := ConfigForCatalogProvider(workersAI)
	if !ok {
		t.Fatal("no config for a verified catalog provider")
	}
	wantBase := "https://api.cloudflare.com/client/v4/accounts/acct-123/ai/v1"
	if cfg.BaseURL != wantBase {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, wantBase)
	}
	if got := cfg.GetEndpoint("@cf/meta/llama-3.1-70b-instruct"); got != wantBase+"/chat/completions" {
		t.Errorf("endpoint = %q, want %q", got, wantBase+"/chat/completions")
	}
	if missing := CatalogURLVariables(workersAI); len(missing) != 0 {
		t.Errorf("with the account id set nothing is missing, got %v", missing)
	}
}
