package sdk

import (
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/pkg/agent"
)

// SetAgentDefinitions replaces the whole set of agent types the host
// registers on this client, live: unlike ClientConfig, it can be called after
// NewClient, as often as needed, and from any goroutine.
//
// The agent tool, spawn_agent and resume_agent then accept those types (the
// tools' schemas list them), and RunAgentTask resolves them. Passing nil or an
// empty slice removes every host-registered type; a type left out of defs is
// removed. The swap is atomic: a concurrent spawn sees the old set or the new
// one, never a mix. Agents already running keep the definition they started
// with.
//
// A definition is rejected, and the others still installed, when its
// AgentType is not 1-64 lowercase letters, digits and dashes (starting and
// ending with a letter or digit), is listed twice, is nil, or is the name of a
// built-in agent type (general-purpose, explore, browse, plan, verify) or of a
// skill-defined agent. A host adds agent types; it cannot shadow or replace
// one, and there is no namespacing: a clashing definition is refused. The
// returned error joins one *HostDefinitionError per rejected
// definition (use errors.As / errors.Is with agent.ErrShadowsExistingAgent);
// the accepted ones are live even when an error is returned.
//
// Definitions only narrow what the spawning session allows:
//   - PermissionMode is clamped to the spawning session's mode (the stricter of
//     the two wins) and an unknown value is treated as stricter, never looser;
//   - Tools is an allow list ("*", "prefix*", "*suffix" or an exact name; empty
//     means all tools) and DisallowedTools a deny list in the same syntax. Deny
//     wins over allow, also for tools found later through tool_search, and a
//     sub-agent never regains a tool its parent lost;
//   - MaxTurns is the turn budget when the caller names none.
//
// Source is ignored (the registry records the definition as host-supplied),
// and the slices are copied, so changing them afterwards has no effect. Model,
// Isolation and McpServers are not applied yet.
func (c *Client) SetAgentDefinitions(defs []*agent.AgentDefinition) error {
	if c == nil || c.agentRegistry == nil {
		return fmt.Errorf("client is not initialised")
	}
	return c.agentRegistry.ReplaceHostDefinitions(defs)
}

// AgentDefinitions returns a copy of the definitions currently registered with
// SetAgentDefinitions, sorted by AgentType.
func (c *Client) AgentDefinitions() []*agent.AgentDefinition {
	if c == nil || c.agentRegistry == nil {
		return nil
	}
	return c.agentRegistry.HostDefinitions()
}

// HostDefinitionError is why SetAgentDefinitions refused one definition.
type HostDefinitionError = agent.HostDefinitionError
