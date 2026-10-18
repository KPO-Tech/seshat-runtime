package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/repomap"
)

// buildAPIRequest builds the provider-facing request data for the current turn.
func (s *Session) buildAPIRequest(ctx context.Context) (types.APIRequest, error) {
	runtimeTools := s.state.EffectiveToolSurface(s.engine.toolRegistry)
	return s.buildAPIRequestForRuntimeTools(ctx, runtimeTools, s.state.PendingDeferredToolNames(s.engine.toolRegistry), prompt.StageDefault)
}

func (s *Session) refreshSystemPrompt(ctx context.Context, tools map[string]tool.Tool, pendingDeferred []string, stage prompt.ExecutionStage) (PromptRefresh, error) {
	apiReq, err := s.buildAPIRequestForRuntimeTools(ctx, tools, pendingDeferred, stage)
	if err != nil {
		return PromptRefresh{}, err
	}
	return PromptRefresh{
		SystemPrompt:       apiReq.SystemPrompt,
		SystemPromptBlocks: append([]types.SystemPromptBlock(nil), apiReq.SystemPromptBlocks...),
	}, nil
}

func (s *Session) workingDirectory() string {
	if s != nil && s.workingDirectoryOverride != nil && *s.workingDirectoryOverride != "" {
		return *s.workingDirectoryOverride
	}
	if s != nil && s.state != nil && s.state.Metadata != nil && s.state.Metadata.RootPath != "" {
		return s.state.Metadata.RootPath
	}
	if s != nil && s.engine != nil {
		return s.engine.workingDirectory()
	}
	return "."
}

func (s *Session) buildAPIRequestForRuntimeTools(ctx context.Context, runtimeTools map[string]tool.Tool, pendingDeferred []string, detectedStage prompt.ExecutionStage) (types.APIRequest, error) {
	customSystemPrompt := trimmedStringPtr(s.config.SystemPromptTemplate)
	if s.systemPromptTemplateOverride != nil {
		customSystemPrompt = s.systemPromptTemplateOverride
	}
	appendPrompt := trimmedStringPtr(s.config.AppendSystemPrompt)
	if s.appendSystemPromptOverride != nil {
		appendPrompt = s.appendSystemPromptOverride
	}
	appendPrompt = withManagedInstructions(appendPrompt, s.config.ManagedInstructions)
	workingDirectory := s.workingDirectory()

	stage := s.config.PromptStage
	if stage == prompt.StageDefault && detectedStage != prompt.StageDefault {
		stage = detectedStage
	}

	projectInstructions := readTrustedProjectInstructions(workingDirectory, s.config.TrustProjectInstructions)
	if repoMap := s.repoMapContext(ctx, workingDirectory); repoMap != "" {
		projectInstructions = strings.TrimSpace(projectInstructions + "\n\n" + repoMap)
	}

	partsInput := prompt.FetchSystemPromptPartsInput{
		Tools:               runtimeTools,
		Model:               s.config.Model,
		WorkingDirectory:    workingDirectory,
		DeferredToolNames:   deferredToolEntries(s.engine.toolRegistry, pendingDeferred),
		MemoryContext:       joinMemoryContext(s.engine.memoryContext(s.sessionProjectID()), s.recallLongTermMemory(ctx)),
		CustomSystemPrompt:  customSystemPrompt,
		AppendSystemPrompt:  nil,
		Stage:               stage,
		StageOverrides:      s.config.PromptStageOverrides,
		ToolHints:           s.config.PromptToolHints,
		ProjectInstructions: projectInstructions,
	}

	parts, err := s.engine.promptBuilder.FetchSystemPromptParts(ctx, partsInput)
	if err != nil {
		return types.APIRequest{}, fmt.Errorf("failed to fetch system prompt parts: %w", err)
	}

	apiReq, err := prompt.BuildProviderRequestWithAppendPrompt(
		ctx,
		s.engine.promptBuilder,
		s.state.SessionID,
		s.state.TurnNumber,
		workingDirectory,
		parts,
		appendPrompt,
		s.state.CloneMessages(),
		runtimeTools,
		s.config.Model,
		s.config.MaxTokens,
		s.engine.loop.config.EnableStreaming,
	)
	if err != nil {
		return types.APIRequest{}, fmt.Errorf("failed to compose canonical prompt request: %w", err)
	}

	if len(s.config.PromptToolHints) > 0 {
		apiReq.Tools = prompt.BuildProviderToolDefinitionsWithHints(runtimeTools, s.config.PromptToolHints)
	}

	return apiReq, nil
}

func (s *Session) repoMapContext(ctx context.Context, workingDirectory string) string {
	if !envTruthy(os.Getenv("SESHAT_REPO_MAP")) {
		return ""
	}
	m, err := repomap.Build(ctx, repomap.Options{Root: workingDirectory, TokenBudget: 1024})
	if err != nil {
		return ""
	}
	rendered := repomap.Render(m, 1024)
	if strings.TrimSpace(rendered) == "" {
		return ""
	}
	return "<repo_map>\n" + rendered + "\n</repo_map>"
}

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

// withManagedInstructions appends the organization's instructions after whatever append prompt the
// session has, so they come last and a per-session override cannot remove them.
func withManagedInstructions(appendPrompt *string, managed string) *string {
	managed = strings.TrimSpace(managed)
	if managed == "" {
		return appendPrompt
	}
	joined := managed
	if appendPrompt != nil && strings.TrimSpace(*appendPrompt) != "" {
		joined = strings.TrimSpace(*appendPrompt) + "\n\n" + managed
	}
	return &joined
}

// deferredToolEntries turns the names of the deferred tools into the lines the system prompt lists: the name and, when there
// is one, a short description of what the tool is for. The agent cannot see the tool's schema until it finds the tool with
// tool_search, so this line is what tells it that the tool exists and when to look for it.
func deferredToolEntries(reg *tool.Registry, names []string) []string {
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entry := name
		if reg != nil {
			if resolved, ok := reg.Resolve(name); ok && resolved != nil {
				if short := tool.ShortDescription(resolved.Definition()); short != "" {
					entry = name + ": " + short
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries
}
