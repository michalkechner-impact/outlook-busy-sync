package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/schedule"
)

// newScheduleCmd registers the periodic sync with the OS scheduler, so users
// do not have to copy and edit the files under examples/scheduler.
func newScheduleCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Run sync periodically via launchd, a systemd user timer or Task Scheduler",
	}

	var interval time.Duration
	var installDryRun bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Register sync with the OS scheduler, replacing an earlier install",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := scheduleOptions(g, cmd, installDryRun)
			if err != nil {
				return coded(ExitError, err)
			}
			o.Interval = interval
			return coded(ExitError, schedule.Install(o))
		},
	}
	install.Flags().DurationVar(&interval, "interval", schedule.DefaultInterval, "how often sync runs, in whole minutes")
	install.Flags().BoolVar(&installDryRun, "dry-run", false, "print the files and commands without changing anything")

	var uninstallDryRun bool
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the scheduled sync; does nothing if none is installed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := scheduleOptions(g, cmd, uninstallDryRun)
			if err != nil {
				return coded(ExitError, err)
			}
			return coded(ExitError, schedule.Uninstall(o))
		},
	}
	uninstall.Flags().BoolVar(&uninstallDryRun, "dry-run", false, "print the files and commands without changing anything")

	cmd.AddCommand(install, uninstall)
	return cmd
}

// scheduleOptions is the only place the schedule commands touch the real
// home directory, the running binary and the scheduler tools.
func scheduleOptions(g *globalOpts, cmd *cobra.Command, dryRun bool) (schedule.Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return schedule.Options{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return schedule.Options{}, err
	}
	o := schedule.Options{
		GOOS:         runtime.GOOS,
		Home:         home,
		LocalAppData: os.Getenv("LOCALAPPDATA"),
		User:         os.Getenv("USERNAME"),
		UID:          os.Getuid(),
		Now:          time.Now(),
		Binary:       schedule.StableBinary(exe),
		DryRun:       dryRun,
		Out:          cmd.OutOrStdout(),
		Run:          runTool,
	}
	// A job that dropped --config would read the default path and fail on
	// every run.
	if g.configPath != "" {
		if o.Config, err = filepath.Abs(g.configPath); err != nil {
			return schedule.Options{}, err
		}
	}
	return o, nil
}

func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
