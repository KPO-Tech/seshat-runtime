package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	skills "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"
)

// AgentRegistry is a unified registry for built-in and skill-derived agents.
// Built-in agents are pre-loaded at construction; dynamic agents are loaded
// via LoadFromSkills. Skill-defined agents never shadow built-in ones.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*AgentDefinition
}

// NewAgentRegistry creates a registry pre-populated with all built-in agents.
func NewAgentRegistry() *AgentRegistry {
	r := &AgentRegistry{agents: make(map[string]*AgentDefinition, len(BuiltInAgents))}
	for _, b := range BuiltInAgents {
		def := ToAgentDefinition(b)
		r.agents[def.AgentType] = def
	}
	return r
}

// Register adds or replaces an agent definition. Built-in agents can be
// overridden explicitly via this method (e.g. from tests or admin tooling).
func (r *AgentRegistry) Register(def *AgentDefinition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[def.AgentType] = def
}

// Get returns the agent definition for the given slug, or (nil, false).
func (r *AgentRegistry) Get(slug string) (*AgentDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.agents[slug]
	return def, ok
}

// All returns a snapshot of all registered agent definitions.
func (r *AgentRegistry) All() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentDefinition, 0, len(r.agents))
	for _, def := range r.agents {
		result = append(result, def)
	}
	return result
}

// LoadFromSkills scans all skills visible to the given user and registers any
// that carry an `agent:` frontmatter field. Skill-defined agents do not
// override built-in agents (Source == AgentSourceBuiltIn).
func (r *AgentRegistry) LoadFromSkills(cwd, userID string) error {
	all, err := skills.GetAllSkillsForUser(cwd, userID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range all {
		sk := &all[i]
		if sk.Agent == "" {
			continue
		}
		def := skillToAgentDefinition(sk)
		// Never override built-ins, nor what the host registered, from skill files.
		if existing, ok := r.agents[def.AgentType]; ok && (existing.Source == AgentSourceBuiltIn || existing.Source == AgentSourceHost) {
			continue
		}
		r.agents[def.AgentType] = def
	}
	return nil
}

// skillToAgentDefinition converts a Skill with a non-empty Agent field into
// an AgentDefinition. The skill's markdown body becomes the agent's system
// prompt; AllowedTools, Model, and Effort map to the corresponding fields.
func skillToAgentDefinition(sk *skills.Skill) *AgentDefinition {
	slug := sk.Agent // e.g. "skill-creator"

	getPromptFn := sk.GetPromptForCommand
	getSystemPrompt := func() string {
		if getPromptFn == nil {
			return ""
		}
		blocks, err := getPromptFn("", context.Background())
		if err != nil || len(blocks) == 0 {
			return ""
		}
		var sb strings.Builder
		for _, b := range blocks {
			sb.WriteString(b.Text)
		}
		return sb.String()
	}

	return &AgentDefinition{
		AgentType:       slug,
		WhenToUse:       firstNonEmpty(sk.WhenToUse, sk.Description),
		Source:          AgentSourceUser,
		BaseDir:         sk.SkillRoot,
		Filename:        sk.Name + "/skill.md",
		Tools:           sk.AllowedTools,
		Model:           sk.Model,
		MaxTurns:        effortToMaxTurns(sk.Effort),
		GetSystemPrompt: getSystemPrompt,
	}
}

// effortToMaxTurns converts a skill effort string to a sensible MaxTurns value.
func effortToMaxTurns(effort string) int {
	switch effort {
	case "minimal":
		return 10
	case "low":
		return 20
	case "high":
		return 100
	case "maximum":
		return 150
	default: // "medium" or unset
		return 50
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// agentTypePattern is what a host-registered agent type must look like:
// lowercase letters, digits and dashes, starting and ending with a letter or
// digit, at most 64 characters.
var agentTypePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ValidateHostAgentType reports why name cannot be the type of a
// host-registered agent; nil when it can.
func ValidateHostAgentType(name string) error {
	if !agentTypePattern.MatchString(name) {
		return fmt.Errorf("agent type %q is invalid: use 1-64 lowercase letters, digits and dashes, starting and ending with a letter or digit", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("agent type %q is invalid: no consecutive dashes", name)
	}
	return nil
}

// HostDefinitionError is why one host-supplied definition was refused.
type HostDefinitionError struct {
	AgentType string
	Err       error
}

func (e *HostDefinitionError) Error() string {
	return fmt.Sprintf("agent definition %q rejected: %v", e.AgentType, e.Err)
}

func (e *HostDefinitionError) Unwrap() error { return e.Err }

// ErrShadowsExistingAgent is wrapped by the HostDefinitionError of a
// definition whose type is already taken by a built-in or skill-defined agent.
var ErrShadowsExistingAgent = errors.New("agent type is already used by a built-in or skill-defined agent")

// ReplaceHostDefinitions makes defs the whole set of host-registered agents:
// every host definition registered before is dropped, the valid ones in defs
// are installed, and the swap happens under one lock so a concurrent reader
// sees the old set or the new one, never a mix.
//
// A definition is refused, and the others still installed, when its type is
// not a valid name (ValidateHostAgentType), is listed twice, is nil, or is the
// type of a built-in or skill-defined agent (ErrShadowsExistingAgent): a host
// can add agent types, never replace one. The returned error joins one
// *HostDefinitionError per refused definition; nil when all were installed.
//
// What is stored is a private copy with Source forced to AgentSourceHost, so
// later changes by the host to the slices it passed cannot change a policy
// already in force. Fields only narrow what the parent allows: the permission
// mode is clamped to the spawning session's, Tools and DisallowedTools apply
// as for any agent.
func (r *AgentRegistry) ReplaceHostDefinitions(defs []*AgentDefinition) error {
	accepted := make(map[string]*AgentDefinition, len(defs))
	var errs []error
	reject := func(name string, err error) {
		errs = append(errs, &HostDefinitionError{AgentType: name, Err: err})
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, def := range defs {
		if def == nil {
			reject("", errors.New("definition is nil"))
			continue
		}
		name := def.AgentType
		if err := ValidateHostAgentType(name); err != nil {
			reject(name, err)
			continue
		}
		if _, dup := accepted[name]; dup {
			reject(name, errors.New("listed more than once"))
			continue
		}
		if existing, ok := r.agents[name]; ok && existing.Source != AgentSourceHost {
			reject(name, ErrShadowsExistingAgent)
			continue
		}
		accepted[name] = copyHostDefinition(def)
	}

	for name, existing := range r.agents {
		if existing.Source == AgentSourceHost {
			delete(r.agents, name)
		}
	}
	for name, def := range accepted {
		r.agents[name] = def
	}
	return errors.Join(errs...)
}

// copyHostDefinition returns an independent copy of def with Source set to
// AgentSourceHost.
func copyHostDefinition(def *AgentDefinition) *AgentDefinition {
	c := *def
	c.Source = AgentSourceHost
	c.Tools = append([]string(nil), def.Tools...)
	c.DisallowedTools = append([]string(nil), def.DisallowedTools...)
	c.Skills = append([]string(nil), def.Skills...)
	c.McpServers = append([]string(nil), def.McpServers...)
	return &c
}

// HostDefinitions returns copies of the host-registered definitions, sorted by type.
func (r *AgentRegistry) HostDefinitions() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*AgentDefinition
	for _, def := range r.agents {
		if def.Source == AgentSourceHost {
			out = append(out, copyHostDefinition(def))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentType < out[j].AgentType })
	return out
}
