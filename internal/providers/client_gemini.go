package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func (c *Client) buildGeminiRequestBody(req types.APIRequest) (io.Reader, error) {
	body := map[string]any{
		"contents": geminiContents(req.Messages),
	}
	if req.SystemPrompt != "" {
		body["system_instruction"] = map[string]any{
			"parts": []map[string]any{{"text": req.SystemPrompt}},
		}
	} else if len(req.SystemPromptBlocks) > 0 {
		body["system_instruction"] = map[string]any{
			"parts": []map[string]any{{"text": types.FlattenSystemPromptBlocks(req.SystemPromptBlocks)}},
		}
	}
	if len(req.Tools) > 0 {
		body["tools"] = []map[string]any{{
			"functionDeclarations": geminiToolDeclarations(req.Tools),
		}}
	}
	generationConfig := map[string]any{}
	if req.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = req.MaxTokens
	}
	if sendsTemperature(req) {
		generationConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		generationConfig["topP"] = *req.TopP
	}
	if req.OutputSchema != nil {
		generationConfig["responseMimeType"] = "application/json"
		generationConfig["responseSchema"] = req.OutputSchema.GeminiResponseSchema()
	}
	if len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func geminiContents(messages []types.Message) []map[string]any {
	contents := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		parts := geminiParts(message.Content, message.Role)
		if len(parts) == 0 {
			continue
		}
		role := "user"
		if message.Role == types.RoleAssistant {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": parts,
		})
	}
	return contents
}

func geminiParts(content []types.ContentBlock, role types.Role) []map[string]any {
	parts := make([]map[string]any, 0, len(content))
	for _, block := range content {
		switch typed := block.(type) {
		case types.TextContent:
			if typed.Text == "" {
				continue
			}
			parts = append(parts, map[string]any{"text": typed.Text})
		case types.ToolUseContent:
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"name": typed.Name,
					"args": typed.Input,
				},
			})
		case types.ImageContent:
			parts = append(parts, map[string]any{
				"inlineData": map[string]any{
					"mimeType": typed.Source.MediaType,
					"data":     typed.Source.Data,
				},
			})
		case types.ToolResultContent:
			if role != types.RoleUser {
				continue
			}
			name := typed.ToolUseID
			if typed.Metadata != nil {
				if toolName, ok := (*typed.Metadata)["tool_name"].(string); ok && strings.TrimSpace(toolName) != "" {
					name = toolName
				}
			}
			parts = append(parts, map[string]any{
				"functionResponse": map[string]any{
					"name": name,
					"response": map[string]any{
						"content":  typed.Content,
						"is_error": typed.IsError,
					},
				},
			})
		}
	}
	return parts
}

func geminiToolDeclarations(tools []types.APIToolDefinition) []map[string]any {
	declarations := make([]map[string]any, 0, len(tools))
	for _, toolDef := range tools {
		declarations = append(declarations, map[string]any{
			"name":        toolDef.Name,
			"description": toolDef.Description,
			"parameters":  toolDef.InputSchema,
		})
	}
	return declarations
}

func decodeGeminiResponse(body io.Reader, model types.ModelIdentifier) (types.APIResponse, error) {
	var resp struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return types.APIResponse{}, err
	}
	content := make([]types.ContentBlock, 0)
	stopReason := types.StopReasonEndTurn
	if len(resp.Candidates) > 0 {
		candidate := resp.Candidates[0]
		for idx, part := range candidate.Content.Parts {
			if part.Text != "" {
				content = append(content, types.TextContent{Text: part.Text})
			}
			if part.FunctionCall != nil {
				content = append(content, types.ToolUseContent{
					ID:    fmt.Sprintf("gemini-tool-%d", idx+1),
					Name:  part.FunctionCall.Name,
					Input: part.FunctionCall.Args,
				})
			}
		}
		stopReason = normalizeGeminiStopReason(candidate.FinishReason, content)
	}
	return types.APIResponse{
		Role:       types.RoleAssistant,
		Content:    content,
		StopReason: stopReason,
		Usage: types.TokenUsage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		},
		Model: model,
		ID:    "gemini-response",
	}, nil
}

func normalizeGeminiStopReason(reason string, content []types.ContentBlock) string {
	if types.ContentBlocksContainToolUse(content) {
		return types.StopReasonToolUse
	}
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "MAX_TOKENS":
		return types.StopReasonMaxTokens
	case "", "STOP":
		return types.StopReasonEndTurn
	default:
		return types.StopReasonEndTurn
	}
}

func (c *Client) createGeminiStreamResult(ctx context.Context, req types.APIRequest, onChunk func(types.APIResponseChunk)) (*types.APIStreamResult, error) {
	req.Stream = true
	resp, err := c.sendStreamRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp, nil)
	}

	content := make([]types.ContentBlock, 0)
	var text strings.Builder
	collected := make([]types.APIResponseChunk, 0)
	usage := types.TokenUsage{}
	finishReason := ""
	reader := bufio.NewReader(resp.Body)
	partIndex := 0

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					if text.Len() > 0 {
						content = append([]types.ContentBlock{types.TextContent{Text: text.String()}}, content...)
					}
					stopChunk := types.APIResponseChunk{
						Type:       types.APIChunkTypeMessageStop,
						StopReason: stringPtr(normalizeGeminiStopReason(finishReason, content)),
						Usage:      &usage,
					}
					emitStreamChunk(onChunk, stopChunk)
					collected = append(collected, stopChunk)
					response := types.APIResponse{
						Role:       types.RoleAssistant,
						Content:    content,
						StopReason: normalizeGeminiStopReason(finishReason, content),
						Usage:      usage,
						Model:      req.Model,
						ID:         "gemini-stream-response",
					}
					return &types.APIStreamResult{Response: response, Chunks: collected}, nil
				}
				return nil, types.WrapError(types.ErrCodeAPIResponse, "failed to read gemini stream", err)
			}
			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			var chunk struct {
				Candidates []struct {
					FinishReason string `json:"finishReason"`
					Content      struct {
						Parts []struct {
							Text         string `json:"text"`
							FunctionCall *struct {
								Name string         `json:"name"`
								Args map[string]any `json:"args"`
							} `json:"functionCall"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
				UsageMetadata struct {
					PromptTokenCount     int `json:"promptTokenCount"`
					CandidatesTokenCount int `json:"candidatesTokenCount"`
				} `json:"usageMetadata"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return nil, types.WrapError(types.ErrCodeAPIResponse, "failed to decode gemini stream chunk", err)
			}
			if chunk.UsageMetadata.PromptTokenCount > 0 {
				usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
			}
			if chunk.UsageMetadata.CandidatesTokenCount > 0 {
				usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
			}
			for _, candidate := range chunk.Candidates {
				if candidate.FinishReason != "" {
					finishReason = candidate.FinishReason
				}
				for _, part := range candidate.Content.Parts {
					if part.Text != "" {
						text.WriteString(part.Text)
						streamChunk := types.APIResponseChunk{
							Type:      types.APIChunkTypeContentBlockDelta,
							Delta:     part.Text,
							DeltaType: "text_delta",
						}
						emitStreamChunk(onChunk, streamChunk)
						collected = append(collected, streamChunk)
					}
					if part.FunctionCall != nil {
						partIndex++
						toolUse := types.ToolUseContent{
							ID:    fmt.Sprintf("gemini-tool-%d", partIndex),
							Name:  part.FunctionCall.Name,
							Input: part.FunctionCall.Args,
						}
						content = append(content, toolUse)
						streamChunk := types.APIResponseChunk{
							Type:         types.APIChunkTypeContentBlockStart,
							ContentBlock: toolUse,
						}
						emitStreamChunk(onChunk, streamChunk)
						collected = append(collected, streamChunk)
					}
				}
			}
		}
	}
}
