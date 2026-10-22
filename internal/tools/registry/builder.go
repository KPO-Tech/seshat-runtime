package registry

import (
	"context"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Builder builds a tool from a definition and handler function
type Builder struct {
	definition        Definition
	handler           HandlerFn
	validateInput     ValidateInputFn
	checkPermissions  CheckPermissionsFn
	isConcurrencySafe ToolPredicateFn
	isReadOnly        ToolPredicateFn
	isEnabled         func() bool
	formatResult      FormatResultFn
	backfillInput     BackfillInputFn
	schemaValidator   *schema.Validator
}

// HandlerFn is a function that handles a tool call
type HandlerFn func(ctx context.Context, input CallInput, toolCtx ToolUseContext) (CallResult, error)

// ValidateInputFn validates and optionally normalizes tool input.
type ValidateInputFn func(ctx context.Context, input map[string]any) (map[string]any, error)

// CheckPermissionsFn performs tool-specific permission checks.
type CheckPermissionsFn func(ctx context.Context, input map[string]any, toolCtx ToolUseContext) types.PermissionResult

// ToolPredicateFn returns a runtime boolean for a given tool input.
type ToolPredicateFn func(input map[string]any) bool

// FormatResultFn serialises tool output into the tool_result content string.
type FormatResultFn func(data any) string

// BackfillInputFn enriches a shallow clone of the parsed input with derived fields.
type BackfillInputFn func(ctx context.Context, input map[string]any) map[string]any

// NewBuilder creates a new tool builder
func NewBuilder(name string) *Builder {
	return &Builder{
		definition: Definition{
			Name:               name,
			IsReadOnly:         false,
			IsDestructive:      false,
			IsConcurrencySafe:  false,
			RequiresPermission: true,
			InputSchema:        schema.JSONSchema{Type: "object"},
			Metadata:           make(map[string]any),
		},
	}
}

// WithDescription sets the tool description
func (b *Builder) WithDescription(description string) *Builder {
	b.definition.Description = description
	return b
}

// WithDisplayName sets the display name
func (b *Builder) WithDisplayName(displayName string) *Builder {
	b.definition.DisplayName = displayName
	return b
}

// WithSearchHint sets the keywords tool_search matches when the tool is deferred.
func (b *Builder) WithSearchHint(hint string) *Builder {
	b.definition.SearchHint = hint
	return b
}

// AsMCP marks the tool as one that comes from an MCP server. Such a tool is deferred: tool_search finds it and it is loaded when
// the model asks for it, instead of every request carrying the definitions of every server.
func (b *Builder) AsMCP() *Builder {
	b.definition.IsMCP = true
	return b
}

// WithAlwaysLoad keeps the tool in the definitions every request carries even when it comes from an MCP server (which are otherwise
// deferred): a small server costs less than the round trip tool_search needs to load it.
func (b *Builder) WithAlwaysLoad(always bool) *Builder {
	b.definition.AlwaysLoad = always
	return b
}

// WithCategory sets the category
func (b *Builder) WithCategory(category string) *Builder {
	b.definition.Category = category
	return b
}

// WithInputSchema sets the input schema.
func (b *Builder) WithInputSchema(schema schema.JSONSchema) *Builder {
	b.definition.InputSchema = schema
	return b
}

// ReadOnly marks the tool as read-only
func (b *Builder) ReadOnly() *Builder {
	b.definition.IsReadOnly = true
	return b
}

// Destructive marks the tool as destructive
func (b *Builder) Destructive() *Builder {
	b.definition.IsDestructive = true
	return b
}

// ConcurrencySafe marks the tool as concurrency-safe
func (b *Builder) ConcurrencySafe() *Builder {
	b.definition.IsConcurrencySafe = true
	return b
}

// NoPermission marks the tool as not requiring permission
func (b *Builder) NoPermission() *Builder {
	b.definition.RequiresPermission = false
	return b
}

// WithAliases sets the tool aliases
func (b *Builder) WithAliases(aliases ...string) *Builder {
	b.definition.Aliases = aliases
	return b
}

// WithMetadata adds metadata to the tool
func (b *Builder) WithMetadata(key string, value any) *Builder {
	if b.definition.Metadata == nil {
		b.definition.Metadata = make(map[string]any)
	}
	b.definition.Metadata[key] = value
	return b
}

// WithHandler sets the handler function
func (b *Builder) WithHandler(handler HandlerFn) *Builder {
	b.handler = handler
	return b
}

// WithInputValidator sets the input validator.
func (b *Builder) WithInputValidator(validator ValidateInputFn) *Builder {
	b.validateInput = validator
	return b
}

// WithPermissionChecker sets the tool-specific permission checker.
func (b *Builder) WithPermissionChecker(checker CheckPermissionsFn) *Builder {
	b.checkPermissions = checker
	return b
}

// WithConcurrencySafetyEvaluator sets the runtime concurrency predicate.
func (b *Builder) WithConcurrencySafetyEvaluator(predicate ToolPredicateFn) *Builder {
	b.isConcurrencySafe = predicate
	return b
}

// WithReadOnlyEvaluator sets the runtime read-only predicate.
func (b *Builder) WithReadOnlyEvaluator(predicate ToolPredicateFn) *Builder {
	b.isReadOnly = predicate
	return b
}

// WithEnabled sets the enabled predicate (default true).
func (b *Builder) WithEnabled(fn func() bool) *Builder {
	b.isEnabled = fn
	return b
}

// WithFormatResult sets the result formatter.
func (b *Builder) WithFormatResult(fn FormatResultFn) *Builder {
	b.formatResult = fn
	return b
}

// WithBackfillInput sets the input backfill function.
func (b *Builder) WithBackfillInput(fn BackfillInputFn) *Builder {
	b.backfillInput = fn
	return b
}

// WithMaxResultSize sets the maximum result size in characters.
func (b *Builder) WithMaxResultSize(max int) *Builder {
	b.definition.MaxResultSize = max
	return b
}

// WithSchemaValidator sets a schema validator for this tool.
func (b *Builder) WithSchemaValidator(validator *schema.Validator) *Builder {
	b.schemaValidator = validator
	return b
}

// WithJSONSchema registers a JSON schema for this tool.
func (b *Builder) WithJSONSchema(toolName string, jsonSchema schema.JSONSchema) *Builder {
	if b.schemaValidator == nil {
		b.schemaValidator = schema.NewValidator()
	}
	b.schemaValidator.RegisterSchema(toolName, jsonSchema)
	return b
}

// Build builds the tool
func (b *Builder) Build() (Tool, error) {
	// Validate definition
	if err := b.definition.Validate(); err != nil {
		return nil, fmt.Errorf("invalid tool definition: %w", err)
	}

	// Validate handler
	if b.handler == nil {
		return nil, fmt.Errorf("tool %s: no handler specified", b.definition.Name)
	}

	return &baseTool{
		definition:        b.definition,
		handler:           b.handler,
		validateInput:     b.validateInput,
		checkPermissions:  b.checkPermissions,
		isConcurrencySafe: b.isConcurrencySafe,
		isReadOnly:        b.isReadOnly,
		isEnabled:         b.isEnabled,
		formatResult:      b.formatResult,
		backfillInput:     b.backfillInput,
		schemaValidator:   b.schemaValidator,
	}, nil
}

// baseTool is a basic implementation of the Tool interface
type baseTool struct {
	definition        Definition
	handler           HandlerFn
	validateInput     ValidateInputFn
	checkPermissions  CheckPermissionsFn
	isConcurrencySafe ToolPredicateFn
	isReadOnly        ToolPredicateFn
	isEnabled         func() bool
	formatResult      FormatResultFn
	backfillInput     BackfillInputFn
	schemaValidator   *schema.Validator
}

// Definition returns the tool's definition
func (t *baseTool) Definition() Definition {
	return t.definition
}

// Call executes the tool
func (t *baseTool) Call(ctx context.Context, input CallInput, permissionCheck types.CanUseToolFn) (CallResult, error) {
	toolCtx := input.ToolContextValue()
	if toolCtx.CanUseTool == nil {
		toolCtx.CanUseTool = permissionCheck
	}

	result, err := t.handler(ctx, input, toolCtx)
	if err != nil {
		return NewErrorResult(err), nil
	}

	return result, nil
}

// Description returns the tool's description
func (t *baseTool) Description(ctx context.Context) (string, error) {
	return t.definition.Description, nil
}

// ValidateInput validates and optionally normalizes tool input.
func (t *baseTool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	// First, run JSON Schema validation if available
	if t.schemaValidator != nil {
		_, err := t.schemaValidator.Validate(t.definition.Name, input)
		if err != nil {
			return nil, err
		}
	}

	// Then, run custom validation if provided
	if t.validateInput != nil {
		return t.validateInput(ctx, input)
	}
	return input, nil
}

// CheckPermissions performs tool-specific permission checks.
func (t *baseTool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx ToolUseContext) types.PermissionResult {
	if t.checkPermissions != nil {
		return t.checkPermissions(ctx, input, toolCtx)
	}
	return types.AllowWithInput("", input)
}

// IsConcurrencySafe returns whether this tool use can run concurrently.
func (t *baseTool) IsConcurrencySafe(input map[string]any) bool {
	if t.isConcurrencySafe != nil {
		return t.isConcurrencySafe(input)
	}
	return t.definition.IsConcurrencySafe
}

// IsReadOnly returns whether this tool use is read-only.
func (t *baseTool) IsReadOnly(input map[string]any) bool {
	if t.isReadOnly != nil {
		return t.isReadOnly(input)
	}
	return t.definition.IsReadOnly
}

// IsEnabled returns whether this tool is currently active.
func (t *baseTool) IsEnabled() bool {
	if t.isEnabled != nil {
		return t.isEnabled()
	}
	return true
}

// FormatResult serialises the tool output into the tool_result content string.
func (t *baseTool) FormatResult(data any) string {
	if t.formatResult != nil {
		return t.formatResult(data)
	}
	// Default: if data is a string use it directly, otherwise use fmt.Sprint.
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput enriches a shallow clone of the parsed input with derived fields.
func (t *baseTool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	if t.backfillInput != nil {
		return t.backfillInput(ctx, input)
	}
	return input
}

// NewSimpleTool creates a simple tool from a handler function
func NewSimpleTool(
	name string,
	description string,
	handler func(ctx context.Context, input string) (string, error),
) (Tool, error) {
	return NewBuilder(name).
		WithDescription(description).
		WithHandler(func(ctx context.Context, input CallInput, toolCtx ToolUseContext) (CallResult, error) {
			result, err := handler(ctx, input.Raw)
			if err != nil {
				return NewErrorResult(err), nil
			}
			return NewTextResult(result), nil
		}).
		Build()
}
