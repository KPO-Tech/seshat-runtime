package types

import (
	"encoding/json"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/schema"
	toolschema "github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
)

// APIProvider represents the API provider being used
type APIProvider string

const (
	// Anthropic - Direct API (Claude models)
	APIProviderAnthropic APIProvider = "anthropic"

	// OpenAI - Direct API (GPT models)
	APIProviderOpenAI APIProvider = "openai"

	// Ollama - Local models
	APIProviderOllama APIProvider = "ollama"

	// AWS Bedrock - Claude via AWS
	APIProviderBedrock APIProvider = "bedrock"

	// Google Cloud Vertex AI - Claude via GCP
	APIProviderVertex APIProvider = "vertex"

	// Microsoft Azure AI Foundry - Claude via Azure
	APIProviderFoundry APIProvider = "foundry"

	// Google Gemini - Direct API
	APIProviderGemini APIProvider = "gemini"

	// Z.ai - Chinese AI platform (GLM models)
	APIProviderZAi APIProvider = "z-ai"

	// OpenRouter - Unified API for multiple providers
	APIProviderOpenRouter APIProvider = "openrouter"

	// MiniMax - Chinese AI platform
	APIProviderMiniMax APIProvider = "minimax"

	// Cloudflare Workers AI
	APIProviderWorkersAI APIProvider = "workers-ai"

	// Mistral AI - Direct API (OpenAI-compatible)
	APIProviderMistral APIProvider = "mistral"

	// Codex - ChatGPT Pro subscription via chatgpt.com/backend-api (Responses API, OAuth)
	APIProviderCodex APIProvider = "codex"

	// DeepSeek - Direct API (OpenAI-compatible)
	APIProviderDeepSeek APIProvider = "deepseek"

	// OpenCode Zen - Curated model gateway (OpenAI-compatible)
	APIProviderOpenCode APIProvider = "opencode"

	// Kimi (Moonshot AI) - Direct API (OpenAI-compatible)
	APIProviderKimi APIProvider = "kimi"
)

// ModelIdentifier uniquely identifies a model
type ModelIdentifier struct {
	// Provider is the API provider
	Provider APIProvider `json:"provider"`

	// Model is the model name (e.g., "claude-3-5-sonnet-20241022")
	Model string `json:"model"`

	// Version is the optional version
	Version string `json:"version,omitempty"`
}

// String returns the string representation
func (m ModelIdentifier) String() string {
	if m.Version != "" {
		return string(m.Provider) + ":" + m.Model + "@" + m.Version
	}
	return string(m.Provider) + ":" + m.Model
}

// ProviderModelName returns the provider-facing model identifier.
// For most providers, this just returns the model as-is.
// For providers with aliases (like Z.ai), this should resolve via provider config.
func (m ModelIdentifier) ProviderModelName() string {
	if m.Version != "" {
		return m.Model + "@" + m.Version
	}
	// TODO: wire up provider config for model resolution
	// For now, just return as-is - caller should resolve via Config.ResolveModel
	return m.Model
}

// PromptCacheControl represents prompt caching metadata for a prompt block.
type PromptCacheControl struct {
	Type  string `json:"type"`
	Scope string `json:"scope,omitempty"`
	TTL   string `json:"ttl,omitempty"`
}

// SystemPromptBlock is a provider-facing prompt block.
type SystemPromptBlock struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *PromptCacheControl `json:"cache_control,omitempty"`
}

// APIToolDefinition is the stable provider-facing tool schema used by Seshat.
type APIToolDefinition struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	InputSchema toolschema.JSONSchema `json:"input_schema,omitempty"`
	// CacheControl, when set, instructs Anthropic to cache all content up to
	// and including this tool definition. Set on the last tool in the list.
	CacheControl *PromptCacheControl `json:"cache_control,omitempty"`
}

// NewTextSystemPromptBlock creates a canonical text block.
func NewTextSystemPromptBlock(text string, cacheControl *PromptCacheControl) SystemPromptBlock {
	return SystemPromptBlock{
		Type:         "text",
		Text:         text,
		CacheControl: cacheControl,
	}
}

// NewEphemeralPromptCacheControl creates the minimal cache-control payload used
// by Anthropic-compatible prompt caching.
func NewEphemeralPromptCacheControl() *PromptCacheControl {
	return &PromptCacheControl{Type: "ephemeral"}
}

// NewPromptCacheControl creates a cache-control payload with optional TTL.
// The TTL "1h" provides longer cache retention (5 min default without TTL).
// Use ShouldUse1hCacheTTL() to determine if the user is eligible for 1h TTL.
func NewPromptCacheControl(ttl string) *PromptCacheControl {
	control := &PromptCacheControl{Type: "ephemeral"}
	if ttl == "1h" {
		control.TTL = "1h"
	}
	return control
}

// CacheControlWithTTL creates a cache-control with 1h TTL (longer cache).
// This is typically used for stable content that won't change across turns.
func CacheControlWithTTL() *PromptCacheControl {
	return &PromptCacheControl{Type: "ephemeral", TTL: "1h"}
}

// CacheControlEphemeral creates a cache-control with default 5min TTL.
// This is used for content that changes frequently.
func CacheControlEphemeral() *PromptCacheControl {
	return &PromptCacheControl{Type: "ephemeral"}
}

// FlattenSystemPromptBlocks joins prompt blocks in order for providers that only
// support a single flattened system prompt string.
func FlattenSystemPromptBlocks(blocks []SystemPromptBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text == "" {
			continue
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n\n")
}

// APIRequest represents a request to the API
type APIRequest struct {
	// Model is the model to use
	Model ModelIdentifier `json:"model"`

	// Messages are the messages to send
	Messages []Message `json:"messages"`

	// SystemPrompt is the legacy flattened system prompt.
	// When SystemPromptBlocks is provided, request builders should prefer the
	// structured representation and use this field only as a fallback.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// SystemPromptBlocks carries the canonical prompt split.
	SystemPromptBlocks []SystemPromptBlock `json:"system_prompt_blocks,omitempty"`

	// Tools is the stable provider-facing tool surface.
	Tools []APIToolDefinition `json:"tools,omitempty"`

	// MaxTokens is the maximum tokens to generate
	MaxTokens int `json:"max_tokens"`

	// Temperature controls randomness (0-1)
	Temperature *float64 `json:"temperature,omitempty"`

	// TopK controls diversity
	TopK *int `json:"top_k,omitempty"`

	// TopP controls nucleus sampling
	TopP *float64 `json:"top_p,omitempty"`

	// StopSequences are sequences that will stop generation
	StopSequences []string `json:"stop_sequences,omitempty"`

	// Stream enables streaming responses
	Stream bool `json:"stream,omitempty"`

	// Metadata contains additional request metadata
	Metadata map[string]any `json:"metadata,omitempty"`

	// DisableThinking asks the provider not to let the model reason before answering, for work that does not need it
	// (a summary): reasoning spends the output budget and the time. It is honoured where the provider has a switch for
	// it (z.ai) and ignored elsewhere, where the model's own default applies.
	DisableThinking bool `json:"-"`

	// OutputSchema, when set, constrains the model to return JSON matching the
	// given schema. Providers with native support (OpenAI, Gemini) use their
	// structured-output API. Other providers receive a system message injection.
	OutputSchema *schema.StructuredOutputInfo `json:"output_schema,omitempty"`

	// PreviousResponseID and PreviousResponseMessageCount are an optional
	// hint for a provider that supports server-side conversation
	// continuation (currently only Codex's Responses API
	// previous_response_id): if set, Messages[PreviousResponseMessageCount:]
	// are the only messages new since that stored response, so a supporting
	// provider MAY send just those plus the reference instead of the full
	// Messages list. A provider that doesn't support this simply ignores
	// both fields and sends the full Messages list as before - this is a
	// pure optimization hint, never a correctness requirement.
	PreviousResponseID           string `json:"-"`
	PreviousResponseMessageCount int    `json:"-"`
}

// APIChunkType represents the type of chunk in a streaming response
type APIChunkType string

const (
	APIChunkTypeContentBlockStart = "content_block_start"
	APIChunkTypeContentBlockDelta = "content_block_delta"
	APIChunkTypeContentBlockStop  = "content_block_stop"
	APIChunkTypeMessageDelta      = "message_delta"
	APIChunkTypeMessageStop       = "message_stop"
	APIChunkTypeError             = "error"
)

// APIResponseChunk represents a chunk of a streaming API response
type APIResponseChunk struct {
	// Type is the type of chunk
	Type APIChunkType `json:"type"`

	// Delta is the text delta (for text chunks)
	Delta string `json:"delta,omitempty"`

	// DeltaType identifies the provider delta payload variant.
	DeltaType string `json:"delta_type,omitempty"`

	// PartialJSON carries an incremental tool_use input payload.
	PartialJSON string `json:"partial_json,omitempty"`

	// ContentBlock is a complete content block
	ContentBlock ContentBlock `json:"content_block,omitempty"`

	// StopReason is why generation stopped
	StopReason *string `json:"stop_reason,omitempty"`

	// StopSequence is the stop sequence emitted by the provider, if any.
	StopSequence *string `json:"stop_sequence,omitempty"`

	// Usage is token usage information
	Usage *TokenUsage `json:"usage,omitempty"`

	// Error contains any error
	Error *EngineError `json:"error,omitempty"`
}

// APIStreamResult is the canonical aggregated result reconstructed from streaming chunks.
type APIStreamResult struct {
	Response APIResponse        `json:"response"`
	Chunks   []APIResponseChunk `json:"chunks,omitempty"`
}

// APIChunkAccumulator rebuilds a complete response from streaming chunks.
type APIChunkAccumulator struct {
	content      []ContentBlock
	currentBlock ContentBlock
	usage        TokenUsage
	stopReason   string
	stopSequence *string
	toolUseInput string
}

// AddChunk folds a chunk into the aggregate response state.
func (a *APIChunkAccumulator) AddChunk(chunk APIResponseChunk) {
	switch chunk.Type {
	case APIChunkTypeContentBlockStart:
		a.currentBlock = cloneContentBlock(chunk.ContentBlock)
		a.toolUseInput = ""
	case APIChunkTypeContentBlockDelta:
		a.applyDelta(chunk)
	case APIChunkTypeContentBlockStop:
		a.finalizeCurrentBlock()
	case APIChunkTypeMessageDelta:
		if chunk.StopReason != nil {
			a.stopReason = *chunk.StopReason
		}
		if chunk.StopSequence != nil {
			a.stopSequence = chunk.StopSequence
		}
	case APIChunkTypeMessageStop:
		a.finalizeCurrentBlock()
		if chunk.StopReason != nil {
			a.stopReason = *chunk.StopReason
		}
		if chunk.StopSequence != nil {
			a.stopSequence = chunk.StopSequence
		}
	}
	if chunk.Usage != nil {
		a.usage = *chunk.Usage
	}
}

func (a *APIChunkAccumulator) applyDelta(chunk APIResponseChunk) {
	switch block := a.currentBlock.(type) {
	case TextContent:
		if chunk.DeltaType == "" || chunk.DeltaType == "text_delta" {
			block.Text += chunk.Delta
			a.currentBlock = block
		}
	case ThinkingContent:
		if chunk.DeltaType == "thinking_delta" {
			block.Thinking += chunk.Delta
			a.currentBlock = block
		}
	case ToolUseContent:
		if chunk.DeltaType == "input_json_delta" {
			a.toolUseInput += chunk.PartialJSON
			if input, ok := parseToolUseInputJSON(a.toolUseInput); ok {
				block.Input = input
				a.currentBlock = block
			}
		}
	}
}

func (a *APIChunkAccumulator) finalizeCurrentBlock() {
	if a.currentBlock == nil {
		a.toolUseInput = ""
		return
	}
	if toolUse, ok := a.currentBlock.(ToolUseContent); ok && a.toolUseInput != "" {
		if input, parsed := parseToolUseInputJSON(a.toolUseInput); parsed {
			toolUse.Input = input
			a.currentBlock = toolUse
		}
	}
	a.content = append(a.content, cloneContentBlock(a.currentBlock))
	a.currentBlock = nil
	a.toolUseInput = ""
}

// APIResponse represents a complete API response
type APIResponse struct {
	// Role is always "assistant" for responses
	Role Role `json:"role"`

	// Content is the response content
	Content []ContentBlock `json:"content"`

	// StopReason is why generation stopped
	StopReason string `json:"stop_reason"`

	// StopSequence is the sequence that caused the stop (if applicable)
	StopSequence *string `json:"stop_sequence,omitempty"`

	// Usage is token usage information
	Usage TokenUsage `json:"usage"`

	// Model is the model that was used
	Model ModelIdentifier `json:"model"`

	// ID is the response ID
	ID string `json:"id"`
}

// Build returns the aggregated response.
func (a *APIChunkAccumulator) Build(model ModelIdentifier, responseID string) APIResponse {
	content := append([]ContentBlock(nil), a.content...)
	if a.currentBlock != nil {
		if toolUse, ok := a.currentBlock.(ToolUseContent); ok && a.toolUseInput != "" {
			if input, parsed := parseToolUseInputJSON(a.toolUseInput); parsed {
				toolUse.Input = input
				a.currentBlock = toolUse
			}
		}
		content = append(content, cloneContentBlock(a.currentBlock))
	}
	return APIResponse{
		Role:         RoleAssistant,
		Content:      content,
		StopReason:   a.stopReason,
		StopSequence: a.stopSequence,
		Usage:        a.usage,
		Model:        model,
		ID:           responseID,
	}
}

func parseToolUseInputJSON(raw string) (map[string]any, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, false
	}
	return input, true
}

func cloneContentBlock(block ContentBlock) ContentBlock {
	switch v := block.(type) {
	case TextContent:
		return v
	case ImageContent:
		return v
	case ToolUseContent:
		clonedInput := make(map[string]any, len(v.Input))
		for k, value := range v.Input {
			clonedInput[k] = value
		}
		cloned := ToolUseContent{ID: v.ID, Name: v.Name, Input: clonedInput}
		if v.Metadata != nil {
			metadata := make(map[string]any, len(*v.Metadata))
			for k, value := range *v.Metadata {
				metadata[k] = value
			}
			cloned.Metadata = &metadata
		}
		return cloned
	case ToolResultContent:
		cloned := ToolResultContent{ToolUseID: v.ToolUseID, Content: v.Content, IsError: v.IsError, CacheControl: v.CacheControl}
		if v.Metadata != nil {
			metadata := make(map[string]any, len(*v.Metadata))
			for k, value := range *v.Metadata {
				metadata[k] = value
			}
			cloned.Metadata = &metadata
		}
		return cloned
	case ThinkingContent:
		return v
	default:
		return block
	}
}

// NormalizeStopReason returns the canonical runtime stop reason.
func NormalizeStopReason(reason string, content []ContentBlock) string {
	if ContentBlocksContainToolUse(content) {
		return StopReasonToolUse
	}
	if reason != "" {
		return reason
	}
	return StopReasonEndTurn
}

// IsStreamingRetryable returns true if a chunk error should be handled like a retryable API error.
func IsStreamingRetryable(err *EngineError) bool {
	return err != nil && err.IsRetryable()
}

// ContentBlocksContainToolUse returns true when a response content array includes a tool_use block.
func ContentBlocksContainToolUse(content []ContentBlock) bool {
	for _, block := range content {
		if _, ok := block.(ToolUseContent); ok {
			return true
		}
	}
	return false
}

func (r APIResponse) MarshalJSON() ([]byte, error) {
	type apiResponseAlias struct {
		Role         Role              `json:"role"`
		Content      []json.RawMessage `json:"content"`
		StopReason   string            `json:"stop_reason"`
		StopSequence *string           `json:"stop_sequence,omitempty"`
		Usage        TokenUsage        `json:"usage"`
		Model        ModelIdentifier   `json:"model"`
		ID           string            `json:"id"`
	}

	content, err := marshalContentBlocks(r.Content)
	if err != nil {
		return nil, err
	}

	return json.Marshal(apiResponseAlias{
		Role:         r.Role,
		Content:      content,
		StopReason:   r.StopReason,
		StopSequence: r.StopSequence,
		Usage:        r.Usage,
		Model:        r.Model,
		ID:           r.ID,
	})
}

func (r *APIResponse) UnmarshalJSON(data []byte) error {
	type apiResponseAlias struct {
		Role         Role              `json:"role"`
		Content      []json.RawMessage `json:"content"`
		StopReason   string            `json:"stop_reason"`
		StopSequence *string           `json:"stop_sequence,omitempty"`
		Usage        TokenUsage        `json:"usage"`
		Model        ModelIdentifier   `json:"model"`
		ID           string            `json:"id"`
	}

	var aux apiResponseAlias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	content, err := unmarshalContentBlocks(aux.Content)
	if err != nil {
		return err
	}

	r.Role = aux.Role
	r.Content = content
	r.StopReason = aux.StopReason
	r.StopSequence = aux.StopSequence
	r.Usage = aux.Usage
	r.Model = aux.Model
	r.ID = aux.ID
	return nil
}

// APIStream is a stream of API response chunks
type APIStream <-chan APIResponseChunk

// RetryConfig configures retry behavior for API requests
type RetryConfig struct {
	// MaxAttempts is the maximum number of attempts
	MaxAttempts int `json:"max_attempts"`

	// InitialBackoff is the initial backoff duration in milliseconds
	InitialBackoff int64 `json:"initial_backoff_ms"`

	// MaxBackoff is the maximum backoff duration in milliseconds
	MaxBackoff int64 `json:"max_backoff_ms"`

	// BackoffMultiplier multiplies the backoff after each attempt
	BackoffMultiplier float64 `json:"backoff_multiplier"`

	// RetryableErrors is a set of error codes that are retryable
	RetryableErrors map[ErrorCode]bool `json:"retryable_errors,omitempty"`
}

// DefaultRetryConfig returns a default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    1000,
		MaxBackoff:        30000,
		BackoffMultiplier: 2.0,
		RetryableErrors: map[ErrorCode]bool{
			ErrCodeAPIRateLimit: true,
			ErrCodeAPITimeout:   true,
		},
	}
}

// ContextWindow represents the context window for a model
type ContextWindow struct {
	// MaxTokens is the maximum tokens for the model
	MaxTokens int `json:"max_tokens"`

	// MaxOutputTokens is the maximum tokens that can be generated
	MaxOutputTokens int `json:"max_output_tokens"`
}

// GetContextWindow returns the context window for a model.
// It consults the centralised model.Global registry first; if the model is
// not found there it falls back to a conservative default (128k/4k).
func GetContextWindow(m ModelIdentifier) ContextWindow {
	cw := model.Global.ContextWindowFor(string(m.Provider), m.Model)
	return ContextWindow{MaxTokens: cw.MaxTokens, MaxOutputTokens: cw.MaxOutputTokens}
}
