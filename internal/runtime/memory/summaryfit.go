package memory

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// A summary request carries everything a compaction drops. It must fit the window of the model that writes the
// summary, which can be smaller than the session's (a cheaper model), and which a conversation that jumped past the
// threshold can exceed. opencode meets this by shrinking the request to 70 %, 50 % and 35 % of the size the provider
// rejected, and by leaving out the oldest exchanges first; this does the same.

const (
	// summaryOutputReserve is the room left in the window for the summary itself, which may be asked for up to
	// maxSummaryRetryTokens, and summaryPromptMargin the room left for what the estimate gets wrong.
	summaryOutputReserve = maxSummaryRetryTokens
	summaryPromptMargin  = 2_000

	// minSummaryPromptTokens is the least a summary request is allowed to be shrunk to.
	minSummaryPromptTokens = 2_000
)

// summaryShrinkSteps are the shares of the size of a rejected request that the next attempt aims at.
var summaryShrinkSteps = []float64{0.7, 0.5, 0.35}

// summaryPromptBudget is how many tokens of conversation a request to model may carry.
func summaryPromptBudget(model types.ModelIdentifier) int {
	window := types.GetContextWindow(model).MaxTokens
	if window <= 0 {
		return 1 << 30 // an unknown window cannot be planned for; only a rejection can limit the request
	}
	return max(minSummaryPromptTokens, window-summaryOutputReserve-summaryPromptMargin)
}

// estimateTextTokens is the usual four characters to the token.
func estimateTextTokens(s string) int { return (len(s) + 3) / 4 }

// summaryLines renders each message as the line the summarizer is shown.
func (e *Engine) summaryLines(messages []types.Message) []string {
	lines := make([]string, 0, len(messages))
	for _, msg := range messages {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("[%s] ", msg.Role))
		for _, block := range msg.Content {
			switch content := block.(type) {
			case types.TextContent:
				b.WriteString(content.Text)
			case types.ToolUseContent:
				b.WriteString(fmt.Sprintf("Tool use: %s %s ", content.Name, clipForSummary(toolInputText(content.Input), summaryToolInputChars)))
			case types.ToolResultContent:
				b.WriteString(fmt.Sprintf("Tool result (%s): %s ", e.microCompactor.CalculateToolResultSummary(content), clipForSummary(content.Content, summaryToolResultChars)))
			}
		}
		b.WriteString("\n")
		lines = append(lines, b.String())
	}
	return lines
}

// buildSummaryPromptWithin is buildSummaryPrompt for a request of at most maxTokens: when the conversation is larger,
// the oldest messages are left out, and the prompt says how many. The newest message is always kept. It returns the
// prompt and the number of messages left out.
func (e *Engine) buildSummaryPromptWithin(messages []types.Message, maxTokens int) (string, int) {
	lines := e.summaryLines(messages)
	used := estimateTextTokens(summaryInstructions)
	for _, l := range lines {
		used += estimateTextTokens(l)
	}
	omitted := 0
	for used > maxTokens && omitted < len(lines)-1 {
		used -= estimateTextTokens(lines[omitted])
		omitted++
	}
	var b strings.Builder
	b.WriteString(summaryInstructions)
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("[%d older messages are left out: the conversation is too long to be summarized at once.]\n", omitted))
	}
	for _, l := range lines[omitted:] {
		b.WriteString(l)
	}
	return b.String(), omitted
}

// contextOverflowPhrases are what providers say when a request is longer than the model's window. There is no common
// error code for it, so the message is matched.
var contextOverflowPhrases = []string{
	"context length", "context_length", "maximum context", "context window", "too many tokens", "prompt is too long",
	"prompt too long", "input is too long", "exceeds the model", "request too large", "reduce the length",
}

// isContextOverflow reports whether err is a provider refusing a request as too long for the model.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range contextOverflowPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// summarySectionNumber matches the start of a numbered section of the summary: "1." or "2)", possibly after "#".
var summarySectionNumber = regexp.MustCompile(`(?m)^\s*(?:#+\s*)?(?:\*\*)?([1-6])[.)]`)

// minSummarySections is how many of the six numbered sections a reply must show to count as following the format.
const minSummarySections = 4

// followsSummaryFormat reports whether text shows enough of the numbered sections that were asked for.
func followsSummaryFormat(text string) bool {
	seen := map[string]bool{}
	for _, m := range summarySectionNumber.FindAllStringSubmatch(text, -1) {
		seen[m[1]] = true
	}
	return len(seen) >= minSummarySections
}

const summaryFormatReminder = "Your reply did not follow the six numbered sections. Write the summary again with exactly those six " +
	"numbered sections, in order, a section with nothing to say written \"none\". Return only the summary."

// maxSummaryRequests bounds the requests one summary may take: shrinking after a rejection, a larger output budget
// after an empty reply, one reminder about the format.
const maxSummaryRequests = 6

// requestSummary asks model for the summary of messages and returns its text. It copes with what a summary request
// meets in practice:
//   - a request longer than the model's window is shrunk before it is sent, and again, to 70 %, 50 % then 35 % of its
//     size, each time a provider rejects it as too long;
//   - a reasoning model that spends its whole output budget thinking, and so returns no text, is asked again with a
//     larger budget (and told not to reason, where the provider has a switch for it);
//   - a reply that does not follow the numbered sections is sent back once with a reminder; if the answer is still
//     not in the format it is used anyway, because a summary that is a little off beats a compaction that fails.
func (e *Engine) requestSummary(ctx context.Context, messages []types.Message, model types.ModelIdentifier, sessionID types.SessionID, turnID types.TurnID) (string, error) {
	promptBudget := summaryPromptBudget(model)
	outputBudget := e.config.MaxSummaryTokens
	shrinks, empties := 0, 0
	var lastStop, pending string

	for request := 0; request < maxSummaryRequests; request++ {
		prompt, _ := e.buildSummaryPromptWithin(messages, promptBudget)
		conversation := []types.Message{types.UserMessage("compact_summary_request", prompt)}
		if pending != "" {
			conversation = append(conversation, types.AssistantMessage("compact_summary_draft", []types.ContentBlock{types.TextContent{Text: pending}}), types.UserMessage("compact_summary_reminder", summaryFormatReminder))
		}
		resp, err := e.apiClient.CreateMessage(ctx, types.APIRequest{
			Model:           model,
			Messages:        conversation,
			MaxTokens:       outputBudget,
			DisableThinking: true,
		})
		if err != nil {
			if isContextOverflow(err) && shrinks < len(summaryShrinkSteps) {
				promptBudget = max(minSummaryPromptTokens, int(float64(estimateTextTokens(prompt))*summaryShrinkSteps[shrinks]))
				shrinks++
				continue
			}
			return "", err
		}

		text := firstNonEmptyText(resp.Content)
		switch {
		case text == "" && pending != "":
			return pending, nil // the reminder produced nothing: the first draft stands
		case text == "":
			lastStop = resp.StopReason
			empties++
			if empties >= summaryAttempts {
				return "", fmt.Errorf("no summary generated for session %s turn %s (stop reason %q after %d attempts)", sessionID, turnID, lastStop, empties)
			}
			outputBudget = min(outputBudget*summaryBudgetGrowth, maxSummaryRetryTokens)
		case pending == "" && !followsSummaryFormat(text):
			pending = text
		default:
			return text, nil
		}
	}
	return "", fmt.Errorf("no summary generated for session %s turn %s after %d requests", sessionID, turnID, maxSummaryRequests)
}

func firstNonEmptyText(blocks []types.ContentBlock) string {
	for _, block := range blocks {
		if text, ok := block.(types.TextContent); ok && strings.TrimSpace(text.Text) != "" {
			return text.Text
		}
	}
	return ""
}
