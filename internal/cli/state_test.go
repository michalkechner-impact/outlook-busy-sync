package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/michalkechner-impact/outlook-busy-sync/internal/auth"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/config"
	"github.com/michalkechner-impact/outlook-busy-sync/internal/state"
)

// These tests never build a real auth.Authenticator: its token store
// probes the OS keyring, which tests must not touch.

var testPairs = []config.SyncPair{{From: "work", To: "client"}, {From: "client", To: "work"}}

func okResults() []state.PairResult {
	return []state.PairResult{
		{From: "work", To: "client", Created: 1},
		{From: "client", To: "work", Updated: 2},
	}
}

func TestRecordSyncState_dryRunWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outlook-busy-sync", state.FileName)
	recordSyncState(path, true, time.Now(), testPairs, okResults(), nil)
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry run must not create the state dir or file, stat err: %v", err)
	}
}

func TestRecordSyncState_writesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model only")
	}
	path := filepath.Join(t.TempDir(), "outlook-busy-sync", state.FileName)
	recordSyncState(path, false, time.Now(), testPairs, okResults(), nil)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file must be 0600, got %o", info.Mode().Perm())
	}
}

func TestRecordSyncState_failuresKeepLastSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), state.FileName)
	first := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	recordSyncState(path, false, first, testPairs, okResults(), nil)

	// Partial failure: one pair ran, the other failed.
	second := first.Add(15 * time.Minute)
	partial := okResults()
	partial[1].Failed, partial[1].Error = 1, "1 event operation(s) failed"
	recordSyncState(path, false, second, testPairs, partial, coded(ExitError, errors.New("1 sync pair(s) failed")))
	got := mustLoad(t, path)
	if !got.LastAttempt.Equal(second) || got.LastSuccess == nil || !got.LastSuccess.Equal(first) {
		t.Errorf("partial failure: want attempt %v success %v, got %v / %v", second, first, got.LastAttempt, got.LastSuccess)
	}

	// Auth failure: no pair ran at all, every pair carries the reason.
	third := second.Add(15 * time.Minute)
	authErr := coded(ExitAuth, errors.New("missing credentials for 1 account(s):\n  client   used by: work→client"))
	recordSyncState(path, false, third, testPairs, nil, authErr)
	got = mustLoad(t, path)
	if !got.LastAttempt.Equal(third) || got.LastSuccess == nil || !got.LastSuccess.Equal(first) {
		t.Errorf("auth failure: want attempt %v success %v, got %v / %v", third, first, got.LastAttempt, got.LastSuccess)
	}
	if len(got.Pairs) != 2 {
		t.Fatalf("auth failure should list every configured pair, got %+v", got.Pairs)
	}
	for _, p := range got.Pairs {
		if !strings.HasPrefix(p.Error, "missing credentials") || strings.Contains(p.Error, "\n") {
			t.Errorf("pair %s->%s: want a one-line auth error, got %q", p.From, p.To, p.Error)
		}
	}
}

func mustLoad(t *testing.T, path string) *state.State {
	t.Helper()
	s, err := state.Load(path)
	if err != nil || s == nil {
		t.Fatalf("load state: %v (nil: %t)", err, s == nil)
	}
	return s
}

type fakeSource struct{ err error }

func (f fakeSource) Token(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "token", nil
}

func fakeSources(acc config.Account) (tokenSource, error) {
	switch acc.Name {
	case "work":
		return fakeSource{}, nil
	case "client":
		return fakeSource{err: fmt.Errorf("silent token: %w", context.DeadlineExceeded)}, nil
	case "home":
		return fakeSource{err: auth.ErrLoginRequired}, nil
	default:
		return nil, errors.New("token store: mkdir: permission denied")
	}
}

func statusTestConfig() *config.Config {
	return &config.Config{
		Accounts: []config.Account{
			{Name: "work", Email: "me@work.example", TenantID: "common"},
			{Name: "client", Email: "me@client.example", TenantID: "common"},
			{Name: "home", Email: "me@home.example", TenantID: "common"},
			{Name: "lab", Email: "me@lab.example", TenantID: "common"},
		},
		SyncPairs: []config.SyncPair{
			{From: "work", To: "client"},
			{From: "client", To: "work", Mode: config.ModeMirror},
		},
	}
}

// statusJSON is the shape a script decoding `status --json` relies on.
type statusJSON struct {
	ConfigPath string `json:"config_path"`
	StatePath  string `json:"state_path"`
	Accounts   []struct {
		Name      string `json:"name"`
		Email     string `json:"email"`
		Auth      string `json:"auth"`
		AuthError string `json:"auth_error"`
	} `json:"accounts"`
	Pairs []struct {
		From string `json:"from"`
		To   string `json:"to"`
		Mode string `json:"mode"`
	} `json:"pairs"`
	LastSync *struct {
		SchemaVersion int                `json:"schema_version"`
		ToolVersion   string             `json:"tool_version"`
		LastAttempt   string             `json:"last_attempt"`
		LastSuccess   *string            `json:"last_success"`
		Pairs         []state.PairResult `json:"pairs"`
	} `json:"last_sync"`
}

func decodeStatus(t *testing.T, r statusReport) statusJSON {
	t.Helper()
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&buf)
	dec.DisallowUnknownFields()
	var got statusJSON
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("status --json does not decode into the expected shape: %v\n%s", err, buf.String())
	}
	return got
}

func TestStatusJSON_shape(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), state.FileName)
	attempt := time.Date(2026, 11, 25, 8, 30, 0, 0, time.UTC)
	if err := state.Save(statePath, state.Next(nil, attempt, okResults())); err != nil {
		t.Fatal(err)
	}

	r := buildStatus(context.Background(), statusTestConfig(), "/cfg/config.yaml", statePath, fakeSources)
	got := decodeStatus(t, r)

	if got.ConfigPath != "/cfg/config.yaml" || got.StatePath != statePath {
		t.Errorf("paths: got %q / %q", got.ConfigPath, got.StatePath)
	}
	wantAuth := map[string]string{"work": authLoggedIn, "client": authTimeout, "home": authNotLoggedIn, "lab": authError}
	if len(got.Accounts) != len(wantAuth) {
		t.Fatalf("want %d accounts, got %+v", len(wantAuth), got.Accounts)
	}
	for _, a := range got.Accounts {
		if a.Auth != wantAuth[a.Name] {
			t.Errorf("account %s: auth %q, want %q", a.Name, a.Auth, wantAuth[a.Name])
		}
		hasErr := a.AuthError != ""
		if wantErr := a.Auth == authTimeout || a.Auth == authError; hasErr != wantErr {
			t.Errorf("account %s: auth_error %q (want present: %t)", a.Name, a.AuthError, wantErr)
		}
	}
	if len(got.Pairs) != 2 || got.Pairs[0].Mode != config.ModeBusy || got.Pairs[1].Mode != config.ModeMirror {
		t.Errorf("pairs: got %+v", got.Pairs)
	}
	// A timed-out probe must not cost us the last-sync data.
	if got.LastSync == nil {
		t.Fatal("last_sync missing despite a state file")
	}
	if got.LastSync.LastAttempt != "2026-11-25T08:30:00Z" || got.LastSync.LastSuccess == nil || *got.LastSync.LastSuccess != "2026-11-25T08:30:00Z" {
		t.Errorf("last_sync timestamps: %+v", got.LastSync)
	}
	if len(got.LastSync.Pairs) != 2 || got.LastSync.Pairs[0].Created != 1 {
		t.Errorf("last_sync pairs: %+v", got.LastSync.Pairs)
	}
}

func TestStatusJSON_noStateFileIsNull(t *testing.T) {
	r := buildStatus(context.Background(), statusTestConfig(), "/cfg/config.yaml", filepath.Join(t.TempDir(), state.FileName), fakeSources)
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"last_sync": null`) {
		t.Errorf("last_sync should be null before the first sync:\n%s", buf.String())
	}
}

func TestLastSyncLine(t *testing.T) {
	first := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	ok := state.Next(nil, first, okResults())
	failed := state.Next(&ok, first.Add(time.Hour), []state.PairResult{{From: "a", To: "b", Error: "boom"}, {From: "b", To: "a"}})
	neverOK := state.Next(nil, first, []state.PairResult{{From: "a", To: "b", Error: "boom"}})
	tests := []struct {
		name string
		s    *state.State
		err  error
		want string
	}{
		{"no file", nil, nil, "Last sync: never"},
		{"unreadable", nil, errors.New("parse state"), "Last sync: unknown (parse state)"},
		{"ok", &ok, nil, "Last sync: " + localTime(first) + " (ok)"},
		{"failed", &failed, nil, "Last sync: " + localTime(first.Add(time.Hour)) + " (1 of 2 pair(s) failed), last success: " + localTime(first)},
		{"never ok", &neverOK, nil, "Last sync: " + localTime(first) + " (1 of 1 pair(s) failed), last success: never"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastSyncLine(tc.s, tc.err); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}
