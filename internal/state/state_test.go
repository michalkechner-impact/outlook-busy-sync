package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSaveLoad_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	now := time.Date(2026, 11, 25, 8, 30, 15, 0, time.UTC)
	want := Next(nil, now, []PairResult{
		{From: "work", To: "client", Created: 2, Updated: 1, Deleted: 3},
		{From: "client", To: "work", Created: 1},
	})
	if err := Save(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil state after save")
	}
	if got.SchemaVersion != SchemaVersion || !got.LastAttempt.Equal(now) {
		t.Errorf("header mismatch: %+v", got)
	}
	if got.LastSuccess == nil || !got.LastSuccess.Equal(now) {
		t.Errorf("last_success should equal last_attempt after a clean run, got %v", got.LastSuccess)
	}
	if len(got.Pairs) != 2 || got.Pairs[0] != want.Pairs[0] || got.Pairs[1] != want.Pairs[1] {
		t.Errorf("pairs mismatch: got %+v want %+v", got.Pairs, want.Pairs)
	}
}

func TestLoad_missingFileIsNil(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil || got != nil {
		t.Errorf("missing file should be (nil, nil), got (%v, %v)", got, err)
	}
}

func TestLoad_corruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("corrupt state file must surface an error")
	}
}

// A partial failure must not move last_success forward, otherwise a status
// bar would report a healthy sync while one direction is broken.
func TestNext_partialFailureKeepsPreviousSuccess(t *testing.T) {
	first := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	prev := Next(nil, first, []PairResult{{From: "work", To: "client"}})

	got := Next(&prev, second, []PairResult{
		{From: "work", To: "client", Created: 1},
		{From: "client", To: "work", Failed: 2, Error: "2 event operation(s) failed"},
	})
	if !got.LastAttempt.Equal(second) {
		t.Errorf("last_attempt should advance to %v, got %v", second, got.LastAttempt)
	}
	if got.LastSuccess == nil || !got.LastSuccess.Equal(first) {
		t.Errorf("last_success must stay at %v, got %v", first, got.LastSuccess)
	}
	if got.Succeeded() {
		t.Error("state with a failed pair must not report success")
	}
}

func TestNext_firstRunFailureHasNoSuccess(t *testing.T) {
	got := Next(nil, time.Now(), []PairResult{{From: "a", To: "b", Error: "boom"}})
	if got.LastSuccess != nil {
		t.Errorf("last_success must be null until a clean run, got %v", got.LastSuccess)
	}
}

func TestNext_timestampsAreUTCWholeSeconds(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	got := Next(nil, time.Date(2026, 11, 25, 9, 30, 15, 987654321, loc), nil)
	if got.LastAttempt.Location() != time.UTC || got.LastAttempt.Nanosecond() != 0 {
		t.Errorf("last_attempt should be UTC whole seconds, got %v", got.LastAttempt)
	}
	if got.Pairs == nil {
		t.Error("pairs must encode as [] not null")
	}
}

// Status-bar scripts parse this file directly; pin the wire format.
func TestState_jsonFormat(t *testing.T) {
	now := time.Date(2026, 11, 25, 8, 30, 0, 0, time.UTC)
	s := Next(nil, now, []PairResult{
		{From: "work", To: "client", Created: 1, Updated: 2, Deleted: 3},
		{From: "client", To: "work", Failed: 1, Error: "1 event operation(s) failed"},
	})
	s.ToolVersion = "0.4.0"
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"tool_version":"0.4.0","last_attempt":"2026-11-25T08:30:00Z","last_success":null,"pairs":[{"from":"work","to":"client","created":1,"updated":2,"deleted":3,"failed":0},{"from":"client","to":"work","created":0,"updated":0,"deleted":0,"failed":1,"error":"1 event operation(s) failed"}]}`
	if string(b) != want {
		t.Errorf("wire format changed:\n got %s\nwant %s", b, want)
	}
}

func TestSave_isMode0600AndLeavesNoTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model only")
	}
	dir := filepath.Join(t.TempDir(), "outlook-busy-sync")
	path := filepath.Join(dir, FileName)
	for i := 0; i < 2; i++ {
		if err := Save(path, Next(nil, time.Now(), nil)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file must be 0600, got %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".state-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

func TestShortError_flattensAndTruncates(t *testing.T) {
	if got := ShortError(nil); got != "" {
		t.Errorf("nil error should be empty, got %q", got)
	}
	got := ShortError(errors.New("missing credentials:\n  client   used by: work→client"))
	if want := "missing credentials: client used by: work→client"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	long := ShortError(errors.New(strings.Repeat("é", 500)))
	if n := len([]rune(long)); n != maxErrLen || !strings.HasSuffix(long, "...") {
		t.Errorf("long error should be cut to %d runes ending in ..., got %d", maxErrLen, n)
	}
}

func TestDefaultPath_followsConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses APPDATA, not XDG")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join("custom", "xdg"))
	want := filepath.Join("custom", "xdg", "outlook-busy-sync", "state.json")
	if got := DefaultPath(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
