package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"
)

// msalStyleErr mimics the text MSAL returns when the token endpoint rejects
// a request: the HTTP status line, then the JSON body.
func msalStyleErr(code string) error {
	return fmt.Errorf("http call(https://login.microsoftonline.com/common/oauth2/v2.0/token)(POST) error: reply status code was 400:\n"+
		`{"error":"invalid_grant","error_description":"AADSTS%s: Example description.\r\nTrace ID: 00000000-0000-0000-0000-000000000000","error_codes":[%s]}`,
		code, code)
}

func TestAADSTSCodeOf(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"msal body", msalStyleErr("70043").Error(), "70043"},
		{"six digits", "AADSTS530032: blocked by security policy", "530032"},
		{"first of several", "AADSTS50076: see also AADSTS53003", "50076"},
		{"no code", "dial tcp: connection refused", ""},
		{"prefix without digits", "AADSTS: something", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aadstsCodeOf(tt.in); got != tt.want {
				t.Errorf("aadstsCodeOf(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWithHint_mappedCodes(t *testing.T) {
	tests := []struct {
		code string
		want []string
	}{
		{"53003", []string{"Conditional Access policy blocked this sign-in", "Contact your IT administrator"}},
		{"53000", []string{"compliant device", "Contact your IT administrator"}},
		{"53001", []string{"domain-joined device", "Contact your IT administrator"}},
		{"50076", []string{"multifactor authentication", "`outlook-busy-sync auth work`"}},
		{"50079", []string{"register security info", "`outlook-busy-sync auth work`"}},
		{"65001", []string{"Consent", "`outlook-busy-sync auth work`"}},
		{"90094", []string{"administrator must approve", "Contact your IT administrator"}},
		{"700016", []string{"not found in this tenant", "tenant_id"}},
		{"50020", []string{"configured tenant", "`outlook-busy-sync auth work`"}},
		{"50173", []string{"revoked", "`outlook-busy-sync auth work`"}},
		{"70043", []string{"sign-in frequency", "`outlook-busy-sync auth work`"}},
		{"70008", []string{"inactivity", "`outlook-busy-sync auth work`"}},
		{"700082", []string{"inactivity", "`outlook-busy-sync auth work`"}},
	}
	if len(tests) != len(aadstsHints) {
		t.Fatalf("table covers %d codes, aadstsHints has %d", len(tests), len(aadstsHints))
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			orig := msalStyleErr(tt.code)
			got := withHint(orig, "work").Error()
			prefix := orig.Error() + "\nhint: "
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("original text must come first, then the hint:\n%s", got)
			}
			hint := strings.TrimPrefix(got, prefix)
			if strings.Contains(hint, "\n") {
				t.Errorf("hint must be one line: %q", hint)
			}
			for _, w := range tt.want {
				if !strings.Contains(hint, w) {
					t.Errorf("hint %q missing %q", hint, w)
				}
			}
			if strings.Contains(hint, accountPlaceholder) || strings.Contains(hint, "%!") {
				t.Errorf("hint not fully rendered: %q", hint)
			}
		})
	}
}

func TestWithHint_unknownOrAbsentCodeUnchanged(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"code sharing a prefix with 53003", msalStyleErr("530032")},
		{"unmapped code", msalStyleErr("99999")},
		{"no code", errors.New("silent token: dial tcp: connection refused")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withHint(tt.err, "work"); got != tt.err {
				t.Errorf("error must be returned unchanged, got %q", got)
			}
		})
	}
}

func TestWithHint_nil(t *testing.T) {
	if err := withHint(nil, "work"); err != nil {
		t.Errorf("withHint(nil) = %v, want nil", err)
	}
}

func TestWithHint_keepsChain(t *testing.T) {
	sentinel := errors.New("sentinel")
	call := msalerrors.CallErr{Err: msalStyleErr("53003")}
	orig := fmt.Errorf("silent token: %w (%w)", call, sentinel)
	got := withHint(orig, "work")

	var he *hintedError
	if !errors.As(got, &he) {
		t.Fatalf("expected a hinted error, got %T", got)
	}
	if errors.Unwrap(got) != orig {
		t.Error("Unwrap must return the original error")
	}
	if !errors.Is(got, sentinel) {
		t.Error("errors.Is must still find errors wrapped by the original")
	}
	var ce msalerrors.CallErr
	if !errors.As(got, &ce) {
		t.Error("errors.As must still find the MSAL CallErr")
	}
	if !strings.HasPrefix(got.Error(), orig.Error()) {
		t.Errorf("original text must be kept: %q", got.Error())
	}
}
