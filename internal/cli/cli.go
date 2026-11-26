// Package cli wires the cobra command tree. Keeping it in its own package
// makes main.go trivially small and lets us unit-test command construction.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/graph"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/state"
	syncpkg "github.com/michalkechner-impact/outlook-busy-sync/internal/sync"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/version"
)

// Exit codes. Keep stable: launchd / shell scripts key off these.
const (
	ExitOK     = 0
	ExitError  = 1
	ExitAuth   = 2
	ExitConfig = 3
	// ExitSkipped means no pair failed, but at least one was skipped because
	// an account's precondition (for example a VPN check) was not met.
	ExitSkipped = 4
)

// ExitCode extracts the intended exit code from an error returned by one of
// our commands. Unknown errors default to ExitError.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	if errors.Is(err, auth.ErrLoginRequired) {
		return ExitAuth
	}
	return ExitError
}

type codedError struct {
	code int
	err  error
}

func (c *codedError) Error() string { return c.err.Error() }
func (c *codedError) Unwrap() error { return c.err }

func coded(code int, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

type globalOpts struct {
	configPath string
	verbose    bool
}

// NewRoot returns the `outlook-busy-sync` root command.
func NewRoot() *cobra.Command {
	var g globalOpts
	cmd := &cobra.Command{
		Use:           "outlook-busy-sync",
		Short:         "Mirror busy blocks between two Microsoft 365 calendars",
		Long:          rootLong,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version.Version,
	}
	cmd.PersistentFlags().StringVar(&g.configPath, "config", "", "path to config.yaml (default: $XDG_CONFIG_HOME/outlook-busy-sync/config.yaml)")
	cmd.PersistentFlags().BoolVarP(&g.verbose, "verbose", "v", false, "verbose logging")
	cmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		level := slog.LevelInfo
		if g.verbose {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	}

	cmd.AddCommand(newAuthCmd(&g))
	cmd.AddCommand(newLogoutCmd(&g))
	cmd.AddCommand(newSyncCmd(&g))
	cmd.AddCommand(newConfigCmd(&g))
	cmd.AddCommand(newStatusCmd(&g))
	cmd.AddCommand(newEventsCmd(&g))
	cmd.AddCommand(newInitCmd(&g))
	cmd.AddCommand(newScheduleCmd(&g))
	return cmd
}

// newLogoutCmd clears cached tokens for a single account locally. The
// Graph-side refresh token remains valid until the tenant's configured
// sliding lifetime expires; to force hard revocation the user (or an
// admin) must invalidate the session via Entra ID. This is called out
// in `--help` so users aren't surprised.
func newLogoutCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "logout <account>",
		Short: "Clear cached credentials for one account",
		Long: `Clear the cached refresh token for an account from the platform
keyring and the 0600 file fallback.

The refresh token itself remains valid server-side until its configured
lifetime expires (default 90 days sliding). For immediate revocation
after, e.g., a lost laptop, ask your tenant admin to revoke active
sessions for the user via Entra ID.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			acc := cfg.Account(args[0])
			if acc == nil {
				return coded(ExitConfig, fmt.Errorf("account %q not defined in config", args[0]))
			}
			a, err := auth.New(*acc)
			if err != nil {
				return coded(ExitError, err)
			}
			if err := a.Logout(); err != nil {
				return coded(ExitError, fmt.Errorf("clear cached credentials: %w", err))
			}
			fmt.Fprintf(os.Stderr, "cleared local credentials for %s\n", acc.Name)
			return nil
		},
	}
}

// newInitCmd scaffolds a config file at the user's default path so a
// first-time install is three commands (brew install, init, auth) instead
// of "go figure out where the config goes, go find the example, edit
// YAML blindly". Idempotent: if a config already exists it prints its
// path and exits 0 without touching the file.
func newInitCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold a starter config at the default path",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := g.configPath
			if path == "" {
				path = config.DefaultPath()
			}
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(os.Stderr, "config already exists: %s (leaving it alone)\n", path)
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return coded(ExitError, err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return coded(ExitError, fmt.Errorf("create config dir: %w", err))
			}
			if err := os.WriteFile(path, []byte(starterConfig), 0o600); err != nil {
				return coded(ExitError, fmt.Errorf("write config: %w", err))
			}
			fmt.Fprintf(os.Stderr, "wrote starter config to %s\n", path)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "next steps:")
			fmt.Fprintf(os.Stderr, "  1. edit %s - replace the 'work' / 'client' account names\n", path)
			fmt.Fprintln(os.Stderr, "     and emails. tenant_id can stay as 'common' unless you")
			fmt.Fprintln(os.Stderr, "     know your tenant UUID; MSAL will resolve it at login.")
			fmt.Fprintln(os.Stderr, "  2. outlook-busy-sync auth work")
			fmt.Fprintln(os.Stderr, "  3. outlook-busy-sync auth client")
			fmt.Fprintln(os.Stderr, "  4. outlook-busy-sync sync --dry-run   (preview)")
			fmt.Fprintln(os.Stderr, "  5. outlook-busy-sync sync              (go live)")
			return nil
		},
	}
}

// starterConfig is the minimal bootstrap YAML init writes. tenant_id is
// set to "common" so users don't have to find UUIDs before their first
// login - MSAL resolves the real tenant during the device-code flow.
const starterConfig = `# outlook-busy-sync starter config.
#
# Written by ` + "`outlook-busy-sync init`" + `. Edit the two accounts
# below, then run ` + "`outlook-busy-sync auth work`" + ` and
# ` + "`outlook-busy-sync auth client`" + `.
#
# tenant_id: "common" asks MSAL to resolve your actual tenant at login
# time, which works in the vast majority of M365 setups without you
# looking up UUIDs. You can replace it with your directory UUID later
# if you want to pin authentication to a specific tenant.

accounts:
  - name: work
    email: you@company-a.example
    tenant_id: common

  - name: client
    email: you@company-b.example
    tenant_id: common

# Bidirectional sync: two one-way pairs with swapped endpoints. Both
# pairs use the default privacy-preserving "busy" mode. If you own both
# tenants and want full meeting detail copied in one direction, add
# "mode: mirror" to that pair (see README, "Mirror mode" section).
sync_pairs:
  - from: work
    to: client
  - from: client
    to: work

# Defaults apply to every pair unless overridden inline. Safe defaults
# exclude all-day events (vacations, OOO) and declined invites so your
# first run does not leak private patterns to the other tenant.
defaults:
  lookback_days: 1
  lookahead_days: 30
  title: Busy
  skip_all_day: true
  skip_declined: true
`

// newEventsCmd is a diagnostic that lists upcoming events for one account.
// Useful for confirming that auth and Graph access actually work without
// running a full sync (which requires both sides to be authenticated).
func newEventsCmd(g *globalOpts) *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "events <account>",
		Short: "List upcoming events for one account (diagnostic)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			acc := cfg.Account(args[0])
			if acc == nil {
				return coded(ExitConfig, fmt.Errorf("account %q not in config", args[0]))
			}
			a, err := auth.New(*acc)
			if err != nil {
				return coded(ExitError, err)
			}
			gc := graph.New(a)
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			start := time.Now().Add(-1 * time.Hour)
			end := time.Now().Add(time.Duration(days) * 24 * time.Hour)
			events, err := gc.ListEvents(ctx, start, end)
			if err != nil {
				return coded(ExitError, err)
			}
			fmt.Printf("%d events in window %s -> %s\n", len(events), start.Format("2006-01-02"), end.Format("2006-01-02"))
			for _, ev := range events {
				marker := ""
				if ev.SourceRef != "" {
					marker = " [synced from " + ev.SourceRef + "]"
				}
				fmt.Printf("  %s -> %s  %-8s %s%s\n",
					ev.Start.Local().Format("Mon 02 Jan 15:04"),
					ev.End.Local().Format("15:04"),
					ev.ShowAs,
					ev.Subject,
					marker,
				)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "look ahead N days")
	return cmd
}

const rootLong = `outlook-busy-sync keeps two (or more) Microsoft 365 calendars aware of
each other by copying free/busy information from one account to another.

By default ("mode: busy") it writes generic "Busy" blocks - no titles,
no locations, no bodies, no attendees cross the tenant boundary. An
opt-in "mode: mirror" copies subject / location / organiser / attendees /
body into the target event marked sensitivity:private; this is intended
only for users who own both tenants. The mode is per-pair, so a
bidirectional setup can be asymmetric (e.g., client tenant sees only
"Busy", employer tenant sees full detail).

Typical use case: a consultant whose employer and client both use M365.
Running this tool makes both sides see you as busy during the other's
meetings without either organisation needing to federate calendars or
install third-party software in the tenant.`

func loadCfg(g *globalOpts) (*config.Config, error) {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return nil, coded(ExitConfig, err)
	}
	return cfg, nil
}

// newSyncCmd runs all configured sync pairs once.
func newSyncCmd(g *globalOpts) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Run all configured sync pairs once",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			return runSync(ctx, cfg, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"show what would be created/updated/deleted without making any Graph writes")
	return cmd
}

// runSync runs every pair once and, unless dryRun, records the outcome in
// the state file read by status bars and `status`.
func runSync(ctx context.Context, cfg *config.Config, dryRun bool) error {
	started := time.Now()
	results, err := syncPairs(ctx, cfg, dryRun)
	recordSyncState(state.DefaultPath(), dryRun, started, cfg.SyncPairs, results, err)
	return err
}

func syncPairs(ctx context.Context, cfg *config.Config, dryRun bool) ([]state.PairResult, error) {
	return syncPairsWith(ctx, cfg, dryRun, newTokenSource)
}

// syncPairsWith is syncPairs with the token sources injected, so tests can
// check which accounts are asked for a token.
func syncPairsWith(ctx context.Context, cfg *config.Config, dryRun bool, newSource func(config.Account) (tokenSource, error)) ([]state.PairResult, error) {
	// Accounts whose precondition fails are dropped before anything else:
	// no token store and no token request, so a tenant that only allows
	// sign-in from its VPN sees no failed sign-ins while the VPN is down.
	used := map[string]bool{}
	for _, p := range cfg.SyncPairs {
		used[p.From], used[p.To] = true, true
	}
	unmet := checkPreconditions(ctx, cfg, used)

	// Build clients up front so we can report all missing auths in one go,
	// rather than failing on the first pair the user happens to hit.
	sources := map[string]tokenSource{}
	for _, acc := range cfg.Accounts {
		if unmet[acc.Name] != nil {
			continue
		}
		src, err := newSource(*cfg.Account(acc.Name))
		if err != nil {
			return nil, coded(ExitConfig, fmt.Errorf("auth setup for %s: %w", acc.Name, err))
		}
		sources[acc.Name] = src
	}

	// Determine which accounts are needed by a pair that will run.
	usedBy := map[string][]string{} // account -> list of "from→to" pairs that need it
	for _, p := range cfg.SyncPairs {
		if skipReason(p, unmet) != nil {
			continue
		}
		usedBy[p.From] = append(usedBy[p.From], fmt.Sprintf("%s→%s", p.From, p.To))
		usedBy[p.To] = append(usedBy[p.To], fmt.Sprintf("%s→%s", p.From, p.To))
	}

	// Probe auth for each needed account. `ErrLoginRequired` groups into a
	// single "please re-auth" message; any other error (transport, MSAL
	// invalid_grant, corrupted cache) is surfaced per-account so the user
	// actually sees the root cause instead of a downstream Graph 401.
	var missing []string
	var probeErrors []error
	blocked := map[string]bool{}
	for _, acc := range cfg.Accounts {
		accName := acc.Name
		if len(usedBy[accName]) == 0 {
			continue
		}
		if _, err := sources[accName].Token(ctx); err != nil {
			if errors.Is(err, auth.ErrLoginRequired) {
				missing = append(missing, accName)
			} else {
				probeErrors = append(probeErrors, fmt.Errorf("%s: %w", accName, err))
				blocked[accName] = auth.IsPolicyBlock(err)
			}
		}
	}
	if len(missing) > 0 {
		var lines []string
		for _, m := range missing {
			lines = append(lines, fmt.Sprintf("  %s   used by: %s   fix: outlook-busy-sync auth %s",
				m, strings.Join(usedBy[m], ", "), m))
		}
		err := coded(ExitAuth, fmt.Errorf("missing credentials for %d account(s):\n%s",
			len(missing), strings.Join(lines, "\n")))
		if len(unmet) > 0 {
			// Keep the skip reason on pairs that would not have run anyway.
			return probeFailureResults(cfg.SyncPairs, err, unmet, nil), err
		}
		return nil, err
	}
	if len(probeErrors) > 0 {
		err := coded(ExitAuth, fmt.Errorf("auth probe failed: %w", errors.Join(probeErrors...)))
		return probeFailureResults(cfg.SyncPairs, err, unmet, blocked), err
	}

	for _, m := range cfg.MirrorPairs() {
		if unmet[m.From] != nil || unmet[m.To] != nil {
			continue // skipped below, nothing is copied
		}
		fmt.Fprintf(os.Stderr,
			"WARNING: pair %s -> %s is in mirror mode. Subject, location, organizer/attendees-as-text, and body will be copied from %q into %q (marked private). Confirm %q is a tenant you control.\n",
			m.From, m.To, m.From, m.To, m.To)
	}

	clients := syncpkg.Clients{}
	for name, src := range sources {
		clients[name] = graph.New(src)
	}
	engine := syncpkg.New(clients, slog.Default())
	var pairErrors []error
	skipped := 0
	results := make([]state.PairResult, 0, len(cfg.SyncPairs))
	for _, p := range cfg.SyncPairs {
		if reason := skipReason(p, unmet); reason != nil {
			slog.Info("sync pair skipped",
				slog.String("from", p.From),
				slog.String("to", p.To),
				slog.String("reason", reason.Error()))
			results = append(results, skippedResult(p, reason))
			skipped++
			continue
		}
		resolved := p.Resolved(cfg.Defaults)
		resolved.DryRun = dryRun
		stats, err := engine.RunPair(ctx, resolved)
		result := state.PairResult{
			From:    resolved.From,
			To:      resolved.To,
			Created: stats.Created,
			Updated: stats.Updated,
			Deleted: stats.Deleted,
			Failed:  stats.Errors,
		}
		if err != nil {
			slog.Error("sync pair failed",
				slog.String("from", resolved.From),
				slog.String("to", resolved.To),
				slog.String("err", err.Error()))
			pairErrors = append(pairErrors, fmt.Errorf("%s→%s: %w", resolved.From, resolved.To, err))
			result.Error = state.ShortError(err)
		}
		results = append(results, result)
	}
	if len(pairErrors) > 0 {
		return results, coded(ExitError, fmt.Errorf("%d sync pair(s) failed: %w", len(pairErrors), errors.Join(pairErrors...)))
	}
	if skipped > 0 {
		return results, coded(ExitSkipped, fmt.Errorf("%d of %d sync pair(s) skipped: %w",
			skipped, len(cfg.SyncPairs), errors.Join(unmetErrors(cfg, unmet)...)))
	}
	return results, nil
}

// skipReason returns why p must be skipped, or nil when it can run.
func skipReason(p config.SyncPair, unmet map[string]error) error {
	if err := unmet[p.From]; err != nil {
		return err
	}
	return unmet[p.To]
}

func skippedResult(p config.SyncPair, reason error) state.PairResult {
	return state.PairResult{
		From:   p.From,
		To:     p.To,
		Error:  state.ShortError(fmt.Errorf("skipped: %w", reason)),
		Reason: state.ReasonPreconditionUnmet,
	}
}

// probeFailureResults records a run stopped by a failed token probe. Pairs
// that would have been skipped keep their precondition reason; every other
// pair carries the probe error, marked blocked_by_policy when one of its
// accounts was refused by Conditional Access.
func probeFailureResults(pairs []config.SyncPair, err error, unmet map[string]error, blocked map[string]bool) []state.PairResult {
	msg := state.ShortError(err)
	out := make([]state.PairResult, 0, len(pairs))
	for _, p := range pairs {
		if reason := skipReason(p, unmet); reason != nil {
			out = append(out, skippedResult(p, reason))
			continue
		}
		r := state.PairResult{From: p.From, To: p.To, Error: msg}
		if blocked[p.From] || blocked[p.To] {
			r.Reason = state.ReasonBlockedByPolicy
		}
		out = append(out, r)
	}
	return out
}

// newAuthCmd runs the device code flow for a single account.
func newAuthCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "auth <account>",
		Short: "Log in to an account using device code flow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			acc := cfg.Account(args[0])
			if acc == nil {
				return coded(ExitConfig, fmt.Errorf("account %q not defined in config", args[0]))
			}
			a, err := auth.New(*acc)
			if err != nil {
				return coded(ExitError, err)
			}
			present := func(userCode, url, message string) {
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, message)
				fmt.Fprintln(os.Stderr)
				fmt.Fprintf(os.Stderr, "  URL:  %s\n", url)
				fmt.Fprintf(os.Stderr, "  Code: %s\n", userCode)
				fmt.Fprintln(os.Stderr)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			if err := a.Login(ctx, present); err != nil {
				return coded(ExitAuth, err)
			}
			fmt.Fprintf(os.Stderr, "logged in as %s (%s)\n", acc.Name, acc.Email)
			return nil
		},
	}
}

// newConfigCmd groups config-related subcommands.
func newConfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Config utilities"}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ok: %d accounts, %d sync pairs\n", len(cfg.Accounts), len(cfg.SyncPairs))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the effective config path",
		Run: func(cmd *cobra.Command, args []string) {
			path := g.configPath
			if path == "" {
				path = config.DefaultPath()
			}
			fmt.Println(path)
		},
	})
	return cmd
}

// newStatusCmd prints each account's auth state, the configured pairs and
// the last sync recorded by `sync`. --json prints the same data for
// scripts; a status bar that only needs "last sync" should read the state
// file directly instead, because this command makes network auth checks.
func newStatusCmd(g *globalOpts) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show configured pairs, auth status and last sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			// Bound per-account probes so `status` cannot hang on a slow
			// authority endpoint or a locked keychain prompt. The user
			// running `status` wants a fast diagnosis, not a 30s wait.
			rootCtx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			configPath := g.configPath
			if configPath == "" {
				configPath = config.DefaultPath()
			}
			r := buildStatus(rootCtx, cfg, configPath, state.DefaultPath(), newTokenSource)
			if !jsonOut {
				printStatus(cfg, r)
				return nil
			}
			if r.stateErr != nil {
				slog.Warn("could not read sync state", slog.String("err", r.stateErr.Error()))
			}
			if err := writeStatusJSON(os.Stdout, r); err != nil {
				return coded(ExitError, err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON")
	return cmd
}
