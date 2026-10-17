package engine

import (
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/system/mcp"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	browsercore "github.com/KPO-Tech/seshat-runtime/internal/web/browser"
)

// Config represents the engine configuration.
type Config struct {
	MaxTurns       int                   `json:"max_turns"`
	AutoCompact    bool                  `json:"auto_compact"`
	PermissionMode types.PermissionMode  `json:"permission_mode"`
	Model          types.ModelIdentifier `json:"model"`
	TitleModel     types.ModelIdentifier `json:"title_model,omitempty"`
	// CompactSummaryModel overrides which model generates compaction summaries.
	// Zero value means "use Model" (the session's own model), so compaction
	// works on any provider without needing an Anthropic key.
	CompactSummaryModel types.ModelIdentifier `json:"compact_summary_model,omitempty"`
	// TrustProjectInstructions gates SESHAT.md/AGENTS.md/.seshat/instructions.md
	// found in the working directory behind project trust before they are
	// injected into the system prompt (see docs/project-trust.md and
	// readTrustedProjectInstructions). nil fails closed: no project's own
	// instructions are ever injected without a host wiring this in - e.g. to
	// the same hash-based trust store it already uses for the project's other
	// configuration files.
	TrustProjectInstructions func(path string, content []byte) bool `json:"-"`
	MaxTokens                int                                    `json:"max_tokens"`
	WorkingDirectory         string                                 `json:"working_directory,omitempty"`
	SystemPromptTemplate     string                                 `json:"system_prompt_template"`
	MCPServers               []mcp.ServerConfig                     `json:"mcp_servers"`
	EnableMemory             bool                                   `json:"enable_memory"`
	EnableMonitoring         bool                                   `json:"enable_monitoring"`

	// MaxIterations caps model→tool→model cycles per turn. 0 = loop default
	// (100 - see DefaultLoopConfig; this comment previously said 10, which
	// was never the actual default - M3 in TODO.md).
	MaxIterations int `json:"max_iterations"`

	// TurnTokenBudget enables token-budget continuation when > 0.
	TurnTokenBudget int `json:"turn_token_budget"`

	// BudgetContinuationLimit caps how many budget-continuation cycles may occur.
	// 0 = loop default.
	BudgetContinuationLimit int `json:"budget_continuation_limit"`

	// ContinuationNudgeLimit caps how many text-signal nudges the loop sends.
	// 0 = loop default (3).
	ContinuationNudgeLimit int `json:"continuation_nudge_limit"`

	// TaskNudgeLimit caps how many task-list reminders one turn sends. 0 = loop
	// default (3); a negative value disables the reminder entirely.
	TaskNudgeLimit int `json:"task_nudge_limit"`

	// StopHooks are post-turn policy hooks that may request one more cycle.
	StopHooks []StopHook `json:"-"`

	// MaxConsecutiveDenials is the number of back-to-back permission denials that
	// trigger orchestrator fallback. 0 = use the default (3).
	MaxConsecutiveDenials int `json:"max_consecutive_denials"`

	// PromptStage sets the execution stage overlay injected into the dynamic
	// section of the system prompt every turn.
	PromptStage prompt.ExecutionStage `json:"prompt_stage,omitempty"`

	// PromptStageOverrides replaces built-in stage overlay text per stage.
	PromptStageOverrides map[prompt.ExecutionStage]string `json:"-"`

	// PromptToolHints provides per-tool extra guidance appended to provider-facing
	// tool descriptions. Key is the canonical tool name.
	PromptToolHints map[string]string `json:"-"`

	// AppendSystemPrompt is appended to the system prompt after all other sections.
	AppendSystemPrompt string `json:"append_system_prompt,omitempty"`

	// ManagedInstructions is text imposed by whoever runs the host (an organization).
	// It is appended after everything else, including a per-session append prompt,
	// so a session setter cannot replace or drop it.
	ManagedInstructions string `json:"-"`

	// BrowserManager manages native browser sessions for browser tools.
	BrowserManager browsercore.Manager `json:"-"`

	// MainAgentTools, when non-empty, restricts the tools the main agent
	// (the mono_run surface) is offered to exactly these names. Sub-agents are
	// not affected: they build their own surface from the whole registry, so a
	// "browse" sub-agent keeps its browser tools even when the main agent has
	// none. Empty keeps every registered tool.
	MainAgentTools []string `json:"main_agent_tools,omitempty"`

	// MainAgentDeferredTools are tools the main agent does not have loaded but can find with tool_search and then use
	// (they are not in MainAgentTools). A tool in neither list is not offered to the main agent at all. Sub-agents are
	// not given this list. Empty keeps the behaviour where only MCP tools and skills are found through tool_search.
	MainAgentDeferredTools []string `json:"main_agent_deferred_tools,omitempty"`

	// DisableStreaming opts out of server-sent-event streaming. Streaming is
	// enabled by default; set this to true only when the provider or test
	// harness does not support SSE.
	DisableStreaming bool `json:"disable_streaming,omitempty"`
}

// DefaultConfig returns default engine configuration.
func DefaultConfig() *Config {
	return &Config{
		MaxTurns:       100,
		AutoCompact:    true,
		PermissionMode: types.PermissionModeOnRequest,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderAnthropic,
			Model:    providers.DefaultSonnetModel,
		},
		MaxTokens:            8192,
		SystemPromptTemplate: "",
		EnableMemory:         true,
		EnableMonitoring:     true,
	}
}
