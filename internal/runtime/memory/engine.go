package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/internal/utils"
)

const (
	defaultAutoCompactBufferTokens      = 13_000
	defaultManualCompactBufferTokens    = 3_000
	defaultMaxSummaryTokens             = 2_000
	defaultMaxConsecutiveCompactFailure = 3

	// summaryAttempts is how many times a summary is requested when the model returns no text, and summaryBudgetGrowth
	// how much larger each attempt's token budget is than the last, up to maxSummaryRetryTokens.
	summaryAttempts       = 2
	summaryBudgetGrowth   = 4
	maxSummaryRetryTokens = 16_000

	// manualKeepDivisor: a manual compaction keeps the most recent 1/5 of the conversation as it is.
	manualKeepDivisor       = 5
	compactSummaryMessageID = "compact_summary"
	compactMarkerMessageID  = "compact_marker"
)

// Engine performs runtime-driven conversation compaction.
type Engine struct {
	apiClient      *providers.Client
	preparer       *MessagePreparer
	microCompactor *MicroCompactor
	contextManager *utils.ContextManager
	config         *Config
}

// NewEngine creates a new compaction engine.
func NewEngine(apiClient *providers.Client, config *Config) *Engine {
	if config == nil {
		config = DefaultConfig()
	}
	if config.AutoCompactThreshold <= 0 {
		config.AutoCompactThreshold = DefaultConfig().AutoCompactThreshold
	}
	if config.CompactTargetPercentage <= 0 {
		config.CompactTargetPercentage = DefaultConfig().CompactTargetPercentage
	}
	if config.MaxSummaryTokens <= 0 {
		config.MaxSummaryTokens = defaultMaxSummaryTokens
	}
	if config.AutoCompactBufferTokens <= 0 {
		config.AutoCompactBufferTokens = defaultAutoCompactBufferTokens
	}
	if config.ManualCompactBufferTokens <= 0 {
		config.ManualCompactBufferTokens = defaultManualCompactBufferTokens
	}
	if config.MaxConsecutiveFailures <= 0 {
		config.MaxConsecutiveFailures = defaultMaxConsecutiveCompactFailure
	}

	return &Engine{
		apiClient:      apiClient,
		preparer:       NewMessagePreparer(),
		microCompactor: NewMicroCompactor(),
		contextManager: utils.NewContextManager(),
		config:         config,
	}
}

// EffectiveContextWindow returns the effective context window after reserving summary output budget.
func (e *Engine) EffectiveContextWindow(model types.ModelIdentifier) int {
	window := types.GetContextWindow(model)
	reserved := e.config.MaxSummaryTokens
	if window.MaxOutputTokens > 0 && window.MaxOutputTokens < reserved {
		reserved = window.MaxOutputTokens
	}
	effective := window.MaxTokens - reserved
	minimum := reserved + e.config.AutoCompactBufferTokens
	if effective < minimum {
		return minimum
	}
	return effective
}

// AutoCompactThresholdTokens returns the absolute token threshold for auto-compaction.
func (e *Engine) AutoCompactThresholdTokens(model types.ModelIdentifier) int {
	effective := e.EffectiveContextWindow(model)
	threshold := int(float64(effective) * e.config.AutoCompactThreshold)
	hardCap := effective - e.config.AutoCompactBufferTokens
	if threshold > hardCap {
		return hardCap
	}
	return threshold
}

// BlockingLimitTokens returns the hard blocking limit for the current model.
func (e *Engine) BlockingLimitTokens(model types.ModelIdentifier) int {
	effective := e.EffectiveContextWindow(model)
	return effective - e.config.ManualCompactBufferTokens
}

// CheckBlockingLimit implements CompactionStrategy.CheckBlockingLimit.
func (e *Engine) CheckBlockingLimit(systemPrompt string, messages []types.Message, model types.ModelIdentifier, tracking *TrackingState) error {
	used := e.UsedTokens(systemPrompt, messages, tracking)
	limit := e.BlockingLimitTokens(model)
	if used > limit {
		return types.NewError(types.ErrCodeCompactFull, fmt.Sprintf(
			"request needs %d tokens but the hard blocking limit for %s is %d, even after compaction",
			used, model, limit,
		))
	}
	return nil
}

// TargetTokens returns the post-compaction target token count.
func (e *Engine) TargetTokens(model types.ModelIdentifier) int {
	effective := e.EffectiveContextWindow(model)
	return int(float64(effective) * e.config.CompactTargetPercentage)
}

// CalculateUsage estimates the tokens of a system prompt and messages. It ignores the tool definitions and anything the
// provider reported; UsedTokens is the figure to decide on.
func (e *Engine) CalculateUsage(systemPrompt string, messages []types.Message) int {
	return e.contextManager.EstimateRequestTokens(systemPrompt, messages)
}

// UsedTokens is the size of the request about to be sent. With a provider-reported anchor it is that figure plus an
// estimate of the messages added since; without one it is the estimate plus the tool definitions.
func (e *Engine) UsedTokens(systemPrompt string, messages []types.Message, tracking *TrackingState) int {
	if tracking != nil && tracking.Anchor != nil && tracking.Anchor.Tokens > 0 &&
		tracking.Anchor.MessageCount <= len(messages) {
		return tracking.Anchor.Tokens + e.contextManager.CalculateMessagesTokens(messages[tracking.Anchor.MessageCount:])
	}
	used := e.CalculateUsage(systemPrompt, messages)
	if tracking != nil {
		used += tracking.ExtraTokens
	}
	return used
}

// Budget reports the window of a model and the two thresholds that matter to a host showing a context gauge. A nil
// engine (a host that wires none) reports a zero budget.
func (e *Engine) Budget(model types.ModelIdentifier) ContextBudget {
	if e == nil {
		return ContextBudget{}
	}
	return ContextBudget{
		WindowTokens:        types.GetContextWindow(model).MaxTokens,
		AutoCompactAtTokens: e.AutoCompactThresholdTokens(model),
		BlockingLimitTokens: e.BlockingLimitTokens(model),
	}
}

// ShouldCompact returns true if compaction should be attempted.
func (e *Engine) ShouldCompact(systemPrompt string, messages []types.Message, model types.ModelIdentifier, tracking *TrackingState) bool {
	used := e.UsedTokens(systemPrompt, messages, tracking)
	return used >= e.AutoCompactThresholdTokens(model)
}

// Compact performs compaction and returns a canonical result.
func (e *Engine) Compact(
	ctx context.Context,
	systemPrompt string,
	messages []types.Message,
	model types.ModelIdentifier,
	sessionID types.SessionID,
	turnID types.TurnID,
	tracking *TrackingState,
) (CompactionResult, error) {
	preCompactTokens := e.UsedTokens(systemPrompt, messages, tracking)
	targetTokens := e.TargetTokens(model)
	manual := tracking != nil && tracking.Manual
	if manual {
		targetTokens = max(1, preCompactTokens/manualKeepDivisor)
	}
	// What replaces the messages is not covered by the provider's figure, so it is measured by estimate.
	rewritten := &TrackingState{}
	if tracking != nil {
		rewritten.ExtraTokens = tracking.ExtraTokens
	}

	microCompacted := e.microCompact(messages)
	postMicroTokens := e.UsedTokens(systemPrompt, microCompacted, rewritten)
	if !manual && postMicroTokens <= targetTokens {
		return CompactionResult{
			Messages:            microCompacted,
			DidCompact:          true,
			UsedMicroCompact:    true,
			PreCompactTokens:    preCompactTokens,
			PostCompactTokens:   postMicroTokens,
			TargetTokens:        targetTokens,
			ConsecutiveFailures: 0,
		}, nil
	}

	// Only what is dropped needs a summary: the tail is kept as it is.
	preservedStart := e.findPreservedStart(microCompacted, targetTokens)
	toSummarize := microCompacted[:preservedStart]
	if preservedStart == 0 {
		toSummarize = microCompacted
	}
	summary, err := e.generateSummary(ctx, toSummarize, model, sessionID, turnID)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("failed to generate summary: %w", err)
	}

	compactedMessages := e.assembleCompactedWithin(microCompacted, preservedStart, summary, e.keptUserTokens(model))
	postCompactTokens := e.UsedTokens(systemPrompt, compactedMessages, rewritten)
	metadata, err := e.buildCompactionMetadata("summary", preCompactTokens, postCompactTokens, targetTokens, compactedMessages)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("failed to build compaction metadata: %w", err)
	}
	compactedMessages = attachCompactionMetadata(compactedMessages, metadata)

	return CompactionResult{
		Messages:            compactedMessages,
		DidCompact:          true,
		UsedMicroCompact:    true,
		UsedSummaryCompact:  true,
		PreCompactTokens:    preCompactTokens,
		PostCompactTokens:   postCompactTokens,
		TargetTokens:        targetTokens,
		Summary:             summary,
		ConsecutiveFailures: 0,
		RuntimeMetadata:     metadata,
	}, nil
}

// AutoCompact performs compaction if needed and respects the circuit breaker.
func (e *Engine) AutoCompact(
	ctx context.Context,
	systemPrompt string,
	messages []types.Message,
	model types.ModelIdentifier,
	sessionID types.SessionID,
	turnID types.TurnID,
	tracking *TrackingState,
) (CompactionResult, error) {
	current := TrackingState{}
	if tracking != nil {
		current = *tracking
	}
	if current.ConsecutiveFailures >= e.config.MaxConsecutiveFailures {
		return CompactionResult{
			Messages:            messages,
			DidCompact:          false,
			PreCompactTokens:    e.UsedTokens(systemPrompt, messages, tracking),
			PostCompactTokens:   e.UsedTokens(systemPrompt, messages, tracking),
			TargetTokens:        e.TargetTokens(model),
			ConsecutiveFailures: current.ConsecutiveFailures,
		}, nil
	}
	if !e.ShouldCompact(systemPrompt, messages, model, tracking) {
		return CompactionResult{
			Messages:            messages,
			DidCompact:          false,
			PreCompactTokens:    e.UsedTokens(systemPrompt, messages, tracking),
			PostCompactTokens:   e.UsedTokens(systemPrompt, messages, tracking),
			TargetTokens:        e.TargetTokens(model),
			ConsecutiveFailures: current.ConsecutiveFailures,
		}, nil
	}

	result, err := e.Compact(ctx, systemPrompt, messages, model, sessionID, turnID, &current)
	if err != nil {
		current.ConsecutiveFailures++
		return CompactionResult{
			Messages:            messages,
			DidCompact:          false,
			PreCompactTokens:    e.UsedTokens(systemPrompt, messages, tracking),
			PostCompactTokens:   e.UsedTokens(systemPrompt, messages, tracking),
			TargetTokens:        e.TargetTokens(model),
			ConsecutiveFailures: current.ConsecutiveFailures,
		}, err
	}
	result.ConsecutiveFailures = 0
	return result, nil
}

func (e *Engine) microCompact(messages []types.Message) []types.Message {
	compacted := make([]types.Message, len(messages))
	for i, msg := range messages {
		compacted[i] = e.preparer.PrepareMessageForAPI(msg)
	}
	return compacted
}

func (e *Engine) generateSummary(
	ctx context.Context,
	messages []types.Message,
	model types.ModelIdentifier,
	sessionID types.SessionID,
	turnID types.TurnID,
) (string, error) {
	summaryModel := e.config.SummaryModel
	if summaryModel.Model == "" {
		// No override configured: summarize with the session's own model so
		// compaction never needs a key for a provider the session isn't using.
		summaryModel = model
	}
	prompt := e.buildSummaryPrompt(messages)

	// A reasoning model spends part of max_tokens thinking: with the budget sized for the summary alone it can end on
	// the limit having written only its reasoning, and the summary is empty (seen on glm-5.3-flash: stop reason
	// max_tokens, one thinking block). The attempt is repeated with a larger budget before compaction is given up.
	budget := e.config.MaxSummaryTokens
	var lastStop string
	for attempt := 0; attempt < summaryAttempts; attempt++ {
		resp, err := e.apiClient.CreateMessage(ctx, types.APIRequest{
			Model:     summaryModel,
			Messages:  []types.Message{types.UserMessage("compact_summary_request", prompt)},
			MaxTokens: budget,
			// A summary does not need the model to reason first; where the provider can switch it off, it is cheaper
			// and faster, and the budget goes to the summary. The retry below covers providers that cannot.
			DisableThinking: true,
		})
		if err != nil {
			return "", err
		}
		for _, block := range resp.Content {
			if text, ok := block.(types.TextContent); ok && strings.TrimSpace(text.Text) != "" {
				return text.Text, nil
			}
		}
		lastStop = resp.StopReason
		budget = min(budget*summaryBudgetGrowth, maxSummaryRetryTokens)
	}
	return "", fmt.Errorf("no summary generated for session %s turn %s (stop reason %q after %d attempts)", sessionID, turnID, lastStop, summaryAttempts)
}

const summaryInstructions = `You are writing a handoff for another model that will carry on this work with only your summary and the last few messages. It cannot see anything you leave out. Write, in this order:

1. What the user asked for: the goal, and any change of direction since.
2. Decisions made, and the reason for each.
3. What has been done: files changed, commands run, results that still matter.
4. Where things stand now, and what remains, as concrete next steps.
5. Facts to keep exactly: paths, names, ids, numbers, error messages, URLs.
6. The user's preferences and constraints.

Be concise and specific. Do not invent anything that is not in the conversation.

Conversation:

`

const (
	// summaryToolInputChars and summaryToolResultChars bound how much of a tool call the summarizer is shown: enough
	// to see what was asked and what came back, not the whole payload.
	summaryToolInputChars  = 300
	summaryToolResultChars = 600

	// keptUserMessageTokens caps the earlier user messages that are carried over word for word, and
	// keptUserMessageChars caps each one: a pasted log or document must not be copied whole into the summary, or
	// compaction would gain nothing.
	keptUserMessageTokens = 20_000
	// keptUserWindowDivisor: the user's messages carried over word for word may take at most 1/20 (5 %) of the
	// effective window, whatever keptUserMessageTokens says. Twenty thousand tokens is 15 % of a 128k window, which
	// leaves a compaction at 85 % ending near 80 % and running again almost at once.
	keptUserWindowDivisor = 20
	keptUserMessageChars  = 2_000

	// handoffPrefix tells the model that resumes what the summary is.
	handoffPrefix = "The conversation was compacted to free context. Another model worked on this before you; " +
		"below is the summary it wrote, then the user's earlier messages word for word. " +
		"Build on the work already done and do not repeat it."
)

func (e *Engine) buildSummaryPrompt(messages []types.Message) string {
	var builder strings.Builder
	builder.WriteString(summaryInstructions)
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

func toolInputText(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(raw)
}

// clipForSummary cuts s to at most n characters, on a character boundary, marking the cut.
func clipForSummary(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

func (e *Engine) buildPostCompactMessages(messages []types.Message, summary string, targetTokens int) []types.Message {
	return e.assembleCompacted(messages, e.findPreservedStart(messages, targetTokens), summary)
}

// assembleCompacted builds the transcript that replaces messages: a summary (with the user's earlier messages kept
// word for word), a marker, then the preserved tail from preservedStart on.
func (e *Engine) assembleCompacted(messages []types.Message, preservedStart int, summary string) []types.Message {
	return e.assembleCompactedWithin(messages, preservedStart, summary, keptUserMessageTokens)
}

// keptUserTokens is how many tokens of the user's earlier messages a compaction for model may carry over.
func (e *Engine) keptUserTokens(model types.ModelIdentifier) int {
	return min(keptUserMessageTokens, e.EffectiveContextWindow(model)/keptUserWindowDivisor)
}

// assembleCompactedWithin is assembleCompacted with the budget for the user's messages given.
func (e *Engine) assembleCompactedWithin(messages []types.Message, preservedStart int, summary string, userTokens int) []types.Message {
	compacted := make([]types.Message, 0, len(messages)-preservedStart+2)

	summaryMessage := types.SystemMessage(compactSummaryMessageID, e.summaryTextWithin(messages[:preservedStart], summary, userTokens))
	summaryMessage.Metadata = &types.MessageMetadata{TurnID: "compact"}
	compacted = append(compacted, summaryMessage)

	// The marker must sit before the preserved tail so downstream transcript and
	// resume logic can see where compaction cut the history before it reads the
	// still-live messages that follow.
	marker := types.SystemMessage(compactMarkerMessageID, "[Context compacted for token pressure]")
	marker.Metadata = &types.MessageMetadata{TurnID: "compact"}
	compacted = append(compacted, marker)
	compacted = append(compacted, types.CloneMessages(messages[preservedStart:])...)
	return compacted
}

// summaryText is the hand-off prefix, the summary, and the user's messages from the dropped part, newest kept first
// within keptUserMessageTokens, written oldest first.
func (e *Engine) summaryText(dropped []types.Message, summary string) string {
	return e.summaryTextWithin(dropped, summary, keptUserMessageTokens)
}

// summaryTextWithin is summaryText with the token budget for the user's messages given.
func (e *Engine) summaryTextWithin(dropped []types.Message, summary string, userTokens int) string {
	var kept []string
	budget := userTokens
	for i := len(dropped) - 1; i >= 0; i-- {
		text, ok := userText(dropped[i])
		if !ok {
			continue
		}
		text = clipForSummary(text, keptUserMessageChars)
		cost := len(text)/4 + 10
		if cost > budget {
			break
		}
		budget -= cost
		kept = append(kept, text)
	}

	var b strings.Builder
	b.WriteString(handoffPrefix)
	b.WriteString("\n\nSummary:\n\n")
	b.WriteString(summary)
	if len(kept) > 0 {
		b.WriteString("\n\nThe user's earlier messages, word for word:\n")
		for i := len(kept) - 1; i >= 0; i-- {
			b.WriteString("\n---\n")
			b.WriteString(kept[i])
		}
	}
	return b.String()
}

// userText returns the text of a message the user wrote. Compaction artifacts, which are system messages, and tool
// results, which are not the user's words, are not.
func userText(msg types.Message) (string, bool) {
	if msg.Role != types.RoleUser {
		return "", false
	}
	var parts []string
	for _, block := range msg.Content {
		switch c := block.(type) {
		case types.TextContent:
			if strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		case types.ToolResultContent:
			return "", false
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

func (e *Engine) findPreservedStart(messages []types.Message, targetTokens int) int {
	if len(messages) == 0 {
		return 0
	}

	// Keep the most recent messages up to targetTokens, and at least the last one. A message that would take the tail
	// past the target is not kept: with one giant message (a pasted log) in the middle, keeping "at least the target"
	// swallowed everything before it and compaction gained nothing.
	preservedTokens := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		cost := e.preparer.CalculateMessageTokens(messages[i])
		if start < len(messages) && preservedTokens+cost > targetTokens {
			break
		}
		preservedTokens += cost
		start = i
	}
	return adjustPreservedStartForToolPairs(messages, start)
}

// adjustPreservedStartForToolPairs widens the preserved tail when the naive
// token-based start would keep a tool_result without the assistant tool_use it
// answers. Preserving the pair keeps the post-compact transcript structurally
// valid for both the runtime and future replay/resume logic.
func adjustPreservedStartForToolPairs(messages []types.Message, start int) int {
	if start <= 0 || start >= len(messages) {
		return start
	}

	adjustedStart := start
	for {
		neededToolUseIDs := make(map[string]bool)
		for i := adjustedStart; i < len(messages); i++ {
			for _, toolUseID := range collectToolResultIDs(messages[i]) {
				neededToolUseIDs[toolUseID] = true
			}
		}
		if len(neededToolUseIDs) == 0 {
			return adjustedStart
		}

		updated := adjustedStart
		for i := adjustedStart - 1; i >= 0; i-- {
			toolUseIDs := collectToolUseIDs(messages[i])
			if len(toolUseIDs) == 0 {
				continue
			}
			matched := false
			for _, toolUseID := range toolUseIDs {
				if neededToolUseIDs[toolUseID] {
					matched = true
					delete(neededToolUseIDs, toolUseID)
				}
			}
			if matched {
				updated = i
				if len(neededToolUseIDs) == 0 {
					break
				}
			}
		}
		if updated == adjustedStart {
			return adjustedStart
		}
		adjustedStart = updated
	}
}

func collectToolUseIDs(message types.Message) []string {
	ids := make([]string, 0)
	for _, block := range message.Content {
		if toolUse, ok := block.(types.ToolUseContent); ok {
			ids = append(ids, toolUse.ID)
		}
	}
	return ids
}

func collectToolResultIDs(message types.Message) []string {
	ids := make([]string, 0)
	for _, block := range message.Content {
		if toolResult, ok := block.(types.ToolResultContent); ok {
			ids = append(ids, toolResult.ToolUseID)
		}
	}
	return ids
}

func attachCompactionMetadata(messages []types.Message, metadata *types.CompactionMetadata) []types.Message {
	if metadata == nil || len(messages) == 0 {
		return messages
	}
	compacted := types.CloneMessages(messages)
	for i := range compacted {
		if compacted[i].ID == types.MessageID(compactSummaryMessageID) || compacted[i].ID == types.MessageID(compactMarkerMessageID) {
			compacted[i] = types.WithCompactionMetadata(compacted[i], metadata)
		}
	}
	return compacted
}

func (e *Engine) buildCompactionMetadata(kind string, preCompactTokens int, postCompactTokens int, targetTokens int, messages []types.Message) (*types.CompactionMetadata, error) {
	preservedTailStart := 0
	for i, message := range messages {
		if message.ID == types.MessageID(compactMarkerMessageID) {
			preservedTailStart = i + 1
			break
		}
	}
	preserved := messages[preservedTailStart:]
	preservedTailHash, err := types.CanonicalTranscriptHash(preserved)
	if err != nil {
		return nil, err
	}
	metadata := &types.CompactionMetadata{
		Kind:               kind,
		PreCompactTokens:   preCompactTokens,
		PostCompactTokens:  postCompactTokens,
		TargetTokens:       targetTokens,
		PreservedMessages:  len(preserved),
		PreservedTurns:     countPreservedTurns(preserved),
		PreservedToolPairs: countPreservedToolPairs(preserved),
	}
	if len(preserved) > 0 {
		metadata.BoundaryVersion = types.CompactionBoundaryVersionV1
		metadata.FirstPreservedMessageID = preserved[0].ID
		metadata.LastPreservedMessageID = preserved[len(preserved)-1].ID
		metadata.PreservedTailHash = preservedTailHash
	}
	if len(preserved) == 0 {
		metadata.BoundaryVersion = types.CompactionBoundaryVersionV1
		metadata.PreservedTailHash = preservedTailHash
	}
	return metadata, nil
}

func countPreservedTurns(messages []types.Message) int {
	return types.CountDistinctTurnIDs(messages)
}

func countPreservedToolPairs(messages []types.Message) int {
	toolUseIDs := make(map[string]bool)
	for _, message := range messages {
		for _, toolUseID := range collectToolUseIDs(message) {
			toolUseIDs[toolUseID] = true
		}
	}
	count := 0
	for _, message := range messages {
		for _, toolUseID := range collectToolResultIDs(message) {
			if toolUseIDs[toolUseID] {
				count++
			}
		}
	}
	return count
}

// CalculateCompactionImpact calculates the impact of compaction.
func (e *Engine) CalculateCompactionImpact(messages []types.Message) CompactionImpact {
	originalTokens := e.preparer.CalculateMessagesTokens(messages)
	microCompacted := e.microCompact(messages)
	microTokens := e.preparer.CalculateMessagesTokens(microCompacted)
	estimatedTokens := int(float64(e.TargetTokens(e.config.SummaryModel)))
	if estimatedTokens <= 0 {
		estimatedTokens = microTokens
	}
	return CompactionImpact{
		OriginalTokens:              originalTokens,
		MicroCompactTokens:          microTokens,
		EstimatedFullCompactTokens:  estimatedTokens,
		MicroCompactSavings:         originalTokens - microTokens,
		EstimatedFullCompactSavings: originalTokens - estimatedTokens,
	}
}

// CompactionImpact represents the impact of compaction.
type CompactionImpact struct {
	OriginalTokens              int `json:"original_tokens"`
	MicroCompactTokens          int `json:"micro_compact_tokens"`
	EstimatedFullCompactTokens  int `json:"estimated_full_compact_tokens"`
	MicroCompactSavings         int `json:"micro_compact_savings"`
	EstimatedFullCompactSavings int `json:"estimated_full_compact_savings"`
}
