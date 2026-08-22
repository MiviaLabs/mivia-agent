package cli

import "strings"

// tomlPathLiteral is duplicated from internal/clichat for the workflow
// fixture tests that stayed in this package.
func tomlPathLiteral(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}
