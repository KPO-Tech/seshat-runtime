package sdk

import (
	"context"
	"fmt"
	"os"
	"strings"

	authstore "github.com/KPO-Tech/seshat-runtime/internal/auth/store"
	authtypes "github.com/KPO-Tech/seshat-runtime/internal/auth/types"
	publicoauth "github.com/KPO-Tech/seshat-runtime/pkg/auth/oauth"
)

const DefaultOpenAIDeviceClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// OAuthDeviceFlowConfig configures a public device-flow login that can be used
// by SDK hosts to authenticate Codex / ChatGPT-backed providers without going
// through the CLI.
type OAuthDeviceFlowConfig struct {
	Provider string
	ClientID string
	AuthPath string
	Persist  bool
}

// OAuthDeviceChallenge is the user-facing challenge returned by a device flow.
type OAuthDeviceChallenge struct {
	DeviceCode              string
	UserCode                string
	VerificationURL         string
	VerificationURLComplete string
	ExpiresIn               int
	Interval                int
}

// StoredCredentialResolverConfig configures an auth-store backed resolver for
// SDK client creation. This is the clean bridge between a host-managed login
// flow and sdk.NewClient.
type StoredCredentialResolverConfig struct {
	AuthPath string
	EnvFirst bool
}

// StoredCredentialResolver resolves provider credentials from env vars and the
// local auth store. It implements CredentialResolver.
type StoredCredentialResolver struct {
	authPath string
	envFirst bool
}

// OAuthDeviceFlow is a reusable public wrapper around the OpenAI / Codex device
// flow. It can persist the resulting token and expose a resolver that plugs
// directly into sdk.ClientConfig.
type OAuthDeviceFlow struct {
	provider    string
	authPath    string
	persist     bool
	oauthCfg    *publicoauth.Config
	oauthClient *publicoauth.Client
}

// NewStoredCredentialResolver creates a resolver that can inject saved API keys
// or refreshed OAuth access tokens into sdk.NewClient.
func NewStoredCredentialResolver(config StoredCredentialResolverConfig) *StoredCredentialResolver {
	return &StoredCredentialResolver{
		authPath: resolveSDKAuthPath(config.AuthPath),
		envFirst: config.EnvFirst,
	}
}

// ResolveAPIKey implements CredentialResolver.
func (r *StoredCredentialResolver) ResolveAPIKey(ctx context.Context, provider string) (string, error) {
	return resolveStoredProviderCredential(ctx, normalizeAuthProvider(provider), r.authPath, r.envFirst)
}

// NewOAuthDeviceFlow creates a public SDK-friendly device flow for providers
// backed by ChatGPT OAuth, currently codex and openai.
func NewOAuthDeviceFlow(config OAuthDeviceFlowConfig) (*OAuthDeviceFlow, error) {
	provider := normalizeAuthProvider(config.Provider)
	if provider == "" {
		provider = string(APIProviderCodex)
	}

	switch provider {
	case string(APIProviderCodex), string(APIProviderOpenAI):
	default:
		return nil, fmt.Errorf("oauth device flow is not supported for provider %q", provider)
	}

	oauthCfg := publicoauth.DefaultOpenAIConfig()
	if strings.TrimSpace(oauthCfg.ClientID) == "" {
		oauthCfg.ClientID = DefaultOpenAIDeviceClientID
	}
	if clientID := strings.TrimSpace(config.ClientID); clientID != "" {
		cloned := *oauthCfg
		cloned.ClientID = clientID
		oauthCfg = &cloned
	}

	return &OAuthDeviceFlow{
		provider:    provider,
		authPath:    resolveSDKAuthPath(config.AuthPath),
		persist:     config.Persist || config.AuthPath != "",
		oauthCfg:    oauthCfg,
		oauthClient: publicoauth.NewClient(oauthCfg),
	}, nil
}

// Start begins the device flow and returns the user challenge to display.
func (f *OAuthDeviceFlow) Start(ctx context.Context) (*OAuthDeviceChallenge, error) {
	resp, err := f.oauthClient.DeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	return &OAuthDeviceChallenge{
		DeviceCode:              resp.DeviceCode,
		UserCode:                resp.UserCode,
		VerificationURL:         resp.VerificationURI,
		VerificationURLComplete: resp.VerificationURIComplete,
		ExpiresIn:               resp.ExpiresIn,
		Interval:                resp.Interval,
	}, nil
}

// Wait blocks until the device flow completes, optionally persisting the
// resulting token for later SDK sessions.
func (f *OAuthDeviceFlow) Wait(ctx context.Context, challenge *OAuthDeviceChallenge) (*publicoauth.Token, error) {
	if challenge == nil {
		return nil, fmt.Errorf("challenge is required")
	}
	resp, err := f.oauthClient.PollToken(ctx, challenge.DeviceCode, challenge.UserCode, challenge.Interval)
	if err != nil {
		return nil, err
	}
	token := resp.ToToken()
	if f.persist {
		if err := f.SaveToken(ctx, token); err != nil {
			return nil, err
		}
	}
	return token, nil
}

// SaveToken persists the token into the SDK auth store for this provider.
func (f *OAuthDeviceFlow) SaveToken(ctx context.Context, token *publicoauth.Token) error {
	if token == nil {
		return fmt.Errorf("token is required")
	}
	store, err := authstore.NewFileStore(f.authPath)
	if err != nil {
		return err
	}
	creds := &authtypes.Credentials{
		Provider:   f.provider,
		AuthMethod: authtypes.AuthMethodOAuth,
		APIKey:     token.AccessToken,
		OAuth: &authtypes.OAuthCredentials{
			BaseURL:        f.oauthCfg.BaseURL,
			ClientID:       f.oauthCfg.ClientID,
			ClientSecret:   f.oauthCfg.ClientSecret,
			AuthURL:        f.oauthCfg.AuthURL,
			TokenURL:       f.oauthCfg.TokenURL,
			Scopes:         append([]string(nil), f.oauthCfg.Scopes...),
			DeviceAuthURL:  f.oauthCfg.DeviceAuthURL,
			DeviceTokenURL: f.oauthCfg.DeviceTokenURL,
		},
		Token: &authtypes.Token{
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			IDToken:      token.IDToken,
			TokenType:    authtypes.TokenType(token.TokenType),
			ExpiresAt:    token.ExpiresAt,
			Scope:        token.Scope,
		},
	}
	return store.SaveCredentials(ctx, creds)
}

// ResolveAccessToken resolves a valid access token from the configured auth
// store, refreshing it when possible.
func (f *OAuthDeviceFlow) ResolveAccessToken(ctx context.Context) (string, error) {
	return resolveStoredProviderCredential(ctx, f.provider, f.authPath, true)
}

// CredentialResolver returns a resolver that uses the same auth store.
func (f *OAuthDeviceFlow) CredentialResolver() CredentialResolver {
	return NewStoredCredentialResolver(StoredCredentialResolverConfig{
		AuthPath: f.authPath,
		EnvFirst: true,
	})
}

// AuthPath returns the auth-store path used by the flow.
func (f *OAuthDeviceFlow) AuthPath() string {
	return f.authPath
}

func resolveStoredProviderCredential(ctx context.Context, provider, authPath string, envFirst bool) (string, error) {
	if provider == "" {
		return "", nil
	}
	if envFirst {
		for _, envVar := range providerEnvVars(provider) {
			if key := strings.TrimSpace(os.Getenv(envVar)); key != "" {
				return key, nil
			}
		}
	}
	store, err := authstore.NewFileStore(resolveSDKAuthPath(authPath))
	if err != nil {
		return "", err
	}
	creds, err := store.LoadCredentials(ctx, provider)
	if err != nil {
		if isMissingCredentialError(err) {
			return "", nil
		}
		return "", err
	}
	if creds.AuthMethod != authtypes.AuthMethodOAuth {
		return strings.TrimSpace(creds.APIKey), nil
	}
	token := creds.Token
	if token == nil {
		return strings.TrimSpace(creds.APIKey), nil
	}
	if !token.NeedsRefresh() && strings.TrimSpace(token.AccessToken) != "" {
		return strings.TrimSpace(token.AccessToken), nil
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		if strings.TrimSpace(token.AccessToken) != "" {
			return strings.TrimSpace(token.AccessToken), nil
		}
		return "", fmt.Errorf("oauth token for provider %q has no refresh token", provider)
	}

	oauthCfg := oauthConfigFromCredentials(provider, creds)
	refreshed, err := publicoauth.NewClient(oauthCfg).RefreshToken(ctx, token.RefreshToken)
	if err != nil {
		return "", err
	}
	newToken := refreshed.ToToken()
	creds.APIKey = newToken.AccessToken
	creds.Token = &authtypes.Token{
		AccessToken:  newToken.AccessToken,
		RefreshToken: newToken.RefreshToken,
		IDToken:      newToken.IDToken,
		TokenType:    authtypes.TokenType(newToken.TokenType),
		ExpiresAt:    newToken.ExpiresAt,
		Scope:        newToken.Scope,
	}
	if err := store.SaveCredentials(ctx, creds); err != nil {
		return "", err
	}
	return strings.TrimSpace(newToken.AccessToken), nil
}

func oauthConfigFromCredentials(provider string, creds *authtypes.Credentials) *publicoauth.Config {
	if creds != nil && creds.OAuth != nil {
		return &publicoauth.Config{
			BaseURL:        creds.OAuth.BaseURL,
			ClientID:       creds.OAuth.ClientID,
			ClientSecret:   creds.OAuth.ClientSecret,
			AuthURL:        creds.OAuth.AuthURL,
			TokenURL:       creds.OAuth.TokenURL,
			DeviceAuthURL:  creds.OAuth.DeviceAuthURL,
			DeviceTokenURL: creds.OAuth.DeviceTokenURL,
			Scopes:         append([]string(nil), creds.OAuth.Scopes...),
		}
	}
	switch normalizeAuthProvider(provider) {
	case string(APIProviderCodex), string(APIProviderOpenAI):
		return publicoauth.DefaultOpenAIConfig()
	default:
		return publicoauth.DefaultOpenAIConfig()
	}
}

func resolveSDKAuthPath(explicit string) string {
	return authstore.ResolvePath(explicit)
}

func normalizeAuthProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func providerEnvVars(provider string) []string {
	switch normalizeAuthProvider(provider) {
	case string(APIProviderOpenAI):
		return []string{"OPENAI_API_KEY"}
	case string(APIProviderAnthropic):
		return []string{"ANTHROPIC_API_KEY"}
	case string(APIProviderGemini):
		return []string{"GOOGLE_API_KEY", "GOOGLE_GEMINI_API_KEY"}
	case string(APIProviderOllama):
		return []string{"OLLAMA_API_KEY"}
	case string(APIProviderZAi):
		// Order matches pkg/config/provider_catalog.go's
		// ProviderCredentialEnvVars (M5d in TODO.md - the two lists used to
		// disagree on which env var to check/suggest first).
		return []string{"ZHIPUAI_API_KEY", "Z_AI_API_KEY"}
	case string(APIProviderOpenRouter):
		return []string{"OPENROUTER_API_KEY"}
	case string(APIProviderMiniMax):
		return []string{"MINIMAX_API_KEY"}
	case string(APIProviderMistral):
		return []string{"MISTRAL_API_KEY"}
	case string(APIProviderCodex):
		// Order matches pkg/config/provider_catalog.go's
		// ProviderCredentialEnvVars (M5d in TODO.md).
		return []string{"CODEX_API_KEY", "CODEX_ACCESS_TOKEN"}
	case string(APIProviderDeepSeek):
		return []string{"DEEPSEEK_API_KEY"}
	case string(APIProviderOpenCode):
		return []string{"OPENCODE_API_KEY"}
	case string(APIProviderKimi):
		// Matches pkg/config/provider_catalog.go's ProviderCredentialEnvVars -
		// this list used to fall through to the generic "API_KEY" default for
		// Kimi instead (M5d in TODO.md).
		return []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"}
	default:
		return []string{"API_KEY"}
	}
}

func providerEnvVar(provider string) string {
	return providerEnvVars(provider)[0]
}

func isMissingCredentialError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no credentials for provider")
}
