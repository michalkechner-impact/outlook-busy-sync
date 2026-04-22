package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
)

// Scheduled runners (launchd, systemd, Task Scheduler) key off these exit
// codes to decide whether to alert / retry. A regression here would make
// silent failures invisible in ops dashboards.
func TestExitCode_mapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil -> ok", nil, ExitOK},
		{"login required -> auth", auth.ErrLoginRequired, ExitAuth},
		{"wrapped login required -> auth", fmt.Errorf("outer: %w", auth.ErrLoginRequired), ExitAuth},
		{"explicit config coded -> config", coded(ExitConfig, errors.New("bad")), ExitConfig},
		{"explicit auth coded -> auth", coded(ExitAuth, errors.New("no creds")), ExitAuth},
		{"generic error -> generic", errors.New("boom"), ExitError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestNewRoot_hasExpectedSubcommands(t *testing.T) {
	cmd := NewRoot()
	want := map[string]bool{"auth": false, "sync": false, "config": false, "status": false, "events": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q missing from root", name)
		}
	}
}

func TestNewSyncCmd_hasDryRunFlag(t *testing.T) {
	cmd := newSyncCmd(&globalOpts{})
	if cmd.Flags().Lookup("dry-run") == nil {
		t.Error("`sync --dry-run` must be supported for safe previews of destructive runs")
	}
}
