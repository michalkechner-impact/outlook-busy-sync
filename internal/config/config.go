// Package config loads and validates the user's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultClientID is the Azure CLI public client ID. It is pre-consented for
// Microsoft Graph in virtually every M365 tenant, which means users do not
// need to register their own app to use this tool. Individual accounts can
// override it via Account.ClientID if their IT has restricted first-party
// app consents or if they prefer their own registration.
const DefaultClientID = "04b07795-8ddb-461a-bbee-02f9e1bf7b46"

// Config is the top-level configuration.
type Config struct {
	Accounts   []Account   `yaml:"accounts"`
	SyncPairs  []SyncPair  `yaml:"sync_pairs"`
	Defaults   Defaults    `yaml:"defaults"`
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

// Defaults provides fallback values for SyncPair fields.
type Defaults struct {
	LookbackDays  int    `yaml:"lookback_days"`
	LookaheadDays int    `yaml:"lookahead_days"`
	Title         string `yaml:"title"`
	SkipAllDay    bool   `yaml:"skip_all_day"`
	SkipDeclined  bool   `yaml:"skip_declined"`
}

// Resolved returns a SyncPair with all optional fields filled from Defaults.
func (p SyncPair) Resolved(d Defaults) ResolvedPair {
	r := ResolvedPair{
		From:          p.From,
		To:            p.To,
		LookbackDays:  d.LookbackDays,
		LookaheadDays: d.LookaheadDays,
		Title:         d.Title,
		SkipAllDay:    d.SkipAllDay,
		SkipDeclined:  d.SkipDeclined,
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
}

// DefaultPath returns the XDG-style default config path.
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
	cfg.applyHardDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyHardDefaults() {
	if c.Defaults.LookbackDays == 0 {
		c.Defaults.LookbackDays = 1
	}
	if c.Defaults.LookaheadDays == 0 {
		c.Defaults.LookaheadDays = 30
	}
	if c.Defaults.Title == "" {
		c.Defaults.Title = "Busy"
	}
	// SkipDeclined and SkipAllDay default to true: declined events are not
	// "busy" for the user, and all-day events (vacations, OOO, focus days)
	// would otherwise leak an entire day of "Busy" to the other tenant, which
	// is almost never what people want on first run. Users can flip either
	// via the per-pair override.
	//
	// yaml Unmarshal leaves bools at false on omission, so we can't
	// distinguish "explicit false" from "unset" at the top-level Defaults.
	// We accept that ambiguity: to *include* declined or all-day events,
	// users set the override inline on a sync_pair (where it's a *bool).
	c.Defaults.SkipDeclined = true
	c.Defaults.SkipAllDay = true
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
			return fmt.Errorf("accounts[%d] (%s): tenant_id is required (use the directory UUID, or \"common\")", i, a.Name)
		}
	}
	for i, p := range c.SyncPairs {
		if !names[p.From] {
			return fmt.Errorf("sync_pairs[%d]: unknown from account %q (known: %s)", i, p.From, strings.Join(keys(names), ", "))
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

// Account returns the named account or nil.
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

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
