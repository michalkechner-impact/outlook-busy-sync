package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
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
	want := map[string]bool{"auth": false, "logout": false, "sync": false, "config": false, "status": false, "events": false, "init": false}
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

func TestInit_scaffoldsConfigAndIsIdempotent(t *testing.T) {
	// First-time install UX: `init` must write a valid starter config
	// to a custom path and, on a second invocation, refuse to overwrite
	// so a user re-running it by mistake doesn't nuke their work.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	g := &globalOpts{configPath: path}
	cmd := newInitCmd(g)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("first init failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		// Windows does not honour POSIX permission bits; ACL inheritance
		// from the config directory is the Windows-side protection.
		t.Errorf("config must be 0600 on POSIX, got %o", info.Mode().Perm())
	}

	// The scaffolded file must be loadable by the config package.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("scaffolded config does not pass validation: %v", err)
	}
	if len(cfg.Accounts) != 2 || len(cfg.SyncPairs) != 2 {
		t.Errorf("scaffold should have 2 accounts and 2 pairs, got %d/%d", len(cfg.Accounts), len(cfg.SyncPairs))
	}

	// Tamper the file, then re-run: it must NOT be overwritten.
	if err := os.WriteFile(path, []byte("# user-edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("idempotent init failed: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "# user-edited\n" {
		t.Error("second init overwrote existing config - must be idempotent")
	}
}

func TestNewSyncCmd_hasDryRunFlag(t *testing.T) {
	cmd := newSyncCmd(&globalOpts{})
	if cmd.Flags().Lookup("dry-run") == nil {
		t.Error("`sync --dry-run` must be supported for safe previews of destructive runs")
	}
}
