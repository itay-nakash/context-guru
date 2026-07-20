// Package buildinfo exposes version metadata stamped at build time via -ldflags.
package buildinfo

// Version is the released version, overridden at build time with
// -ldflags "-X github.com/rossoctl/lab-context-engineering/internal/buildinfo.Version=...".
var Version = "dev"

// Commit is the git commit the binary was built from.
var Commit = "none"
