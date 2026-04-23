# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `init` command: scaffolds a starter YAML at the platform-default
  path with `tenant_id: common` pre-filled, so first-time users do
  not need to find UUIDs in Azure portal before they can log in.
  Idempotent: a second run refuses to overwrite an existing file.
- `logout <account>` command: clears the local token cache (keyring
  + file fallback) for one account. The `--help` text reminds users
  that server-side revocation still requires Entra ID admin action.
- `sync --dry-run` flag: preview every create/update/delete without
  making any Graph writes. Strongly recommended before the first
  scheduled run and after toggling `skip_all_day` / `skip_declined`.
- CI `vulncheck` job running `govulncheck` on every push.
- Full test coverage for the `auth` and `cli` packages (previously at
  zero); new test cases covering pagination with `SourceRef`
  preservation, empty sync windows, recurring-instance handling,
  timezone-aware shape equality, and partial-failure propagation.

### Changed
- `sync_pairs[].skip_all_day` / `skip_declined` at the `defaults:` level
  are now honoured when set explicitly to `false`. Previously the safe
  defaults were force-applied over any top-level setting, silently
  overriding user configuration.
- `sync` now exits non-zero when any individual event op fails, so
  launchd/systemd/Task Scheduler can alert on partial failures.
  Previously a pair with dozens of failed `CreateEvent` calls could
  still report success to the scheduler.
- Graph retry loop respects `ctx.Done()` during backoff and
  `Retry-After` waits. Scheduled tasks can now be stopped cleanly.
- Writes (`POST`/`PATCH`) are no longer retried on 5xx responses. Only
  explicit 429 throttling triggers a retry for non-idempotent methods,
  preventing duplicate busy blocks when the server committed the write
  before returning an error.
- `parseGraphTime` returns an error instead of `time.Time{}` on
  unparseable input, so malformed Graph responses can no longer produce
  year-0001 busy blocks that self-perpetuate.
- `ListEvents` uses `$top=25` when `$expand`ing extended properties, to
  avoid documented Graph quirks that can silently drop events from
  paginated responses.
- Token-cache file writes are atomic (temp + rename) and mode 0600 is
  enforced on every POSIX platform, not just darwin.
- Token-cache keyring / file selection uses a probe with cleanup, and
  `preferFile` is pinned for the process lifetime so `load()` and
  `save()` cannot disagree about where the cache lives.
- `tenant_id` and `client_id` are validated as UUIDs (or
  `common`/`organizations`/`consumers` literals for tenant), closing
  off a config-injection surface on the MSAL authority URL.
- `APIError.Body` is truncated to 512 bytes in error messages so logs
  don't splash tenant-identifying correlation IDs.
- `status` uses bounded per-account timeouts and distinguishes a
  network/keychain error from "not logged in".
- GitHub Actions pinned to commit SHAs across both workflows. Go
  toolchain pinned to `1.23.x`.
- YAML parser migrated from the unmaintained `gopkg.in/yaml.v3` to
  `go.yaml.in/yaml/v3`.
- `github.com/google/uuid` bumped to `v1.6.0`.

### Fixed
- `Defaults{}` omitted fields no longer leak to resolved pairs; the
  pointer-bool / pointer-int representation correctly distinguishes
  "unset" from "explicit zero value".
- Error messages from `sync_pairs` validation are deterministic
  (sorted known-account list).

## [0.1.0] - 2026-04-23

First public release.

### Added
- Privacy-preserving one-way and bidirectional calendar sync between
  Microsoft 365 tenants, using Graph `calendarView` + extended properties
  for stateless, loop-proof operation.
- Device-code OAuth via MSAL with the Azure CLI public client ID, so most
  users do not need their own Azure app registration.
- Token cache in the platform keyring (macOS Keychain, Windows Credential
  Manager, Linux Secret Service) with a `0600` file fallback.
- Commands: `auth`, `sync`, `status`, `config validate`, `config path`,
  `events` (diagnostic).
- Distribution: release archives for macOS (amd64, arm64), Linux (amd64,
  arm64), and Windows (amd64). Homebrew tap and Scoop bucket published
  from CI.
- Scheduler templates for launchd (macOS), systemd user timer (Linux),
  and Task Scheduler (Windows) under `examples/scheduler/`.
- CI matrix on Linux, macOS, and Windows runners with cross-compile
  smoke tests for every release target.

### Defaults
- `skip_all_day` defaults to `true`: vacations, OOO, and focus days are
  excluded unless a pair explicitly opts in. Prevents first-run leakage
  of private patterns.
- `skip_declined` defaults to `true`.

[Unreleased]: https://github.com/michalkechner-impact/outlook-busy-sync/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/michalkechner-impact/outlook-busy-sync/releases/tag/v0.1.0
