// Package cli wires the cobra command tree. Keeping it in its own package
// makes main.go trivially small and lets us unit-test command construction.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/graph"
	syncpkg "github.com/michalkechner-impact/outlook-busy-sync/internal/sync"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/version"
)

// Exit codes. Keep stable: launchd / shell scripts key off these.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitAuth     = 2
	ExitConfig   = 3
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
	cmd.AddCommand(newSyncCmd(&g))
	cmd.AddCommand(newConfigCmd(&g))
	cmd.AddCommand(newStatusCmd(&g))
	cmd.AddCommand(newEventsCmd(&g))
	return cmd
}

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

const rootLong = `outlook-busy-sync keeps two (or more) Microsoft 365 calendars aware of each
other by copying free/busy information from one account to another as
generic "Busy" blocks. It never copies event titles, locations, bodies or
attendees, so the detail of meetings on one side is never exposed on the
other.

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
	return &cobra.Command{
		Use:   "sync",
		Short: "Run all configured sync pairs once",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			return runSync(ctx, cfg)
		},
	}
}

func runSync(ctx context.Context, cfg *config.Config) error {
	// Build clients up front so we can report all missing auths in one go,
	// rather than failing on the first pair the user happens to hit.
	clients := syncpkg.Clients{}
	authenticators := map[string]*auth.Authenticator{}
	for _, acc := range cfg.Accounts {
		resolved := *cfg.Account(acc.Name)
		a, err := auth.New(resolved)
		if err != nil {
			return coded(ExitConfig, fmt.Errorf("auth setup for %s: %w", acc.Name, err))
		}
		authenticators[acc.Name] = a
		clients[acc.Name] = graph.New(a)
	}

	// Determine which accounts are actually used by any configured pair.
	usedBy := map[string][]string{} // account -> list of "from→to" pairs that need it
	for _, p := range cfg.SyncPairs {
		usedBy[p.From] = append(usedBy[p.From], fmt.Sprintf("%s→%s", p.From, p.To))
		usedBy[p.To] = append(usedBy[p.To], fmt.Sprintf("%s→%s", p.From, p.To))
	}

	// Probe auth for each used account so we can report ALL missing accounts
	// in one error rather than failing one pair at a time.
	var missing []string
	for accName := range usedBy {
		if _, err := authenticators[accName].Token(ctx); err != nil {
			if errors.Is(err, auth.ErrLoginRequired) {
				missing = append(missing, accName)
			}
		}
	}
	if len(missing) > 0 {
		var lines []string
		for _, m := range missing {
			lines = append(lines, fmt.Sprintf("  %s   used by: %s   fix: outlook-busy-sync auth %s",
				m, strings.Join(usedBy[m], ", "), m))
		}
		return coded(ExitAuth, fmt.Errorf("missing credentials for %d account(s):\n%s",
			len(missing), strings.Join(lines, "\n")))
	}

	engine := syncpkg.New(clients, slog.Default())
	var failed bool
	for _, p := range cfg.SyncPairs {
		resolved := p.Resolved(cfg.Defaults)
		if _, err := engine.RunPair(ctx, resolved); err != nil {
			slog.Error("sync pair failed",
				slog.String("from", resolved.From),
				slog.String("to", resolved.To),
				slog.String("err", err.Error()))
			failed = true
		}
	}
	if failed {
		return coded(ExitError, errors.New("one or more sync pairs failed - see logs"))
	}
	return nil
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

// newStatusCmd prints a high-level status. MVP: just show configured pairs
// and whether each account has a cached token. Richer stats (last-run
// timestamps) are a future iteration once we persist a run log.
func newStatusCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show configured pairs and auth status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			for _, acc := range cfg.Accounts {
				resolved := *cfg.Account(acc.Name)
				a, err := auth.New(resolved)
				if err != nil {
					fmt.Printf("  %s\t(%s)\tERROR: %v\n", acc.Name, acc.Email, err)
					continue
				}
				if _, err := a.Token(ctx); err != nil {
					fmt.Printf("  %s\t(%s)\tNOT LOGGED IN (run: outlook-busy-sync auth %s)\n", acc.Name, acc.Email, acc.Name)
					continue
				}
				fmt.Printf("  %s\t(%s)\tlogged in\n", acc.Name, acc.Email)
			}
			fmt.Println()
			fmt.Println("Sync pairs:")
			for _, p := range cfg.SyncPairs {
				r := p.Resolved(cfg.Defaults)
				fmt.Printf("  %s -> %s  (window: -%dd..+%dd, title: %q)\n",
					r.From, r.To, r.LookbackDays, r.LookaheadDays, r.Title)
			}
			return nil
		},
	}
}
