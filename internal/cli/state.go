package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/state"
)

// recordSyncState writes the outcome of a real sync run to path so status
// bars and `status` can show the last sync. Dry runs write nothing. A
// failed write only warns: the exit code reflects the calendar operations,
// not this bookkeeping.
func recordSyncState(path string, dryRun bool, started time.Time, pairs []config.SyncPair, results []state.PairResult, runErr error) {
	if dryRun {
		return
	}
	if results == nil && runErr != nil {
		// The run stopped before any pair ran (auth or setup failure).
		// Record the reason against every pair so a status bar can show it.
		msg := state.ShortError(runErr)
		for _, p := range pairs {
			results = append(results, state.PairResult{From: p.From, To: p.To, Error: msg})
		}
	}
	prev, err := state.Load(path)
	if err != nil {
		slog.Warn("ignoring unreadable sync state", slog.String("path", path), slog.String("err", err.Error()))
	}
	if err := state.Save(path, state.Next(prev, started, results)); err != nil {
		slog.Warn("could not write sync state", slog.String("path", path), slog.String("err", err.Error()))
	}
}

// Auth states reported per account by `status --json`.
const (
	authLoggedIn    = "logged_in"
	authNotLoggedIn = "not_logged_in"
	authTimeout     = "timeout"
	authError       = "error"
)

// statusReport is the `status --json` document. LastSync is the state
// file content as written by `sync`, or null if there is none yet.
type statusReport struct {
	ConfigPath string          `json:"config_path"`
	StatePath  string          `json:"state_path"`
	Accounts   []accountStatus `json:"accounts"`
	Pairs      []pairStatus    `json:"pairs"`
	LastSync   *state.State    `json:"last_sync"`

	stateErr error
}

type accountStatus struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Auth      string `json:"auth"`
	AuthError string `json:"auth_error,omitempty"`

	// setupFailed marks an authenticator that could not be built at all,
	// which the text output has always labelled differently.
	setupFailed bool
}

type pairStatus struct {
	From string `json:"from"`
	To   string `json:"to"`
	Mode string `json:"mode"`
}

// tokenSource is the part of auth.Authenticator that `status` probes. Tests
// stub it so they never touch the OS keyring.
type tokenSource interface {
	Token(ctx context.Context) (string, error)
}

func newTokenSource(acc config.Account) (tokenSource, error) {
	a, err := auth.New(acc)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// buildStatus gathers everything `status` prints. An account whose probe
// fails or times out is reported per account; the last-sync data is read
// from the state file regardless.
func buildStatus(ctx context.Context, cfg *config.Config, configPath, statePath string, newSource func(config.Account) (tokenSource, error)) statusReport {
	r := statusReport{
		ConfigPath: configPath,
		StatePath:  statePath,
		Accounts:   []accountStatus{},
		Pairs:      []pairStatus{},
	}
	for _, acc := range cfg.Accounts {
		r.Accounts = append(r.Accounts, probeAccount(ctx, *cfg.Account(acc.Name), newSource))
	}
	for _, p := range cfg.SyncPairs {
		rp := p.Resolved(cfg.Defaults)
		r.Pairs = append(r.Pairs, pairStatus{From: rp.From, To: rp.To, Mode: rp.Mode})
	}
	r.LastSync, r.stateErr = state.Load(statePath)
	return r
}

func probeAccount(ctx context.Context, acc config.Account, newSource func(config.Account) (tokenSource, error)) accountStatus {
	st := accountStatus{Name: acc.Name, Email: acc.Email}
	src, err := newSource(acc)
	if err != nil {
		st.Auth, st.AuthError, st.setupFailed = authError, err.Error(), true
		return st
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = src.Token(probeCtx)
	switch {
	case err == nil:
		st.Auth = authLoggedIn
	case errors.Is(err, auth.ErrLoginRequired):
		st.Auth = authNotLoggedIn
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded):
		st.Auth, st.AuthError = authTimeout, err.Error()
	default:
		st.Auth, st.AuthError = authError, err.Error()
	}
	return st
}

func writeStatusJSON(w io.Writer, r statusReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func printStatus(cfg *config.Config, r statusReport) {
	for _, a := range r.Accounts {
		switch {
		case a.setupFailed:
			fmt.Printf("  %s\t(%s)\tERROR: %s\n", a.Name, a.Email, a.AuthError)
		case a.Auth == authLoggedIn:
			fmt.Printf("  %s\t(%s)\tlogged in\n", a.Name, a.Email)
		case a.Auth == authNotLoggedIn:
			fmt.Printf("  %s\t(%s)\tNOT LOGGED IN (run: outlook-busy-sync auth %s)\n", a.Name, a.Email, a.Name)
		default:
			fmt.Printf("  %s\t(%s)\tAUTH ERROR: %s\n", a.Name, a.Email, a.AuthError)
		}
	}
	fmt.Println()
	fmt.Println("Sync pairs:")
	for _, p := range cfg.SyncPairs {
		rp := p.Resolved(cfg.Defaults)
		fmt.Printf("  %s -> %s  (mode: %s, window: -%dd..+%dd, title: %q)\n",
			rp.From, rp.To, rp.Mode, rp.LookbackDays, rp.LookaheadDays, rp.Title)
	}
	fmt.Println()
	fmt.Println(lastSyncLine(r.LastSync, r.stateErr))
}

func lastSyncLine(s *state.State, stateErr error) string {
	switch {
	case stateErr != nil:
		return fmt.Sprintf("Last sync: unknown (%v)", stateErr)
	case s == nil:
		return "Last sync: never"
	case s.Succeeded():
		return fmt.Sprintf("Last sync: %s (ok)", localTime(s.LastAttempt))
	}
	failed := 0
	for _, p := range s.Pairs {
		if p.Error != "" {
			failed++
		}
	}
	success := "never"
	if s.LastSuccess != nil {
		success = localTime(*s.LastSuccess)
	}
	return fmt.Sprintf("Last sync: %s (%d of %d pair(s) failed), last success: %s",
		localTime(s.LastAttempt), failed, len(s.Pairs), success)
}

func localTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04:05 MST")
}
