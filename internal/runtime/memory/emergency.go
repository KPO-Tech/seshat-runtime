package memory

import (
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// emergencyMarkerText stands in for the part of a conversation that was dropped without a summary.
const emergencyMarkerText = "[The earlier part of this conversation was removed to fit the model, because it could not be summarized. " +
	"Facts from it may be missing: ask the user if something you need is not in the messages below.]"

// EmergencyTrim shrinks a conversation without asking a model, for the moment a summary could not be written (the model
// returns nothing, the provider is down, the circuit breaker is open) and the request is over the hard limit. Before
// this, such a conversation ended the turn with an error, and once the breaker had opened nothing ever shrank it.
//
// It trims the large tool results, and if that is not enough it drops the oldest messages, keeping the most recent ones
// up to the compaction target, behind a marker that says so. The dropped messages are archived by the session store
// like those of any compaction. It reports false when it changed nothing: the conversation is already within the
// target, or there is nothing older than the last message to drop.
func (e *Engine) EmergencyTrim(systemPrompt string, messages []types.Message, model types.ModelIdentifier, tracking *TrackingState) (CompactionResult, bool) {
	pre := e.UsedTokens(systemPrompt, messages, tracking)
	target := e.TargetTokens(model)
	// What replaces the messages is measured by estimate, not by the provider's figure for the old ones.
	rewritten := &TrackingState{}
	if tracking != nil {
		rewritten.ExtraTokens = tracking.ExtraTokens
	}

	trimmed := e.microCompact(messages)
	if post := e.UsedTokens(systemPrompt, trimmed, rewritten); post <= target {
		if post >= pre {
			return CompactionResult{}, false
		}
		return CompactionResult{Messages: trimmed, DidCompact: true, UsedMicroCompact: true, PreCompactTokens: pre, PostCompactTokens: post, TargetTokens: target}, true
	}

	start := e.findPreservedStart(trimmed, target)
	if start <= 0 {
		return CompactionResult{}, false
	}
	marker := types.SystemMessage(compactMarkerMessageID, emergencyMarkerText)
	marker.Metadata = &types.MessageMetadata{TurnID: "compact", Compaction: &types.CompactionMetadata{Kind: "emergency_trim", PreCompactTokens: pre, TargetTokens: target}}
	out := append([]types.Message{marker}, types.CloneMessages(trimmed[start:])...)
	post := e.UsedTokens(systemPrompt, out, rewritten)
	marker.Metadata.Compaction.PostCompactTokens = post
	out[0] = marker
	return CompactionResult{Messages: out, DidCompact: true, UsedMicroCompact: true, PreCompactTokens: pre, PostCompactTokens: post, TargetTokens: target}, true
}
