package memory

import (
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const summarySections = `1. What the user asked for: the goal, and any change of direction since.
2. Decisions made, and the reason for each.
3. What has been done: files changed, commands run, results that still matter.
4. Where things stand now, and what remains, as concrete next steps.
5. Facts to keep exactly: paths, names, ids, numbers, error messages, URLs.
6. The user's preferences and constraints.`

// summaryInstructions asks for a first summary. Every section is required, written "none" when empty, so that a
// section is never dropped silently.
const summaryInstructions = `You are writing a handoff for another model that will carry on this work with only your summary and the last few messages. It cannot see anything you leave out. Write these sections, in this order, each under its own number:

` + summarySections + `

A section with nothing to say is written "none", never left out. Be concise and specific. Do not invent anything that is not in the conversation.

Conversation:

`

// summaryUpdateInstructions asks for an updated summary. The model is given the summary an earlier compaction wrote
// and only what happened since, so the details the earlier summary kept are not lost to a rewrite from scratch.
const summaryUpdateInstructions = `You are updating the handoff of a long task for another model that will carry on with only this summary and the last few messages. Below is the previous summary, then what happened since. Return the whole updated summary, with the same six sections in the same order, each under its own number:

` + summarySections + `

Update it, do not rewrite it:
- keep what still holds, in the same words;
- add what is new;
- change what the new messages changed or contradicted;
- remove only what the new messages show is obsolete.
Never drop an exact fact (a path, a name, an id, a number, an error message) unless the conversation shows it no longer applies. A section with nothing to say is written "none". Do not invent anything.

`

// buildSummaryPrompt is what the summarizing model reads: the instructions, and the conversation to summarize. When
// the conversation starts with the summary of an earlier compaction, that summary is given as the base to update.
func (e *Engine) buildSummaryPrompt(messages []types.Message) string {
	var builder strings.Builder
	if prev, ok := firstPreviousSummary(messages); ok {
		builder.WriteString(summaryUpdateInstructions)
		builder.WriteString("Previous summary:\n\n" + prev.body + "\n\nWhat happened since:\n\n")
		messages = messages[1:]
	} else {
		builder.WriteString(summaryInstructions)
	}
	for _, msg := range messages {
		builder.WriteString(fmt.Sprintf("[%s] ", msg.Role))
		for _, block := range msg.Content {
			switch content := block.(type) {
			case types.TextContent:
				builder.WriteString(content.Text)
			case types.ToolUseContent:
				builder.WriteString(fmt.Sprintf("Tool use: %s %s ", content.Name, clipForSummary(toolInputText(content.Input), summaryToolInputChars)))
			case types.ToolResultContent:
				builder.WriteString(fmt.Sprintf("Tool result (%s): %s ", e.microCompactor.CalculateToolResultSummary(content), clipForSummary(content.Content, summaryToolResultChars)))
			}
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// firstPreviousSummary reports the summary of an earlier compaction when it is the first message.
func firstPreviousSummary(messages []types.Message) (previousSummary, bool) {
	if len(messages) == 0 {
		return previousSummary{}, false
	}
	return previousSummaryOf(messages[0])
}

// summaryText is the message that replaces the dropped part of a conversation: the hand-off prefix, the summary, the
// files touched, and the user's own messages. The files and the user's messages carry over from an earlier
// compaction, so they survive any number of them; the user's messages are kept newest first within
// keptUserMessageTokens and written oldest first.
func (e *Engine) summaryText(dropped []types.Message, summary string) string {
	var prev previousSummary
	if p, ok := firstPreviousSummary(dropped); ok {
		prev = p
	}

	users := append([]string(nil), prev.users...)
	for _, m := range dropped {
		if text, ok := userText(m); ok {
			users = append(users, clipForSummary(text, keptUserMessageChars))
		}
	}
	var kept []string
	budget := keptUserMessageTokens
	for i := len(users) - 1; i >= 0; i-- {
		cost := len(users[i])/4 + 10
		if cost > budget {
			break
		}
		budget -= cost
		kept = append(kept, users[i])
	}

	var b strings.Builder
	b.WriteString(handoffPrefix)
	b.WriteString("\n\n" + summaryMarker)
	b.WriteString(strings.TrimSpace(summary))
	if files := prev.files.merge(collectFileTrail(dropped)); !files.empty() {
		b.WriteString("\n\n" + files.render())
	}
	if len(kept) > 0 {
		b.WriteString("\n\n" + userMessagesMarker)
		for i := len(kept) - 1; i >= 0; i-- {
			b.WriteString(userMessageDivider)
			b.WriteString(kept[i])
		}
	}
	return b.String()
}
