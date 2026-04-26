# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Mirror mode (`mode: mirror`, opt-in per sync pair). Copies subject,
  location, organiser, attendees-as-text body, and source body into the
  target event with `sensitivity: private` so colleagues with
  shared-calendar access still see only "Private appointment" in standard
  Outlook views. Designed for users who own both tenants and want a single
  unified calendar in their primary tenant; explicit warning is logged at
  every sync run for any mirror pair.

  Privacy guarantees that hold even in mirror mode:
  - Default mode remains `busy`; mirror requires explicit per-pair opt-in.
  - `mode` is per direction, so a bidirectional configuration can be
    asymmetric (typical: `client -> employer` mirrored, `employer -> client`
    busy-only).
  - Attendees are written only as plain text inside body; structured
    attendees are deliberately not populated, preventing the second tenant
    from sending duplicate meeting invitations.
  - Microsoft Teams meeting `joinUrl`s are stripped from copied bodies.
- Mirror update detection uses a SHA-256 hash of the canonical source
  payload stored as a second extended property (`MirrorBodyHash`),
  side-stepping Outlook's silent body-HTML rewrites that would otherwise
  loop the engine into endless updates.

## [0.2.1] - 2026-04-23

### Fixed
- Config and token-cache paths on Windows now live under
  `%APPDATA%\outlook-busy-sync\` instead of the XDG-style
  `~/.config/outlook-busy-sync/` that accidentally leaked over from
  POSIX. The README already documented `%APPDATA%` as the Windows
  path, but v0.1.0 / v0.2.0 code didn't honour it. macOS and Linux
  paths are unchanged (backward compatible).

## [0.2.0] - 2026-04-23

Onboarding + distribution polish. No breaking changes.

### Added
- `init` command: scaffolds a starter YAML at the platform-default
  path with `tenant_id: common` pre-filled, so first-time users do
  not need to find tenant UUIDs in Azure portal before they can log
  in. MSAL resolves the real tenant during device-code auth.
  Idempotent: a second run refuses to overwrite an existing file.
- `logout <account>` command: clears the local token cache (keyring
  + file fallback) for one account. The `--help` text is explicit
  that server-side refresh-token revocation still needs tenant admin
  action via Entra ID.
- README "TL;DR" section at the top with six literal commands to go
  from zero to syncing, plus an anonymised calendar screenshot under
  `docs/images/`.
- Dependabot config (`.github/dependabot.yml`) keeping Go modules and
  GitHub Actions current, weekly cadence.
- CodeQL workflow with `security-extended + security-and-quality`
  query suites, pinned by commit SHA.
- Auto-merge workflow for Dependabot PRs: actions bumps merge on any
  update (they're SHA-pinned, regressions fail CI visibly); Go module
  bumps merge on patch/minor only; major Go bumps get a
  `needs-manual-review` label.

### Changed
- All GitHub Actions bumped to latest via the new Dependabot +
  auto-merge pipeline: `actions/checkout@6`, `actions/setup-go@6`,
  `actions/upload-artifact@7`, `goreleaser/goreleaser-action@7`,
  `github/codeql-action@v3.29.8`.

### Fixed
- `TestInit_scaffoldsConfigAndIsIdempotent` skips the 0600 permission
  assertion on Windows, where POSIX permission bits don't apply and
  ACL inheritance from the config directory is the Windows-side
  protection.

## [0.1.0] - 2026-04-23

First public release.

### Added
- Privacy-preserving one-way and bidirectional calendar sync between
  Microsoft 365 tenants, using Graph `calendarView` + extended properties
  for stateless, loop-proof operation.
- Device-code OAuth via MSAL with the Azure CLI public client ID, so most
  users do not need their own Azure app registration.
- Token cache in the platform keyring (macOS Keychain, Windows Credential
  Manager, Linux Secret Service) with a `0600` file fallback. File writes
  are atomic (temp + rename); mode 0600 is enforced on every POSIX
  platform.
- Commands: `auth`, `sync`, `status`, `config validate`, `config path`,
  `events` (diagnostic).
- `sync --dry-run` flag: preview every create/update/delete without
  making any Graph writes. Recommended before the first scheduled run
  and after toggling `skip_all_day` / `skip_declined`.
- Distribution: release archives for macOS (amd64, arm64), Linux (amd64,
  arm64), and Windows (amd64). Homebrew tap and Scoop bucket published
  from CI.
- Scheduler templates for launchd (macOS), systemd user timer (Linux),
  and Task Scheduler (Windows) under `examples/scheduler/`.
- CI matrix on Linux, macOS, and Windows with cross-compile smoke tests
  for every release target, plus a `vulncheck` job running `govulncheck`
  on every push.
- Test coverage for `config`, `graph`, `sync`, `auth`, and `cli`
  packages, covering the failure paths reviewers identified as
  load-bearing: pagination with `SourceRef` preservation, empty sync
  windows, recurring-instance handling, timezone-aware shape equality,
  and partial-failure propagation.

### Safe defaults
- `skip_all_day` defaults to `true`: vacations, OOO, and focus days
  are excluded unless a pair explicitly opts in. Prevents first-run
  leakage of private patterns to the other tenant.
- `skip_declined` defaults to `true`.

### Security and reliability
- `tenant_id` and `client_id` are validated as UUIDs (or
  `common` / `organizations` / `consumers` literals for tenant),
  closing off a config-injection surface on the MSAL authority URL.
- `sync` exits non-zero when any individual event op fails, so
  launchd / systemd / Task Scheduler can alert on partial failures.
- Graph retry loop respects `ctx.Done()` during backoff and
  `Retry-After` waits; scheduled tasks stop cleanly.
- Writes (`POST` / `PATCH`) are not retried on 5xx responses; only
  explicit 429 throttling triggers a retry for non-idempotent
  methods, preventing duplicate busy blocks when the server committed
  the write before returning an error.
- `ListEvents` uses `$top=25` when `$expand`ing extended properties,
  avoiding documented Graph quirks that can silently drop events
  from paginated responses.
- `parseGraphTime` returns an error instead of `time.Time{}` on
  unparseable input, so malformed Graph responses cannot produce
  year-0001 busy blocks that self-perpetuate via `equalShape`.
- `APIError.Body` is truncated to 512 bytes in error messages so
  logs don't splash tenant-identifying correlation IDs.
- Token-cache keyring / file selection uses a probe with cleanup;
  `preferFile` is pinned for the process lifetime so `load()` and
  `save()` cannot disagree about where the cache lives.
- `status` uses bounded per-account timeouts and distinguishes a
  network / keychain error from "not logged in".
- GitHub Actions pinned to commit SHAs. Go toolchain pinned to
  `1.25.x`.
- YAML parser is `go.yaml.in/yaml/v3` (maintained fork of the
  archived `gopkg.in/yaml.v3`).

[Unreleased]: https://github.com/michalkechner-impact/outlook-busy-sync/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/michalkechner-impact/outlook-busy-sync/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/michalkechner-impact/outlook-busy-sync/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/michalkechner-impact/outlook-busy-sync/releases/tag/v0.1.0
