package cliworkflow

import "strings"

// tomlPathLiteral renders a filesystem path as a TOML basic-string literal.
// Windows paths contain backslashes, which TOML parses as escape sequences
// ("\U" in "C:\Users" is not a valid escape), so each backslash must be
// doubled to survive parsing as a literal backslash.
func tomlPathLiteral(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}
