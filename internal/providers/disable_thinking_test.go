package providers

import (
	"io"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func openAIBody(t *testing.T, provider types.APIProvider, disable bool) string {
	t.Helper()
	c := NewClientWithConfig("k", &Config{Provider: provider, BaseURL: "http://localhost"})
	r, err := c.buildOpenAIRequestBody(types.APIRequest{
		Model:           types.ModelIdentifier{Provider: provider, Model: "m"},
		Messages:        []types.Message{types.UserMessage("m1", "hi")},
		MaxTokens:       10,
		DisableThinking: disable,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestDisableThinkingIsSentToZAi(t *testing.T) {
	if got := openAIBody(t, types.APIProviderZAi, true); !strings.Contains(got, `"thinking":{"type":"disabled"}`) {
		t.Errorf("z.ai must be told not to think: %s", got)
	}
}

func TestThinkingIsLeftAloneUnlessAskedAndSupported(t *testing.T) {
	if got := openAIBody(t, types.APIProviderZAi, false); strings.Contains(got, "thinking") {
		t.Errorf("without the option the model's default applies: %s", got)
	}
	// Providers without a known switch must not receive an unknown parameter, which some reject.
	for _, p := range []types.APIProvider{types.APIProviderOpenAI, types.APIProviderOpenRouter, types.APIProviderMistral} {
		if got := openAIBody(t, p, true); strings.Contains(got, "thinking") {
			t.Errorf("%s has no known switch, the body must not carry one: %s", p, got)
		}
	}
}
