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

// SymbolKind classifies a declaration reported by an outline or symbol search.
type SymbolKind string

const (
	KindFunc   SymbolKind = "func"
	KindMethod SymbolKind = "method"
	KindType   SymbolKind = "type"
	KindConst  SymbolKind = "const"
	KindVar    SymbolKind = "var"
	KindField  SymbolKind = "field"
)

// Symbol is one declaration, either from a single-file outline or from a
// workspace-wide symbol search. Paths are absolute, matching Location.
type Symbol struct {
	Name string     `json:"name"`
	Kind SymbolKind `json:"kind"`
	// Receiver is the method receiver type, or the owning type for a field.
	Receiver string `json:"receiver,omitempty"`
	// Package is the declaring package's import path. Empty in file mode,
	// which does not type-check and therefore knows no import paths.
	Package string `json:"package,omitempty"`
	// Path is omitted when the enclosing result already names the file, which
	// a single-file outline does - repeating it on every symbol is pure cost
	// in the one mode where it carries no information.
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line"`
	EndLine  int    `json:"end_line"`
	Exported bool   `json:"exported"`
	// Signature is a single-line rendering of the declaration.
	Signature string `json:"signature"`
}

// SymbolResult is the outcome of an outline or symbol search.
type SymbolResult struct {
	Symbols   []Symbol `json:"symbols"`
	Complete  bool     `json:"complete"`
	Errors    int      `json:"errors,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Definition is the declaration site of a resolved symbol, with the source
// text read from disk at the reported span.
type Definition struct {
	Symbol    string     `json:"symbol"`
	Kind      SymbolKind `json:"kind"`
	Package   string     `json:"package,omitempty"`
	Receiver  string     `json:"receiver,omitempty"`
	Path      string     `json:"path"`
	Line      int        `json:"line"`
	EndLine   int        `json:"end_line"`
	Signature string     `json:"signature"`
	// Source is the declaration text, bounded to MaxDefinitionLines.
	Source string `json:"source,omitempty"`
	// SourceTruncated reports that the declaration is longer than the bound.
	SourceTruncated bool `json:"source_truncated,omitempty"`
}

// MaxDefinitionLines bounds the source text go_to_definition returns. A
// declaration longer than this is reported with its full span and a truncated
// body; read_file with an offset covers the rest.
const MaxDefinitionLines = 40

// ErrUnavailable reports that analysis could not run at all.
var ErrUnavailable = errors.New("analysis unavailable: workspace does not have a supported language toolchain")
