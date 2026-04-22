package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/zalando/go-keyring"
)

const keyringService = "outlook-busy-sync"

// tokenStore persists MSAL's serialized cache for one account. Policy:
//
//   - One authoritative backend per process. At construction we probe
//     the OS keyring; if it's unusable we pin `preferFile=true` for the
//     lifetime of this store, so load() and save() cannot disagree about
//     where the token lives.
//   - When the keyring IS usable, we still fall back to the file for a
//     single save if the keyring rejects a specific payload (size limits
//     on macOS Keychain are the common case). On the following save the
//     keyring gets tried again and, on success, the stale file copy is
//     atomically replaced or removed.
//   - File writes are atomic (temp-file + rename) so a crash mid-write
//     never truncates the cache. Mode 0600 is enforced on every POSIX
//     platform, not just darwin.
type tokenStore struct {
	account string
	// filePath is the fallback location used whenever the keyring is
	// unavailable OR rejects the payload.
	filePath string
	// preferFile flips to true at construction if the keyring isn't
	// usable at all (e.g. headless Linux with no Secret Service). It
	// never flips back within one process.
	preferFile bool
}

func newTokenStore(account string) (tokenStore, error) {
	ts := tokenStore{account: account}
	dir, err := configDir()
	if err != nil {
		return ts, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ts, err
	}
	ts.filePath = filepath.Join(dir, "tokens-"+account+".json")

	// Probe the keyring with a real round-trip: write+delete a dummy value.
	// ErrUnsupportedPlatform and any actual keyring error mean "unusable".
	// The probe value is immediately cleaned up. We don't key off Get()
	// because ErrNotFound would be the normal return for a fresh install.
	probeKey := account + "__probe__"
	if err := keyring.Set(keyringService, probeKey, "1"); err != nil {
		if !errors.Is(err, keyring.ErrUnsupportedPlatform) {
			slog.Debug("keyring unusable, preferring file backend",
				slog.String("account", account),
				slog.String("err", err.Error()))
		}
		ts.preferFile = true
	} else {
		if err := keyring.Delete(keyringService, probeKey); err != nil {
			// Probe write worked but cleanup didn't. That's odd but not
			// fatal — just log and proceed with keyring-preferred.
			slog.Debug("keyring probe cleanup failed",
				slog.String("account", account),
				slog.String("err", err.Error()))
		}
	}
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
		if !errors.Is(err, keyring.ErrNotFound) && !errors.Is(err, keyring.ErrUnsupportedPlatform) {
			// Surface any non-"missing" keyring error instead of silently
			// pretending the cache is empty. A corrupted or locked
			// keychain is something the user needs to know about.
			return nil, fmt.Errorf("keyring read: %w", err)
		}
	}
	b, err := os.ReadFile(t.filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("file read %s: %w", t.filePath, err)
	}
	return b, nil
}

func (t tokenStore) save(data []byte) error {
	// When the keyring is usable, attempt keyring first. On success we
	// also remove any stale file copy so the on-disk cache doesn't drift.
	if !t.preferFile {
		err := keyring.Set(keyringService, t.account, string(data))
		if err == nil {
			if rmErr := os.Remove(t.filePath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				slog.Warn("keyring save ok but stale file not removed",
					slog.String("path", t.filePath),
					slog.String("err", rmErr.Error()))
			}
			return nil
		}
		// Log-and-fallthrough. Common on macOS when the cache grows past
		// the generic-password size limit; the file fallback is
		// specifically designed for this.
		slog.Debug("keyring save failed, falling back to file",
			slog.String("account", t.account),
			slog.String("err", err.Error()))
	}
	return writeFileAtomic(t.filePath, data, 0o600)
}

// writeFileAtomic writes data to path via a temp file in the same
// directory, then renames. Avoids leaving a truncated cache on crash
// mid-write and enforces mode 0600 on all POSIX platforms.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tokens-*")
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
		if err := os.Chmod(tmpPath, mode); err != nil {
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

func (t tokenStore) clear() error {
	var errs []error
	if err := keyring.Delete(keyringService, t.account); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) &&
		!errors.Is(err, keyring.ErrUnsupportedPlatform) {
		errs = append(errs, fmt.Errorf("keyring delete: %w", err))
	}
	if err := os.Remove(t.filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("file remove: %w", err))
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
