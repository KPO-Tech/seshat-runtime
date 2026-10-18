package multimedia

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/audio/tts"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	ttsToolName = "text_to_speech"
	ttsDesc     = `Convert text to speech audio using the configured TTS provider.

Returns a JSON object with:
- "provider": which provider synthesised the audio (e.g. "openai")
- "model": the TTS model used
- "audio_base64": base64-encoded audio data
- "content_type": MIME type of the audio (e.g. "audio/mpeg")
- "characters_used": number of input characters consumed`
)

// TTSTool implements the text_to_speech built-in tool.
type TTSTool struct{ generator tts.Generation }

// NewTTSTool creates a text_to_speech Tool. Disabled (IsEnabled=false) when generator is nil.
func NewTTSTool(generator tts.Generation) *TTSTool { return &TTSTool{generator: generator} }

func (t *TTSTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ttsToolName,
		SearchHint:  "read text aloud generate speech audio from text",
		DisplayName: "Text to Speech",
		Description: ttsDesc,
		Category:    "audio",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "The text to convert to speech.",
				},
			},
			"required": []string{"text"},
		}),
		Metadata: map[string]any{"surface_profiles": []string{"mono_run"}},
	}
}

func (t *TTSTool) Call(ctx context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
	if t.generator == nil {
		return tool.CallResult{Error: fmt.Errorf("text_to_speech: no TTS provider configured")}, nil
	}
	text, _ := input.Parsed["text"].(string)
	if text == "" {
		return tool.CallResult{Error: fmt.Errorf("text_to_speech: text is required")}, nil
	}
	resp, err := t.generator.GenerateAudio(ctx, text)
	if err != nil {
		return tool.CallResult{Error: fmt.Errorf("text_to_speech: %w", err)}, nil
	}
	result := map[string]any{
		"provider":        t.generator.Provider(),
		"model":           resp.Model,
		"audio_base64":    base64.StdEncoding.EncodeToString(resp.AudioData),
		"content_type":    resp.ContentType,
		"characters_used": resp.CharactersUsed,
	}
	encoded, _ := json.Marshal(result)
	return tool.CallResult{Data: result, ContentType: tool.ContentTypeJSON, Content: string(encoded)}, nil
}

func (t *TTSTool) Description(_ context.Context) (string, error) { return ttsDesc, nil }
func (t *TTSTool) ValidateInput(_ context.Context, in map[string]any) (map[string]any, error) {
	return in, nil
}
func (t *TTSTool) CheckPermissions(_ context.Context, _ map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.PermissionResult{Behavior: types.PermissionBehaviorAllow}
}
func (t *TTSTool) IsConcurrencySafe(_ map[string]any) bool { return true }
func (t *TTSTool) IsReadOnly(_ map[string]any) bool        { return true }
func (t *TTSTool) IsEnabled() bool                         { return t.generator != nil }
func (t *TTSTool) FormatResult(data any) string {
	if m, ok := data.(map[string]any); ok {
		ct, _ := m["content_type"].(string)
		chars, _ := m["characters_used"].(int)
		return fmt.Sprintf("Audio synthesised (%s, %d chars)", ct, chars)
	}
	return fmt.Sprintf("%v", data)
}
func (t *TTSTool) BackfillInput(_ context.Context, in map[string]any) map[string]any { return in }
