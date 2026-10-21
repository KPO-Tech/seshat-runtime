// Package auto implements the auto mode permission classifier for Seshat.
// This aligns with OpenClaude's two-stage security classification system.
//
// Two-Stage Classifier Design:
// - Stage 1 (Fast): Quick yes/no decision with small token budget (64 tokens)
// - Stage 2 (Thinking): Full reasoning when stage 1 blocks, with extended analysis
//
// Key Features:
// - XML-based output parsing (<block>/<reason>/<thinking>)
// - Transcript building from conversation history
// - SESHAT.md integration for user preferences
// - Feature flags for runtime configuration
package auto

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// =============================================================================
// CONSTANTS - Classifier configuration
// =============================================================================

const (
	// MaxTranscriptChars is the maximum character length for transcript context.
	// This includes user messages and tool uses sent to the classifier.
	MaxTranscriptChars = 200000

	// MaxBlockValueChars is the maximum characters per block value.
	// Used to truncate long tool inputs in the transcript.
	MaxBlockValueChars = 32000

	// Stage1MaxTokens is the token budget for stage 1 (fast) classification.
	// Small budget encourages quick yes/no decisions.
	Stage1MaxTokens = 64

	// Stage1MaxTokensFast is the token budget for fast-only mode.
	// Allows space for <reason> tag in response.
	Stage1MaxTokensFast = 256

	// Stage2MaxTokens is the token budget for stage 2 (thinking) classification.
	// Larger budget allows for chain-of-thought reasoning.
	Stage2MaxTokens = 4096

	// ThinkingPadding is extra tokens reserved for models with always-on thinking.
	// Prevents max_tokens exhaustion on extended thinking models.
	ThinkingPadding = 2048
)

// Stage suffixes appended to classifier prompts to guide response format.
// These match OpenClaude's classifier.py stage suffixes.
const (
	// Stage1Suffix nudges the model for immediate <block> decision.
	Stage1Suffix = "\nErr on the side of blocking. <block> immediately."

	// Stage2Suffix encourages chain-of-thought reasoning before decision.
	Stage2Suffix = "\nReview the classification process and follow it carefully, making sure you deny actions that should be blocked. As a reminder, explicit (not suggestive or implicit) user confirmation is required to override blocks. Use <thinking> before responding with <block>."
)

// =============================================================================
// TYPES - Core data structures
// =============================================================================

// TwoStageMode defines which stages of the classifier to run.
// Aligned with OpenClaude's classifier modes.
type TwoStageMode string

const (
	// TwoStageModeBoth runs both stage 1 and stage 2 (default).
	// Fast stage runs first; if blocked, escalates to thinking stage.
	TwoStageModeBoth TwoStageMode = "both"

	// TwoStageModeFast runs only stage 1 (fast) classifier.
	// Stage 1 verdict is final; allows <reason> in response.
	TwoStageModeFast TwoStageMode = "fast"

	// TwoStageModeThinking runs only stage 2 (thinking) classifier.
	// Skips fast stage entirely for complex decisions.
	TwoStageModeThinking TwoStageMode = "thinking"
)

// ClassifierUsage tracks token usage from classifier API calls.
// Used for telemetry and cost tracking.
type ClassifierUsage struct {
	InputTokens              int `json:"input_tokens"`                // Tokens in request
	OutputTokens             int `json:"output_tokens"`               // Tokens in response
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`     // Tokens from cache read
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"` // Tokens for cache creation
}

// ClassifierResult contains the complete classification result.
// Returned by TwoStageClassifier after processing.
type ClassifierResult struct {
	ShouldBlock       bool             `json:"should_block"`                  // Whether action should be blocked
	Reason            string           `json:"reason"`                        // Human-readable reason
	Thinking          string           `json:"thinking,omitempty"`            // Chain-of-thought reasoning
	Model             string           `json:"model"`                         // Model used for classification
	Usage             *ClassifierUsage `json:"usage,omitempty"`               // Token usage statistics
	DurationMs        int64            `json:"duration_ms"`                   // Total classification time
	Stage             string           `json:"stage,omitempty"`               // Which stage produced result (fast/thinking)
	Unavailable       bool             `json:"unavailable,omitempty"`         // Classifier API was unavailable
	TranscriptTooLong bool             `json:"transcript_too_long,omitempty"` // Transcript exceeded context limit
	PromptLengths     PromptLengths    `json:"prompt_lengths,omitempty"`      // Prompt component lengths
	Stage1Usage       *ClassifierUsage `json:"stage1_usage,omitempty"`        // Stage 1 usage (for two-stage)
	Stage1DurationMs  int64            `json:"stage1_duration_ms,omitempty"`  // Stage 1 timing
	Stage2Usage       *ClassifierUsage `json:"stage2_usage,omitempty"`        // Stage 2 usage (for two-stage)
	Stage2DurationMs  int64            `json:"stage2_duration_ms,omitempty"`  // Stage 2 timing
}

// PromptLengths tracks the character length of each prompt component.
// Used for debugging and context size tracking.
type PromptLengths struct {
	SystemPrompt int `json:"system_prompt"` // System prompt length
	ToolCalls    int `json:"tool_calls"`    // Tool calls/transcript length
	UserPrompts  int `json:"user_prompts"`  // User messages length
}

// TranscriptEntry represents a single turn in the classifier transcript.
// Either user message or assistant tool use.
type TranscriptEntry struct {
	Role    string            `json:"role"`    // "user" or "assistant"
	Content []TranscriptBlock `json:"content"` // Content blocks in this entry
}

// TranscriptBlock represents a single content block within a transcript entry.
// Can be either user text or tool use.
type TranscriptBlock struct {
	Type  string `json:"type"`            // "text" or "tool_use"
	Text  string `json:"text,omitempty"`  // For text blocks
	Name  string `json:"name,omitempty"`  // For tool_use blocks (tool name)
	Input any    `json:"input,omitempty"` // For tool_use blocks (tool input)
}

// TwoStageClassifier is the main classifier implementation.
// It handles both fast (stage 1) and thinking (stage 2) classification stages.
type TwoStageClassifier struct {
	model              string
	mode               TwoStageMode
	xmlParser          *XMLParser
	maxTranscriptChars int
	useCacheControl    bool
	enableThinking     bool
	apiClient          ClassifierAPI
}

type TwoStageConfig struct {
	Model              string
	TwoStageMode       TwoStageMode
	MaxTranscriptChars int
	UseCacheControl    bool
	EnableThinking     bool
}

func DefaultTwoStageConfig() *TwoStageConfig {
	return &TwoStageConfig{
		Model:              "claude-sonnet-5-5",
		TwoStageMode:       TwoStageModeBoth,
		MaxTranscriptChars: MaxTranscriptChars,
		UseCacheControl:    true,
		EnableThinking:     true,
	}
}

func NewTwoStageClassifier(config *TwoStageConfig) *TwoStageClassifier {
	if config == nil {
		config = DefaultTwoStageConfig()
	}
	return &TwoStageClassifier{
		model:              config.Model,
		mode:               config.TwoStageMode,
		xmlParser:          NewXMLParser(),
		maxTranscriptChars: config.MaxTranscriptChars,
		useCacheControl:    config.UseCacheControl,
		enableThinking:     config.EnableThinking,
		apiClient:          nil,
	}
}

func NewTwoStageClassifierWithAPI(config *TwoStageConfig, apiClient ClassifierAPI) *TwoStageClassifier {
	if config == nil {
		config = DefaultTwoStageConfig()
	}
	return &TwoStageClassifier{
		model:              config.Model,
		mode:               config.TwoStageMode,
		xmlParser:          NewXMLParser(),
		maxTranscriptChars: config.MaxTranscriptChars,
		useCacheControl:    config.UseCacheControl,
		enableThinking:     config.EnableThinking,
		apiClient:          apiClient,
	}
}

func (c *TwoStageClassifier) Classify(ctx context.Context, toolName string, toolInput map[string]any) (*Classification, error) {
	result, err := c.ClassifyWithTranscript(ctx, toolName, toolInput, nil, "")
	if err != nil {
		return nil, err
	}
	return &Classification{
		Allowed:    !result.ShouldBlock,
		Confidence: 1.0,
		Reason:     result.Reason,
	}, nil
}

func (c *TwoStageClassifier) ClassifyWithTranscript(ctx context.Context, toolName string, toolInput map[string]any, messages []types.Message, toolUseID string) (*ClassifierResult, error) {
	startTime := time.Now()

	var prefixMessages []types.Message

	transcript := BuildTranscriptFromMessages(messages)
	actionEntry := TranscriptEntry{
		Role: "assistant",
		Content: []TranscriptBlock{
			{Type: "tool_use", Name: toolName, Input: toolInput},
		},
	}
	transcript = append(transcript, actionEntry)

	userContentBlocks := SerializeTranscript(transcript)
	actionCompact := FormatToolUseCompact(toolName, toolInput)

	userContentBlocks = append(userContentBlocks, actionCompact)

	systemPrompt := BuildSystemPrompt()

	promptLengths := PromptLengths{
		SystemPrompt: len(systemPrompt),
		ToolCalls:    len(actionCompact),
		UserPrompts:  0,
	}

	switch c.mode {
	case TwoStageModeBoth:
		return c.runTwoStage(ctx, systemPrompt, userContentBlocks, promptLengths, startTime, prefixMessages)
	case TwoStageModeFast:
		return c.runFastOnly(ctx, systemPrompt, userContentBlocks, promptLengths, startTime, prefixMessages)
	case TwoStageModeThinking:
		return c.runThinkingOnly(ctx, systemPrompt, userContentBlocks, promptLengths, startTime, prefixMessages)
	default:
		return c.runTwoStage(ctx, systemPrompt, userContentBlocks, promptLengths, startTime, prefixMessages)
	}
}

func (c *TwoStageClassifier) buildResult(toolName string) *ClassifierResult {
	return &ClassifierResult{
		ShouldBlock: false,
		Reason:      "Allowed by fast classifier",
		Model:       c.model,
		Usage: &ClassifierUsage{
			InputTokens:  100,
			OutputTokens: 10,
		},
		DurationMs: 50,
		Stage:      "fast",
	}
}

func (c *TwoStageClassifier) runTwoStage(ctx context.Context, systemPrompt string, userContentBlocks []string, promptLengths PromptLengths, startTime time.Time, prefixMessages []types.Message) (*ClassifierResult, error) {
	xmlSystemPrompt := ReplaceOutputFormatWithXml(systemPrompt)
	systemBlock := xmlSystemPrompt

	stage1Start := time.Now()
	wrappedContent := WrapWithTranscriptTags(userContentBlocks)
	stage1Content := append(wrappedContent, Stage1Suffix)
	stage1Text := c.callStage1(ctx, systemBlock, strings.Join(stage1Content, ""), prefixMessages)

	stage1DurationMs := time.Since(stage1Start).Milliseconds()
	stage1Block := c.xmlParser.ParseBlock(stage1Text)

	if stage1Block != nil && !*stage1Block {
		return &ClassifierResult{
			ShouldBlock: false,
			Reason:      "Allowed by fast classifier",
			Model:       c.model,
			Usage: &ClassifierUsage{
				InputTokens:  100,
				OutputTokens: 10,
			},
			DurationMs: stage1DurationMs,
			Stage:      "fast",
		}, nil
	}

	stage2Start := time.Now()
	stage2Content := append(wrappedContent, Stage2Suffix)
	stage2Text := c.callStage2(ctx, systemBlock, strings.Join(stage2Content, ""), prefixMessages)

	stage2DurationMs := time.Since(stage2Start).Milliseconds()
	stage2Block := c.xmlParser.ParseBlock(stage2Text)

	if stage2Block == nil {
		LogParseFailure(c.model, "stage2_unparseable")
		return &ClassifierResult{
			ShouldBlock: true,
			Reason:      "Classifier stage 2 unparseable - blocking for safety",
			Model:       c.model,
			DurationMs:  stage1DurationMs + stage2DurationMs,
			Stage:       "thinking",
		}, nil
	}

	return &ClassifierResult{
		Thinking:    c.xmlParser.ParseThinking(stage2Text),
		ShouldBlock: *stage2Block,
		Reason:      c.xmlParser.ParseReason(stage2Text),
		Model:       c.model,
		Usage: &ClassifierUsage{
			InputTokens:  200,
			OutputTokens: 50,
		},
		DurationMs: stage1DurationMs + stage2DurationMs,
		Stage:      "thinking",
	}, nil
}

func (c *TwoStageClassifier) runFastOnly(ctx context.Context, systemPrompt string, userContentBlocks []string, promptLengths PromptLengths, startTime time.Time, prefixMessages []types.Message) (*ClassifierResult, error) {
	xmlSystemPrompt := ReplaceOutputFormatWithXml(systemPrompt)
	stage1Start := time.Now()
	wrappedContent := WrapWithTranscriptTags(userContentBlocks)
	stage1Text := c.callStage1(ctx, xmlSystemPrompt, strings.Join(wrappedContent, ""), prefixMessages)

	stage1DurationMs := time.Since(stage1Start).Milliseconds()
	stage1Block := c.xmlParser.ParseBlock(stage1Text)

	if stage1Block == nil {
		LogParseFailure(c.model, "stage1_unparseable")
		return &ClassifierResult{
			ShouldBlock: true,
			Reason:      "Classifier stage 1 unparseable - blocking for safety",
			Model:       c.model,
			DurationMs:  stage1DurationMs,
			Stage:       "fast",
		}, nil
	}

	if !*stage1Block {
		return &ClassifierResult{
			ShouldBlock: false,
			Reason:      "Allowed by fast classifier",
			Model:       c.model,
			DurationMs:  stage1DurationMs,
			Stage:       "fast",
		}, nil
	}

	return &ClassifierResult{
		ShouldBlock: true,
		Reason:      c.xmlParser.ParseReason(stage1Text),
		Model:       c.model,
		DurationMs:  stage1DurationMs,
		Stage:       "fast",
	}, nil
}

func (c *TwoStageClassifier) runThinkingOnly(ctx context.Context, systemPrompt string, userContentBlocks []string, promptLengths PromptLengths, startTime time.Time, prefixMessages []types.Message) (*ClassifierResult, error) {
	xmlSystemPrompt := ReplaceOutputFormatWithXml(systemPrompt)
	stage2Start := time.Now()
	wrappedContent := WrapWithTranscriptTags(userContentBlocks)
	stage2Content := append(wrappedContent, Stage2Suffix)
	stage2Text := c.callStage2(ctx, xmlSystemPrompt, strings.Join(stage2Content, ""), prefixMessages)

	stage2DurationMs := time.Since(stage2Start).Milliseconds()
	stage2Block := c.xmlParser.ParseBlock(stage2Text)

	if stage2Block == nil {
		LogParseFailure(c.model, "stage2_unparseable")
		return &ClassifierResult{
			ShouldBlock: true,
			Reason:      "Classifier stage 2 unparseable - blocking for safety",
			Model:       c.model,
			DurationMs:  stage2DurationMs,
			Stage:       "thinking",
		}, nil
	}

	return &ClassifierResult{
		Thinking:    c.xmlParser.ParseThinking(stage2Text),
		ShouldBlock: *stage2Block,
		Reason:      c.xmlParser.ParseReason(stage2Text),
		Model:       c.model,
		Usage: &ClassifierUsage{
			InputTokens:  200,
			OutputTokens: 50,
		},
		DurationMs: stage2DurationMs,
		Stage:      "thinking",
	}, nil
}

func (c *TwoStageClassifier) callStage1(ctx context.Context, systemPrompt, userContent string, prefixMessages []types.Message) string {
	if c.apiClient != nil {
		flags := GetFeatureFlags()
		maxTokens := Stage1MaxTokens
		if c.mode == TwoStageModeFast {
			maxTokens = Stage1MaxTokensFast
		}

		temp := 0.0
		stopSeqs := []string{"</block>"}
		if c.mode == TwoStageModeFast {
			stopSeqs = nil
		}

		messages := make([]types.Message, 0, len(prefixMessages)+1)
		messages = append(messages, prefixMessages...)
		messages = append(messages, types.Message{
			ID:      "classifier-stage1",
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.TextContent{Text: userContent}},
		})

		req := &ClassifierAPIRequest{
			Model:         c.model,
			MaxTokens:     maxTokens + ThinkingPadding,
			System:        systemPrompt,
			Temperature:   temp,
			Messages:      messages,
			StopSequences: stopSeqs,
			Thinking:      !flags.ThinkingEnabled,
			// Stage 1 only has to say block or allow. Stage 2 is the one that thinks.
			DisableThinking: true,
		}

		start := time.Now()
		resp, err := c.apiClient.Classify(ctx, req)
		elapsedMs := time.Since(start).Milliseconds()
		if err != nil {
			LogError(c.model, err, false, elapsedMs)
			return ""
		}
		LogSuccess(c.model, elapsedMs)
		return resp.Text
	}
	return `<block>yes</block><reason>No classifier available — blocking by default</reason>`
}

func (c *TwoStageClassifier) callStage2(ctx context.Context, systemPrompt, userContent string, prefixMessages []types.Message) string {
	if c.apiClient != nil {
		flags := GetFeatureFlags()

		temp := 0.0

		messages := make([]types.Message, 0, len(prefixMessages)+1)
		messages = append(messages, prefixMessages...)
		messages = append(messages, types.Message{
			ID:      "classifier-stage2",
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.TextContent{Text: userContent}},
		})

		req := &ClassifierAPIRequest{
			Model:       c.model,
			MaxTokens:   Stage2MaxTokens + ThinkingPadding,
			System:      systemPrompt,
			Temperature: temp,
			Messages:    messages,
			Thinking:    !flags.ThinkingEnabled,
			// This stage reasons in its answer: the prompt asks for a <thinking> section that is read back (ParseThinking). The
			// provider's own hidden reasoning on top of it took 25 seconds on a reasoning model for a harmless call.
			DisableThinking: true,
		}

		start := time.Now()
		resp, err := c.apiClient.Classify(ctx, req)
		elapsedMs := time.Since(start).Milliseconds()
		if err != nil {
			LogError(c.model, err, false, elapsedMs)
			return ""
		}
		LogSuccess(c.model, elapsedMs)
		return resp.Text
	}
	return `<block>yes</block><reason>No classifier available — blocking by default</reason>`
}

func BuildTranscriptFromMessages(messages []types.Message) []TranscriptEntry {
	if messages == nil {
		return nil
	}
	var transcript []TranscriptEntry
	for _, msg := range messages {
		entry := messageToTranscriptEntry(msg)
		if entry != nil {
			transcript = append(transcript, *entry)
		}
	}
	return transcript
}

func messageToTranscriptEntry(msg types.Message) *TranscriptEntry {
	var blocks []TranscriptBlock
	for _, content := range msg.Content {
		switch block := content.(type) {
		case types.TextContent:
			blocks = append(blocks, TranscriptBlock{
				Type: "text",
				Text: block.Text,
			})
		case types.ToolUseContent:
			blocks = append(blocks, TranscriptBlock{
				Type:  "tool_use",
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	role := "user"
	if msg.Role == types.RoleAssistant {
		role = "assistant"
	}
	return &TranscriptEntry{
		Role:    role,
		Content: blocks,
	}
}

func SerializeTranscript(transcript []TranscriptEntry) []string {
	var blocks []string
	for _, entry := range transcript {
		for _, block := range entry.Content {
			if block.Type == "text" && entry.Role == "user" {
				truncated := TruncateString(block.Text, MaxBlockValueChars)
				blocks = append(blocks, fmt.Sprintf("User: %s\n", truncated))
			} else if block.Type == "tool_use" {
				inputMap, ok := block.Input.(map[string]any)
				if !ok {
					inputMap = map[string]any{}
				}
				toolCompact := FormatToolUseCompact(block.Name, inputMap)
				if toolCompact != "" {
					blocks = append(blocks, toolCompact+"\n")
				}
			}
		}
	}
	return blocks
}

// BuildSystemPrompt returns the complete system prompt for the classifier.
// This uses the default template configuration with external template support.
// For customization, use BuildSystemPromptWithConfig() with a custom TemplateConfig.
func BuildSystemPrompt() string {
	return BuildSystemPromptWithConfig(DefaultTemplateConfig())
}

func ReplaceOutputFormatWithXml(systemPrompt string) string {
	toolUseLine := "Use the classify_result tool to report your classification."
	xmlFormat := `## Output Format

If the action should be blocked:
<block>yes</block><reason>one short sentence</reason>

If the action should be allowed:
<block>no</block>

Do NOT include a <reason> tag when the action is allowed.
Your ENTIRE response MUST begin with <block>. Do NOT output any analysis, reasoning, or commentary before <block>. No "Looking at..." or similar preamble.`

	return strings.Replace(systemPrompt, toolUseLine, xmlFormat, 1)
}

func WrapWithTranscriptTags(blocks []string) []string {
	wrapped := make([]string, 0, len(blocks)+2)
	wrapped = append(wrapped, "<transcript>\n")
	wrapped = append(wrapped, blocks...)
	wrapped = append(wrapped, "</transcript>\n")
	return wrapped
}

func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... [truncated]"
}

func CombineUsage(a, b *ClassifierUsage) *ClassifierUsage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &ClassifierUsage{
		InputTokens:              a.InputTokens + b.InputTokens,
		OutputTokens:             a.OutputTokens + b.OutputTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens + b.CacheCreationInputTokens,
	}
}
