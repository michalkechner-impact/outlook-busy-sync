package auth

import (
	"regexp"
	"strings"
)

// aadstsCode matches the Microsoft Entra error code that MSAL carries in the
// token endpoint's error_description, for example "AADSTS50173".
var aadstsCode = regexp.MustCompile(`AADSTS(\d+)`)

// accountPlaceholder in a hint is replaced with the account name, so the
// suggested command can be copied as is.
const accountPlaceholder = "<account>"

// aadstsHints maps common Microsoft Entra error codes to a one-line hint.
// Meanings follow Microsoft's error code reference:
// https://learn.microsoft.com/en-us/entra/identity-platform/reference-error-codes
// Microsoft may add or change codes, so a code missing here leaves the error
// as it is.
var aadstsHints = map[string]string{
	// BlockedByConditionalAccess
	"53003": "Your organization's Conditional Access policy blocked this sign-in. Contact your IT administrator.",
	// DeviceNotCompliant
	"53000": "Your organization's Conditional Access policy requires a compliant device, and this device is not marked compliant. Contact your IT administrator.",
	// DeviceNotDomainJoined
	"53001": "Your organization's Conditional Access policy requires a domain-joined device, and this device is not domain joined. Contact your IT administrator.",
	// UserStrongAuthClientAuthNRequired
	"50076": "Your organization now requires multifactor authentication for this sign-in. Run `outlook-busy-sync auth <account>` again and complete it in the browser.",
	// UserStrongAuthEnrollmentRequired
	"50079": "Your organization requires multifactor authentication and this user must register security info first. Complete that registration, then run `outlook-busy-sync auth <account>` again.",
	// DelegationDoesNotExist
	"65001": "Consent for this application has not been granted. Run `outlook-busy-sync auth <account>` again and accept the consent prompt, or ask your IT administrator if admin consent is required.",
	// AdminConsentRequired
	"90094": "An administrator must approve this application for your organization. Contact your IT administrator.",
	// UnauthorizedClient_DoesNotMatchRequest
	"700016": "The application was not found in this tenant. Check tenant_id (and client_id, if you set one) for this account in your config, or contact your IT administrator.",
	// UserUnauthorized
	"50020": "The user you signed in as does not exist in the configured tenant. Run `outlook-busy-sync auth <account>` again and sign in with the matching work account.",
	// The provided grant has expired due to it being revoked
	"50173": "The saved sign-in was revoked, for example after a password change. Run `outlook-busy-sync auth <account>` again.",
	// BadTokenDueToSignInFrequency
	"70043": "Your organization's sign-in frequency policy has expired the saved sign-in. Run `outlook-busy-sync auth <account>` again.",
	// ExpiredOrRevokedGrant
	"70008": "The saved sign-in expired after a period of inactivity. Run `outlook-busy-sync auth <account>` again.",
	// ExpiredOrRevokedGrantInactiveToken
	"700082": "The saved sign-in expired after a period of inactivity. Run `outlook-busy-sync auth <account>` again.",
}

// hintedError adds a one-line hint to a token acquisition error. The
// original error stays in the chain and its text is printed first.
type hintedError struct {
	err  error
	hint string
}

func (e *hintedError) Error() string { return e.err.Error() + "\nhint: " + e.hint }

func (e *hintedError) Unwrap() error { return e.err }

// aadstsCodeOf returns the number of the first AADSTS code in s, or "" if
// there is none.
func aadstsCodeOf(s string) string {
	m := aadstsCode.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// withHint wraps err with a hint when its text carries a known AADSTS code.
// Any other error, including nil, is returned unchanged.
func withHint(err error, account string) error {
	if err == nil {
		return nil
	}
	hint, ok := aadstsHints[aadstsCodeOf(err.Error())]
	if !ok {
		return err
	}
	return &hintedError{err: err, hint: strings.ReplaceAll(hint, accountPlaceholder, account)}
}
