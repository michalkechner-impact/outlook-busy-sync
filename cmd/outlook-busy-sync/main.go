// Command outlook-busy-sync mirrors busy blocks between Microsoft 365
// calendars without exposing any meeting details. See the repository
// README for usage.
package main

import (
	"fmt"
	"os"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/cli"
)

func main() {
	if err := cli.NewRoot().Execute(); err != nil {
		code := cli.ExitCode(err)
		if code == 0 {
			code = 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(code)
	}
}
