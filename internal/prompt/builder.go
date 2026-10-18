package prompt

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Builder assembles canonical system prompt parts and then composes a
// per-turn provider-facing prompt from those parts.
type Builder struct {
	assembler *Assembler
	config    *BuilderConfig
	cache     *PromptCache // Prompt cache for stable sections
}

// BuilderConfig represents the builder configuration.
type BuilderConfig struct {
	EnableCaching         bool `json:"enable_caching"`
	CacheBoundaryPosition int  `json:"cache_boundary_position"`
}

// DefaultBuilderConfig returns default builder configuration.
func DefaultBuilderConfig() *BuilderConfig {
	return &BuilderConfig{
		EnableCaching:         true,
		CacheBoundaryPosition: 0,
	}
}

// NewBuilder creates a new prompt builder.
func NewBuilder(assembler *Assembler, config *BuilderConfig) *Builder {
	if config == nil {
		config = DefaultBuilderConfig()
	}
	return &Builder{
		assembler: assembler,
		config:    config,
		cache:     NewPromptCache(), // Initialize prompt cache
	}
}

// FetchSystemPromptPartsInput represents input for fetching system prompt parts.
type FetchSystemPromptPartsInput struct {
	Tools                        map[string]tool.Tool  `json:"-"`
	Model                        types.ModelIdentifier `json:"model"`
	WorkingDirectory             string                `json:"working_directory,omitempty"`
	DeferredToolNames            []string              `json:"deferred_tool_names,omitempty"`
	MemoryContext                string                `json:"memory_context,omitempty"`
	AdditionalWorkingDirectories []string              `json:"additional_working_directories,omitempty"`
	MCPClients                   []string              `json:"mcp_clients,omitempty"`
	CustomSystemPrompt           *string               `json:"custom_system_prompt,omitempty"`
	AppendSystemPrompt           *string               `json:"append_system_prompt,omitempty"`

	// Stage sets the execution stage overlay injected into the dynamic section.
	Stage ExecutionStage `json:"stage,omitempty"`

	// StageOverrides replaces the built-in stage overlay text per stage.
	StageOverrides map[ExecutionStage]string `json:"-"`

	// ToolHints provides per-tool extra guidance appended to provider-facing
	// tool descriptions. Key is the canonical tool name.
	ToolHints map[string]string `json:"-"`

	// ProjectInstructions is the content of a project-level instructions file
	// (e.g. SESHAT.md, AGENTS.md). Injected into the system prompt before the
	// memory context section so it takes precedence over accumulated patterns.
	ProjectInstructions string `json:"project_instructions,omitempty"`
}

// SystemPromptParts captures the stable prefix and the dynamic context maps
// needed to rebuild the final prompt for a given turn.
type SystemPromptParts struct {
	DefaultSystemPrompt []string          `json:"default_system_prompt"`
	UserContext         map[string]string `json:"user_context"`
	SystemContext       map[string]string `json:"system_context"`
	StableText          string            `json:"stable_text,omitempty"`
	DynamicText         string            `json:"dynamic_text,omitempty"`
	CacheBreakpoint     int               `json:"cache_breakpoint,omitempty"`

	// Stage and StageOverrides are propagated to BuildCanonicalPrompt so the
	// dynamic section remains stage-aware across the two-phase build path.
	Stage          ExecutionStage            `json:"stage,omitempty"`
	StageOverrides map[ExecutionStage]string `json:"-"`

	// ToolHints provides per-tool extra guidance used when building provider
	// tool definitions for this turn.
	ToolHints map[string]string `json:"-"`
}

// CacheSafePrompt is the canonical provider-facing prompt representation.
type CacheSafePrompt struct {
	SystemPrompt       string                    `json:"system_prompt"`
	StableText         string                    `json:"stable_text"`
	DynamicText        string                    `json:"dynamic_text"`
	CacheBreakpoint    int                       `json:"cache_breakpoint"`
	UserContext        map[string]string         `json:"user_context,omitempty"`
	SystemContext      map[string]string         `json:"system_context,omitempty"`
	SystemPromptBlocks []types.SystemPromptBlock `json:"system_prompt_blocks,omitempty"`
}

// The stable prompt sections and SeshatCoreStablePrompt() are defined in agents.go;
// the text itself lives in prompts/*.md.

var dynamicBoundarySection = Section{
	Type:            SectionTypeDefault,
	Name:            "dynamic_boundary",
	Content:         SystemPromptDynamicBoundary,
	Priority:        850,
	Cacheable:       true,
	DynamicBoundary: true,
	Enabled:         true,
}

var runtimeContextSection = Section{
	Type:      SectionTypeDynamic,
	Name:      "runtime_context",
	Content:   "# Runtime context\n\nsession_id: {{session_id}}\nturn_number: {{turn_number}}\nworking_directory: {{working_directory}}\nadditional_working_directories: {{additional_working_directories}}\nmodel: {{model}}\nprovider: {{provider}}\navailable_tools: {{available_tools}}\ntool_count: {{tool_count}}\nmcp_client_count: {{mcp_client_count}}\nprompt_cache_boundary: {{prompt_cache_boundary}}\nstable_tool_names: {{stable_tool_names}}\n\n<available-deferred-tools>\n{{available_deferred_tools}}\n</available-deferred-tools>\n\n{{git_context_block}}",
	Priority:  800,
	Cacheable: false,
	Enabled:   true,
}

var runtimeGuidanceSection = Section{
	Type:      SectionTypeDynamic,
	Name:      "runtime_guidance",
	Content:   "# Runtime guidance\n\n- The stable system prompt prefix ends before {{prompt_cache_boundary}} and must remain deterministic across turns.\n- Treat the listed tool surface as canonical for this turn.\n- If a needed capability is not listed, say it is unavailable rather than guessing a tool name. Use `tool_search` for deferred tools only when it is in the tool list.\n- Use runtime context fields as live session data, not durable policy.",
	Priority:  780,
	Cacheable: false,
	Enabled:   true,
}

// projectInstructionsSection renders project-level instructions read from a
// SESHAT.md (or equivalent) file in the working directory. Content is injected
// via {{project_instructions_block}} so it stays out of the stable prefix.
var projectInstructionsSection = Section{
	Type:      SectionTypeDynamic,
	Name:      "project_instructions",
	Content:   "{{project_instructions_block}}",
	Priority:  775,
	Cacheable: false,
	Enabled:   true,
}

var runtimeMemorySection = Section{
	Type:      SectionTypeDynamic,
	Name:      "runtime_memory",
	Content:   "{{memory_context_block}}",
	Priority:  760,
	Cacheable: false,
	Enabled:   true,
}

func canonicalPromptSections() []Section {
	sections := append([]Section(nil), stableSystemPromptSections...)
	sections = append(sections, dynamicBoundarySection, runtimeContextSection)
	return sections
}

func stablePromptSections() []Section {
	return append([]Section(nil), stableSystemPromptSections...)
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// mergePromptContextMaps merges context layers in order. Later overlays win so
// turn-specific runtime values override generic session context.
func mergePromptContextMaps(base map[string]string, overlays ...map[string]string) map[string]string {
	merged := cloneStringMap(base)
	for _, overlay := range overlays {
		for key, value := range overlay {
			merged[key] = value
		}
	}
	return merged
}

func joinPromptBlocks(blocks []string) string {
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func splitPromptParts(stable string, dynamic string) []string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(stable) != "" {
		parts = append(parts, strings.TrimSpace(stable))
	}
	if strings.TrimSpace(dynamic) != "" {
		parts = append(parts, strings.TrimSpace(dynamic))
	}
	return parts
}

func canonicalToolNames(tools map[string]tool.Tool) []string {
	primary := make(map[string]bool, len(tools))
	for _, resolved := range tools {
		if resolved == nil {
			continue
		}
		primary[resolved.Definition().Name] = true
	}
	names := make([]string, 0, len(primary))
	for name := range primary {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildPromptVariables(input FetchSystemPromptPartsInput) map[string]string {
	toolNames := canonicalToolNames(input.Tools)
	contextVars := BuildContext("", 0, resolvePromptWorkingDirectory(input.WorkingDirectory), toolNames)
	contextVars["model"] = input.Model.String()
	contextVars["provider"] = string(input.Model.Provider)
	contextVars["tool_count"] = fmt.Sprintf("%d", len(toolNames))
	contextVars["mcp_client_count"] = fmt.Sprintf("%d", len(input.MCPClients))
	contextVars["additional_working_directories"] = formatStringList(input.AdditionalWorkingDirectories)
	contextVars["stable_tool_names"] = formatStringList(toolNames)
	contextVars["available_deferred_tools"] = strings.Join(input.DeferredToolNames, "\n")
	contextVars["prompt_cache_boundary"] = "the dynamic system prompt boundary"
	contextVars["git_root"] = ""
	contextVars["git_branch"] = ""
	return contextVars
}

func buildPromptSections(includeDynamic bool, stage ExecutionStage, stageOverrides map[ExecutionStage]string) []Section {
	sections := append([]Section(nil), stablePromptSections()...)
	if includeDynamic {
		sections = append(sections, dynamicBoundarySection)
		sections = append(sections, dynamicPromptSectionsForStage(stage, stageOverrides)...)
	}
	return sections
}

func dynamicPromptSectionsForStage(stage ExecutionStage, stageOverrides map[ExecutionStage]string) []Section {
	sections := []Section{
		runtimeContextSection,
		runtimeGuidanceSection,
		projectInstructionsSection,
		runtimeMemorySection,
	}
	if s := stageSection(stage, stageOverrides); s != nil {
		sections = append(sections, *s)
	}
	return sections
}

func (b *Builder) buildPrompt(includeDynamic bool, variables map[string]string, stage ExecutionStage, stageOverrides map[ExecutionStage]string) (BuildResult, error) {
	return b.assembler.Build(BuildInput{
		Sections:  buildPromptSections(includeDynamic, stage, stageOverrides),
		Variables: variables,
	})
}

func buildSystemPromptBlocks(stableText string, dynamicText string) []types.SystemPromptBlock {
	blocks := make([]types.SystemPromptBlock, 0, 2)
	if strings.TrimSpace(stableText) != "" {
		blocks = append(blocks, types.NewTextSystemPromptBlock(stableText, types.NewEphemeralPromptCacheControl()))
	}
	if strings.TrimSpace(dynamicText) != "" {
		blocks = append(blocks, types.NewTextSystemPromptBlock(dynamicText, nil))
	}
	return blocks
}

// BuildProviderToolDefinitions builds a stable provider-facing tool surface.
// We only expose canonical primary names here: aliases are a runtime lookup
// convenience, but sending them to the provider would make the prompt surface
// unstable and drift away from the execution contract.
func BuildProviderToolDefinitions(tools map[string]tool.Tool) []types.APIToolDefinition {
	toolNames := canonicalToolNames(tools)
	definitions := make([]types.APIToolDefinition, 0, len(toolNames))
	for _, name := range toolNames {
		resolved := tools[name]
		if resolved == nil {
			continue
		}
		def := resolved.Definition()
		definitions = append(definitions, types.APIToolDefinition{
			Name:        def.Name,
			Description: providerToolDescription(def),
			InputSchema: def.InputSchema,
		})
	}
	return definitions
}

func providerToolDescription(def tool.Definition) string {
	return providerToolDescriptionWithHint(def, "")
}

func providerToolDescriptionWithHint(def tool.Definition, appHint string) string {
	parts := make([]string, 0, 4)
	if strings.TrimSpace(def.Description) != "" {
		parts = append(parts, strings.TrimSpace(def.Description))
	}
	if strings.TrimSpace(def.Prompt) != "" {
		parts = append(parts, strings.TrimSpace(def.Prompt))
	}
	if strings.TrimSpace(def.SearchHint) != "" {
		parts = append(parts, "Search hint: "+strings.TrimSpace(def.SearchHint))
	}
	if strings.TrimSpace(appHint) != "" {
		parts = append(parts, strings.TrimSpace(appHint))
	}
	return strings.Join(parts, "\n\n")
}

// BuildProviderToolDefinitionsWithHints builds provider-facing tool definitions
// and appends per-tool guidance from hints. Keys in hints are canonical tool names.
func BuildProviderToolDefinitionsWithHints(tools map[string]tool.Tool, hints map[string]string) []types.APIToolDefinition {
	toolNames := canonicalToolNames(tools)
	definitions := make([]types.APIToolDefinition, 0, len(toolNames))
	for _, name := range toolNames {
		resolved := tools[name]
		if resolved == nil {
			continue
		}
		def := resolved.Definition()
		hint := ""
		if hints != nil {
			hint = hints[def.Name]
		}
		definitions = append(definitions, types.APIToolDefinition{
			Name:        def.Name,
			Description: providerToolDescriptionWithHint(def, hint),
			InputSchema: def.InputSchema,
		})
	}
	return definitions
}

// BuildProviderRequest converts a canonical prompt plus a tool map into the
// provider-facing API request shape used by Seshat.
func BuildProviderRequest(
	canonicalPrompt CacheSafePrompt,
	model types.ModelIdentifier,
	messages []types.Message,
	tools map[string]tool.Tool,
	maxTokens int,
	stream bool,
) types.APIRequest {
	return types.APIRequest{
		Model:              model,
		Messages:           messages,
		SystemPrompt:       canonicalPrompt.SystemPrompt,
		SystemPromptBlocks: append([]types.SystemPromptBlock(nil), canonicalPrompt.SystemPromptBlocks...),
		Tools:              BuildProviderToolDefinitions(tools),
		MaxTokens:          maxTokens,
		Stream:             stream,
	}
}

func BuildProviderRequestWithHints(
	canonicalPrompt CacheSafePrompt,
	model types.ModelIdentifier,
	messages []types.Message,
	tools map[string]tool.Tool,
	toolHints map[string]string,
	maxTokens int,
	stream bool,
) types.APIRequest {
	return types.APIRequest{
		Model:              model,
		Messages:           messages,
		SystemPrompt:       canonicalPrompt.SystemPrompt,
		SystemPromptBlocks: append([]types.SystemPromptBlock(nil), canonicalPrompt.SystemPromptBlocks...),
		Tools:              BuildProviderToolDefinitionsWithHints(tools, toolHints),
		MaxTokens:          maxTokens,
		Stream:             stream,
	}
}

// BuildProviderRequestWithAppendPrompt bridges canonical prompt assembly and the
// provider-facing request model. Session/query code goes through this helper so
// the provider sees the same prompt split that prompt assembly already computed,
// instead of rebuilding a second system prompt shape later in the stack.
func BuildProviderRequestWithAppendPrompt(
	ctx context.Context,
	builder *Builder,
	sessionID types.SessionID,
	turnNumber int,
	workingDirectory string,
	parts SystemPromptParts,
	appendPrompt *string,
	messages []types.Message,
	tools map[string]tool.Tool,
	model types.ModelIdentifier,
	maxTokens int,
	stream bool,
) (types.APIRequest, error) {
	canonicalPrompt, err := builder.BuildCanonicalPrompt(ctx, sessionID, turnNumber, workingDirectory, parts, appendPrompt)
	if err != nil {
		return types.APIRequest{}, err
	}
	return BuildProviderRequestWithHints(canonicalPrompt, model, messages, tools, parts.ToolHints, maxTokens, stream), nil
}

// FetchSystemPromptParts fetches the stable prefix and the dynamic context maps.
// Uses prompt cache for stable sections when enabled.
func (b *Builder) FetchSystemPromptParts(
	ctx context.Context,
	input FetchSystemPromptPartsInput,
) (SystemPromptParts, error) {
	_ = ctx
	if input.CustomSystemPrompt != nil {
		customText := strings.TrimSpace(*input.CustomSystemPrompt)
		return SystemPromptParts{
			DefaultSystemPrompt: []string{customText},
			UserContext:         map[string]string{},
			SystemContext:       map[string]string{},
			StableText:          customText,
			DynamicText:         "",
			CacheBreakpoint:     len(customText),
		}, nil
	}

	promptVariables := buildPromptVariables(input)

	// ========================================================================
	// CACHE INTEGRATION: Try cache for stable sections first
	// ========================================================================
	cacheSectionNames := stableSectionNames()

	if b.config.EnableCaching {
		// Try to get from cache
		if cached, hit := b.cache.GetSectionCache(
			cacheSectionNames,
			input.Tools,
			input.Model,
			promptVariables,
		); hit && !cached.IsStale {
			// Cache hit: reuse the stable prefix and rebuild only the dynamic suffix.
			userContext := buildUserContext(input, promptVariables)
			systemContext := buildSystemContext(input, promptVariables)

			dynamicResult, err := b.buildPrompt(true, mergePromptContextMaps(userContext, systemContext), input.Stage, input.StageOverrides)
			if err != nil {
				return SystemPromptParts{}, fmt.Errorf("failed to build dynamic system prompt suffix: %w", err)
			}

			return SystemPromptParts{
				DefaultSystemPrompt: []string{cached.Content},
				UserContext:         userContext,
				SystemContext:       systemContext,
				StableText:          cached.Content,
				DynamicText:         dynamicResult.DynamicText,
				CacheBreakpoint:     len(cached.Content),
				Stage:               input.Stage,
				StageOverrides:      input.StageOverrides,
				ToolHints:           input.ToolHints,
			}, nil
		}
	}

	// Cache miss or stale: rebuild stable prefix
	stableResult, err := b.buildPrompt(false, nil, StageDefault, nil)
	if err != nil {
		return SystemPromptParts{}, fmt.Errorf("failed to build stable system prompt prefix: %w", err)
	}

	// ========================================================================
	// CACHE INTEGRATION: Store rebuilt prefix in cache
	// ========================================================================
	if b.config.EnableCaching {
		b.cache.SetSectionCache(
			cacheSectionNames,
			stableResult.StaticText,
			input.Tools,
			input.Model,
			promptVariables,
			nil, // No TTL: valid until explicit invalidation
		)
	}

	userContext := buildUserContext(input, promptVariables)
	systemContext := buildSystemContext(input, promptVariables)

	dynamicResult, err := b.buildPrompt(true, mergePromptContextMaps(userContext, systemContext), input.Stage, input.StageOverrides)
	if err != nil {
		return SystemPromptParts{}, fmt.Errorf("failed to build dynamic system prompt suffix: %w", err)
	}

	return SystemPromptParts{
		DefaultSystemPrompt: []string{stableResult.StaticText},
		UserContext:         userContext,
		SystemContext:       systemContext,
		StableText:          stableResult.StaticText,
		DynamicText:         dynamicResult.DynamicText,
		CacheBreakpoint:     stableResult.CacheBreakpoint,
		Stage:               input.Stage,
		StageOverrides:      input.StageOverrides,
		ToolHints:           input.ToolHints,
	}, nil
}

func buildUserContext(
	input FetchSystemPromptPartsInput,
	promptVariables map[string]string,
) map[string]string {
	userContext := BuildContext("", 0, resolvePromptWorkingDirectory(input.WorkingDirectory), canonicalToolNames(input.Tools))
	if value, ok := promptVariables["additional_working_directories"]; ok && value != "" {
		userContext["additional_working_directories"] = value
	}
	return userContext
}

func buildSystemContext(
	input FetchSystemPromptPartsInput,
	promptVariables map[string]string,
) map[string]string {
	memoryBlock := ""
	if trimmed := strings.TrimSpace(input.MemoryContext); trimmed != "" {
		memoryBlock = "# Memory context\n\n" + trimmed
	}
	projectInstructionsBlock := ""
	if trimmed := strings.TrimSpace(input.ProjectInstructions); trimmed != "" {
		projectInstructionsBlock = "# Project instructions\n\n" + trimmed
	}
	return map[string]string{
		"model":                      input.Model.String(),
		"provider":                   string(input.Model.Provider),
		"tool_count":                 promptVariables["tool_count"],
		"mcp_client_count":           promptVariables["mcp_client_count"],
		"available_tools":            promptVariables["available_tools"],
		"available_deferred_tools":   promptVariables["available_deferred_tools"],
		"stable_tool_names":          promptVariables["stable_tool_names"],
		"prompt_cache_boundary":      promptVariables["prompt_cache_boundary"],
		"memory_context_block":       memoryBlock,
		"project_instructions_block": projectInstructionsBlock,
		"git_context_block":          gitContextBlock(resolvePromptWorkingDirectory(input.WorkingDirectory)),
	}
}

// BuildCanonicalPrompt composes a per-turn canonical prompt from fetched parts.
func (b *Builder) BuildCanonicalPrompt(
	ctx context.Context,
	sessionID types.SessionID,
	turnNumber int,
	workingDirectory string,
	parts SystemPromptParts,
	appendPrompt *string,
) (CacheSafePrompt, error) {
	_ = ctx
	runtimeContext := map[string]string{
		"session_id":        sessionID.String(),
		"turn_number":       strconv.Itoa(turnNumber),
		"working_directory": workingDirectory,
	}
	mergedRuntimeContext := mergePromptContextMaps(parts.UserContext, parts.SystemContext, runtimeContext)
	stableText := parts.StableText
	if stableText == "" {
		stableText = joinPromptBlocks(parts.DefaultSystemPrompt)
	}
	dynamicResult, err := b.buildPrompt(true, mergedRuntimeContext, parts.Stage, parts.StageOverrides)
	if err != nil {
		return CacheSafePrompt{}, err
	}
	dynamicText := dynamicResult.DynamicText
	systemPrompt := joinPromptBlocks(splitPromptParts(stableText, dynamicText))
	if appendPrompt != nil && *appendPrompt != "" {
		if systemPrompt != "" {
			systemPrompt += "\n\n"
		}
		systemPrompt += *appendPrompt
	}
	return CacheSafePrompt{
		SystemPrompt:       systemPrompt,
		StableText:         stableText,
		DynamicText:        dynamicText,
		CacheBreakpoint:    len(stableText),
		UserContext:        cloneStringMap(parts.UserContext),
		SystemContext:      cloneStringMap(parts.SystemContext),
		SystemPromptBlocks: buildSystemPromptBlocks(stableText, dynamicText),
	}, nil
}

// BuildSystemPrompt builds the final flattened system prompt from canonical parts.
func (b *Builder) BuildSystemPrompt(
	ctx context.Context,
	sessionID types.SessionID,
	turnNumber int,
	workingDirectory string,
	parts SystemPromptParts,
	appendPrompt *string,
) (string, error) {
	canonicalPrompt, err := b.BuildCanonicalPrompt(ctx, sessionID, turnNumber, workingDirectory, parts, appendPrompt)
	if err != nil {
		return "", err
	}
	return canonicalPrompt.SystemPrompt, nil
}

// CacheSafeParams represents cache-safe parameters for query.
type CacheSafeParams struct {
	SystemPrompt  string
	UserContext   map[string]string
	SystemContext map[string]string
	Tools         map[string]tool.Tool
	Model         types.ModelIdentifier
	CustomPrompt  *string
	AppendPrompt  *string
}

// BuildCacheSafeParams builds cache-safe parameters.
func (b *Builder) BuildCacheSafeParams(
	ctx context.Context,
	input FetchSystemPromptPartsInput,
) (CacheSafeParams, error) {
	parts, err := b.FetchSystemPromptParts(ctx, input)
	if err != nil {
		return CacheSafeParams{}, err
	}
	canonicalPrompt, err := b.BuildCanonicalPrompt(
		ctx,
		types.NewSessionID("side-question"),
		1,
		resolvePromptWorkingDirectory(input.WorkingDirectory),
		parts,
		input.AppendSystemPrompt,
	)
	if err != nil {
		return CacheSafeParams{}, err
	}
	return CacheSafeParams{
		SystemPrompt:  canonicalPrompt.SystemPrompt,
		UserContext:   canonicalPrompt.UserContext,
		SystemContext: canonicalPrompt.SystemContext,
		Tools:         input.Tools,
		Model:         input.Model,
		CustomPrompt:  input.CustomSystemPrompt,
		AppendPrompt:  input.AppendSystemPrompt,
	}, nil
}

func formatStringList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return strings.Join(items, ", ")
}

func getCurrentWorkingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func resolvePromptWorkingDirectory(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	return getCurrentWorkingDirectory()
}

// ================================================================================
// Cache Management Methods (Slice 1: State Kernel + Prompt Caching)
// ================================================================================

// InvalidateCache invalide tout le cache de prompt
// Appelé sur /clear, /compact, ou changement significatif de configuration
func (b *Builder) InvalidateCache() {
	b.cache.InvalidateAll()
}

// InvalidateSectionCache invalide des sections spécifiques
func (b *Builder) InvalidateSectionCache(sectionNames []string) {
	b.cache.InvalidateSectionCache(sectionNames)
}

// InvalidateByToolHash invalide toutes les entrées dépendant d'un hash d'outils
func (b *Builder) InvalidateByToolHash(toolHash string) {
	b.cache.InvalidateByToolHash(toolHash)
}

// GetCacheSize retourne le nombre d'entrées cacheées
func (b *Builder) GetCacheSize() int {
	return b.cache.Size()
}

// ClearStaleEntries supprime les entrées expirées du cache
func (b *Builder) ClearStaleEntries() int {
	return b.cache.ClearStaleEntries()
}
