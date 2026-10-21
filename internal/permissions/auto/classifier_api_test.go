package auto

import (
	"context"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func TestClassifierRequestCarriesTheProviderAndTheThinkingSwitch(t *testing.T) {
	temp := 0.0
	req := &ClassifierAPIRequest{Model: "glm-5.1", MaxTokens: 64, DisableThinking: true}

	withProvider := NewClassifierAPIClient(nil).WithProvider(types.APIProviderZAi).buildAPIRequest(req, nil, "sys", &temp)
	if withProvider.Model.Provider != types.APIProviderZAi || !withProvider.DisableThinking {
		t.Errorf("request = provider %q, disable thinking %v; want z.ai and true", withProvider.Model.Provider, withProvider.DisableThinking)
	}
	if without := NewClassifierAPIClient(nil).buildAPIRequest(req, nil, "sys", &temp); without.Model.Provider != types.APIProviderAnthropic {
		t.Errorf("without a provider the request keeps its old address, got %q", without.Model.Provider)
	}
}

type capturingAPI struct {
	requests []*ClassifierAPIRequest
	reply    string
}

func (c *capturingAPI) Classify(_ context.Context, req *ClassifierAPIRequest) (*ClassifierAPIResponse, error) {
	c.requests = append(c.requests, req)
	return &ClassifierAPIResponse{Text: c.reply}, nil
}

// Neither stage lets a reasoning model think on its own: the verdict stage needs no thinking, and the second stage already writes
// its reasoning in the answer. That hidden thinking is where the seconds went (25 s on a reasoning model for a harmless call).
func TestNeitherStageLetsTheProviderReason(t *testing.T) {
	api := &capturingAPI{reply: "<block>yes</block>"}
	c := NewTwoStageClassifierWithAPI(DefaultTwoStageConfig(), api)
	if _, err := c.ClassifyWithTranscript(context.Background(), "some_tool", map[string]any{"a": 1}, nil, "id1"); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if len(api.requests) != 2 {
		t.Fatalf("a blocked fast verdict goes on to the thinking stage: got %d requests", len(api.requests))
	}
	for i, req := range api.requests {
		if !req.DisableThinking {
			t.Errorf("stage %d must ask the provider for no hidden reasoning", i+1)
		}
	}
}
