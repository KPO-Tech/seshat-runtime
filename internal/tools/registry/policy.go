package registry

// MatchesToolPattern reports whether a tool name matches any of the patterns.
//
// This is the one pattern syntax shared by AgentDefinition.Tools,
// AgentDefinition.DisallowedTools, the agent tool's `tools` input and
// RunConfig.ToolFilter, so an allow list and a deny list mean the same thing:
// "*" matches everything, "prefix*" matches a prefix, "*suffix" matches a
// suffix, anything else is an exact name.
func MatchesToolPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == name {
			return true
		}
		if len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
			prefix := pattern[:len(pattern)-1]
			if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
				return true
			}
		}
		if len(pattern) > 1 && pattern[0] == '*' {
			suffix := pattern[1:]
			if len(name) >= len(suffix) && name[len(name)-len(suffix):] == suffix {
				return true
			}
		}
	}
	return false
}

// IsUnrestrictedToolPatterns reports whether an allow list restricts nothing:
// it is empty, or a bare "*".
func IsUnrestrictedToolPatterns(patterns []string) bool {
	return len(patterns) == 0 || (len(patterns) == 1 && patterns[0] == "*")
}

// ToolPolicy is the set of rules deciding which tools a session may use. Rules
// only ever accumulate (Narrow), so a child policy can drop tools its parent
// had but never regain one the parent lost.
//
//   - Allow holds layers. A tool must satisfy EVERY layer. A nil layer
//     restricts nothing; a non-nil empty layer allows nothing.
//   - Deny holds patterns. A tool matching any of them is never available,
//     whatever the Allow layers say: deny wins over allow.
//
// The zero value permits every tool.
type ToolPolicy struct {
	Allow [][]string
	Deny  []string
}

// IsZero reports whether the policy restricts nothing.
func (p ToolPolicy) IsZero() bool {
	if len(p.Deny) > 0 {
		return false
	}
	for _, layer := range p.Allow {
		if layer != nil && (len(layer) == 0 || !IsUnrestrictedToolPatterns(layer)) {
			return false
		}
	}
	return true
}

// Narrow returns a copy of p with one more allow layer (nil means none) and
// more deny patterns. The receiver is not modified.
func (p ToolPolicy) Narrow(allow []string, deny []string) ToolPolicy {
	out := ToolPolicy{
		Allow: make([][]string, 0, len(p.Allow)+1),
		Deny:  make([]string, 0, len(p.Deny)+len(deny)),
	}
	for _, layer := range p.Allow {
		out.Allow = append(out.Allow, cloneLayer(layer))
	}
	if allow != nil && !IsUnrestrictedToolPatterns(allow) {
		out.Allow = append(out.Allow, append([]string{}, allow...))
	} else if allow != nil && len(allow) == 0 {
		// A non-nil empty layer is "no tool at all", not "no restriction".
		out.Allow = append(out.Allow, []string{})
	}
	out.Deny = append(out.Deny, p.Deny...)
	for _, d := range deny {
		if d != "" {
			out.Deny = append(out.Deny, d)
		}
	}
	return out
}

// Merge returns the policy that satisfies both p and other.
func (p ToolPolicy) Merge(other ToolPolicy) ToolPolicy {
	out := p.Narrow(nil, other.Deny)
	for _, layer := range other.Allow {
		out.Allow = append(out.Allow, cloneLayer(layer))
	}
	return out
}

// Permits reports whether a tool may be used. names are every name the tool
// answers to (its canonical name and its aliases): a deny pattern matching any
// of them removes the tool, so an alias cannot be used to slip past a deny of
// the canonical name. An allow layer is satisfied when any of the names
// matches it.
func (p ToolPolicy) Permits(names ...string) bool {
	for _, name := range names {
		if MatchesToolPattern(name, p.Deny) {
			return false
		}
	}
	for _, layer := range p.Allow {
		if layer != nil && len(layer) == 0 {
			return false
		}
		if IsUnrestrictedToolPatterns(layer) {
			continue
		}
		matched := false
		for _, name := range names {
			if MatchesToolPattern(name, layer) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// PermitsTool is Permits for a registered tool, using its key in the session
// map plus its canonical name and aliases.
func (p ToolPolicy) PermitsTool(key string, t Tool) bool {
	if p.IsZero() {
		return true
	}
	names := []string{key}
	if t != nil {
		def := t.Definition()
		names = append(names, def.Name)
		names = append(names, def.Aliases...)
	}
	return p.Permits(names...)
}

// cloneLayer copies an allow layer, keeping a nil layer nil (no restriction)
// and a non-nil empty one non-nil (nothing allowed).
func cloneLayer(layer []string) []string {
	if layer == nil {
		return nil
	}
	return append([]string{}, layer...)
}
