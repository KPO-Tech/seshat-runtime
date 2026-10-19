package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/auth/oauth"
	"github.com/KPO-Tech/seshat-runtime/internal/auth/store"
	authTypes "github.com/KPO-Tech/seshat-runtime/internal/auth/types"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func saveCodexCredentials(t *testing.T, access, refresh string, expires time.Time) {
	t.Helper()
	s, err := store.NewFileStore(defaultAuthPath())
	if err != nil {
		t.Fatal(err)
	}
	creds := &authTypes.Credentials{
		Provider: "codex", AuthMethod: authTypes.AuthMethodOAuth, APIKey: access,
		Token: &authTypes.Token{AccessToken: access, RefreshToken: refresh, ExpiresAt: expires, TokenType: authTypes.TokenTypeAccess},
	}
	if err := s.SaveCredentials(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
}

func useTempAuthStore(t *testing.T) {
	t.Helper()
	t.Setenv("SESHAT_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
}

func TestCodexClientRenewsItsTokenFromTheAuthStore(t *testing.T) {
	useTempAuthStore(t)
	saveCodexCredentials(t, "token-a", "refresh-1", time.Now().Add(time.Hour))

	// The client is created with the token the store holds, as the terminal app does.
	client := NewClientWithConfig("token-a", &Config{Provider: types.APIProviderCodex})
	if got := client.authKey(context.Background()); got != "token-a" {
		t.Fatalf("key = %q, want the stored token", got)
	}

	// Another renewal (a second client, or the next start) wrote a newer token: the next request uses it.
	saveCodexCredentials(t, "token-b", "refresh-1", time.Now().Add(time.Hour))
	if got := client.authKey(context.Background()); got != "token-b" {
		t.Fatalf("key = %q, want the token the store holds now", got)
	}
}

func TestCodexClientRenewsAnExpiredTokenAndKeepsTheRefreshToken(t *testing.T) {
	useTempAuthStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "refresh-1" {
			http.Error(w, "bad refresh token", http.StatusBadRequest)
			return
		}
		// The answer carries no new refresh token.
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-new", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	cfg := oauth.DefaultOpenAIConfig()
	cfg.TokenURL = server.URL
	previous := oauthClient
	oauthClient = oauth.NewClient(cfg)
	t.Cleanup(func() { oauthClient = previous })

	saveCodexCredentials(t, "token-old", "refresh-1", time.Now().Add(-time.Minute))
	client := NewClientWithConfig("token-old", &Config{Provider: types.APIProviderCodex})

	if got := client.authKey(context.Background()); got != "token-new" {
		t.Fatalf("key = %q, want the renewed token", got)
	}
	if got := client.authKey(context.Background()); got != "token-new" || calls.Load() != 1 {
		t.Fatalf("the renewed token is reused without renewing again: key %q, %d renewals", got, calls.Load())
	}
	s, _ := store.NewFileStore(defaultAuthPath())
	creds, err := s.LoadCredentials(context.Background(), "codex")
	if err != nil || creds.Token.AccessToken != "token-new" || creds.Token.RefreshToken != "refresh-1" {
		t.Fatalf("the store must hold the new token and still the refresh token: %+v, %v", creds, err)
	}
}

// A server gives each user's own key: a client created with a key that is not the stored one never looks at the store.
func TestCodexClientWithAnotherKeyNeverReadsTheStore(t *testing.T) {
	useTempAuthStore(t)
	saveCodexCredentials(t, "token-a", "refresh-1", time.Now().Add(time.Hour))

	client := NewClientWithConfig("a-users-own-key", &Config{Provider: types.APIProviderCodex})
	if client.keySource != nil {
		t.Fatal("a client with its own key must not be managed by the auth store")
	}
	if got := client.authKey(context.Background()); got != "a-users-own-key" {
		t.Fatalf("key = %q", got)
	}
}

func TestOtherProvidersKeepTheirKey(t *testing.T) {
	useTempAuthStore(t)
	client := NewClientWithConfig("sk-test", &Config{Provider: types.APIProviderOpenAI})
	if client.keySource != nil || client.authKey(context.Background()) != "sk-test" {
		t.Fatal("only Codex renews its key from the auth store")
	}
}
