package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/KPO-Tech/seshat-runtime/internal/db"
	engineconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// SetStartupConfig stores the options needed to rebuild the SDK client when
// the user switches provider or model from within the TUI.
// Must be called before the Bubble Tea program starts.
func (w *SeshatWorkspace) SetStartupConfig(sqlitePath string, permMode sdk.PermissionMode, monitoring *sdk.MonitoringSystem) {
	w.sqlitePath = sqlitePath
	w.permMode = permMode
	w.monitoring = monitoring
}

// DetectProviders probes env vars, the credentials DB, and Ollama to populate
// the workspace's provider key store. Run this in a background goroutine
// immediately after SetStartupConfig — it blocks on network I/O.
func (w *SeshatWorkspace) DetectProviders() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, p := range engineconfig.AvailableProviders() {
		pid := string(p.Name)
		if _, already := w.providerKeys.Load(pid); !already {
			for _, envVar := range engineconfig.ProviderCredentialEnvVars(p.Name) {
				if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
					w.providerKeys.Store(pid, v)
					break
				}
			}
		}
		if pid == "ollama" {
			if v := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); v != "" {
				normalized := strings.TrimRight(v, "/")
				w.providerBaseURLs.Store(pid, normalized)
				_ = os.Setenv("OLLAMA_HOST", normalized)
			}
		}
	}

	if engCfg, err := engineconfig.Load(); err == nil {
		dbPath := engineconfig.EffectiveSessionDBPath(engCfg)
		if database, err := db.Open(context.Background(), db.DefaultSQLiteConfig(dbPath)); err == nil {
			defer database.Close()
			dbCtx := context.Background()
			for _, p := range engineconfig.AvailableProviders() {
				pid := string(p.Name)
				if _, already := w.providerKeys.Load(pid); !already {
					if val, ok, _ := database.GetCredential(dbCtx, "api_key:"+strings.ToLower(pid)); ok && strings.TrimSpace(val) != "" {
						w.providerKeys.Store(pid, val)
					}
				}
				if _, already := w.providerBaseURLs.Load(pid); !already {
					if val, ok, _ := database.GetCredential(dbCtx, "base_url:"+strings.ToLower(pid)); ok && strings.TrimSpace(val) != "" {
						w.providerBaseURLs.Store(pid, strings.TrimRight(strings.TrimSpace(val), "/"))
					}
				}
			}

			if os.Getenv("WEB_SEARCH_PROVIDER") == "" {
				if val, ok, _ := database.GetCredential(dbCtx, "setting:web_search_provider"); ok && strings.TrimSpace(val) != "" {
					_ = os.Setenv("WEB_SEARCH_PROVIDER", strings.TrimSpace(val))
				}
			}
			for _, pid := range []string{"tavily", "exa", "jina", "langsearch"} {
				envVar := webSearchAPIKeyEnvVar(pid)
				if envVar == "" || os.Getenv(envVar) != "" {
					continue
				}
				if val, ok, _ := database.GetCredential(dbCtx, "web_search_api_key:"+pid); ok && strings.TrimSpace(val) != "" {
					_ = os.Setenv(envVar, strings.TrimSpace(val))
				}
			}
			if os.Getenv("SEARXNG_BASE_URL") == "" {
				if val, ok, _ := database.GetCredential(dbCtx, "web_search_base_url:searxng"); ok && strings.TrimSpace(val) != "" {
					_ = os.Setenv("SEARXNG_BASE_URL", strings.TrimSpace(val))
				}
			}
		}
	}

	ollamaBase := w.resolveProviderBaseURL("ollama")
	if models := fetchOllamaModels(ctx, ollamaBase); len(models) > 0 {
		w.ollamaMu.Lock()
		w.ollamaModels = models
		w.ollamaMu.Unlock()
		w.providerKeys.Store("ollama", "")
	}

	w.cfgMu.Lock()
	w.cfg = nil
	w.cfgMu.Unlock()
}

// resolveAPIKey returns the stored API key for providerID, falling back to env.
func (w *SeshatWorkspace) resolveAPIKey(providerID string) string {
	if v, ok := w.providerKeys.Load(providerID); ok {
		if key, _ := v.(string); key != "" {
			return key
		}
	}
	for _, envVar := range engineconfig.ProviderCredentialEnvVars(engineconfig.ResolveProvider(providerID)) {
		if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
			return v
		}
	}
	return ""
}

func (w *SeshatWorkspace) resolveProviderBaseURL(providerID string) string {
	if v, ok := w.providerBaseURLs.Load(providerID); ok {
		if baseURL, _ := v.(string); strings.TrimSpace(baseURL) != "" {
			return strings.TrimSpace(baseURL)
		}
	}
	if providerID == "ollama" {
		if v := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return defaultProviderBaseURL(providerID)
}

func defaultProviderBaseURL(providerID string) string {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "openai":
		return "https://api.openai.com/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "z-ai", "zai":
		return "https://api.z.ai/api/paas/v4"
	case "minimax":
		return "https://api.minimax.chat/v1"
	case "opencode":
		return "https://opencode.ai/zen/v1"
	case "kimi":
		return "https://api.moonshot.ai/v1"
	case "ollama":
		return "http://localhost:11434"
	default:
		return ""
	}
}

func sdkProviderBaseURL(providerID, baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "anthropic":
		return strings.TrimSuffix(baseURL, "/v1")
	case "ollama":
		return strings.TrimSuffix(baseURL, "/v1")
	case "gemini":
		if strings.Contains(baseURL, "/v1beta") {
			return baseURL
		}
		return strings.TrimRight(baseURL, "/") + "/v1beta"
	default:
		return baseURL
	}
}

func webSearchAPIKeyEnvVar(providerID string) string {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "tavily":
		return "TAVILY_API_KEY"
	case "exa":
		return "EXA_API_KEY"
	case "jina":
		return "JINA_API_KEY"
	case "langsearch":
		return "LANGSEARCH_API_KEY"
	default:
		return ""
	}
}

func webSearchBaseURLEnvVar(providerID string) string {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "searxng":
		return "SEARXNG_BASE_URL"
	default:
		return ""
	}
}

func (w *SeshatWorkspace) persistCredential(key, value string) {
	engCfg, err := engineconfig.Load()
	if err != nil {
		return
	}
	database, err := db.Open(context.Background(), db.DefaultSQLiteConfig(engineconfig.EffectiveSessionDBPath(engCfg)))
	if err != nil {
		return
	}
	defer database.Close()
	ctx := context.Background()
	if strings.TrimSpace(value) == "" {
		_ = database.DeleteCredential(ctx, key)
		return
	}
	_ = database.UpsertCredential(ctx, key, value)
}

// persistProviderAPIKey saves providerID's API key to the credentials DB.
// Best-effort: errors are logged but not propagated.
func (w *SeshatWorkspace) persistProviderAPIKey(providerID, apiKey string) {
	w.persistCredential("api_key:"+strings.ToLower(providerID), apiKey)
}

func (w *SeshatWorkspace) persistProviderBaseURL(providerID, baseURL string) {
	w.persistCredential("base_url:"+strings.ToLower(providerID), baseURL)
}

// fetchOllamaModels queries Ollama's /api/tags endpoint and converts the
// response to a catwalk model list.
func fetchOllamaModels(ctx context.Context, baseURL string) []catwalk.Model {
	baseURL = strings.TrimRight(strings.TrimSuffix(baseURL, "/v1"), "/")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}

	models := make([]catwalk.Model, 0, len(payload.Models))
	for _, m := range payload.Models {
		displayName := m.Name
		if tag := strings.LastIndex(displayName, ":"); tag > 0 && displayName[tag+1:] == "latest" {
			displayName = displayName[:tag]
		}
		models = append(models, catwalk.Model{
			ID:            m.Name,
			Name:          displayName,
			ContextWindow: 128000,
		})
	}
	return models
}

// catwalkTypeFor maps a seshat provider ID to the matching catwalk.Type
// used by the TUI's API key verification flow.
func catwalkTypeFor(providerID string) catwalk.Type {
	switch providerID {
	case "anthropic":
		return catwalk.TypeAnthropic
	case "gemini":
		return catwalk.TypeGoogle
	default:
		return catwalk.TypeOpenAI
	}
}

// displayNameFor returns the human-readable display name for a provider ID.
func displayNameFor(providerID string) string {
	for _, p := range engineconfig.AvailableProviders() {
		if string(p.Name) == providerID {
			return p.DisplayName
		}
	}
	return providerID
}
