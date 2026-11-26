package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
)

// preconditionTimeout bounds one precondition command. A VPN client's
// status command answers in well under a second; a command that hangs
// counts as not met instead of holding up the scheduled run.
const preconditionTimeout = 10 * time.Second

// errPreconditionUnmet marks an account whose precondition command did not
// exit 0. sync skips the pairs that need the account and status reports it,
// both without asking Microsoft for a token.
var errPreconditionUnmet = errors.New("precondition not met")

// preconditionFunc runs one account's precondition command.
type preconditionFunc func(ctx context.Context, argv []string) error

// precondition is the runner sync and status use. Tests replace it so they
// do not depend on real processes.
var precondition preconditionFunc = runPrecondition

// runPrecondition runs argv directly, without a shell, and returns nil when
// it exits 0. Its output is discarded: none of it belongs in the log or the
// state file.
func runPrecondition(ctx context.Context, argv []string) error {
	ctx, cancel := context.WithTimeout(ctx, preconditionTimeout)
	defer cancel()
	name := filepath.Base(argv[0])
	err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %s did not finish in time", errPreconditionUnmet, name)
	default:
		return fmt.Errorf("%w: %s: %v", errPreconditionUnmet, name, err)
	}
}

// checkPreconditions runs the precondition of every account in used that
// has one, in config order, and returns the accounts that must not be used.
func checkPreconditions(ctx context.Context, cfg *config.Config, used map[string]bool) map[string]error {
	unmet := map[string]error{}
	for _, acc := range cfg.Accounts {
		if !used[acc.Name] || len(acc.Precondition) == 0 {
			continue
		}
		if err := precondition(ctx, acc.Precondition); err != nil {
			unmet[acc.Name] = fmt.Errorf("account %s: %w", acc.Name, err)
		}
	}
	return unmet
}

// unmetErrors lists the reasons in unmet in config order.
func unmetErrors(cfg *config.Config, unmet map[string]error) []error {
	var errs []error
	for _, acc := range cfg.Accounts {
		if err := unmet[acc.Name]; err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
