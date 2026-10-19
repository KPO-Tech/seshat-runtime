package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// FetchedModel is a provider-neutral model description returned by FetchModels
// and StaticModels. Callers convert it to their own storage type.
type FetchedModel struct {
	ModelID       string
	DisplayName   string
	ContextWindow int
	MaxOutput     int
	IsDefault     bool
	SortOrder     int
}

// FetchModels retrieves the live model list from a provider API.
// For Ollama it calls /api/tags and enriches each model via /api/show.
// For OpenAI-compatible providers it calls /v1/models.
// Returns an error so the caller can fall back to StaticModels.
func FetchModels(ctx context.Context, providerName, baseURL, apiKey string) ([]FetchedModel, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL(providerName)
	}
	client := &http.Client{Timeout: 10 * time.Second}

	switch strings.ToLower(providerName) {
	case "ollama":
		return fetchOllamaModels(ctx, client, baseURL)
	default:
		raw, err := fetchOpenAICompatModels(ctx, client, baseURL, apiKey)
		if err != nil {
			return nil, err
		}
		return NormalizeModels(providerName, raw), nil
	}
}

// StaticModels returns the hardcoded catalog for a provider.
// Returns nil for Ollama, which has no static model list (100% dynamic).
func StaticModels(providerName string) []FetchedModel {
	provider := ResolveProviderFromString(providerName)
	info, ok := GetProviderInfo(provider)
	if !ok {
		return nil
	}
	out := make([]FetchedModel, 0, len(info.Models))
	for i, m := range info.Models {
		out = append(out, FetchedModel{
			ModelID:       m.Identifier,
			DisplayName:   m.Identifier,
			ContextWindow: m.ContextWindow,
			MaxOutput:     m.MaxOutput,
			IsDefault:     i == 0,
			SortOrder:     i,
		})
	}
	return out
}

// DefaultBaseURL returns the default API base URL for a provider.
// These are sync/discovery URLs (no /v1 suffix); client endpoints live in config.go.
func DefaultBaseURL(providerName string) string {
	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case "ollama":
		return "http://localhost:11434"
	case "openai":
		return "https://api.openai.com"
	case "anthropic":
		return "https://api.anthropic.com"
	case "z-ai", "z.ai", "zai":
		return "https://api.z.ai/api/paas/v4"
	case "openrouter":
		return "https://openrouter.ai/api"
	case "mistral", "mistral-ai", "mistralai":
		return "https://api.mistral.ai"
	case "deepseek", "deep-seek":
		return "https://api.deepseek.com"
	case "opencode", "opencode-zen", "opencode_zen":
		return "https://opencode.ai/zen"
	case "kimi", "moonshot", "moonshot-ai", "moonshotai":
		// International endpoint - api.moonshot.cn is a separate China
		// mainland deployment with its own accounts/billing; keys aren't
		// interchangeable between the two.
		return "https://api.moonshot.ai"
	case "minimax":
		// International endpoint - api.minimaxi.com is the separate mainland
		// China deployment; keys aren't interchangeable between the two.
		return "https://api.minimax.io"
	default:
		// A generic provider of the catalog: the base URL the catalog gives (already versioned, see modelsURL).
		return catalogBaseURL(ResolveProviderFromString(providerName))
	}
}

// versionedBase matches a base URL that already ends in an API version, as the catalog's do ("https://api.groq.com/openai/v1").
var versionedBase = regexp.MustCompile(`/v\d+[a-z0-9]*$`)

// modelsURL is the route that lists the models of an OpenAI-compatible provider: /models under a versioned base URL, else /v1/models.
func modelsURL(baseURL string) string {
	if versionedBase.MatchString(baseURL) {
		return baseURL + "/models"
	}
	return baseURL + "/v1/models"
}

// NeedsCatalogReseed returns true when none of the supplied model IDs exist in
// the provider's static catalog — meaning models from the wrong provider were
// seeded, or the list is empty.
func NeedsCatalogReseed(providerName string, knownModelIDs []string) bool {
	if len(knownModelIDs) == 0 {
		return true
	}
	provider := ResolveProviderFromString(providerName)
	for _, id := range knownModelIDs {
		if _, ok := GetModelInfo(provider, id); ok {
			return false
		}
	}
	return true
}

// NormalizeModels applies provider-specific post-processing to a raw model list.
// Currently handles Z.ai (restricts to the 3 allowed GLM models and merges static metadata).
func NormalizeModels(providerName string, models []FetchedModel) []FetchedModel {
	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case "z-ai", "z.ai", "zai":
		return normalizeZAiModels(models)
	default:
		return models
	}
}

// ─── Ollama ───────────────────────────────────────────────────────────────────

func fetchOllamaModels(ctx context.Context, client *http.Client, baseURL string) ([]FetchedModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/tags returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var result struct {
		Models []struct {
			Name    string `json:"name"`
			Details struct {
				Family   string   `json:"family"`
				Families []string `json:"families"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	hints := OllamaModelLookup()

	candidates := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		if !isLikelyOllamaEmbeddingModel(m.Name, m.Details.Family, m.Details.Families) {
			candidates = append(candidates, m.Name)
		}
	}
	// If everything looks like an embedding model, keep all (avoid empty list).
	if len(candidates) == 0 {
		for _, m := range result.Models {
			candidates = append(candidates, m.Name)
		}
	}

	out := make([]FetchedModel, 0, len(candidates))
	for i, name := range candidates {
		m := FetchedModel{
			ModelID:     name,
			DisplayName: name,
			IsDefault:   i == 0,
			SortOrder:   i,
		}
		if ctxWin, maxOut, ok := fetchOllamaModelInfo(ctx, client, baseURL, name); ok {
			m.ContextWindow = ctxWin
			m.MaxOutput = maxOut
		} else if hint, ok2 := hints[name]; ok2 {
			m.ContextWindow = hint.ContextWindow
			m.MaxOutput = hint.MaxOutput
		}
		out = append(out, m)
	}
	return out, nil
}

func isLikelyOllamaEmbeddingModel(name, family string, families []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(normalized, "embed") || strings.Contains(normalized, "embedding") {
		return true
	}
	checkFamily := func(v string) bool {
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "bert" || strings.Contains(v, "embed") || strings.Contains(v, "embedding")
	}
	if checkFamily(family) {
		return true
	}
	for _, f := range families {
		if checkFamily(f) {
			return true
		}
	}
	return false
}

func fetchOllamaModelInfo(ctx context.Context, client *http.Client, baseURL, modelName string) (ctxWindow, maxOutput int, ok bool) {
	body, err := json.Marshal(map[string]string{"name": modelName})
	if err != nil {
		return 0, 0, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/show", strings.NewReader(string(body)))
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return 0, 0, false
	}
	var show struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(data, &show); err != nil {
		return 0, 0, false
	}
	ctxLen := 0
	if v, ok2 := show.ModelInfo["llama.context_length"]; ok2 {
		if n, ok3 := v.(float64); ok3 {
			ctxLen = int(n)
		}
	}
	if ctxLen <= 0 {
		return 0, 0, false
	}
	maxOut := ctxLen / 4
	if maxOut > 32768 {
		maxOut = 32768
	}
	return ctxLen, maxOut, true
}

// ─── OpenAI-compatible ────────────────────────────────────────────────────────

func fetchOpenAICompatModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]FetchedModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(baseURL), nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider /v1/models returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	out := make([]FetchedModel, 0, len(result.Data))
	for i, m := range result.Data {
		out = append(out, FetchedModel{
			ModelID:     m.ID,
			DisplayName: m.ID,
			IsDefault:   i == 0,
			SortOrder:   i,
		})
	}
	return out, nil
}

// ─── Z.ai normalization ───────────────────────────────────────────────────────

func normalizeZAiModels(models []FetchedModel) []FetchedModel {
	allowed := map[string]struct{}{
		"glm-4.5": {},
		"glm-4.7": {},
		"glm-5.1": {},
	}
	static := StaticModels("z-ai")
	staticByID := make(map[string]FetchedModel, len(static))
	for _, m := range static {
		staticByID[m.ModelID] = m
	}

	out := make([]FetchedModel, 0, len(static))
	seen := make(map[string]struct{}, len(allowed))
	for _, m := range models {
		if _, ok := allowed[m.ModelID]; !ok {
			continue
		}
		merged := m
		if cat, ok := staticByID[m.ModelID]; ok {
			if merged.DisplayName == "" {
				merged.DisplayName = cat.DisplayName
			}
			if merged.ContextWindow <= 0 {
				merged.ContextWindow = cat.ContextWindow
			}
			if merged.MaxOutput <= 0 {
				merged.MaxOutput = cat.MaxOutput
			}
			merged.IsDefault = cat.IsDefault
			merged.SortOrder = cat.SortOrder
		}
		out = append(out, merged)
		seen[m.ModelID] = struct{}{}
	}
	// Ensure all 3 allowed GLM models appear even if the API omitted them.
	for _, m := range static {
		if _, ok := seen[m.ModelID]; ok {
			continue
		}
		out = append(out, m)
	}
	for i := range out {
		out[i].IsDefault = i == 0
		out[i].SortOrder = i
		if out[i].DisplayName == "" {
			out[i].DisplayName = out[i].ModelID
		}
	}
	return out
}
