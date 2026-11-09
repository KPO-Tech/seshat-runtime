package engine

import (
	"errors"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ErrNothingToRewind is returned by Session.RewindLastTurn when no turn can be taken back: the conversation is empty, or the
// last turn starts inside what a compaction preserved, so removing it would leave the conversation inconsistent.
var ErrNothingToRewind = errors.New("there is no turn to rewind")

// RewindLastTurn takes the last turn out of the conversation: the prompt of the user that started it and everything the agent
// did after it, calls, results and an error note included. The next prompt then starts from the conversation as it was before,
// which is how an answer is asked again without the first attempt staying in what the model sees.
//
// It refuses while a turn is running. It returns the number of messages removed.
func (s *Session) RewindLastTurn() (int, error) {
	if err := s.beginTurn(); err != nil {
		return 0, err
	}
	defer s.endTurn()

	previous := s.state.CloneMessages()
	cut, ok := lastTurnStart(previous)
	if !ok {
		return 0, ErrNothingToRewind
	}
	kept := append([]types.Message(nil), previous[:cut]...)
	s.state.ReplaceMessages(kept)
	if s.state.Metadata != nil {
		s.state.Metadata.UpdatedAt = currentTime()
	}
	if err := s.persistSessionState(previous); err != nil {
		return 0, fmt.Errorf("failed to persist the rewound session: %w", err)
	}
	return len(previous) - cut, nil
}

// lastTurnStart is the index of the first message of the last turn. The turn is the run of messages that share the turn id of
// the last message; when the messages carry none, it starts at the last message the user wrote.
func lastTurnStart(messages []types.Message) (int, bool) {
	last := len(messages) - 1
	for last >= 0 && types.IsCompactionArtifact(messages[last]) {
		last--
	}
	if last < 0 {
		return 0, false
	}
	start := last
	if turn := turnIDOf(messages[last]); turn != "" {
		for start > 0 && turnIDOf(messages[start-1]) == turn && !types.IsCompactionArtifact(messages[start-1]) {
			start--
		}
	} else {
		for start > 0 && !(messages[start].Role == types.RoleUser && !isToolResultMessage(messages[start])) {
			start--
		}
	}
	if start == 0 && messages[0].Role != types.RoleUser {
		// A conversation cannot open on the agent's own message.
		return 0, false
	}
	// What a compaction preserved is described by its marker: a cut inside it would make the description wrong.
	if meta, _, tailStart, ok := types.ActiveCompactionBoundary(messages); ok && start < tailStart+meta.PreservedMessages {
		return 0, false
	}
	return start, true
}

func turnIDOf(message types.Message) string {
	if message.Metadata == nil {
		return ""
	}
	return message.Metadata.TurnID
}
