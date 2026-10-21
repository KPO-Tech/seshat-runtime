// Package auto - Classifier API client.
//
// This module provides the API client interface for making classification
// requests to the LLM provider. It wraps the Seshat providers.Client to provide
// a classifier-specific interface with proper request/response handling.
//
// The ClassifierAPI interface defines the contract for classifier API clients,
// allowing for different implementations (e.g., mock for testing, real for production).
package auto

import (
	"context"
	"math/rand"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ClassifierAPI interface defines the contract for classifier API clients.
// Implementations must provide a Classify method that processes classification requests.
type ClassifierAPI interface {
	Classify(ctx context.Context, req *ClassifierAPIRequest) (*ClassifierAPIResponse, error)
}

type ClassifierAPIRequest struct {
	Model         string
	MaxTokens     int
	System        string
	Temperature   float64
	Messages      []types.Message
	StopSequences []string
	Thinking      bool
	// DisableThinking asks a provider that can switch reasoning off to answer at once. The fast stage needs a verdict, not a
	// chain of thought: on a reasoning model it otherwise spends seconds thinking before it writes one word.
	DisableThinking bool
}

type ClassifierAPIResponse struct {
	Text       string
	Usage      *ClassifierUsage
	MessageID  string
	RequestID  string
	StopReason string
}

type ClassifierAPIClient struct {
	client   *providers.Client
	provider types.APIProvider
}

func NewClassifierAPIClient(client *providers.Client) *ClassifierAPIClient {
	return &ClassifierAPIClient{
		client: client,
	}
}

// WithProvider names the provider the client talks to. The request carries it so that provider-specific options (turning
// reasoning off) reach the right adapter; without it the request is addressed to Anthropic.
func (c *ClassifierAPIClient) WithProvider(provider types.APIProvider) *ClassifierAPIClient {
	c.provider = provider
	return c
}

func (c *ClassifierAPIClient) Classify(ctx context.Context, req *ClassifierAPIRequest) (*ClassifierAPIResponse, error) {
	messages := append(make([]types.Message, 0, len(req.Messages)), req.Messages...)

	lastMsg := messages[len(messages)-1]
	userContent := lastMsg.Content
	messages = messages[:len(messages)-1]

	userMsg := types.Message{
		ID:      types.MessageID("classifier-user-" + generateID()),
		Role:    types.RoleUser,
		Content: userContent,
	}
	messages = append(messages, userMsg)

	systemPrompt := req.System
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant."
	}

	temp := req.Temperature

	apiReq := c.buildAPIRequest(req, messages, systemPrompt, &temp)

	response, err := c.client.CreateMessage(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	var text string
	for _, block := range response.Content {
		if tc, ok := block.(types.TextContent); ok {
			text = tc.Text
			break
		}
	}

	usage := &ClassifierUsage{
		InputTokens:  0,
		OutputTokens: 0,
	}
	if response.Usage.InputTokens > 0 || response.Usage.OutputTokens > 0 {
		usage.InputTokens = response.Usage.InputTokens
		usage.OutputTokens = response.Usage.OutputTokens
	}

	return &ClassifierAPIResponse{
		Text:       text,
		Usage:      usage,
		MessageID:  string(response.ID),
		StopReason: response.StopReason,
	}, nil
}

// buildAPIRequest turns a classifier request into a provider request.
func (c *ClassifierAPIClient) buildAPIRequest(req *ClassifierAPIRequest, messages []types.Message, systemPrompt string, temp *float64) types.APIRequest {
	provider := c.provider
	if provider == "" {
		provider = types.APIProviderAnthropic
	}
	return types.APIRequest{
		Model: types.ModelIdentifier{
			Provider: provider,
			Model:    req.Model,
		},
		MaxTokens:       req.MaxTokens,
		Temperature:     temp,
		SystemPrompt:    systemPrompt,
		Messages:        messages,
		StopSequences:   req.StopSequences,
		DisableThinking: req.DisableThinking,
	}
}

func generateID() string {
	return "msg-" + randomString(8)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
