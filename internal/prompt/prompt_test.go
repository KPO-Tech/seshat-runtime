package prompt

import (
	"context"
	"strings"
	"testing"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// --- from builder_test.go ---

func TestBuildContextWithGit(t *testing.T) {
	// Test that BuildContextWithGit includes git context
	context := BuildContextWithGit("test-session", 1, "/test/dir", []string{"tool1", "tool2"}, "/git/root", "main")

	// Check basic context
	if context["session_id"] != "test-session" {
		t.Errorf("Expected session_id 'test-session', got '%s'", context["session_id"])
	}

	if context["working_directory"] != "/test/dir" {
		t.Errorf("Expected working_directory '/test/dir', got '%s'", context["working_directory"])
	}

	// Check git context
	if context["git_root"] != "/git/root" {
		t.Errorf("Expected git_root '/git/root', got '%s'", context["git_root"])
	}

	if context["git_branch"] != "main" {
		t.Errorf("Expected git_branch 'main', got '%s'", context["git_branch"])
	}
}

func TestBuildContextWithoutGit(t *testing.T) {
	// Test that BuildContext does not include git context
	context := BuildContext("test-session", 1, "/test/dir", []string{"tool1", "tool2"})

	// Check basic context exists
	if context["session_id"] != "test-session" {
		t.Errorf("Expected session_id 'test-session', got '%s'", context["session_id"])
	}

	// Check git context does not exist
	if _, ok := context["git_root"]; ok {
		t.Error("git_root should not exist in BuildContext")
	}

	if _, ok := context["git_branch"]; ok {
		t.Error("git_branch should not exist in BuildContext")
	}
}

func TestBuildContextWithGitEmptyValues(t *testing.T) {
	// Test that BuildContextWithGit handles empty git values
	context := BuildContextWithGit("test-session", 1, "/test/dir", []string{"tool1", "tool2"}, "", "")

	// Check basic context exists
	if context["session_id"] != "test-session" {
		t.Errorf("Expected session_id 'test-session', got '%s'", context["session_id"])
	}

	// Check git context exists but is empty
	if context["git_root"] != "" {
		t.Errorf("Expected git_root '', got '%s'", context["git_root"])
	}

	if context["git_branch"] != "" {
		t.Errorf("Expected git_branch '', got '%s'", context["git_branch"])
	}
}

func TestBuildPromptVariablesIncludesGit(t *testing.T) {
	// Create a mock input
	input := FetchSystemPromptPartsInput{
		Tools:      make(map[string]tool.Tool),
		Model:      types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
		MCPClients: []string{},
	}

	// Build prompt variables
	vars := buildPromptVariables(input)

	// Check that git variables are present (they may be empty if not in git repo)
	if _, ok := vars["git_root"]; !ok {
		t.Error("git_root should be present in prompt variables")
	}

	if _, ok := vars["git_branch"]; !ok {
		t.Error("git_branch should be present in prompt variables")
	}
}

type promptTestTool struct {
	def tool.Definition
}

func (t promptTestTool) Definition() tool.Definition { return t.def }
func (t promptTestTool) Call(ctx context.Context, input tool.CallInput, permissionCheck types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewTextResult("ok"), nil
}
func (t promptTestTool) Description(ctx context.Context) (string, error) {
	return t.def.Description, nil
}
func (t promptTestTool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	return input, nil
}
func (t promptTestTool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	return types.AllowWithUpdatedInput(input)
}
func (t promptTestTool) IsConcurrencySafe(input map[string]any) bool { return t.def.IsConcurrencySafe }
func (t promptTestTool) IsReadOnly(input map[string]any) bool        { return t.def.IsReadOnly }
func (t promptTestTool) IsEnabled() bool                             { return true }
func (t promptTestTool) FormatResult(data any) string                { return "ok" }
func (t promptTestTool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

func TestBuildPromptVariablesIncludesDeferredToolNames(t *testing.T) {
	input := FetchSystemPromptPartsInput{
		Tools:             make(map[string]tool.Tool),
		Model:             types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
		DeferredToolNames: []string{"Deploy", "Release"},
	}

	vars := buildPromptVariables(input)
	if got := vars["available_deferred_tools"]; got != "Deploy\nRelease" {
		t.Fatalf("expected deferred tool names in prompt variables, got %q", got)
	}
}

func TestBuildProviderToolDefinitionsIncludesPromptGuidance(t *testing.T) {
	definitions := BuildProviderToolDefinitions(map[string]tool.Tool{
		"Deploy": promptTestTool{def: tool.Definition{
			Name:        "Deploy",
			Description: "Deploys the service.",
			Prompt:      "Use only after validation passes.",
			SearchHint:  "deploy release production",
			InputSchema: schema.FromMap(map[string]any{"type": "object"}),
		}},
	})

	if len(definitions) != 1 {
		t.Fatalf("expected 1 provider tool definition, got %d", len(definitions))
	}
	description := definitions[0].Description
	if !strings.Contains(description, "Use only after validation passes.") {
		t.Fatalf("expected provider description to include tool prompt guidance, got %q", description)
	}
	if !strings.Contains(description, "Search hint: deploy release production") {
		t.Fatalf("expected provider description to include search hint, got %q", description)
	}
}

func TestBuildProviderRequestWithHintsIncludesAppHints(t *testing.T) {
	builder := NewBuilder(NewAssembler(), DefaultBuilderConfig())
	tools := map[string]tool.Tool{
		"Deploy": promptTestTool{def: tool.Definition{
			Name:        "Deploy",
			Description: "Deploys the service.",
			Prompt:      "Use only after validation passes.",
			SearchHint:  "deploy release production",
			InputSchema: schema.FromMap(map[string]any{"type": "object"}),
		}},
	}

	parts, err := builder.FetchSystemPromptParts(context.Background(), FetchSystemPromptPartsInput{
		Tools:     tools,
		Model:     types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
		ToolHints: map[string]string{"Deploy": "Prefer blue/green rollout for production."},
	})
	if err != nil {
		t.Fatalf("expected prompt parts, got error: %v", err)
	}

	req, err := BuildProviderRequestWithAppendPrompt(
		context.Background(),
		builder,
		types.NewSessionID("test-session"),
		1,
		"/test/dir",
		parts,
		nil,
		nil,
		tools,
		types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
		2048,
		false,
	)
	if err != nil {
		t.Fatalf("expected provider request, got error: %v", err)
	}

	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(req.Tools))
	}
	if !strings.Contains(req.Tools[0].Description, "Prefer blue/green rollout for production.") {
		t.Fatalf("expected app hint in tool description, got %q", req.Tools[0].Description)
	}
}

func TestCanonicalPromptIncludesWorkflowSections(t *testing.T) {
	builder := NewBuilder(NewAssembler(), DefaultBuilderConfig())

	parts, err := builder.FetchSystemPromptParts(context.Background(), FetchSystemPromptPartsInput{
		Tools: map[string]tool.Tool{},
		Model: types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
	})
	if err != nil {
		t.Fatalf("expected prompt parts, got error: %v", err)
	}

	canonicalPrompt, err := builder.BuildCanonicalPrompt(
		context.Background(),
		types.NewSessionID("test-session"),
		1,
		"/test/dir",
		parts,
		nil,
	)
	if err != nil {
		t.Fatalf("expected canonical prompt, got error: %v", err)
	}

	for _, expected := range []string{"# Role", "# Principles", "# What to do in each situation", "# Asking the user", "# Plans and tasks", "# Research and reports", "# Delegation", "# Safety", "# How you answer"} {
		if !strings.Contains(canonicalPrompt.SystemPrompt, expected) {
			t.Fatalf("expected system prompt to include %q, got %q", expected, canonicalPrompt.SystemPrompt)
		}
	}
}

func TestFetchSystemPromptPartsIncludesMemoryContextBlock(t *testing.T) {
	builder := NewBuilder(NewAssembler(), DefaultBuilderConfig())

	parts, err := builder.FetchSystemPromptParts(context.Background(), FetchSystemPromptPartsInput{
		Tools:         map[string]tool.Tool{},
		Model:         types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
		MemoryContext: "## Project Memory\n- style: keep tests focused",
	})
	if err != nil {
		t.Fatalf("expected prompt parts, got error: %v", err)
	}

	canonicalPrompt, err := builder.BuildCanonicalPrompt(
		context.Background(),
		types.NewSessionID("test-session"),
		1,
		"/test/dir",
		parts,
		nil,
	)
	if err != nil {
		t.Fatalf("expected canonical prompt, got error: %v", err)
	}

	if !strings.Contains(canonicalPrompt.SystemPrompt, "# Memory context") {
		t.Fatalf("expected system prompt to include memory section, got %q", canonicalPrompt.SystemPrompt)
	}
	if !strings.Contains(canonicalPrompt.SystemPrompt, "keep tests focused") {
		t.Fatalf("expected system prompt to include memory content, got %q", canonicalPrompt.SystemPrompt)
	}
}

func TestBuildCacheSafeParamsUsesConfiguredWorkingDirectory(t *testing.T) {
	builder := NewBuilder(NewAssembler(), DefaultBuilderConfig())
	workingDir := t.TempDir()

	params, err := builder.BuildCacheSafeParams(context.Background(), FetchSystemPromptPartsInput{
		Tools:            map[string]tool.Tool{},
		Model:            types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-20241022"},
		WorkingDirectory: workingDir,
	})
	if err != nil {
		t.Fatalf("expected cache-safe params, got error: %v", err)
	}

	if got := params.UserContext["working_directory"]; got != workingDir {
		t.Fatalf("expected user context working directory %q, got %q", workingDir, got)
	}
	if !strings.Contains(params.SystemPrompt, "working_directory: "+workingDir) {
		t.Fatalf("expected system prompt to include configured working directory, got %q", params.SystemPrompt)
	}
}

// --- from stages_test.go ---

// TestStageSectionDefaultReturnsNil verifies no overlay is injected for the default stage.
func TestStageSectionDefaultReturnsNil(t *testing.T) {
	if got := stageSection(StageDefault, nil); got != nil {
		t.Fatalf("expected nil section for StageDefault, got %+v", got)
	}
}

// TestStageSectionUnknownReturnsNil verifies no overlay is injected for unknown stages.
func TestStageSectionUnknownReturnsNil(t *testing.T) {
	if got := stageSection(ExecutionStage("unknown"), nil); got != nil {
		t.Fatalf("expected nil section for unknown stage, got %+v", got)
	}
}

func TestStageSectionKnownStagesReturnSection(t *testing.T) {
	stages := []ExecutionStage{
		StageToolCall,
		StageToolResult,
		StageContinuation,
		StagePlan,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			s := stageSection(stage, nil)
			if s == nil {
				t.Fatalf("expected non-nil section for stage %q", stage)
			} else {
				if s.Name != "stage_overlay" {
					t.Errorf("expected section name 'stage_overlay', got %q", s.Name)
				}
				if s.Cacheable {
					t.Error("stage overlay sections must not be cacheable")
				}
				if !s.Enabled {
					t.Error("stage overlay section must be enabled")
				}
				if strings.TrimSpace(s.Content) == "" {
					t.Error("stage overlay content must not be empty")
				}
			}
		})
	}
}

// TestStageSectionOverrideWinsOverDefault verifies caller-supplied text replaces the built-in template.
func TestStageSectionOverrideWinsOverDefault(t *testing.T) {
	overrideText := "Custom plan guidance for this app."
	s := stageSection(StagePlan, map[ExecutionStage]string{
		StagePlan: overrideText,
	})
	if s == nil {
		t.Fatal("expected non-nil section")
	} else if s.Content != overrideText {
		t.Errorf("expected override content %q, got %q", overrideText, s.Content)
	}
}

// TestStageSectionEmptyOverrideFallsBackToDefault verifies empty override falls back to built-in.
func TestStageSectionEmptyOverrideFallsBackToDefault(t *testing.T) {
	s := stageSection(StagePlan, map[ExecutionStage]string{
		StagePlan: "   ", // whitespace only
	})
	if s == nil {
		t.Fatal("expected non-nil section")
	} else if !strings.Contains(s.Content, "plan mode") {
		t.Errorf("expected built-in plan overlay content, got %q", s.Content)
	}
}

// TestStageOverlayAppearsInBuiltPrompt verifies the stage section is included in the
// assembled dynamic system prompt text.
func TestStageOverlayAppearsInBuiltPrompt(t *testing.T) {
	builder := NewBuilder(NewAssembler(), DefaultBuilderConfig())

	parts, err := builder.FetchSystemPromptParts(context.Background(), FetchSystemPromptPartsInput{
		Tools: map[string]tool.Tool{},
		Model: types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"},
		Stage: StagePlan,
	})
	if err != nil {
		t.Fatalf("FetchSystemPromptParts: %v", err)
	}

	canonical, err := builder.BuildCanonicalPrompt(
		context.Background(),
		types.NewSessionID("test"),
		1,
		"/tmp",
		parts,
		nil,
	)
	if err != nil {
		t.Fatalf("BuildCanonicalPrompt: %v", err)
	}

	if !strings.Contains(canonical.SystemPrompt, "plan mode") {
		t.Errorf("expected plan stage overlay in system prompt, got:\n%s", canonical.SystemPrompt)
	}
}

// TestStageOverlayAbsentForDefaultStage verifies no stage overlay text is injected by default.
func TestStageOverlayAbsentForDefaultStage(t *testing.T) {
	builder := NewBuilder(NewAssembler(), DefaultBuilderConfig())

	parts, err := builder.FetchSystemPromptParts(context.Background(), FetchSystemPromptPartsInput{
		Tools: map[string]tool.Tool{},
		Model: types.ModelIdentifier{Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"},
		// Stage not set → StageDefault
	})
	if err != nil {
		t.Fatalf("FetchSystemPromptParts: %v", err)
	}

	canonical, err := builder.BuildCanonicalPrompt(
		context.Background(),
		types.NewSessionID("test"),
		1,
		"/tmp",
		parts,
		nil,
	)
	if err != nil {
		t.Fatalf("BuildCanonicalPrompt: %v", err)
	}

	if strings.Contains(canonical.SystemPrompt, "Stage:") {
		t.Errorf("expected no Stage: overlay for StageDefault, got:\n%s", canonical.SystemPrompt)
	}
}

// TestToolHintsAppearsInProviderDescription verifies per-tool hints are included in descriptions.
func TestToolHintsAppearsInProviderDescription(t *testing.T) {
	toolMap := map[string]tool.Tool{
		"bash": stagesTestTool{def: tool.Definition{
			Name:        "bash",
			Description: "Execute shell commands.",
		}},
	}
	hints := map[string]string{
		"bash": "Only use for read-only operations in this session.",
	}

	defs := BuildProviderToolDefinitionsWithHints(toolMap, hints)
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	if !strings.Contains(defs[0].Description, "Only use for read-only") {
		t.Errorf("expected hint in description, got %q", defs[0].Description)
	}
	if !strings.Contains(defs[0].Description, "Execute shell commands") {
		t.Errorf("expected base description preserved, got %q", defs[0].Description)
	}
}

// TestToolHintsNilDoesNotBreakToolDefinitions verifies nil hints work like the base function.
func TestToolHintsNilDoesNotBreakToolDefinitions(t *testing.T) {
	toolMap := map[string]tool.Tool{
		"read": stagesTestTool{def: tool.Definition{
			Name:        "read",
			Description: "Read a file.",
		}},
	}
	defs := BuildProviderToolDefinitionsWithHints(toolMap, nil)
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	if defs[0].Description != "Read a file." {
		t.Errorf("expected unmodified description, got %q", defs[0].Description)
	}
}

// stagesTestTool is a minimal tool.Tool for testing prompt builder functions.
type stagesTestTool struct {
	def tool.Definition
}

func (t stagesTestTool) Definition() tool.Definition { return t.def }
func (t stagesTestTool) Call(_ context.Context, _ tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	return tool.NewTextResult("ok"), nil
}
func (t stagesTestTool) Description(_ context.Context) (string, error) { return t.def.Description, nil }
func (t stagesTestTool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	return input, nil
}
func (t stagesTestTool) CheckPermissions(_ context.Context, input map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.AllowWithUpdatedInput(input)
}
func (t stagesTestTool) IsConcurrencySafe(_ map[string]any) bool { return t.def.IsConcurrencySafe }
func (t stagesTestTool) IsReadOnly(_ map[string]any) bool        { return t.def.IsReadOnly }
func (t stagesTestTool) IsEnabled() bool                         { return true }
func (t stagesTestTool) FormatResult(_ any) string               { return "ok" }
func (t stagesTestTool) BackfillInput(_ context.Context, input map[string]any) map[string]any {
	return input
}
