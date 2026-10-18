package memory

import (
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// A compaction is not a one-off: a long task compacts again and again. Regenerating the summary from scratch each
// time makes it drift (details dropped at each pass are gone for good), so the next compaction starts from the
// previous summary and updates it. For that the summary message is written in a form that can be read back: the
// model's summary, the file trail, and the user's earlier messages, each behind a fixed marker.

const (
	summaryMarker      = "Summary:\n\n"
	userMessagesMarker = "The user's earlier messages, word for word:\n"
	userMessageDivider = "\n---\n"
)

// previousSummary is what an earlier compaction left in the conversation.
type previousSummary struct {
	body  string
	files fileTrail
	users []string
}

// previousSummaryOf reads the summary message a compaction wrote earlier, or reports that msg is not one.
func previousSummaryOf(msg types.Message) (previousSummary, bool) {
	if msg.ID != compactSummaryMessageID {
		return previousSummary{}, false
	}
	var text strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.(types.TextContent); ok {
			text.WriteString(t.Text)
		}
	}
	all := text.String()
	start := strings.Index(all, summaryMarker)
	if start < 0 {
		return previousSummary{}, false
	}
	rest := all[start+len(summaryMarker):]

	usersAt := strings.Index(rest, userMessagesMarker)
	filesAt := strings.Index(rest, filesMarker)
	end := len(rest)
	for _, at := range []int{usersAt, filesAt} {
		if at >= 0 && at < end {
			end = at
		}
	}
	prev := previousSummary{body: strings.TrimSpace(rest[:end])}
	if filesAt >= 0 {
		block := rest[filesAt:]
		if usersAt > filesAt {
			block = rest[filesAt:usersAt]
		}
		prev.files = parseFileTrail(block)
	}
	if usersAt >= 0 {
		for _, u := range strings.Split(rest[usersAt+len(userMessagesMarker):], userMessageDivider) {
			if u = strings.TrimSpace(u); u != "" {
				prev.users = append(prev.users, u)
			}
		}
	}
	return prev, true
}
