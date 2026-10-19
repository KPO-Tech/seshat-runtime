// CLI-era credential helpers.
//
// LoginProvider, WaitForLogin, GetOAuthToken, SetAPIKey and LoginCommand are
// designed for headless / CLI use. They store credentials in a local file
// (~/.seshat/auth.json via internal/auth/store.FileStore) and are NOT suited
// for multi-user server deployments.
//
// In server mode, pass credentials to the engine via sdk.ClientConfig.APIKey
// or by implementing sdk.CredentialResolver for per-user resolution.
package providers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/auth/oauth"
	"github.com/KPO-Tech/seshat-runtime/internal/auth/store"
	authTypes "github.com/KPO-Tech/seshat-runtime/internal/auth/types"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ============================================================================
// OAuth Login Implementation — CLI-era
// ============================================================================

// chatGPTClientID is the public OAuth client ID used by Codex CLI (github.com/openai/codex)
// for the Auth0 device-code flow. It is embedded in OpenAI's open-source CLI.
const chatGPTClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

var (
	oauthHandler *oauth.SimpleAuthenticator
	oauthClient  *oauth.Client
)

// InitOAuth initializes the OAuth client. clientID defaults to the Codex CLI client ID.
func InitOAuth(clientID string) {
	if clientID == "" {
		clientID = chatGPTClientID
	}
	config := oauth.DefaultOpenAIConfig()
	config.ClientID = clientID
	oauthClient = oauth.NewClient(config)
	oauthHandler = oauth.NewSimpleAuthenticator(clientID)
}

// LoginProvider starts the ChatGPT device-code flow and returns the user code
// + URL to display. CLI-era: saves result to ~/.seshat/auth.json.
func LoginProvider(ctx context.Context, provider string) (string, string, error) {
	if oauthHandler == nil {
		InitOAuth(os.Getenv("OPENAI_CLIENT_ID"))
	}
	userCode, verificationURL, err := oauthHandler.StartDeviceFlow(ctx)
	if err != nil {
		return "", "", fmt.Errorf("start OAuth device flow: %w", err)
	}
	return userCode, verificationURL, nil
}

// WaitForLogin polls Auth0 until the user completes authentication, then
// persists the full token to ~/.seshat/auth.json. CLI-era.
func WaitForLogin(ctx context.Context, provider string) error {
	if oauthHandler == nil {
		return fmt.Errorf("OAuth not initialized — call LoginProvider first")
	}

	token, err := oauthHandler.WaitDeviceFlow(ctx)
	if err != nil {
		return fmt.Errorf("wait for device flow: %w", err)
	}

	authStore, err := store.NewFileStore(defaultAuthPath())
	if err != nil {
		return err
	}

	creds := &authTypes.Credentials{
		Provider:   provider,
		AuthMethod: authTypes.AuthMethodOAuth,
		// Mirror access token in APIKey for backward-compat with getAPIKeyForProvider.
		APIKey: token.AccessToken,
		Token: &authTypes.Token{
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			IDToken:      token.IDToken,
			TokenType:    authTypes.TokenTypeAccess,
			ExpiresAt:    token.ExpiresAt,
		},
	}
	return authStore.SaveCredentials(ctx, creds)
}

// GetOAuthToken returns a valid OAuth access token for provider, refreshing if needed.
func GetOAuthToken(ctx context.Context, provider string) (string, error) {
	authStore, err := store.NewFileStore(defaultAuthPath())
	if err != nil {
		return "", err
	}
	creds, err := authStore.LoadCredentials(ctx, provider)
	if err != nil {
		return "", err
	}
	if creds.AuthMethod != authTypes.AuthMethodOAuth {
		return "", fmt.Errorf("provider %q is not using OAuth", provider)
	}
	return resolveOAuthToken(ctx, authStore, creds)
}

// resolveOAuthToken returns a non-expired access token, refreshing via refresh_token when needed.
func resolveOAuthToken(ctx context.Context, s store.Store, creds *authTypes.Credentials) (string, error) {
	t := creds.Token
	if t == nil {
		// Legacy: token data was stored only in APIKey before this fix.
		return creds.APIKey, nil
	}

	if !t.NeedsRefresh() {
		return t.AccessToken, nil
	}

	if t.RefreshToken == "" {
		return "", fmt.Errorf("OAuth token expired and no refresh token available — run `seshat login`")
	}

	newToken, err := refreshAccessToken(ctx, t.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}

	// Persist refreshed token.
	creds.APIKey = newToken.AccessToken
	creds.Token = newToken
	_ = s.SaveCredentials(ctx, creds) // best-effort

	return newToken.AccessToken, nil
}

// refreshAccessToken exchanges a refresh token for a new access token via Auth0.
func refreshAccessToken(ctx context.Context, refreshToken string) (*authTypes.Token, error) {
	if oauthClient == nil {
		config := oauth.DefaultOpenAIConfig()
		config.ClientID = chatGPTClientID
		oauthClient = oauth.NewClient(config)
	}
	resp, err := oauthClient.RefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Time{}
	if resp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	return &authTypes.Token{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		IDToken:      resp.IDToken,
		TokenType:    authTypes.TokenTypeAccess,
		ExpiresAt:    expiresAt,
	}, nil
}

// ============================================================================
// Existing AuthClient code (unchanged)
// ============================================================================

// AuthClient wraps a Client with authentication support
type AuthClient struct {
	*Client
}

// NewAuthClient creates a new auth-aware client
func NewAuthClient(ctx context.Context, provider types.APIProvider) (*AuthClient, error) {
	// Get API key using env var / auth store
	apiKey, err := getAPIKeyForProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	if apiKey == "" {
		return nil, fmt.Errorf("no API key found for provider: %s", provider)
	}

	client := NewClient(apiKey, provider)
	return &AuthClient{Client: client}, nil
}

// NewAuthClientWithConfig creates a new auth-aware client with config
func NewAuthClientWithConfig(ctx context.Context, config *Config) (*AuthClient, error) {
	apiKey, err := getAPIKeyForConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if apiKey == "" {
		return nil, fmt.Errorf("no API key found for provider: %s", config.Provider)
	}

	client := NewClientWithConfig(apiKey, config)
	return &AuthClient{Client: client}, nil
}

// getAPIKeyForProvider returns the API key (or OAuth access token) for a provider.
// Resolution order: env var → FileStore (~/.seshat/auth.json). CLI-era.
// In server mode, use sdk.ClientConfig.CredentialResolver instead.
func getAPIKeyForProvider(ctx context.Context, provider types.APIProvider) (string, error) {
	// Environment variable always wins.
	if key := os.Getenv(providerEnvVar(provider)); key != "" {
		return key, nil
	}

	s, err := store.NewFileStore(defaultAuthPath())
	if err != nil {
		return "", nil
	}

	creds, err := s.LoadCredentials(ctx, string(provider))
	if err != nil {
		return "", nil
	}

	if creds.AuthMethod == authTypes.AuthMethodOAuth {
		return resolveOAuthToken(ctx, s, creds)
	}
	return creds.APIKey, nil
}

// getAPIKeyForConfig returns the API key (or OAuth access token) for a config.
func getAPIKeyForConfig(ctx context.Context, config *Config) (string, error) {
	if key := os.Getenv(providerEnvVar(config.Provider)); key != "" {
		return key, nil
	}
	if config.APIKey != "" {
		return config.APIKey, nil
	}

	s, err := store.NewFileStore(defaultAuthPath())
	if err != nil {
		return "", nil
	}

	creds, err := s.LoadCredentials(ctx, string(config.Provider))
	if err != nil {
		return "", nil
	}

	if creds.AuthMethod == authTypes.AuthMethodOAuth {
		return resolveOAuthToken(ctx, s, creds)
	}
	return creds.APIKey, nil
}

// providerEnvVar returns the environment variable for a provider
func providerEnvVar(provider types.APIProvider) string {
	switch provider {
	case types.APIProviderOpenAI:
		return "OPENAI_API_KEY"
	case types.APIProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case types.APIProviderGemini:
		return "GOOGLE_API_KEY"
	case types.APIProviderOllama:
		return "OLLAMA_API_KEY"
	case types.APIProviderZAi:
		return "Z_AI_API_KEY"
	case types.APIProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case types.APIProviderMiniMax:
		return "MINIMAX_API_KEY"
	case types.APIProviderWorkersAI:
		return "CLOUDFLARE_API_KEY"
	case types.APIProviderFoundry:
		return "ANTHROPIC_FOUNDRY_API_KEY"
	case types.APIProviderMistral:
		return "MISTRAL_API_KEY"
	case types.APIProviderKimi:
		return "KIMI_API_KEY"
	case types.APIProviderCodex:
		return "CODEX_API_KEY"
	case types.APIProviderDeepSeek:
		return "DEEPSEEK_API_KEY"
	case types.APIProviderOpenCode:
		return "OPENCODE_API_KEY"
	default:
		if env := catalogEnvVar(provider); env != "" {
			return env
		}
		return "API_KEY"
	}
}

func defaultAuthPath() string {
	return store.ResolvePath("")
}

// ============================================================================
// Global API Key
// ============================================================================

var globalAPIKey string

// SetGlobalAPIKey sets the global API key
func SetGlobalAPIKey(key string) {
	globalAPIKey = key
}

// GetGlobalAPIKey gets the global API key
func GetGlobalAPIKey() string {
	if globalAPIKey != "" {
		return globalAPIKey
	}
	return os.Getenv("OPENAI_API_KEY")
}

// ============================================================================
// Auth API for CLI
// ============================================================================

// SetAPIKey explicitly sets an API key
func SetAPIKey(ctx context.Context, provider, apiKey string) error {
	globalAPIKey = apiKey

	// Also save to store
	s, err := store.NewFileStore(defaultAuthPath())
	if err != nil {
		return err
	}

	creds := &authTypes.Credentials{
		Provider:   provider,
		AuthMethod: authTypes.AuthMethodAPIKey,
		APIKey:     apiKey,
	}

	return s.SaveCredentials(ctx, creds)
}

// LoginCommand performs provider login
type LoginCommand struct {
	Provider string
	Method   string
}

// Execute performs login
func (c *LoginCommand) Execute(ctx context.Context) error {
	if c.Provider == "" {
		c.Provider = "openai"
	}

	switch c.Method {
	case "oauth":
		return performOAuthLogin(ctx, c.Provider)
	case "api_key", "":
		fmt.Printf("For API key login, set the %s_API_KEY environment variable\n",
			strings.ToUpper(c.Provider))
		return nil
	default:
		return fmt.Errorf("unknown login method: %s", c.Method)
	}
}

func performOAuthLogin(ctx context.Context, provider string) error {
	userCode, url, err := LoginProvider(ctx, provider)
	if err != nil {
		return err
	}

	fmt.Printf("\n📱 To authenticate with %s:\n", strings.ToUpper(provider))
	fmt.Printf("   1. Visit: %s\n", url)
	fmt.Printf("   2. Enter code: %s\n\n", userCode)
	fmt.Printf("⏳ Waiting for authentication...\n")

	// Wait for completion
	if err := WaitForLogin(ctx, provider); err != nil {
		return err
	}

	fmt.Printf("✅ Successfully authenticated!\n")
	return nil
}
