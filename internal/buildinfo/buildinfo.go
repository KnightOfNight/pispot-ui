// Package buildinfo exposes identifying information about the running
// binary — short git commit, dirty-tree flag, and build timestamp — so
// a deployed instance can be matched back to the exact source revision
// that produced it.
//
// The string variables below are populated at link time via -ldflags -X.
// Their default values apply when the binary is built outside the
// project's Makefile (e.g. `go build` from within a shallow clone or
// a tarball extraction). See the Makefile's LDFLAGS definition and the
// Dockerfile build stage for the production wiring.
package buildinfo

var (
	// Commit is the short git SHA of the source tree at build time, or
	// "unknown" when built outside the project Makefile.
	Commit = "unknown"

	// Dirty is the string "true" if the working tree had uncommitted
	// or untracked changes at build time, "false" otherwise. Callers
	// should use IsDirty() for a bool view.
	Dirty = "false"

	// BuildTime is the RFC3339 UTC timestamp at which the binary was
	// built, or "unknown" when built outside the project Makefile.
	BuildTime = "unknown"
)

// IsDirty returns true when the binary was built from a working tree
// with uncommitted or untracked changes.
func IsDirty() bool {
	return Dirty == "true"
}
