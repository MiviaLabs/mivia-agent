// Package version reports build identity for the mivia CLI.
package version

// Version is the semantic version of the mivia binary.
// Overridden at link time in release builds via -ldflags.
var Version = "0.0.0-dev"

// Product is the human product name.
const Product = "mivia"

// Binary is the shipped CLI binary name.
const Binary = "mivia"
