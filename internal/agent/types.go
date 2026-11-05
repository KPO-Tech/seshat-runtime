package agent

import (
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// AgentSource indicates where the agent definition comes from
type AgentSource string

const (
	AgentSourceBuiltIn AgentSource = "built-in"
	AgentSourceUser    AgentSource = "user"
	// AgentSourceHost marks a definition handed in by the embedding host
	// (AgentRegistry.ReplaceHostDefinitions). Whatever Source the host declared,
	// the registry stores this one.
	AgentSourceHost AgentSource = "host"
)

// AgentDefinition defines an agent
type AgentDefinition struct {
	// AgentType is the unique agent type identifier
	AgentType string `json:"agentType"`

	// WhenToUse is the description for when to use this agent
	WhenToUse string `json:"whenToUse"`

	// Source indicates where this agent comes from
	Source AgentSource `json:"source"`

	// BaseDir is the base directory (for user/project agents)
	BaseDir string `json:"baseDir,omitempty"`

	// Filename is the original filename (for user/project agents)
	Filename string `json:"filename,omitempty"`

	// Tools is the list of allowed tool patterns (glob patterns)
	// nil or ["*"] means all tools
	Tools []string `json:"tools,omitempty"`

	// DisallowedTools is the list of disallowed tool patterns, same syntax as
	// Tools. A tool matching one is never available to the agent, whatever Tools
	// or the caller's tool list say (deny wins), and neither are the sub-agents
	// it spawns.
	DisallowedTools []string `json:"disallowedTools,omitempty"`

	// Skills is the list of skill names to preload
	Skills []string `json:"skills,omitempty"`

	// MCPservers is the list of MCP server names to connect
	McpServers []string `json:"mcpServers,omitempty"`

	// Model is the model to use (empty = default)
	Model string `json:"model,omitempty"`

	// Effort is the effort level (1-5)
	Effort int `json:"effort,omitempty"`

	// PermissionMode is the permission mode
	PermissionMode types.PermissionMode `json:"permissionMode,omitempty"`

	// MaxTurns is the maximum number of turns
	MaxTurns int `json:"maxTurns,omitempty"`

	// Memory is the memory scope
	Memory string `json:"memory,omitempty"`

	// Background indicates if the agent should run in background
	Background bool `json:"background,omitempty"`

	// Isolation is the isolation mode
	Isolation string `json:"isolation,omitempty"`

	// SystemPromptGetter is the function to get the system prompt
	GetSystemPrompt func() string `json:"-"`
}

// GetToolPatterns returns the allowed tool patterns; nil or empty Tools means
// all tools. DisallowedTools is applied on top of it, see EffectiveToolPolicy.
func (a *AgentDefinition) GetToolPatterns() []string {
	if len(a.Tools) == 0 {
		return []string{"*"}
	}
	return a.Tools
}

// BuiltInAgentDefinition is an agent defined in code (not loaded from disk)
type BuiltInAgentDefinition struct {
	AgentType       string
	WhenToUse       string
	Tools           []string
	Source          AgentSource
	BaseDir         string
	Model           string
	GetSystemPrompt func() string
	MaxTurns        int
}
