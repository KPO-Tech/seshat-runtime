package agent

import internalagent "github.com/KPO-Tech/seshat-runtime/internal/agent"

type (
	// AgentDefinition defines an agent
	AgentDefinition = internalagent.AgentDefinition
	// AgentSource indicates where the agent definition comes from
	AgentSource = internalagent.AgentSource
	// AgentRegistry is a unified registry for built-in and skill-derived agents.
	// Built-in agents are pre-loaded at construction; dynamic agents are loaded
	// via LoadFromSkills. Skill-defined agents never shadow built-in ones.
	AgentRegistry = internalagent.AgentRegistry
	// AsyncAgent represents an asynchronous agent execution
	AsyncAgent = internalagent.AsyncAgent
	// AsyncAgentManager manages concurrent async agent executions
	AsyncAgentManager = internalagent.AsyncAgentManager
)

// NewAgentRegistry creates a registry pre-populated with all built-in agents.
func NewAgentRegistry() *AgentRegistry {
	return internalagent.NewAgentRegistry()
}

// DefaultAsyncManager returns the process-wide manager backing the
// spawn_agent/wait_agent/send_agent_message/close_agent tool family. Host
// applications (e.g. seshat-backend) use it to expose management operations —
// like cancelling a runaway or unwanted background agent — through their own
// API, without duplicating the engine's agent-lifecycle bookkeeping.
func DefaultAsyncManager() *AsyncAgentManager {
	return internalagent.GetDefaultAsyncManager()
}

// HostDefinitionError is why one host-supplied agent definition was refused.
type HostDefinitionError = internalagent.HostDefinitionError

// ErrShadowsExistingAgent is wrapped by the HostDefinitionError of a definition
// whose type is already used by a built-in or skill-defined agent.
var ErrShadowsExistingAgent = internalagent.ErrShadowsExistingAgent
