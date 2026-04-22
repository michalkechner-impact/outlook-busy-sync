// Package config loads and validates the user's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultClientID is the Azure CLI public client ID. It is pre-consented for
// Microsoft Graph in virtually every M365 tenant, which means users do not
// need to register their own app to use this tool. Individual accounts can
// override it via Account.ClientID if their IT has restricted first-party
// app consents or if they prefer their own registration.
const DefaultClientID = "04b07795-8ddb-461a-bbee-02f9e1bf7b46"

// tenantIDRegexp matches a UUID (case-insensitive). Tenant and client IDs
// must either be a UUID or one of a small set of literal authority names
// (TenantID only). Rejecting anything else closes off a config-injection
// class where a malicious config could alter the MSAL authority URL.
var tenantIDRegexp = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// allowedTenantLiterals are the non-UUID authority names MSAL accepts.
var allowedTenantLiterals = map[string]bool{
	"common":         true,
	"organizations":  true,
	"consumers":      true,
}

// Config is the top-level configuration.
type Config struct {
	Accounts  []Account  `yaml:"accounts"`
	SyncPairs []SyncPair `yaml:"sync_pairs"`
	Defaults  Defaults   `yaml:"defaults"`
}

// Account represents a single M365 mailbox we can authenticate against.
type Account struct {
	// Name is the local identifier used in sync_pairs and `auth` commands.
	Name string `yaml:"name"`
	// Email is the primary UPN for the account. Used for display only; Graph
	// API operates on /me after auth.
	Email string `yaml:"email"`
	// TenantID is the directory (tenant) UUID, or "common" / "organizations"
	// for multi-tenant auth. Prefer the explicit tenant UUID when known.
	TenantID string `yaml:"tenant_id"`
	// ClientID overrides DefaultClientID for this account. Leave empty to use
	// the pre-approved Azure CLI client ID.
	ClientID string `yaml:"client_id,omitempty"`
}

// SyncPair is a one-way copy of busy blocks from one account to another.
// Bidirectional sync is expressed as two SyncPairs.
type SyncPair struct {
	From          string `yaml:"from"`
	To            string `yaml:"to"`
	LookbackDays  *int   `yaml:"lookback_days,omitempty"`
	LookaheadDays *int   `yaml:"lookahead_days,omitempty"`
	Title         string `yaml:"title,omitempty"`
	SkipAllDay    *bool  `yaml:"skip_all_day,omitempty"`
	SkipDeclined  *bool  `yaml:"skip_declined,omitempty"`
}

// Defaults provides fallback values for SyncPair fields. Pointer bools let
// the user distinguish "unset" from "explicit false" — required because we
// want the hard-coded defaults to be safe (true) while still letting users
// override them at the top level.
type Defaults struct {
	LookbackDays  *int   `yaml:"lookback_days,omitempty"`
	LookaheadDays *int   `yaml:"lookahead_days,omitempty"`
	Title         string `yaml:"title,omitempty"`
	SkipAllDay    *bool  `yaml:"skip_all_day,omitempty"`
	SkipDeclined  *bool  `yaml:"skip_declined,omitempty"`
}

// Resolved returns a SyncPair with all optional fields filled from Defaults.
func (p SyncPair) Resolved(d Defaults) ResolvedPair {
	r := ResolvedPair{
		From:          p.From,
		To:            p.To,
		LookbackDays:  deref(d.LookbackDays, 1),
		LookaheadDays: deref(d.LookaheadDays, 30),
		Title:         firstNonEmpty(d.Title, "Busy"),
		SkipAllDay:    derefBool(d.SkipAllDay, true),
		SkipDeclined:  derefBool(d.SkipDeclined, true),
	}
	if p.LookbackDays != nil {
		r.LookbackDays = *p.LookbackDays
	}
	if p.LookaheadDays != nil {
		r.LookaheadDays = *p.LookaheadDays
	}
	if p.Title != "" {
		r.Title = p.Title
	}
	if p.SkipAllDay != nil {
		r.SkipAllDay = *p.SkipAllDay
	}
	if p.SkipDeclined != nil {
		r.SkipDeclined = *p.SkipDeclined
	}
	return r
}

// ResolvedPair is a SyncPair with defaults applied.
type ResolvedPair struct {
	From          string
	To            string
	LookbackDays  int
	LookaheadDays int
	Title         string
	SkipAllDay    bool
	SkipDeclined  bool
	// DryRun is set from a CLI flag, not YAML. When true the engine must
	// log what it would do but make no Create/Update/Delete calls.
	DryRun bool
}

// DefaultPath returns the platform-appropriate default config path.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "outlook-busy-sync", "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "outlook-busy-sync", "config.yaml")
}

// Load parses and validates the config file at path. If path is empty,
// DefaultPath() is used.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks that the config is internally consistent.
func (c *Config) Validate() error {
	if len(c.Accounts) == 0 {
		return errors.New("no accounts defined")
	}
	if len(c.SyncPairs) == 0 {
		return errors.New("no sync_pairs defined")
	}
	names := map[string]bool{}
	for i, a := range c.Accounts {
		if a.Name == "" {
			return fmt.Errorf("accounts[%d]: name is required", i)
		}
		if names[a.Name] {
			return fmt.Errorf("accounts[%d]: duplicate name %q", i, a.Name)
		}
		names[a.Name] = true
		if a.TenantID == "" {
			return fmt.Errorf("accounts[%d] (%s): tenant_id is required (use the directory UUID, or %q)", i, a.Name, "common")
		}
		if !validTenantID(a.TenantID) {
			return fmt.Errorf("accounts[%d] (%s): tenant_id %q must be a UUID or one of: common, organizations, consumers", i, a.Name, a.TenantID)
		}
		if a.ClientID != "" && !tenantIDRegexp.MatchString(a.ClientID) {
			return fmt.Errorf("accounts[%d] (%s): client_id %q must be a UUID", i, a.Name, a.ClientID)
		}
	}
	for i, p := range c.SyncPairs {
		if !names[p.From] {
			return fmt.Errorf("sync_pairs[%d]: unknown from account %q (known: %s)", i, p.From, strings.Join(sortedKeys(names), ", "))
		}
		if !names[p.To] {
			return fmt.Errorf("sync_pairs[%d]: unknown to account %q", i, p.To)
		}
		if p.From == p.To {
			return fmt.Errorf("sync_pairs[%d]: from and to must differ", i)
		}
	}
	return nil
}

// Account returns a copy of the named account with ClientID defaulted, or
// nil if no account matches.
func (c *Config) Account(name string) *Account {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			a := c.Accounts[i]
			if a.ClientID == "" {
				a.ClientID = DefaultClientID
			}
			return &a
		}
	}
	return nil
}

func validTenantID(s string) bool {
	if tenantIDRegexp.MatchString(s) {
		return true
	}
	return allowedTenantLiterals[s]
}

func deref(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

func derefBool(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sortedKeys returns map keys in deterministic order. Used only for error
// messages where nondeterminism would make output painful to test.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small slices, stable deterministic sort without importing sort for one
	// callsite — insertion sort in place.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
