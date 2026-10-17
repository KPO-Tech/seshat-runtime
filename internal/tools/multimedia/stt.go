package multimedia

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/audio/stt"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	sttToolName = "speech_to_text"
	sttDesc     = `Transcribe audio to text using the configured STT provider (Whisper or equivalent).

Input: base64-encoded audio bytes (MP3, WAV, WebM, M4A supported).

Returns a JSON object with:
- "text": the full transcript
- "language": detected language (IETF tag)
- "duration": audio length in seconds
- "model": the transcription model used
- "provider": which provider performed the transcription`
)

// STTTool implements the speech_to_text built-in tool.
type STTTool struct{ transcriber stt.SpeechToText }

// NewSTTTool creates a speech_to_text Tool. Disabled when transcriber is nil.
func NewSTTTool(transcriber stt.SpeechToText) *STTTool { return &STTTool{transcriber: transcriber} }

func (t *STTTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        sttToolName,
		SearchHint:  "transcribe audio to text speech recognition",
		DisplayName: "Speech to Text",
		Description: sttDesc,
		Category:    "audio",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"audio_base64": map[string]any{
					"type":        "string",
					"description": "Base64-encoded audio data to transcribe.",
				},
				"mime_type": map[string]any{
					"type":        "string",
					"description": "Optional MIME type hint (e.g. audio/mp3, audio/wav).",
				},
			},
			"required": []string{"audio_base64"},
		}),
		Metadata: map[string]any{"surface_profiles": []string{"mono_run"}},
	}
}

func (t *STTTool) Call(ctx context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	if t.transcriber == nil {
		return tool.CallResult{Error: fmt.Errorf("speech_to_text: no STT provider configured")}, nil
	}
	b64, _ := input.Parsed["audio_base64"].(string)
	if b64 == "" {
		return tool.CallResult{Error: fmt.Errorf("speech_to_text: audio_base64 is required")}, nil
	}
	audioData, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return tool.CallResult{Error: fmt.Errorf("speech_to_text: invalid base64: %w", err)}, nil
	}
	resp, err := t.transcriber.Transcribe(ctx, audioData)
	if err != nil {
		return tool.CallResult{Error: fmt.Errorf("speech_to_text: %w", err)}, nil
	}
	result := map[string]any{
		"text":     resp.Text,
		"language": resp.Language,
		"duration": resp.Duration,
		"model":    resp.Model,
		"provider": t.transcriber.Provider(),
	}
	encoded, _ := json.Marshal(result)
	return tool.CallResult{Data: result, ContentType: tool.ContentTypeJSON, Content: string(encoded)}, nil
}

func (t *STTTool) Description(_ context.Context) (string, error) { return sttDesc, nil }
func (t *STTTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *STTTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.PermissionResult{Behavior: types.PermissionBehaviorAllow}
}
func (t *STTTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *STTTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *STTTool) IsEnabled() bool                         { return t.transcriber != nil }
func (t *STTTool) FormatResult(data any) string {
	if m, ok := data.(map[string]any); ok {
		if text, ok := m["text"].(string); ok && text != "" {
			if len(text) > 120 {
				return "Transcript: " + text[:120] + "…"
			}
			return "Transcript: " + text
		}
	}
	return fmt.Sprintf("%v", data)
}
func (t *STTTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }
