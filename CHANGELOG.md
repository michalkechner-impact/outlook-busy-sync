# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
