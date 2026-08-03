// Package version reports build identity for the mivia CLI.
package version

import "fmt"

// Version is the semantic version of the mivia binary.
// Overridden at link time in release builds via -ldflags.
var Version = "0.0.0-dev"

// Commit is the short Git commit (git rev-parse --short HEAD) this binary was
// built from. Overridden at link time via -ldflags (see Makefile's
// VERSION_LDFLAGS); "unknown" means no provenance was injected, e.g. a plain
// `go build` outside the Makefile.
var Commit = "unknown"

// Dirty reports whether the working tree had uncommitted changes when this
// binary was built. Overridden at link time via -ldflags; values are "clean"
// or "dirty" ("" when no provenance was injected).
var Dirty = ""

// Product is the human product name.
const Product = "mivia"

// Binary is the shipped CLI binary name.
const Binary = "mivia"

// String renders the --version line, e.g.
//
//	mivia 0.0.0-dev (commit abc1234, clean)
//
// It degrades gracefully to a bare "mivia <version>" when Commit was not
// injected at link time, so output from a plain `go build` stays stable and
// the CLI always compiles regardless of -ldflags.
func String() string {
	line := fmt.Sprintf("%s %s", Binary, Version)
	if Commit == "" || Commit == "unknown" {
		return line
	}
	line += " (commit " + Commit
	if Dirty != "" {
		line += ", " + Dirty
	}
	line += ")"
	return line
}
