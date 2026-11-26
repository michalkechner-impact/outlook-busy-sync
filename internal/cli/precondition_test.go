package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPreconditionHelper is not a real test. The runPrecondition tests run
// the test binary itself as the precondition command, so they need no
// platform-specific programs; the child process lands here.
func TestPreconditionHelper(t *testing.T) {
	if os.Getenv("OBS_PRECONDITION_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch args[0] {
	case "exit":
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "sleep":
		time.Sleep(time.Minute)
	}
	os.Exit(99)
}

func helperArgv(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestPreconditionHelper$", "--"}, args...)
}

func TestRunPrecondition(t *testing.T) {
	t.Setenv("OBS_PRECONDITION_HELPER", "1")

	if err := runPrecondition(context.Background(), helperArgv("exit", "0")); err != nil {
		t.Errorf("exit 0 must count as met: %v", err)
	}

	err := runPrecondition(context.Background(), helperArgv("exit", "3"))
	if !errors.Is(err, errPreconditionUnmet) || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("exit 3: want an unmet error naming the status, got %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = runPrecondition(ctx, helperArgv("sleep"))
	if !errors.Is(err, errPreconditionUnmet) || !strings.Contains(err.Error(), "did not finish in time") {
		t.Errorf("hanging command: want a timeout error, got %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("hanging command was not stopped at the deadline, took %s", took)
	}

	missing := filepath.Join(t.TempDir(), "no-such-program")
	if err := runPrecondition(context.Background(), []string{missing}); !errors.Is(err, errPreconditionUnmet) {
		t.Errorf("missing program: want an unmet error, got %v", err)
	}
}
