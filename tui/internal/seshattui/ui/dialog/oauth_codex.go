package dialog

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/oauth"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
)

// NewOAuthCodex opens the sign-in of Codex, which is a ChatGPT account and not an API key: the dialog shows the address and the
// code, and the browser is opened by itself.
func NewOAuthCodex(
	com *common.Common,
	isOnboarding bool,
	provider catwalk.Provider,
	model config.SelectedModel,
	modelType config.SelectedModelType,
) (*OAuth, tea.Cmd) {
	return newOAuth(com, isOnboarding, provider, model, modelType, &OAuthCodex{})
}

// OAuthCodex is the device flow of a ChatGPT account. The runtime keeps the token, with its refresh token, in its own auth store,
// so the next start can renew it; the dialog only needs the access token to mark the provider as configured.
type OAuthCodex struct {
	flow       *sdk.OAuthDeviceFlow
	challenge  *sdk.OAuthDeviceChallenge
	cancelFunc func()
}

var _ OAuthProvider = (*OAuthCodex)(nil)

func (m *OAuthCodex) name() string { return "Codex (ChatGPT)" }

func (m *OAuthCodex) initiateAuth() tea.Msg {
	flow, err := sdk.NewOAuthDeviceFlow(sdk.OAuthDeviceFlowConfig{Provider: string(sdk.APIProviderCodex), Persist: true})
	if err != nil {
		return ActionOAuthErrored{Error: fmt.Errorf("failed to prepare the sign-in: %w", err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	challenge, err := flow.Start(ctx)
	if err != nil {
		return ActionOAuthErrored{Error: fmt.Errorf("failed to initiate device auth: %w", err)}
	}
	m.flow, m.challenge = flow, challenge

	// Open the browser without waiting for a key press. The address that already carries the code is preferred.
	link := challenge.VerificationURLComplete
	if link == "" {
		link = challenge.VerificationURL
	}
	if err := openWebLink(link); err != nil {
		// Not fatal: the dialog shows the address and the code for the user to use by hand.
		_ = err
	}

	return ActionInitiateOAuth{
		DeviceCode:      challenge.DeviceCode,
		UserCode:        challenge.UserCode,
		VerificationURL: challenge.VerificationURL,
		ExpiresIn:       challenge.ExpiresIn,
		Interval:        challenge.Interval,
	}
}

func (m *OAuthCodex) startPolling(string, int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFunc = cancel

		token, err := m.flow.Wait(ctx, m.challenge)
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancelled, don't report an error.
			}
			return ActionOAuthErrored{Error: err}
		}
		out := &oauth.Token{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken}
		if !token.ExpiresAt.IsZero() {
			out.ExpiresAt = token.ExpiresAt.Unix()
			out.ExpiresIn = int(time.Until(token.ExpiresAt).Seconds())
		}
		return ActionCompleteOAuth{Token: out}
	}
}

func (m *OAuthCodex) stopPolling() tea.Msg {
	if m.cancelFunc != nil {
		m.cancelFunc()
	}
	return nil
}
