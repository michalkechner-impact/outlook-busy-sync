# Contributing

Thanks for considering a contribution. This project is intentionally small
and focused on one problem (mirroring M365 busy blocks), so the scope bar
for new features is high. Bug fixes, platform compatibility patches, and
docs improvements are always welcome.

## Development setup

```
git clone https://github.com/michalkechner-impact/outlook-busy-sync
cd outlook-busy-sync
make test
make build
```

Requires Go 1.22+. `golangci-lint` is used in CI; install it locally with
`brew install golangci-lint` for identical checks.

## Project layout

```
cmd/outlook-busy-sync/     entry point (main)
internal/auth/             MSAL device-code flow + keychain persistence
internal/cli/              cobra command tree
internal/config/           YAML config loading and validation
internal/graph/            Microsoft Graph client (calendar ops only)
internal/sync/             diff + apply engine
internal/version/          build-time version string
examples/
  config.yaml              annotated example config
  scheduler/
    macos/                 launchd LaunchAgent template
    linux/                 systemd user service + timer
    windows/               Task Scheduler registration script
```

The `sync` package is the interesting code. `graph.Client` talks to
Microsoft Graph; `sync.Engine` is stateless and exercises it via an
interface, which makes it straightforward to unit-test without network
access (see `engine_test.go`).

## Principles

- Keep the binary small. Resist adding features that duplicate what
  existing tools (Reclaim, OneCal, inovex/CalendarSync) already do well
  for users willing to pay or register their own Azure app.
- No cloud state. Everything lives in Graph (via the extended property) or
  in the user's own keychain.
- No secret capture. Titles, bodies, locations, and attendees must never
  be written to a target calendar.
- No breaking changes to the sync extended-property GUID or name. Older
  events written by previous versions would become orphaned.

## Opening a pull request

1. Open an issue first for anything non-trivial.
2. Include a test for the change when behaviour changes.
3. `make test && make lint` should pass locally.
4. Use conventional commit messages (`feat:`, `fix:`, `docs:`, `refactor:`,
   `test:`). This helps goreleaser generate clean release notes.

## Cutting a release

Releases are driven entirely by git tags. The `release` workflow runs
goreleaser on a macOS runner and produces archives for darwin/linux/windows
along with the Homebrew formula and Scoop manifest.

1. Make sure `main` is green (`ci` workflow) and the CHANGELOG has an entry
   for the new version under `## [X.Y.Z]`.
2. Tag and push:
   ```sh
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```
3. Watch the `release` workflow. On success, the GitHub Release page is
   populated and the tap/bucket repos receive an updated formula/manifest.

Required secrets on the repository:

- `HOMEBREW_TAP_TOKEN`  PAT with `repo` scope on `michalkechner-impact/homebrew-tap`.
  Absence is non-fatal: the Release still produces, but the brew formula
  is not updated.
- `SCOOP_BUCKET_TOKEN`  PAT with `repo` scope on
  `michalkechner-impact/scoop-bucket`. Same fallback behaviour.

If either tap/bucket repo does not yet exist, create it as an empty public
repository before cutting the first release that needs it.
