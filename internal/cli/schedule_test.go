package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// Checks wiring only. Never call RunE on these commands in a test: it would
// drive the real launchctl, systemctl or schtasks of the machine running it.
func TestNewRoot_hasScheduleCommands(t *testing.T) {
	var sched *cobra.Command
	for _, sub := range NewRoot().Commands() {
		if sub.Name() == "schedule" {
			sched = sub
		}
	}
	if sched == nil {
		t.Fatal("subcommand \"schedule\" missing from root")
	}
	subs := map[string]*cobra.Command{}
	for _, sub := range sched.Commands() {
		subs[sub.Name()] = sub
	}
	install, uninstall := subs["install"], subs["uninstall"]
	if install == nil || uninstall == nil {
		t.Fatalf("schedule needs install and uninstall, has %v", subs)
	}
	if f := install.Flags().Lookup("interval"); f == nil || f.DefValue != "15m0s" {
		t.Errorf("install --interval must default to 15m like examples/scheduler, got %v", f)
	}
	for _, c := range []*cobra.Command{install, uninstall} {
		if c.Flags().Lookup("dry-run") == nil {
			t.Errorf("%s --dry-run missing", c.Name())
		}
	}
}
