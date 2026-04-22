package config

import (
	"os"
	"path/filepath"
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
	if cfg.Defaults.LookaheadDays != 30 {
		t.Errorf("default lookahead_days should be 30, got %d", cfg.Defaults.LookaheadDays)
	}
	if cfg.Account("work").ClientID != DefaultClientID {
		t.Errorf("work should use default client ID when not set")
	}
	if !cfg.Defaults.SkipDeclined {
		t.Error("skip_declined should default to true")
	}
	if !cfg.Defaults.SkipAllDay {
		t.Error("skip_all_day should default to true (vacations/OOO must not leak)")
	}
}

func TestLoad_overrideClientID(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: work
    email: test@example.com
    tenant_id: aaaa
    client_id: custom-id
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
	cases := map[string]string{
		"duplicate account name": `
accounts:
  - {name: a, tenant_id: t}
  - {name: a, tenant_id: t}
sync_pairs:
  - {from: a, to: a}`,
		"unknown account in pair": `
accounts:
  - {name: a, tenant_id: t}
sync_pairs:
  - {from: a, to: b}`,
		"missing tenant_id": `
accounts:
  - {name: a}
sync_pairs:
  - {from: a, to: a}`,
		"no accounts": `
sync_pairs:
  - {from: a, to: b}`,
		"no sync_pairs": `
accounts:
  - {name: a, tenant_id: t}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTemp(t, body))
			if err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestSyncPair_Resolved(t *testing.T) {
	look := 7
	p := SyncPair{From: "a", To: "b", LookaheadDays: &look}
	d := Defaults{LookbackDays: 2, LookaheadDays: 30, Title: "Busy"}
	r := p.Resolved(d)
	if r.LookbackDays != 2 {
		t.Errorf("lookback should fall back to default: got %d", r.LookbackDays)
	}
	if r.LookaheadDays != 7 {
		t.Errorf("lookahead should override default: got %d", r.LookaheadDays)
	}
	if r.Title != "Busy" {
		t.Errorf("title should fall back to default: got %q", r.Title)
	}
}
