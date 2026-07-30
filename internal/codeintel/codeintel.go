// Package codeintel resolves symbol references by type-checking a workspace.
//
// The types in this file define the model-facing result of a reference query.
// Host-language details (Go types, packages) are confined to analyzer.go
// and roles.go; this file is the shared vocabulary between the analyzer
// and the tool layer.
package codeintel

import "errors"

// Role classifies how a source location uses a symbol.
type Role string

const (
	RoleDefinition     Role = "definition"
	RoleImplementation Role = "implementation"
	RoleCaller         Role = "caller"
	RoleReturn         Role = "return"
	RoleComparison     Role = "comparison"
)

// String returns the role name.
func (r Role) String() string { return string(r) }

// Location is one classified reference to a symbol.
type Location struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
	Role   Role   `json:"role"`
}

// Result is the outcome of a reference query.
type Result struct {
	Symbol    string     `json:"symbol"`
	Locations []Location `json:"locations"`
	Complete  bool       `json:"complete"`
	Errors    int        `json:"errors,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// ErrUnavailable reports that analysis could not run at all.
var ErrUnavailable = errors.New("analysis unavailable: workspace does not have a supported language toolchain")
