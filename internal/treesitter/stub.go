//go:build !cg_skeleton

// This file is the pure-Go face of the treesitter package: when the cg_skeleton
// tag is absent, the real cgo implementation (treesitter.go) is excluded and this
// empty package takes its place, so `go build ./...` under CGO_ENABLED=0 links no
// tree-sitter grammars. The only consumer (the skeleton component) is gated by the
// same tag, so nothing references the omitted symbols in this build.
package treesitter
