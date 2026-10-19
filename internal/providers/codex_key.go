package providers

import (
	"context"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/auth/store"
	authTypes "github.com/KPO-Tech/seshat-runtime/internal/auth/types"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// codexRefreshMu serialises the renewal of the ChatGPT token across every client of the process: the refresh token may be rotated by
// the server, so two clients renewing at once could leave one of them holding a token that is no longer valid.
var codexRefreshMu sync.Mutex

// keySourceFor returns the function a client calls to get the key of each request, or nil when the key it was created with is final.
func keySourceFor(provider types.APIProvider, apiKey string) func(context.Context) string {
	if provider == types.APIProviderCodex {
		return codexKeySource(apiKey)
	}
	return nil
}

// codexKeySource keeps a session on Codex signed in. The ChatGPT access token lasts a few hours; the runtime's auth store holds the
// refresh token that renews it. A client that was created with the token that the store holds (or with none) asks the store before each
// request, which renews the token when it is about to expire and writes the new one back. A client created with any other key, such as
// a user's own key given by a server, never looks at the store.
func codexKeySource(apiKey string) func(context.Context) string {
	managed := false
	if s, err := store.NewFileStore(defaultAuthPath()); err == nil {
		if creds, err := s.LoadCredentials(context.Background(), string(types.APIProviderCodex)); err == nil && creds.AuthMethod == authTypes.AuthMethodOAuth {
			managed = apiKey == "" || apiKey == creds.APIKey || (creds.Token != nil && apiKey == creds.Token.AccessToken)
		}
	}
	if !managed {
		return nil
	}
	return func(ctx context.Context) string {
		codexRefreshMu.Lock()
		defer codexRefreshMu.Unlock()
		s, err := store.NewFileStore(defaultAuthPath())
		if err != nil {
			return ""
		}
		creds, err := s.LoadCredentials(ctx, string(types.APIProviderCodex))
		if err != nil || creds.AuthMethod != authTypes.AuthMethodOAuth {
			return ""
		}
		token, err := resolveOAuthToken(ctx, s, creds)
		if err != nil {
			return "" // the key the client has is used, and the request fails with the provider's own answer
		}
		return token
	}
}

// authKey is the key to send with a request: the one renewed from the auth store when the client has a source for it, else its own.
func (c *Client) authKey(ctx context.Context) string {
	if c.keySource != nil {
		if key := c.keySource(ctx); key != "" {
			return key
		}
	}
	return c.apiKey
}
