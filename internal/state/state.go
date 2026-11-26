// Package state persists a small summary of the last real `sync` run so a
// status bar or monitoring script can show "last sync" by reading one file,
// without calling Graph. The file holds timestamps, per-pair counts and
// short error strings only: never event subjects, bodies, attendees or
// locations.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/version"
)

// SchemaVersion is bumped on any incompatible change to the file format.
// Readers should check it before relying on field names.
const SchemaVersion = 1

// FileName is the state file's name inside the config directory.
const FileName = "state.json"

// maxErrLen caps each stored error string. The full error is already in
// the sync log; the state file only needs enough to show what went wrong.
const maxErrLen = 200

// State is the content of the state file. Timestamps are UTC, whole
// seconds, RFC 3339.
type State struct {
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`
	// LastAttempt is when the most recent real sync run started.
	LastAttempt time.Time `json:"last_attempt"`
	// LastSuccess is when the most recent run in which every pair
	// succeeded started. Null until the first fully successful run.
	LastSuccess *time.Time   `json:"last_success"`
	Pairs       []PairResult `json:"pairs"`
}

// PairResult summarises one sync pair in the most recent run. Failed counts
// event operations that failed; Error is set whenever the pair as a whole
// failed or was skipped, including failures before any event was touched
// (auth, listing). Reason, when set, classifies Error for status bars.
type PairResult struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Created int    `json:"created"`
	Updated int    `json:"updated"`
	Deleted int    `json:"deleted"`
	Failed  int    `json:"failed"`
	Error   string `json:"error,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Reasons a pair did not sync. Any other failure leaves Reason empty.
const (
	// ReasonPreconditionUnmet: an account's precondition command did not
	// exit 0, so the pair was skipped without asking Microsoft for a token.
	ReasonPreconditionUnmet = "precondition_unmet"
	// ReasonBlockedByPolicy: the tenant's Conditional Access refused the
	// sign-in, for example from outside its trusted network. Best effort:
	// it relies on the AADSTS code in the error.
	ReasonBlockedByPolicy = "blocked_by_policy"
)

// Succeeded reports whether every pair in the run succeeded.
func (s State) Succeeded() bool {
	for _, p := range s.Pairs {
		if p.Error != "" {
			return false
		}
	}
	return true
}

// DefaultPath returns the state file path: the same per-user directory as
// the default config and the token-cache fallback. It does not follow
// --config, so external readers always find it in one place.
func DefaultPath() string {
	return filepath.Join(filepath.Dir(config.DefaultPath()), FileName)
}

// Next builds the state for a run that started at now. LastSuccess advances
// only when every pair succeeded; otherwise it is carried over from prev.
func Next(prev *State, now time.Time, pairs []PairResult) State {
	if pairs == nil {
		pairs = []PairResult{}
	}
	s := State{
		SchemaVersion: SchemaVersion,
		ToolVersion:   version.Version,
		LastAttempt:   now.UTC().Truncate(time.Second),
		Pairs:         pairs,
	}
	if prev != nil {
		s.LastSuccess = prev.LastSuccess
	}
	if s.Succeeded() {
		t := s.LastAttempt
		s.LastSuccess = &t
	}
	return s
}

// ShortError flattens err to a single line of at most maxErrLen characters
// for storage in the state file.
func ShortError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(msg); len(r) > maxErrLen {
		msg = string(r[:maxErrLen-3]) + "..."
	}
	return msg
}

// Load reads the state file at path. A missing file returns (nil, nil).
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	return &s, nil
}

// Save writes s to path atomically (temp file + rename) with mode 0600 on
// POSIX, creating the directory with 0700 if needed. A reader polling the
// file never sees a partial write.
func Save(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we bail out before rename.
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			cleanup()
			return fmt.Errorf("chmod temp: %w", err)
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
