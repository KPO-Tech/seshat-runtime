package memory

import (
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// A summary is a job for a small model that does not reason: the work is reading and condensing, and a reasoning model
// spends its output budget thinking (and, run on z.ai's glm-5.3-flash, took 200 seconds where glm-4.5 took 20). So the
// model that writes the summary can differ from the one that runs the session, and is chosen by rules.

// SummaryModelRule says which model writes the summary for a session on a given provider and model.
type SummaryModelRule struct {
	// Provider is the provider of the session this rule applies to. Empty applies to any provider.
	Provider types.APIProvider `json:"provider,omitempty"`

	// Model is the session model this rule applies to: an exact name, or a name ending in "*" for a prefix
	// ("glm-*"). Empty applies to any model of the provider.
	Model string `json:"model,omitempty"`

	// Use is the model that writes the summary. An empty Use.Provider means the session's own provider: a rule written
	// without a provider stays at the vendor the session already trusts with its conversation, and so with its key.
	Use types.ModelIdentifier `json:"use"`
}

func (r SummaryModelRule) matches(session types.ModelIdentifier) bool {
	if r.Provider != "" && r.Provider != session.Provider {
		return false
	}
	switch {
	case r.Model == "":
		return true
	case strings.HasSuffix(r.Model, "*"):
		return strings.HasPrefix(session.Model, strings.TrimSuffix(r.Model, "*"))
	default:
		return r.Model == session.Model
	}
}

// summaryRuleReserve is the room a rule's model needs beyond the request itself: the summary it writes, and a margin for
// what the estimate gets wrong.
const summaryRuleReserve = maxSummaryRetryTokens + 2_000

// defaultSummaryModelRules are the built-in choices. They stay within the session's provider, and each one has been
// measured: only a model of the same family that does not need to reason qualifies. A provider that is not listed gets
// the session's own model until a measurement shows a better one.
//
// z.ai: glm-4.5-air wrote the summary of a 107,000-token conversation in 16 s against 22 s for glm-4.5 and glm-4.7,
// for the same facts kept and the same size after compaction (2026-10-18, thinking disabled).
// Anthropic: the Haiku model, which does not think unless asked to; not measured here.
var defaultSummaryModelRules = []SummaryModelRule{
	{Provider: types.APIProviderZAi, Model: "glm-*", Use: types.ModelIdentifier{Model: "glm-4.5-air"}},
	{Provider: types.APIProviderAnthropic, Model: "claude-*", Use: types.ModelIdentifier{Model: "claude-haiku-5-5"}},
}

// resolveSummaryModel returns the model that writes the summary for a session on session, given that the summary
// request is promptTokens long. In order: the explicit SummaryModel of the configuration; the rules of the
// configuration, first match; the built-in rules, unless switched off; the session's own model.
//
// A model chosen by a rule (not the explicit one) must have a window that holds the request and its summary,
// otherwise the rule is skipped: a cheaper model with a smaller window would have to be shown less of the
// conversation, and the session's own model sees all of it.
func (e *Engine) resolveSummaryModel(session types.ModelIdentifier, promptTokens int) types.ModelIdentifier {
	if e.config.SummaryModel.Model != "" {
		return e.config.SummaryModel
	}
	rules := e.config.SummaryModelRules
	if !e.config.NoDefaultSummaryModels {
		rules = append(append([]SummaryModelRule(nil), rules...), defaultSummaryModelRules...)
	}
	for _, r := range rules {
		if !r.matches(session) || r.Use.Model == "" {
			continue
		}
		use := r.Use
		if use.Provider == "" {
			use.Provider = session.Provider
		}
		if use == session {
			return session
		}
		if window := types.GetContextWindow(use).MaxTokens; window > 0 && window < promptTokens+summaryRuleReserve {
			continue
		}
		return use
	}
	return session
}
