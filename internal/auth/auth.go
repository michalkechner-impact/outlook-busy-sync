// Package auth handles OAuth2 device code login against Microsoft Graph and
// caches refresh tokens in the macOS Keychain (or a local file on systems
// without a keyring). A single Authenticator is bound to one account.
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
)

// Scopes is the minimum set of permissions we need: read/write the signed-in
// user's calendars, plus the offline_access implicit in MSAL's refresh-token
// handling.
var Scopes = []string{"https://graph.microsoft.com/Calendars.ReadWrite"}

// DeviceCodePresenter is called by Login with a user-visible instruction. The
// CLI implementation prints the verification URL and user code; tests can stub
// it to capture values.
type DeviceCodePresenter func(userCode, verificationURL, message string)

// Authenticator owns MSAL state for a single account.
type Authenticator struct {
	account  config.Account
	client   public.Client
	store    tokenStore
}

// New creates an Authenticator using the account's tenant and client ID, with
// a persistent token cache bound to the account name.
func New(account config.Account) (*Authenticator, error) {
	if account.ClientID == "" {
		account.ClientID = config.DefaultClientID
	}
	store, err := newTokenStore(account.Name)
	if err != nil {
		return nil, fmt.Errorf("token store: %w", err)
	}
	authority := "https://login.microsoftonline.com/" + account.TenantID
	client, err := public.New(account.ClientID,
		public.WithAuthority(authority),
		public.WithCache(store),
	)
	if err != nil {
		return nil, fmt.Errorf("msal client: %w", err)
	}
	return &Authenticator{account: account, client: client, store: store}, nil
}

// Login starts the device code flow, blocking until the user completes it in
// a browser. The refresh token is persisted on success.
func (a *Authenticator) Login(ctx context.Context, present DeviceCodePresenter) error {
	dc, err := a.client.AcquireTokenByDeviceCode(ctx, Scopes)
	if err != nil {
		return fmt.Errorf("device code: %w", err)
	}
	if present != nil {
		r := dc.Result
		present(r.UserCode, r.VerificationURL, r.Message)
	}
	_, err = dc.AuthenticationResult(ctx)
	if err != nil {
		return fmt.Errorf("device code auth: %w", err)
	}
	return nil
}

// Token returns a valid access token, refreshing silently when possible.
// Returns ErrLoginRequired if no cached account is available.
func (a *Authenticator) Token(ctx context.Context) (string, error) {
	accounts, err := a.client.Accounts(ctx)
	if err != nil {
		return "", fmt.Errorf("list accounts: %w", err)
	}
	if len(accounts) == 0 {
		return "", ErrLoginRequired
	}
	result, err := a.client.AcquireTokenSilent(ctx, Scopes, public.WithSilentAccount(accounts[0]))
	if err != nil {
		return "", fmt.Errorf("silent token: %w", err)
	}
	return result.AccessToken, nil
}

// Logout clears cached tokens for the account.
func (a *Authenticator) Logout() error {
	return a.store.clear()
}

// ErrLoginRequired indicates there is no cached account and the user must
// run `outlook-busy-sync auth <account>` first.
var ErrLoginRequired = errors.New("no cached credentials: run `outlook-busy-sync auth` first")

// cache.ExportReplace is MSAL's storage interface. tokenStore adapts it to
// our keychain/file backends.
var _ cache.ExportReplace = (*tokenStore)(nil)
