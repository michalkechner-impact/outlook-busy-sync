package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/zalando/go-keyring"
)

const keyringService = "outlook-busy-sync"

// tokenStore persists MSAL's serialized cache for one account. It prefers
// the macOS Keychain (via go-keyring) and falls back to a file in the user's
// config dir whenever the keyring is unavailable OR refuses the payload.
//
// macOS Keychain "generic password" items have a payload size limit on the
// order of a few KB. The MSAL cache (multiple tokens, account info,
// authority metadata) routinely exceeds that, especially after a few token
// refreshes. Rather than try to chunk the cache across multiple Keychain
// items, we fall back to a 0600 file in $XDG_CONFIG_HOME the moment the
// Keychain rejects the write. The file is on disk in the user's home
// directory, which is the same trust boundary as the Keychain for a
// single-user macOS install.
type tokenStore struct {
	account string
	// filePath is the fallback location used whenever the keyring rejects
	// the payload (size, locked keychain, missing entitlement, etc).
	filePath   string
	preferFile bool
}

func newTokenStore(account string) (tokenStore, error) {
	ts := tokenStore{account: account}
	// Probe the keyring once. On macOS it is always available; on headless
	// Linux CI it may not be.
	if _, err := keyring.Get(keyringService, account+"__probe"); err != nil && errors.Is(err, keyring.ErrUnsupportedPlatform) {
		ts.preferFile = true
	}
	dir, err := configDir()
	if err != nil {
		return ts, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ts, err
	}
	ts.filePath = filepath.Join(dir, "tokens-"+account+".json")
	return ts, nil
}

func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "outlook-busy-sync"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "outlook-busy-sync"), nil
}

func (t tokenStore) load() ([]byte, error) {
	if !t.preferFile {
		v, err := keyring.Get(keyringService, t.account)
		if err == nil {
			return []byte(v), nil
		}
		// Treat any keyring read error as "no cached value" and fall through
		// to the file. Real fatal errors (permission denied etc.) will resurface
		// when we attempt the file read.
	}
	b, err := os.ReadFile(t.filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func (t tokenStore) save(data []byte) error {
	// Try keychain first. Any failure (size limit, locked, missing entitlement)
	// transparently falls through to the file backend - the cache contents are
	// already protected by the user's home directory permissions on macOS.
	if !t.preferFile {
		if err := keyring.Set(keyringService, t.account, string(data)); err == nil {
			// Drop any prior file-backed copy so we have a single source of
			// truth and don't leak a stale cache on disk.
			_ = os.Remove(t.filePath)
			return nil
		}
	}
	if err := os.WriteFile(t.filePath, data, 0o600); err != nil {
		return fmt.Errorf("file save: %w", err)
	}
	// Paranoid permission check on macOS where umask may widen perms.
	if runtime.GOOS == "darwin" {
		_ = os.Chmod(t.filePath, 0o600)
	}
	return nil
}

func (t tokenStore) clear() error {
	var errs []error
	if err := keyring.Delete(keyringService, t.account); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) &&
		!errors.Is(err, keyring.ErrUnsupportedPlatform) {
		errs = append(errs, err)
	}
	if err := os.Remove(t.filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Replace is called by MSAL before it reads the cache.
func (t tokenStore) Replace(ctx context.Context, unmarshaler cache.Unmarshaler, hints cache.ReplaceHints) error {
	data, err := t.load()
	if err != nil || len(data) == 0 {
		return err
	}
	return unmarshaler.Unmarshal(data)
}

// Export is called by MSAL after it writes the cache.
func (t tokenStore) Export(ctx context.Context, marshaler cache.Marshaler, hints cache.ExportHints) error {
	data, err := marshaler.Marshal()
	if err != nil {
		return err
	}
	return t.save(data)
}
