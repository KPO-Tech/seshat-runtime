package main

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	contract "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	internaltypes "github.com/KPO-Tech/seshat-runtime/internal/types"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// This file translates engine values to the wire. Nothing here decides
// anything: it is the one place where a Go value becomes a protobuf message.

const (
	contentTypeText       = "text"
	contentTypeThinking   = "thinking"
	contentTypeToolUse    = "tool_use"
	contentTypeToolResult = "tool_result"
	contentTypeImage      = "image"
)

// timestampFrom renders a time on the wire, or nil when it is zero: a zero
// timestamp is not a date, and a client that plots one would show 1970.
func timestampFrom(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t.UTC())
}

// structFromMap converts a map the engine built. A value that JSON cannot
// carry (an interface holding a func, a channel, a live tool) makes the whole
// map fail to convert, so the map goes through JSON first: what survives is
// what the contract promises, and a turn never fails because an event carried
// something unprintable.
func structFromMap(in map[string]any) *structpb.Struct {
	if len(in) == 0 {
		return nil
	}
	if s, err := structpb.NewStruct(in); err == nil {
		return s
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return nil
	}
	s, err := structpb.NewStruct(generic)
	if err != nil {
		return nil
	}
	return s
}

// valueFromAny converts one answer of a prompt. It is total: an unconvertible
// value becomes null rather than an error, because a prompt answer is data the
// runtime carries for a tool, not a decision the server makes.
func valueFromAny(in any) *structpb.Value {
	if in == nil {
		return structpb.NewNullValue()
	}
	if v, err := structpb.NewValue(in); err == nil {
		return v
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return structpb.NewNullValue()
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return structpb.NewNullValue()
	}
	v, err := structpb.NewValue(generic)
	if err != nil {
		return structpb.NewNullValue()
	}
	return v
}

// valueFromProto is the other direction: the answer a client sent.
func valueFromProto(in *structpb.Value) any {
	if in == nil {
		return nil
	}
	return in.AsInterface()
}

func structToMap(in *structpb.Struct) map[string]any {
	if in == nil {
		return nil
	}
	return in.AsMap()
}

func tokenUsageToProto(usage *internaltypes.TokenUsage) *pb.TokenUsage {
	if usage == nil {
		return nil
	}
	return &pb.TokenUsage{
		InputTokens:              int64(usage.InputTokens),
		OutputTokens:             int64(usage.OutputTokens),
		TotalTokens:              int64(usage.InputTokens + usage.OutputTokens),
		CachedTokens:             int64(usage.CachedTokens),
		CacheReadInputTokens:     int64(usage.CacheReadInputTokens),
		CacheCreationInputTokens: int64(usage.CacheCreationInputTokens),
	}
}

func contentBlockToProto(block internaltypes.ContentBlock) *pb.ContentBlock {
	switch b := block.(type) {
	case internaltypes.TextContent:
		return &pb.ContentBlock{
			Type:  contentTypeText,
			Block: &pb.ContentBlock_Text{Text: &pb.TextBlock{Text: b.Text}},
		}
	case internaltypes.ThinkingContent:
		return &pb.ContentBlock{
			Type:  contentTypeThinking,
			Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingBlock{Thinking: b.Thinking}},
		}
	case internaltypes.ToolUseContent:
		return &pb.ContentBlock{
			Type: contentTypeToolUse,
			Block: &pb.ContentBlock_ToolUse{ToolUse: &pb.ToolUse{
				Id:    b.ID,
				Name:  b.Name,
				Input: structFromMap(b.Input),
			}},
		}
	case internaltypes.ToolResultContent:
		return &pb.ContentBlock{
			Type: contentTypeToolResult,
			Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResult{
				ToolUseId: b.ToolUseID,
				Content:   b.Content,
				IsError:   b.IsError,
			}},
		}
	case internaltypes.ImageContent:
		return &pb.ContentBlock{
			Type: contentTypeImage,
			Block: &pb.ContentBlock_Image{Image: &pb.ImageBlock{
				SourceType: b.Source.Type,
				MediaType:  b.Source.MediaType,
				Data:       b.Source.Data,
			}},
		}
	default:
		return &pb.ContentBlock{Type: contentTypeText}
	}
}

func messageToProto(message internaltypes.Message) *pb.Message {
	blocks := make([]*pb.ContentBlock, 0, len(message.Content))
	for _, block := range message.Content {
		blocks = append(blocks, contentBlockToProto(block))
	}
	out := &pb.Message{
		Id:        string(message.ID),
		Role:      string(message.Role),
		Content:   blocks,
		Timestamp: timestampFrom(message.Timestamp),
	}
	if message.Metadata != nil {
		out.TurnId = message.Metadata.TurnID
	}
	return out
}

func messagesToProto(messages []internaltypes.Message) []*pb.Message {
	out := make([]*pb.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, messageToProto(message))
	}
	return out
}

func toolUseToProto(use internaltypes.ToolUseContent) *pb.ToolUse {
	return &pb.ToolUse{Id: use.ID, Name: use.Name, Input: structFromMap(use.Input)}
}

// callResultToProto renders what a tool returned. CallResult carries no tool
// use id of its own, so the field is filled only when the result metadata
// names it.
func callResultToProto(result contract.CallResult) *pb.ToolResult {
	out := &pb.ToolResult{Content: result.Content, IsError: result.Error != nil}
	if out.Content == "" && result.Data != nil {
		if s, ok := result.Data.(string); ok {
			out.Content = s
		}
	}
	return out
}

// chunkToProto renders one piece of a streamed response.
func chunkToProto(chunk *internaltypes.APIResponseChunk) *pb.ChunkDelta {
	if chunk == nil {
		return nil
	}
	out := &pb.ChunkDelta{
		Type:        string(chunk.Type),
		DeltaType:   chunk.DeltaType,
		Delta:       chunk.Delta,
		PartialJson: chunk.PartialJSON,
		Usage:       tokenUsageToProto(chunk.Usage),
	}
	if chunk.BlockIndex != nil {
		index := int32(*chunk.BlockIndex)
		out.Index = &index
	}
	if chunk.ContentBlock != nil {
		out.ContentBlock = contentBlockToProto(chunk.ContentBlock)
	}
	if chunk.StopReason != nil {
		out.StopReason = *chunk.StopReason
	}
	return out
}

func toolProgressToProto(progress *internaltypes.ToolProgress) *pb.ToolProgress {
	if progress == nil {
		return nil
	}
	return &pb.ToolProgress{
		ToolName:        progress.ToolName,
		ToolUseId:       progress.ToolUseID,
		Stage:           string(progress.Stage),
		Message:         progress.Message,
		PercentComplete: progress.PercentComplete,
		Metadata:        structFromMap(progress.Metadata),
	}
}

// stringValue reads a string the engine put in a metadata map, tolerating a
// value of another type rather than failing the request that carries it.
func stringValue(in any) string {
	if value, ok := in.(string); ok {
		return value
	}
	return ""
}

// mapValue reads a map the engine put in a metadata map.
func mapValue(in any) map[string]any {
	if value, ok := in.(map[string]any); ok {
		return value
	}
	return nil
}

// permissionRequestFromPrompt renders the approval the engine is waiting for.
// The prompt carries the tool and its input in its metadata (see
// internal/permissions), which is what a client shows.
func permissionRequestFromPrompt(requestID string, request internaltypes.PromptRequest, expiresAt time.Time) *pb.PermissionRequest {
	metadata := request.Metadata
	out := &pb.PermissionRequest{
		RequestId:        requestID,
		ToolName:         stringValue(metadata["tool_name"]),
		Description:      request.Message,
		ToolInput:        structFromMap(mapValue(metadata["tool_input"])),
		ToolUseId:        stringValue(metadata["tool_use_id"]),
		WorkingDirectory: stringValue(metadata["working_directory"]),
		ExpiresAt:        timestamppb.New(expiresAt.UTC()),
	}
	return out
}

// promptRequestFromPrompt renders a question a tool asks the user.
func promptRequestFromPrompt(requestID string, request internaltypes.PromptRequest, expiresAt time.Time) *pb.PromptRequest {
	out := &pb.PromptRequest{
		RequestId:    requestID,
		Type:         string(request.Type),
		Message:      request.Message,
		DefaultValue: valueFromAny(request.Default),
		Metadata:     structFromMap(request.Metadata),
		ExpiresAt:    timestamppb.New(expiresAt.UTC()),
	}
	for _, option := range request.Options {
		out.Options = append(out.Options, &pb.PromptOption{
			Label:       option.Label,
			Value:       valueFromAny(option.Value),
			Description: option.Description,
		})
	}
	return out
}

// latestThinkingText is the reasoning of the last assistant message, the
// counterpart of latestResponseText for the thinking blocks.
func latestThinkingText(messages []internaltypes.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != internaltypes.RoleAssistant {
			continue
		}
		var thinking string
		for _, block := range messages[i].Content {
			if block, ok := block.(internaltypes.ThinkingContent); ok {
				thinking += block.Thinking
			}
		}
		if thinking != "" {
			return thinking
		}
	}
	return ""
}

// turnResultFrom renders the outcome of a turn.
func turnResultFrom(response *sdk.SessionResponse, sessionID string) *pb.TurnResult {
	if response == nil {
		return &pb.TurnResult{SessionId: sessionID}
	}
	out := &pb.TurnResult{
		SessionId:         sessionID,
		Content:           latestResponseText(response.Messages),
		Thinking:          latestThinkingText(response.Messages),
		Messages:          messagesToProto(response.Messages),
		Usage:             tokenUsageToProto(response.Usage),
		StopReason:        response.StopReason,
		TurnNumber:        int32(response.TurnNumber),
		IsComplete:        response.IsComplete,
		Compacted:         response.Compacted,
		CompactPreTokens:  int32(response.CompactPreTokens),
		CompactPostTokens: int32(response.CompactPostTokens),
		TotalTokens:       int64(response.TotalTokens),
	}
	for _, use := range response.ToolUses {
		out.ToolUses = append(out.ToolUses, toolUseToProto(use))
	}
	for _, result := range response.ToolResults {
		out.ToolResults = append(out.ToolResults, callResultToProto(result))
	}
	return out
}
