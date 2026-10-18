package contract

import (
	"encoding/json"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Definition defines a tool's metadata and schema
type Definition struct {
	// Name is the unique identifier for this tool
	Name string `json:"name"`

	// DisplayName is a human-readable name
	DisplayName string `json:"display_name,omitempty"`

	// Description explains what the tool does
	Description string `json:"description"`

	// ShortDescription is one line saying what the tool is for, shown next to its name in the list of deferred tools of the
	// system prompt, where the tool itself is not loaded. When empty, registry.ShortDescription falls back to a line written
	// for the tool, then to the first sentence of Description.
	ShortDescription string `json:"short_description,omitempty"`

	// Prompt provides detailed guidance on when and how to use the tool
	// This is used by the AI to understand the tool's usage patterns
	Prompt string `json:"prompt,omitempty"`

	// SearchHint provides a hint for tool search functionality
	SearchHint string `json:"search_hint,omitempty"`

	// Category groups related tools together
	Category string `json:"category,omitempty"`

	// InputSchema defines the expected input structure
	InputSchema schema.JSONSchema `json:"input_schema"`

	// IsReadOnly indicates if the tool doesn't modify state
	IsReadOnly bool `json:"is_read_only"`

	// IsDestructive indicates if the tool can cause destructive changes
	IsDestructive bool `json:"is_destructive"`

	// IsConcurrencySafe indicates if the tool can run concurrently with others
	IsConcurrencySafe bool `json:"is_concurrency_safe"`

	// RequiresPermission indicates if the tool requires permission checks
	RequiresPermission bool `json:"requires_permission"`

	// Aliases are alternative names for this tool
	Aliases []string `json:"aliases,omitempty"`

	// MaxResultSize is the maximum number of characters allowed in the tool
	// result content. When exceeded the runtime truncates and records a
	// ContentReplacementState (equivalent to OpenClaude's maxResultSizeChars).
	// A value of 0 means unlimited.
	MaxResultSize int `json:"max_result_size,omitempty"`

	// ShouldDefer indicates this tool must be loaded via ToolSearch before calling.
	ShouldDefer bool `json:"should_defer,omitempty"`

	// AlwaysLoad indicates this tool should always be loaded in the initial prompt.
	AlwaysLoad bool `json:"always_load,omitempty"`

	// IsMCP indicates this is an MCP tool.
	IsMCP bool `json:"is_mcp,omitempty"`

	// Metadata contains additional tool metadata
	Metadata map[string]any `json:"metadata,omitempty"`
}

// CallInput represents the input to a tool call
type CallInput struct {
	// Raw is the raw input string (for backward compatibility)
	Raw string `json:"raw,omitempty"`

	// Parsed is the structured input
	Parsed map[string]any `json:"parsed,omitempty"`

	// ToolUseID identifies which tool use this is for
	ToolUseID string `json:"tool_use_id,omitempty"`

	// SessionID identifies the session
	SessionID types.SessionID `json:"session_id,omitempty"`

	// TurnID identifies the turn
	TurnID types.TurnID `json:"turn_id,omitempty"`

	// ToolContext carries the runtime tool context for advanced execution paths.
	ToolContext *ToolUseContext `json:"-"`
}

// CallResult represents the result of a tool call
type CallResult struct {
	// Data is the result data
	Data any `json:"data"`

	// ContentType indicates the type of result
	ContentType ContentType `json:"content_type"`

	// Content is the formatted content (for text results)
	Content string `json:"content,omitempty"`

	// Error contains any error that occurred
	Error error `json:"error,omitempty"`

	// Metadata contains additional result information
	Metadata *ResultMetadata `json:"metadata,omitempty"`

	// ProgressUpdates contains any progress updates made during execution
	ProgressUpdates []types.ToolProgress `json:"progress_updates,omitempty"`

	// NewMessages contains any follow-up runtime messages emitted by the tool.
	NewMessages []types.Message `json:"new_messages,omitempty"`

	// ContextModifier mutates runtime context after successful execution.
	ContextModifier ContextModifier `json:"-"`
}

// ContentType represents the type of tool result
type ContentType string

const (
	ContentTypeText   ContentType = "text"
	ContentTypeJSON   ContentType = "json"
	ContentTypeBinary ContentType = "binary"
	ContentTypeStream ContentType = "stream"
	ContentTypeMixed  ContentType = "mixed"
)

// ResultMetadata contains metadata about a tool result
type ResultMetadata struct {
	// ExecutionDuration is how long the tool took to run (milliseconds)
	ExecutionDuration int64 `json:"execution_duration_ms"`

	// ContentReplacement indicates if content was replaced due to size
	ContentReplacement *types.ContentReplacementState `json:"content_replacement,omitempty"`

	// Additional metadata
	Additional map[string]any `json:"additional,omitempty"`
}

// Validate validates a tool definition
func (d Definition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}

	if !IsValidToolName(d.Name) {
		return fmt.Errorf("invalid tool name: %s", d.Name)
	}

	for _, alias := range d.Aliases {
		if !IsValidToolName(alias) {
			return fmt.Errorf("invalid tool alias: %s", alias)
		}
	}

	return nil
}

// IsValidToolName validates a tool name
func IsValidToolName(name string) bool {
	if name == "" {
		return false
	}

	// Tool names should be alphanumeric with underscores and hyphens
	for _, char := range name {
		isValid := (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.'
		if !isValid {
			return false
		}
	}

	return true
}

// String returns the string representation of a tool result
func (r CallResult) String() string {
	if r.Content != "" {
		return r.Content
	}
	if r.Error != nil {
		return fmt.Sprintf("Error: %v", r.Error)
	}
	return fmt.Sprintf("%v", r.Data)
}

// IsSuccess returns true if the tool call succeeded
func (r CallResult) IsSuccess() bool {
	return r.Error == nil
}

// IsError returns true if the tool call failed
func (r CallResult) IsError() bool {
	return r.Error != nil
}

// GetContent returns the content as a string
func (r CallResult) GetContent() string {
	return r.Content
}

// GetData returns the data
func (r CallResult) GetData() any {
	return r.Data
}

// NewTextResult creates a new text result
func NewTextResult(content string) CallResult {
	return CallResult{
		ContentType: ContentTypeText,
		Content:     content,
		Data:        content,
	}
}

// NewJSONResult creates a new JSON result. Content defaults to the JSON
// encoding of data - callers that want a human-readable summary instead
// (most do, for the model and for chat display) set .Content themselves
// right after calling this. Tools that don't (e.g. grep, list_directory,
// get_file_metadata as of this writing) used to ship an empty Content,
// which every consumer downstream (tool.progress metadata, persisted
// messages, the done payload's tool_results) treats differently when
// empty - one silently omits it, one falls back to a generic "Tool X
// completed" string, one used to fall back to Go's %v-formatted Data dump.
// Defaulting here means there is always at least valid JSON to fall back
// to, for the model and for any frontend renderer, regardless of which of
// those paths a given tool call happens to be read through.
func NewJSONResult(data any) CallResult {
	content := ""
	if encoded, err := json.Marshal(data); err == nil {
		content = string(encoded)
	}
	return CallResult{
		ContentType: ContentTypeJSON,
		Content:     content,
		Data:        data,
	}
}

// NewErrorResult creates a new error result
func NewErrorResult(err error) CallResult {
	return CallResult{
		Error:   err,
		Content: fmt.Sprintf("Error: %v", err),
	}
}
