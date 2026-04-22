package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_minimal(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: work
    email: test@example.com
    tenant_id: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
  - name: client
    email: test@example.com
    tenant_id: 11111111-2222-3333-4444-555555555555
sync_pairs:
  - from: work
    to: client
  - from: client
    to: work
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 2 {
		t.Errorf("want 2 accounts, got %d", len(cfg.Accounts))
	}
	if len(cfg.SyncPairs) != 2 {
		t.Errorf("want 2 sync_pairs, got %d", len(cfg.SyncPairs))
	}
	if cfg.Account("work").ClientID != DefaultClientID {
		t.Errorf("work should use default client ID when not set")
	}
	// Defaults should come through as "nil" (omitted) — meaning Resolved
	// will fill them with the safe hard-coded values.
	r := cfg.SyncPairs[0].Resolved(cfg.Defaults)
	if r.LookaheadDays != 30 {
		t.Errorf("default lookahead_days should be 30, got %d", r.LookaheadDays)
	}
	if !r.SkipDeclined {
		t.Error("skip_declined should default to true")
	}
	if !r.SkipAllDay {
		t.Error("skip_all_day should default to true (vacations/OOO must not leak)")
	}
}

func TestResolved_explicitFalseOverridesSafeDefault(t *testing.T) {
	// Regression test: users must be able to opt back INTO syncing all-day
	// events or declined meetings via an explicit per-pair false. Before the
	// pointer-bool refactor this was silently ignored.
	f := false
	p := SyncPair{From: "a", To: "b", SkipAllDay: &f, SkipDeclined: &f}
	r := p.Resolved(Defaults{})
	if r.SkipAllDay {
		t.Error("explicit per-pair skip_all_day: false must beat the true default")
	}
	if r.SkipDeclined {
		t.Error("explicit per-pair skip_declined: false must beat the true default")
	}
}

func TestResolved_defaultsFalseFromTopLevelAlsoOverrides(t *testing.T) {
	// Top-level `defaults: { skip_all_day: false }` should also work.
	f := false
	r := SyncPair{From: "a", To: "b"}.Resolved(Defaults{SkipAllDay: &f, SkipDeclined: &f})
	if r.SkipAllDay {
		t.Error("top-level defaults.skip_all_day: false must override the safe default")
	}
	if r.SkipDeclined {
		t.Error("top-level defaults.skip_declined: false must override the safe default")
	}
}

func TestLoad_YAMLRoundTripRespectsExplicitFalse(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: a
    tenant_id: common
  - name: b
    tenant_id: common
sync_pairs:
  - from: a
    to: b
    skip_all_day: false
    skip_declined: false
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := cfg.SyncPairs[0].Resolved(cfg.Defaults)
	if r.SkipAllDay {
		t.Error("YAML-level skip_all_day: false must survive to the resolved pair")
	}
	if r.SkipDeclined {
		t.Error("YAML-level skip_declined: false must survive to the resolved pair")
	}
}

func TestLoad_overrideClientID_rejectsSelfSync(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: work
    email: test@example.com
    tenant_id: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
    client_id: 00000000-0000-0000-0000-000000000000
sync_pairs:
  - from: work
    to: work
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected validation error for self-sync")
	}
}

func TestValidate_errors(t *testing.T) {
	cases := map[string]struct {
		body    string
		wantSub string // substring expected in the error
	}{
		"duplicate account name": {
			body: `
accounts:
  - {name: a, tenant_id: common}
  - {name: a, tenant_id: common}
sync_pairs:
  - {from: a, to: a}`,
			wantSub: "duplicate",
		},
		"unknown account in pair": {
			body: `
accounts:
  - {name: a, tenant_id: common}
sync_pairs:
  - {from: a, to: b}`,
			wantSub: "unknown to account",
		},
		"missing tenant_id": {
			body: `
accounts:
  - {name: a}
sync_pairs:
  - {from: a, to: a}`,
			wantSub: "tenant_id is required",
		},
		"invalid tenant_id (path traversal)": {
			body: `
accounts:
  - {name: a, tenant_id: "evil.com/../attacker"}
sync_pairs:
  - {from: a, to: a}`,
			wantSub: "must be a UUID",
		},
		"invalid tenant_id (arbitrary string)": {
			body: `
accounts:
  - {name: a, tenant_id: "not-a-uuid"}
sync_pairs:
  - {from: a, to: a}`,
			wantSub: "must be a UUID",
		},
		"invalid client_id": {
			body: `
accounts:
  - {name: a, tenant_id: common, client_id: "not-a-uuid"}
sync_pairs:
  - {from: a, to: a}`,
			wantSub: "client_id",
		},
		"no accounts": {
			body: `
sync_pairs:
  - {from: a, to: b}`,
			wantSub: "no accounts",
		},
		"no sync_pairs": {
			body: `
accounts:
  - {name: a, tenant_id: common}`,
			wantSub: "no sync_pairs",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil {
				t.Fatalf("expected error for %s", name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidate_acceptsCommonAndUUID(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - {name: a, tenant_id: common}
  - {name: b, tenant_id: 11111111-2222-3333-4444-555555555555}
  - {name: c, tenant_id: organizations}
  - {name: d, tenant_id: consumers}
sync_pairs:
  - {from: a, to: b}
`)
	if _, err := Load(p); err != nil {
		t.Fatalf("all four accepted tenant forms should pass: %v", err)
	}
}

func TestSyncPair_Resolved(t *testing.T) {
	look := 7
	p := SyncPair{From: "a", To: "b", LookaheadDays: &look}
	d := Defaults{Title: "CustomBusy"}
	r := p.Resolved(d)
	if r.LookbackDays != 1 {
		t.Errorf("lookback should fall back to hard default: got %d", r.LookbackDays)
	}
	if r.LookaheadDays != 7 {
		t.Errorf("lookahead should use per-pair override: got %d", r.LookaheadDays)
	}
	if r.Title != "CustomBusy" {
		t.Errorf("title should use defaults override: got %q", r.Title)
	}
}

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := DefaultPath(); got != "/custom/xdg/outlook-busy-sync/config.yaml" {
		t.Errorf("XDG path not honoured: %s", got)
	}
}
