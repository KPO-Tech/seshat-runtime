package memory

import (
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// MessagePreparer prepares messages for compaction
type MessagePreparer struct {
	// microCompactor is the micro compactor
	microCompactor *MicroCompactor

	// turnCounter counts turns
	turnCounter int
}

// NewMessagePreparer creates a new message preparer
func NewMessagePreparer() *MessagePreparer {
	return &MessagePreparer{
		microCompactor: NewMicroCompactor(),
		turnCounter:    0,
	}
}

// PrepareMessages prepares messages for the API request
func (p *MessagePreparer) PrepareMessages(
	messages []types.Message,
	maxTurns int,
) []types.Message {
	// If no limit, return as-is
	if maxTurns <= 0 {
		return messages
	}

	// Count turns and trim if necessary
	turnCount := p.countTurns(messages)

	if turnCount <= maxTurns {
		return messages
	}

	// Need to trim: keep last maxTurns turns
	return p.keepLastTurns(messages, maxTurns)
}

// PrepareMessageForAPI prepares a single message for the API
func (p *MessagePreparer) PrepareMessageForAPI(msg types.Message) types.Message {
	// Trim tool results if needed. msg is a copy, but its Content slice is shared with the caller's message: write
	// into a copy of it, or trimming for one request would also trim the conversation the caller keeps.
	content := append([]types.ContentBlock(nil), msg.Content...)
	for i, block := range content {
		if result, ok := block.(types.ToolResultContent); ok {
			content[i] = p.microCompactor.TrimToolResult(result)
		}
	}
	msg.Content = content

	return msg
}

// PrepareToolUse prepares a tool use for execution
func (p *MessagePreparer) PrepareToolUse(toolUse types.ToolUseContent) types.ToolUseContent {
	// Ensure input is properly formatted
	if toolUse.Input == nil {
		toolUse.Input = make(map[string]any)
	}

	return toolUse
}

// PrepareToolResult prepares a tool result for storage
func (p *MessagePreparer) PrepareToolResult(result types.ToolResultContent) types.ToolResultContent {
	// Trim if needed
	return p.microCompactor.TrimToolResult(result)
}

// SummarizeToolUses summarizes tool uses in a message
func (p *MessagePreparer) SummarizeToolUses(msg types.Message) string {
	toolUseCount := 0
	for _, block := range msg.Content {
		if _, ok := block.(types.ToolUseContent); ok {
			toolUseCount++
		}
	}

	if toolUseCount == 0 {
		return ""
	}

	return fmt.Sprintf("[%d tool use(s)]", toolUseCount)
}

// countTurns counts the number of turns in messages
func (p *MessagePreparer) countTurns(messages []types.Message) int {
	maxTurnID := 0

	for _, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.TurnID != "" {
			// Extract turn number from turn ID
			// Assuming format: turn_<number>
			var turnNum int
			fmt.Sscanf(msg.Metadata.TurnID, "turn_%d", &turnNum)
			if turnNum > maxTurnID {
				maxTurnID = turnNum
			}
		}
	}

	return maxTurnID
}

// keepLastTurns keeps only the last N turns
func (p *MessagePreparer) keepLastTurns(messages []types.Message, maxTurns int) []types.Message {
	// Find the starting turn
	startTurn := p.countTurns(messages) - maxTurns

	if startTurn < 1 {
		startTurn = 1
	}

	// Filter messages
	filtered := make([]types.Message, 0)
	for _, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.TurnID != "" {
			var turnNum int
			fmt.Sscanf(msg.Metadata.TurnID, "turn_%d", &turnNum)

			if turnNum >= startTurn {
				filtered = append(filtered, msg)
			}
		} else {
			// Keep system messages and messages without turn ID
			if msg.Role == types.RoleSystem {
				filtered = append(filtered, msg)
			}
		}
	}

	return filtered
}

// CalculateMessageTokens estimates tokens for a message
func (p *MessagePreparer) CalculateMessageTokens(msg types.Message) int {
	total := 0

	// Add role overhead
	total += len(string(msg.Role)) + 10

	// Add content blocks
	for _, block := range msg.Content {
		total += p.calculateContentBlockTokens(block)
	}

	return total
}

// calculateContentBlockTokens calculates tokens for a content block
func (p *MessagePreparer) calculateContentBlockTokens(block types.ContentBlock) int {
	switch block.ContentType() {
	case types.ContentTypeText:
		if text, ok := block.(types.TextContent); ok {
			return CountTokensInContent(text.Text)
		}
	case types.ContentTypeToolUse:
		if toolUse, ok := block.(types.ToolUseContent); ok {
			// Estimate: name + input JSON
			tokens := len(toolUse.Name) + 20
			for k, v := range toolUse.Input {
				tokens += len(k) + len(fmt.Sprintf("%v", v))
			}
			return tokens
		}
	case types.ContentTypeToolResult:
		if result, ok := block.(types.ToolResultContent); ok {
			return CountTokensInContent(result.Content)
		}
	}

	return 0
}

// CalculateMessagesTokens calculates total tokens for messages
func (p *MessagePreparer) CalculateMessagesTokens(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += p.CalculateMessageTokens(msg)
	}
	return total
}

// ShouldCompact returns true if messages should be compacted
func (p *MessagePreparer) ShouldCompact(
	messages []types.Message,
	contextWindow types.ContextWindow,
	threshold float64,
) bool {
	used := p.CalculateMessagesTokens(messages)
	limit := contextWindow.MaxTokens
	thresholdTokens := int(float64(limit) * threshold)
	return used >= thresholdTokens
}

// GetCompactionTarget calculates how many tokens need to be removed
func (p *MessagePreparer) GetCompactionTarget(
	messages []types.Message,
	contextWindow types.ContextWindow,
	targetPercentage float64,
) int {
	used := p.CalculateMessagesTokens(messages)
	target := int(float64(contextWindow.MaxTokens) * targetPercentage)
	if used <= target {
		return 0
	}
	return used - target
}

// CreateToolUseSummary creates a summary of tool uses
func (p *MessagePreparer) CreateToolUseSummary(toolUses []types.ToolUseContent) string {
	if len(toolUses) == 0 {
		return ""
	}

	summary := fmt.Sprintf("Executed %d tool(s): ", len(toolUses))

	names := make([]string, 0, len(toolUses))
	for _, tu := range toolUses {
		names = append(names, tu.Name)
	}

	summary += fmt.Sprintf("[%s]", fmt.Sprintf("%s", names))

	return summary
}

// CreateTurnSummary creates a summary of a turn
func (p *MessagePreparer) CreateTurnSummary(
	turnNumber int,
	userMessage types.Message,
	assistantMessage types.Message,
	toolResults []types.ToolResultContent,
) string {
	summary := fmt.Sprintf("Turn %d: ", turnNumber)

	// Add user message preview
	if len(userMessage.Content) > 0 {
		if text, ok := userMessage.Content[0].(types.TextContent); ok {
			preview := text.Text
			if len(preview) > 50 {
				preview = preview[:50] + "..."
			}
			summary += fmt.Sprintf("User: %s", preview)
		}
	}

	// Add tool use summary
	if len(toolResults) > 0 {
		summary += fmt.Sprintf(" [%d tool result(s)]", len(toolResults))
	}

	return summary
}
