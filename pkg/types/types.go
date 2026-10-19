package types

import (
	"context"

	internaltypes "github.com/KPO-Tech/seshat-runtime/internal/types"
)

type (
	// APIProvider represents the API provider being used
	APIProvider = internaltypes.APIProvider
	// APIRequest represents a request to the API
	APIRequest = internaltypes.APIRequest
	// APIResponse represents a complete API response
	APIResponse = internaltypes.APIResponse
	// APIChunkType represents the type of chunk in a streaming response
	APIChunkType = internaltypes.APIChunkType
	// APIResponseChunk represents a chunk of a streaming API response
	APIResponseChunk = internaltypes.APIResponseChunk
	// ContentBlock represents a single block of content in a message
	ContentBlock = internaltypes.ContentBlock
	// ExecutionOrigin indicates whether a run is initiated by an interactive user flow
	// or a future background automation.
	ExecutionOrigin = internaltypes.ExecutionOrigin
	// ImageContent represents an image content block
	ImageContent = internaltypes.ImageContent
	// Message represents a single message in the conversation
	Message = internaltypes.Message
	// ModelIdentifier uniquely identifies a model
	ModelIdentifier = internaltypes.ModelIdentifier
	// PermissionMode represents the mode of APPROVAL checking.
	// This determines WHO approves actions (user, classifier, etc.)
	PermissionMode = internaltypes.PermissionMode
	// PromptFn is a function that can prompt the user
	PromptFn = internaltypes.PromptFn
	// PromptOption represents an option in a choice prompt
	PromptOption = internaltypes.PromptOption
	// PromptRequest represents a request to prompt the user
	PromptRequest = internaltypes.PromptRequest
	// PromptResponse represents a response from the user
	PromptResponse = internaltypes.PromptResponse
	// PromptType represents the type of prompt
	PromptType = internaltypes.PromptType
	// Role represents the role of a message sender
	Role = internaltypes.Role
	// RuntimeEvent is the structured event envelope emitted by the runtime.
	RuntimeEvent = internaltypes.RuntimeEvent
	// RuntimeEventType identifies a structured runtime event emitted during a turn.
	RuntimeEventType = internaltypes.RuntimeEventType
	// SessionID uniquely identifies a session
	SessionID = internaltypes.SessionID
	// TextContent represents a text content block
	TextContent = internaltypes.TextContent
	// TokenUsage represents token usage information
	TokenUsage = internaltypes.TokenUsage
	// ContextWindow represents the context window for a model
	ContextWindow = internaltypes.ContextWindow
	// ToolPermissionRequest represents a structured permission check request.
	ToolPermissionRequest = internaltypes.ToolPermissionRequest
	// ToolResultContent represents a tool result content block
	ToolResultContent = internaltypes.ToolResultContent
	// ToolUseContent represents a tool use content block
	ToolUseContent = internaltypes.ToolUseContent
	// TurnID uniquely identifies a turn within a session
	TurnID = internaltypes.TurnID
)

const (
	APIChunkTypeContentBlockStart = internaltypes.APIChunkTypeContentBlockStart
	APIChunkTypeContentBlockDelta = internaltypes.APIChunkTypeContentBlockDelta
	APIChunkTypeContentBlockStop  = internaltypes.APIChunkTypeContentBlockStop
	APIChunkTypeMessageDelta      = internaltypes.APIChunkTypeMessageDelta
	APIChunkTypeMessageStop       = internaltypes.APIChunkTypeMessageStop
	APIChunkTypeError             = internaltypes.APIChunkTypeError

	APIProviderAnthropic  = internaltypes.APIProviderAnthropic
	APIProviderCodex      = internaltypes.APIProviderCodex
	APIProviderDeepSeek   = internaltypes.APIProviderDeepSeek
	APIProviderGemini     = internaltypes.APIProviderGemini
	APIProviderKimi       = internaltypes.APIProviderKimi
	APIProviderMiniMax    = internaltypes.APIProviderMiniMax
	APIProviderMistral    = internaltypes.APIProviderMistral
	APIProviderOllama     = internaltypes.APIProviderOllama
	APIProviderOpenAI     = internaltypes.APIProviderOpenAI
	APIProviderOpenCode   = internaltypes.APIProviderOpenCode
	APIProviderOpenRouter = internaltypes.APIProviderOpenRouter
	APIProviderZAi        = internaltypes.APIProviderZAi

	ExecutionOriginInteractive = internaltypes.ExecutionOriginInteractive
	ExecutionOriginAutomation  = internaltypes.ExecutionOriginAutomation
	ExecutionOriginSkillAgent  = internaltypes.ExecutionOriginSkillAgent

	PermissionModeOnRequest   = internaltypes.PermissionModeOnRequest
	PermissionModeAuto        = internaltypes.PermissionModeAuto
	PermissionModeAcceptEdits = internaltypes.PermissionModeAcceptEdits
	PermissionModeBypass      = internaltypes.PermissionModeBypass
	PermissionModeNever       = internaltypes.PermissionModeNever
	PermissionModeGranular    = internaltypes.PermissionModeGranular

	PromptTypeConfirm = internaltypes.PromptTypeConfirm

	RoleAssistant = internaltypes.RoleAssistant
	RoleUser      = internaltypes.RoleUser
	RoleSystem    = internaltypes.RoleSystem

	RuntimeEventTypePromptRequired         = internaltypes.RuntimeEventTypePromptRequired
	RuntimeEventTypeToolPermissionRequired = internaltypes.RuntimeEventTypeToolPermissionRequired

	StopReasonEndTurn = internaltypes.StopReasonEndTurn
)

var RuntimeEventEmitterKey = internaltypes.RuntimeEventEmitterKey

// NormalizePermissionMode returns the permission mode that raw names, and false when raw is not a known mode.
func NormalizePermissionMode(raw string) (PermissionMode, bool) {
	return internaltypes.NormalizePermissionMode(raw)
}

// NormalizePermissionModeOrDefault returns mode when it is a known mode, otherwise fallback when that is known, otherwise the on-request mode.
func NormalizePermissionModeOrDefault(mode PermissionMode, fallback PermissionMode) PermissionMode {
	return internaltypes.NormalizePermissionModeOrDefault(mode, fallback)
}

// NormalizeExecutionOrigin returns the execution origin that raw names (automation or skill agent), and the interactive origin for anything else.
func NormalizeExecutionOrigin(raw string) ExecutionOrigin {
	return internaltypes.NormalizeExecutionOrigin(raw)
}

// WithAgentUserID returns a context carrying the authenticated user's ID.
func WithAgentUserID(ctx context.Context, userID string) context.Context {
	return internaltypes.WithAgentUserID(ctx, userID)
}

// AgentUserIDFromContext returns the user ID from ctx, or empty string if absent.
func AgentUserIDFromContext(ctx context.Context) string {
	return internaltypes.AgentUserIDFromContext(ctx)
}

// WithSubAgentMaxDepth returns a context carrying the user-configured sub-agent
// depth limit. The agent tool reads this to override the server-wide default.
// Pass 0 to clear any override and fall back to the server default.
func WithSubAgentMaxDepth(ctx context.Context, depth int) context.Context {
	return internaltypes.WithSubAgentMaxDepth(ctx, depth)
}

// SubAgentMaxDepthFromContext returns the configured depth limit from ctx,
// or 0 if none was set (caller should then use the server default constant).
func SubAgentMaxDepthFromContext(ctx context.Context) int {
	return internaltypes.SubAgentMaxDepthFromContext(ctx)
}

// WithPromptFn returns a context carrying this turn's interactive-prompt
// bridge (consumed by ask_user_question and the permission integrator).
// Prefer this over Client.SetPromptFn / Session.SetPromptFn when the same
// *sdk.Client may be reused across concurrent turns (e.g. a per-user/provider
// client cache) - those setters mutate shared state on the client/tools and
// would otherwise race between turns sharing that client.
func WithPromptFn(ctx context.Context, fn PromptFn) context.Context {
	return internaltypes.WithPromptFn(ctx, fn)
}

// PromptFnFromContext returns the current turn's prompt bridge, or nil if none was set.
func PromptFnFromContext(ctx context.Context) PromptFn {
	return internaltypes.PromptFnFromContext(ctx)
}

// WithWebSearchRunner returns a context carrying this turn's web_search
// execution runner (fn should be an sdk.WebSearchRunnerFn). Prefer this over
// Client.SetWebSearchRunner for the same reason as WithPromptFn above.
func WithWebSearchRunner(ctx context.Context, fn any) context.Context {
	return internaltypes.WithWebSearchRunner(ctx, fn)
}

// WebSearchRunnerFromContext returns the current turn's web_search runner
// (as `any` - assert to sdk.WebSearchRunnerFn), or nil if none was set.
func WebSearchRunnerFromContext(ctx context.Context) any {
	return internaltypes.WebSearchRunnerFromContext(ctx)
}

// GetContextWindow returns the context window for a model.
// It consults the centralised model.Global registry first; if the model is
// not found there it falls back to a conservative default (128k/4k).
func GetContextWindow(model ModelIdentifier) ContextWindow {
	return internaltypes.GetContextWindow(model)
}

// UserMessage builds a single-turn user Message - the minimal constructor
// needed by callers doing a one-shot providers.Client.CreateMessage call
// (title generation, memory extraction, and similar) rather than a full
// agentic sdk.Session.
func UserMessage(id string, content string) Message {
	return internaltypes.UserMessage(id, content)
}
