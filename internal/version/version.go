// Package version holds build-time metadata. Value is set by ldflags during
// release builds; falls back to "dev" in local builds.
package version

// Version is the released tool version, injected at build time via
// -ldflags. "dev" indicates an un-stamped local build.
var Version = "dev"
