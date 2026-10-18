package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// --- Request building ---

func (c *Client) buildOpenAIRequestBody(req types.APIRequest) (io.Reader, error) {
	body := map[string]any{
		"model":    req.Model.ProviderModelName(),
		"messages": c.buildOpenAIMessages(req),
		"stream":   req.Stream,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		body["tools"] = openAITools(req.Tools)
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.OutputSchema != nil {
		body["response_format"] = req.OutputSchema.OpenAIResponseFormat()
	}
	if req.DisableThinking && req.Model.Provider == types.APIProviderZAi {
		// z.ai: the GLM-4.5 and later models think unless told not to.
		body["thinking"] = map[string]any{"type": "disabled"}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (c *Client) buildOpenAIMessages(req types.APIRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	systemPrompt := req.SystemPrompt
	if systemPrompt == "" && len(req.SystemPromptBlocks) > 0 {
		systemPrompt = types.FlattenSystemPromptBlocks(req.SystemPromptBlocks)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, map[string]any{
			"role":    string(types.RoleSystem),
			"content": systemPrompt,
		})
	}
	// Strip orphaned tool results before conversion to avoid invalid_request_message_order
	// from OpenAI-compat APIs (z-ai/GLM, OpenAI, etc.).
	sanitized := sanitizeToolResultOrphans(req.Messages)
	for _, message := range sanitized {
		messages = append(messages, openAIMessageParts(message)...)
	}
	return messages
}

// sanitizeToolResultOrphans removes tool_result blocks from user messages when
// the referenced tool_use_id does not exist in any preceding assistant message.
// This prevents "Unexpected tool call id X in tool results" (invalid_request_message_order)
// from OpenAI-compatible APIs when parallel or background agents cause message
// history desynchronization.
func sanitizeToolResultOrphans(messages []types.Message) []types.Message {
	// Build the set of all tool_use IDs seen in assistant messages.
	knownToolUseIDs := make(map[string]struct{}, 16)
	for _, m := range messages {
		if m.Role != types.RoleAssistant {
			continue
		}
		for _, block := range m.Content {
			if tu, ok := block.(types.ToolUseContent); ok {
				knownToolUseIDs[tu.ID] = struct{}{}
			}
		}
	}

	// If no assistant tool_calls exist in this conversation, there's nothing to
	// be orphaned from — pass through unchanged to avoid stripping valid results.
	if len(knownToolUseIDs) == 0 {
		return messages
	}

	result := make([]types.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role != types.RoleUser {
			result = append(result, m)
			continue
		}
		// Filter out tool_result blocks whose IDs are not in knownToolUseIDs.
		hasOrphan := false
		for _, block := range m.Content {
			if tr, ok := block.(types.ToolResultContent); ok {
				if _, known := knownToolUseIDs[tr.ToolUseID]; !known {
					hasOrphan = true
					break
				}
			}
		}
		if !hasOrphan {
			result = append(result, m)
			continue
		}
		// Rebuild message content without orphaned tool results.
		clean := make([]types.ContentBlock, 0, len(m.Content))
		for _, block := range m.Content {
			if tr, ok := block.(types.ToolResultContent); ok {
				if _, known := knownToolUseIDs[tr.ToolUseID]; !known {
					continue // drop orphan
				}
			}
			clean = append(clean, block)
		}
		if len(clean) > 0 {
			cleaned := m
			cleaned.Content = clean
			result = append(result, cleaned)
		}
		// If all content was orphaned tool results, the message is dropped entirely.
	}
	return result
}

func openAIMessageParts(message types.Message) []map[string]any {
	switch message.Role {
	case types.RoleUser:
		if toolResults := toolResultsFromMessage(message); len(toolResults) > 0 {
			parts := make([]map[string]any, 0, len(toolResults)+1)
			for _, result := range toolResults {
				parts = append(parts, map[string]any{
					"role":         "tool",
					"tool_call_id": result.ToolUseID,
					"content":      result.Content,
				})
			}
			// Preserve any free-text the user attached alongside tool results.
			if text := joinTextBlocks(message.Content); text != "" {
				parts = append(parts, map[string]any{
					"role":    "user",
					"content": text,
				})
			}
			return parts
		}
		if hasImageContent(message.Content) {
			return []map[string]any{{
				"role":    "user",
				"content": openAIVisionContent(message.Content),
			}}
		}
		return []map[string]any{{
			"role":    "user",
			"content": joinTextBlocks(message.Content),
		}}
	case types.RoleAssistant:
		payload := map[string]any{"role": "assistant"}
		if text := joinTextBlocks(message.Content); text != "" {
			payload["content"] = text
		} else {
			payload["content"] = nil
		}
		if toolCalls := openAIToolCalls(message.Content); len(toolCalls) > 0 {
			payload["tool_calls"] = toolCalls
		}
		return []map[string]any{payload}
	case types.RoleSystem:
		return []map[string]any{{
			"role":    "system",
			"content": joinTextBlocks(message.Content),
		}}
	default:
		return nil
	}
}

func openAIToolCalls(content []types.ContentBlock) []map[string]any {
	calls := make([]map[string]any, 0)
	for _, block := range content {
		toolUse, ok := block.(types.ToolUseContent)
		if !ok {
			continue
		}
		arguments, _ := json.Marshal(toolUse.Input)
		calls = append(calls, map[string]any{
			"id":   toolUse.ID,
			"type": "function",
			"function": map[string]any{
				"name":      toolUse.Name,
				"arguments": string(arguments),
			},
		})
	}
	return calls
}

func openAITools(tools []types.APIToolDefinition) []map[string]any {
	definitions := make([]map[string]any, 0, len(tools))
	for _, toolDef := range tools {
		definitions = append(definitions, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        toolDef.Name,
				"description": toolDef.Description,
				"parameters":  toolDef.InputSchema,
			},
		})
	}
	return definitions
}

// --- Response decoding ---

func decodeOpenAIResponse(body io.Reader, model types.ModelIdentifier) (types.APIResponse, error) {
	var resp struct {
		ID      string `json:"id"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return types.APIResponse{}, err
	}
	return canonicalResponseFromOpenAIChoice(resp.ID, model, resp.Choices, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

func canonicalResponseFromOpenAIChoice(responseID string, model types.ModelIdentifier, choices []struct {
	FinishReason string `json:"finish_reason"`
	Message      struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content"`
		ToolCalls        []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
}, promptTokens int, completionTokens int) (types.APIResponse, error) {
	content := make([]types.ContentBlock, 0)
	stopReason := types.StopReasonEndTurn
	if len(choices) > 0 {
		choice := choices[0]
		if choice.Message.ReasoningContent != "" {
			content = append(content, types.ThinkingContent{Thinking: choice.Message.ReasoningContent})
		}
		if choice.Message.Content != "" {
			content = append(content, types.TextContent{Text: choice.Message.Content})
		}
		for idx, toolCall := range choice.Message.ToolCalls {
			input := make(map[string]any)
			if strings.TrimSpace(toolCall.Function.Arguments) != "" {
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
					return types.APIResponse{}, err
				}
			}
			toolID := toolCall.ID
			if toolID == "" {
				toolID = fmt.Sprintf("openai-tool-%d", idx+1)
			}
			content = append(content, types.ToolUseContent{
				ID:    toolID,
				Name:  toolCall.Function.Name,
				Input: input,
			})
		}
		stopReason = normalizeOpenAIStopReason(choice.FinishReason, content)
	}
	return types.APIResponse{
		Role:       types.RoleAssistant,
		Content:    content,
		StopReason: stopReason,
		Usage: types.TokenUsage{
			InputTokens:  promptTokens,
			OutputTokens: completionTokens,
		},
		Model: model,
		ID:    responseID,
	}, nil
}

// rawToString extracts a plain string from a json.RawMessage.
// Returns "" if the value is null, an array, or any non-string JSON type.
// This handles providers (e.g. Mistral) that send "content": [] during tool calls.
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func normalizeOpenAIStopReason(reason string, content []types.ContentBlock) string {
	if types.ContentBlocksContainToolUse(content) {
		return types.StopReasonToolUse
	}
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length":
		return types.StopReasonMaxTokens
	case "stop", "":
		return types.StopReasonEndTurn
	default:
		return types.StopReasonEndTurn
	}
}

// --- Streaming ---

type openAIStreamToolCallState struct {
	ID      string
	Name    string
	Started bool
	Args    strings.Builder
}

func (c *Client) createOpenAIStreamResult(ctx context.Context, req types.APIRequest, onChunk func(types.APIResponseChunk)) (*types.APIStreamResult, error) {
	req.Stream = true
	resp, err := c.sendStreamRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp, nil)
	}

	return parseOpenAISSEStream(ctx, resp.Body, req.Model, onChunk)
}

// parseOpenAISSEStream reads an OpenAI-compatible "data: ..." SSE body and
// aggregates it into a canonical APIStreamResult. Providers routed through
// openAICompatAdapter (e.g. Z.ai) only support streaming responses, so
// CreateMessage forces req.Stream = true for them even when the caller (like
// the auto-mode classifier) wants a single non-streaming response - see
// codexAdapter.decodeResponse for the same fix applied to Codex, which failed
// the same way decoding "event: ..." as a single JSON document.
func parseOpenAISSEStream(ctx context.Context, body io.Reader, model types.ModelIdentifier, onChunk func(types.APIResponseChunk)) (*types.APIStreamResult, error) {
	var text strings.Builder
	var thinking strings.Builder
	toolCalls := make(map[int]*openAIStreamToolCallState)
	order := make([]int, 0)
	collected := make([]types.APIResponseChunk, 0)
	usage := types.TokenUsage{}
	finishReason := ""

	reader := bufio.NewReader(body)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return finishOpenAIStream(onChunk, model, text.String(), thinking.String(), toolCalls, order, finishReason, usage, collected)
				}
				return nil, types.WrapError(types.ErrCodeAPIResponse, "failed to read openai stream", err)
			}
			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return finishOpenAIStream(onChunk, model, text.String(), thinking.String(), toolCalls, order, finishReason, usage, collected)
			}

			var chunk struct {
				ID      string `json:"id"`
				Choices []struct {
					Delta struct {
						// Content can be a string or an empty array (Mistral tool-call chunks).
						Content          json.RawMessage `json:"content"`
						ReasoningContent string          `json:"reasoning_content"`
						ToolCalls        []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
				// Error carries an in-band provider failure (e.g. OpenRouter
				// surfacing an upstream 5xx as "data: {\"error\": ...}" inside
				// an otherwise-200 stream instead of a non-200 HTTP status).
				// Without this field it silently unmarshalled into a no-op
				// delta - zero Choices - and the stream finished looking like
				// an ordinary, possibly truncated, success.
				Error *struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    any    `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return nil, types.WrapError(types.ErrCodeAPIResponse, "failed to decode openai stream chunk", err)
			}
			if chunk.Error != nil {
				return nil, types.WrapError(
					types.ErrCodeAPIResponse,
					fmt.Sprintf("provider reported an in-stream error: %s (code %v)", chunk.Error.Message, chunk.Error.Code),
					errProviderStreamError,
				)
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.ReasoningContent != "" {
					thinking.WriteString(choice.Delta.ReasoningContent)
					streamChunk := types.APIResponseChunk{
						Type:      types.APIChunkTypeContentBlockDelta,
						Delta:     choice.Delta.ReasoningContent,
						DeltaType: "thinking_delta",
					}
					emitStreamChunk(onChunk, streamChunk)
					collected = append(collected, streamChunk)
				}
				if contentStr := rawToString(choice.Delta.Content); contentStr != "" {
					text.WriteString(contentStr)
					streamChunk := types.APIResponseChunk{
						Type:      types.APIChunkTypeContentBlockDelta,
						Delta:     contentStr,
						DeltaType: "text_delta",
					}
					emitStreamChunk(onChunk, streamChunk)
					collected = append(collected, streamChunk)
				}
				for _, toolCall := range choice.Delta.ToolCalls {
					state, exists := toolCalls[toolCall.Index]
					if !exists {
						state = &openAIStreamToolCallState{}
						toolCalls[toolCall.Index] = state
						order = append(order, toolCall.Index)
					}
					if toolCall.ID != "" {
						state.ID = toolCall.ID
					}
					if toolCall.Function.Name != "" {
						state.Name = toolCall.Function.Name
					}
					if !state.Started && (state.ID != "" || state.Name != "" || toolCall.Function.Arguments != "") {
						state.Started = true
						streamChunk := types.APIResponseChunk{
							Type: types.APIChunkTypeContentBlockStart,
							ContentBlock: types.ToolUseContent{
								ID:    state.ID,
								Name:  state.Name,
								Input: nil,
							},
						}
						emitStreamChunk(onChunk, streamChunk)
						collected = append(collected, streamChunk)
					}
					if toolCall.Function.Arguments != "" {
						state.Args.WriteString(toolCall.Function.Arguments)
						streamChunk := types.APIResponseChunk{
							Type:        types.APIChunkTypeContentBlockDelta,
							DeltaType:   "input_json_delta",
							PartialJSON: toolCall.Function.Arguments,
						}
						emitStreamChunk(onChunk, streamChunk)
						collected = append(collected, streamChunk)
					}
				}
				if choice.FinishReason != "" {
					finishReason = choice.FinishReason
				}
			}
			if chunk.Usage != nil {
				usage.InputTokens = chunk.Usage.PromptTokens
				usage.OutputTokens = chunk.Usage.CompletionTokens
			}
		}
	}
}

// finishOpenAIStream emits the synthetic message_stop chunk and builds the
// aggregated result when the stream reaches [DONE] or a clean EOF. If the
// provider never sent a finish_reason and produced no visible text or tool
// call, that is a silently truncated/interrupted stream - previously this
// looked identical to a legitimate (if terse) completion.
func finishOpenAIStream(onChunk func(types.APIResponseChunk), model types.ModelIdentifier, text string, thinking string, toolCalls map[int]*openAIStreamToolCallState, order []int, finishReason string, usage types.TokenUsage, collected []types.APIResponseChunk) (*types.APIStreamResult, error) {
	if finishReason == "" && text == "" && len(toolCalls) == 0 {
		return nil, types.WrapError(types.ErrCodeAPIResponse, "stream ended with no finish signal and no visible output", errStreamInterrupted)
	}

	stopChunk := types.APIResponseChunk{
		Type:       types.APIChunkTypeMessageStop,
		StopReason: stringPtr(normalizeOpenAIStopReason(finishReason, nil)),
		Usage:      &usage,
	}
	emitStreamChunk(onChunk, stopChunk)
	collected = append(collected, stopChunk)
	return buildOpenAIStreamResult(model, "openai-stream-response", text, thinking, toolCalls, order, finishReason, usage, collected)
}

func buildOpenAIStreamResult(model types.ModelIdentifier, responseID string, text string, thinking string, toolCalls map[int]*openAIStreamToolCallState, order []int, finishReason string, usage types.TokenUsage, collected []types.APIResponseChunk) (*types.APIStreamResult, error) {
	content := make([]types.ContentBlock, 0, 2+len(order))
	if thinking != "" {
		content = append(content, types.ThinkingContent{Thinking: thinking})
	}
	if text != "" {
		content = append(content, types.TextContent{Text: text})
	}
	sort.Ints(order)
	for _, idx := range order {
		call := toolCalls[idx]
		if call == nil {
			continue
		}
		input := make(map[string]any)
		if strings.TrimSpace(call.Args.String()) != "" {
			if err := json.Unmarshal([]byte(call.Args.String()), &input); err != nil {
				return nil, types.WrapError(types.ErrCodeAPIResponse, "failed to decode streamed openai tool arguments", err)
			}
		}
		toolID := call.ID
		if toolID == "" {
			toolID = fmt.Sprintf("openai-tool-%d", idx+1)
		}
		content = append(content, types.ToolUseContent{ID: toolID, Name: call.Name, Input: input})
	}
	response := types.APIResponse{
		Role:       types.RoleAssistant,
		Content:    content,
		StopReason: normalizeOpenAIStopReason(finishReason, content),
		Usage:      usage,
		Model:      model,
		ID:         responseID,
	}
	return &types.APIStreamResult{Response: response, Chunks: collected}, nil
}
