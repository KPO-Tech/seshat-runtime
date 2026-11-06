package agents

import (
	"strings"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
)

// builtInAgentTypeEnum is the type list when no registry is wired.
var builtInAgentTypeEnum = []string{"general-purpose", "explore", "browse", "plan", "verify"}

// agentTypeEnum lists the agent types the model may name: the built-in ones,
// then those the host registered (sorted). Other registry entries (internal
// agents, skill-defined ones) stay out of the schema as they always did. The
// result is a fresh slice in a stable order.
func agentTypeEnum(reg *coreagent.AgentRegistry) []string {
	out := append([]string(nil), builtInAgentTypeEnum...)
	if reg == nil {
		return out
	}
	for _, def := range reg.HostDefinitions() {
		out = append(out, def.AgentType)
	}
	return out
}

// maxWhenToUseLen bounds what a host-supplied description adds to the prompt.
const maxWhenToUseLen = 300

// hostAgentTypesSection describes the agent types the host registered, so the
// model knows when to pick them; empty when there are none.
func hostAgentTypesSection(reg *coreagent.AgentRegistry) string {
	if reg == nil {
		return ""
	}
	defs := reg.HostDefinitions()
	if len(defs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Additional agent types\n")
	for _, def := range defs {
		sb.WriteString("- `")
		sb.WriteString(def.AgentType)
		sb.WriteString("`: ")
		sb.WriteString(oneLine(def.WhenToUse, maxWhenToUseLen))
		sb.WriteString("\n")
	}
	return sb.String()
}

// oneLine collapses whitespace (newlines included) and truncates to max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}
