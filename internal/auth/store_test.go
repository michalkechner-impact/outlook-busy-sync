package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests deliberately use the file backend only — the keyring
// backend is OS-specific and hard to stub without also stubbing the
// platform, which go-keyring doesn't expose. File-path behaviour is the
// riskier path anyway (permissions, atomic rename, corrupt contents).

func newFileStore(t *testing.T) tokenStore {
	t.Helper()
	dir := t.TempDir()
	return tokenStore{
		account:    "test",
		filePath:   filepath.Join(dir, "tokens-test.json"),
		preferFile: true,
	}
}

func TestTokenStore_fileRoundtrip(t *testing.T) {
	ts := newFileStore(t)
	payload := []byte(`{"k":"v"}`)
	if err := ts.save(payload); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := ts.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("roundtrip mismatch: got %q want %q", got, payload)
	}
}

func TestTokenStore_loadMissingReturnsNil(t *testing.T) {
	ts := newFileStore(t)
	got, err := ts.load()
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("missing file should return nil, got %q", got)
	}
}

func TestTokenStore_loadPermissionErrorPropagates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model only")
	}
	ts := newFileStore(t)
	if err := os.WriteFile(ts.filePath, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := ts.load()
	if err == nil {
		t.Error("unreadable file must surface an error, not silent nil")
	}
}

func TestTokenStore_fileIsMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model only")
	}
	ts := newFileStore(t)
	if err := ts.save([]byte("secret")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ts.filePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file must be 0600, got %o", info.Mode().Perm())
	}
}

func TestTokenStore_saveIsAtomic(t *testing.T) {
	// Write one payload, then overwrite. The first payload must not be
	// observable after the second save — the atomic rename avoids the
	// "partial write then crash" case that would leave a truncated file.
	ts := newFileStore(t)
	if err := ts.save([]byte("first-payload")); err != nil {
		t.Fatal(err)
	}
	if err := ts.save([]byte("second-payload")); err != nil {
		t.Fatal(err)
	}
	got, err := ts.load()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second-payload" {
		t.Errorf("last write should win: got %q", got)
	}
	// And the working directory should not contain any `.tokens-*`
	// temp-file leftovers from the atomic write.
	entries, err := os.ReadDir(filepath.Dir(ts.filePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tokens-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

func TestTokenStore_clearRemovesFile(t *testing.T) {
	ts := newFileStore(t)
	if err := ts.save([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := ts.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(ts.filePath); !os.IsNotExist(err) {
		t.Errorf("file should be removed, stat err: %v", err)
	}
	// clear() again on an already-clean store must be a no-op.
	if err := ts.clear(); err != nil {
		t.Errorf("clear on clean store must not error, got %v", err)
	}
}

func TestConfigDir_honoursXDG(t *testing.T) {
	// Use filepath.Join so Windows's \ separator is accepted. The point
	// of the test is that XDG_CONFIG_HOME is respected; the separator
	// convention is the platform's to choose.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join("custom", "xdg"))
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("custom", "xdg", "outlook-busy-sync")
	if dir != want {
		t.Errorf("XDG path: got %q want %q", dir, want)
	}
}
