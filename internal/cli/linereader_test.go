// Package cli implements mivia command handlers.
// lineReader tests removed: the old bufio-based lineReader was replaced
// by the raw-terminal InputBuffer for interactive use and bufio.Scanner
// fallback (replLineMode). The raw-terminal package is tested via the
// dialog and input.go unit tests.
package cli
