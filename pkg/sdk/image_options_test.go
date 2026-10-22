package sdk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// What a host sets for image generation (size, quality, aspect ratio) must reach the request the provider receives.
func TestInitImageGeneratorPassesSizeQualityAndAspectRatio(t *testing.T) {
	var got map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"QUJD"}]}`))
	}))
	defer stub.Close()

	generator := initImageGenerator(&ClientConfig{ImageGeneration: &ImageGenerationConfig{
		Provider: "openai", APIKey: "k", BaseURL: stub.URL, Model: "gpt-image-1", Size: "1536x1024", Quality: "high",
	}})
	if generator == nil {
		t.Fatal("a generator is built for an OpenAI compatible server")
	}
	if _, err := generator.GenerateImage(context.Background(), "a cat"); err != nil {
		t.Fatal(err)
	}
	if got["size"] != "1536x1024" || got["quality"] != "high" || got["model"] != "gpt-image-1" {
		t.Fatalf("size, quality and model reach the request, got %v", got)
	}

	got = nil
	generator = initImageGenerator(&ClientConfig{ImageGeneration: &ImageGenerationConfig{
		Provider: "openrouter", APIKey: "k", BaseURL: stub.URL, Model: "black-forest-labs/flux-3-image", AspectRatio: "16:9",
	}})
	if generator == nil {
		t.Fatal("an OpenRouter generator is built")
	}
	_, _ = generator.GenerateImage(context.Background(), "a cat")
	if got["aspect_ratio"] != "16:9" {
		t.Fatalf("the aspect ratio reaches the OpenRouter request, got %v", got)
	}
}

func TestImageProvidersListsTheCatalogWithItsDefaultModelFirst(t *testing.T) {
	providers := ImageProviders()
	if len(providers) == 0 {
		t.Fatal("the catalog is not empty")
	}
	for _, provider := range providers {
		if provider.ID == "" || provider.DisplayName == "" || len(provider.Models) == 0 {
			t.Errorf("incomplete entry %+v", provider)
		}
	}
}
