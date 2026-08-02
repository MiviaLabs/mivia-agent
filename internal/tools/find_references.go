package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
)

// referenceFinder is the analyzer capability this tool needs. Declared here so
// tests can substitute a fake without an interface in the analyzer package.
type referenceFinder interface {
	References(ctx context.Context, symbol string, roles []codeintel.Role, limit int) (codeintel.Result, error)
}

type findReferencesTool struct {
	finder   referenceFinder
	maxBytes int
	limit    int
}

type findReferencesArgs struct {
	Symbol string   `json:"symbol"`
	Roles  []string `json:"roles,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

// findReferencesResult is the tool's model-facing JSON shape. It mirrors
// codeintel.Result plus an optional Error string, so every response path
// (nil analyzer, analyzer error, or success) produces the same shape through
// the same budget-enforcing marshal path instead of two independently
// maintained JSON forms.
type findReferencesResult struct {
	Symbol    string               `json:"symbol"`
	Locations []codeintel.Location `json:"locations"`
	Complete  bool                 `json:"complete"`
	Errors    int                  `json:"errors,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
	Error     string               `json:"error,omitempty"`
}

func (t *findReferencesTool) Name() string { return "find_references" }

func (t *findReferencesTool) Description() string {
	return "Resolve references to a named symbol across the codebase. " +
		"Returns classified locations (definition, implementation, caller, return, comparison). " +
		"Params: symbol (required, e.g. 'ClassName.methodName', 'pkgname.FuncName', or 'full/import/path.FuncName'); " +
		"roles (optional, filter by role type); limit (optional, max results, default 50). " +
		"Returns analysis unavailable when the workspace language has no analyzer backend."
}

func (t *findReferencesTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"symbol": map[string]any{"type": "string", "description": "Symbol name to resolve (e.g. 'ClassName.methodName', 'pkgname.FuncName', or 'full/import/path.FuncName')"},
		"roles": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Optional list of role types to filter: definition, implementation, caller, return, comparison",
		},
		"limit": map[string]any{"type": "integer", "description": "Maximum number of results (default 50)"},
	}, []string{"symbol"})
}

func (t *findReferencesTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, MaxResultBytes: t.maxBytes}
}

// ResultBudgetBytes declares the self-truncation budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *findReferencesTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *findReferencesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in findReferencesArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}

	if t.finder == nil {
		return t.marshalBudgeted(findReferencesResult{
			Symbol: in.Symbol,
			Error:  "find_references: no analyzer available",
		}), nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = t.limit
	}
	// When both the user-supplied value and the tool default are zero
	// (direct construction without registration), honor the uncapped intent.
	// The registry always registers with limit=50, so this path is only
	// reachable from test helpers that bypass the registry.
	if limit <= 0 {
		limit = 0
	}

	// Convert string roles to codeintel.Role values.
	var roles []codeintel.Role
	for _, r := range in.Roles {
		switch r {
		case "definition", "implementation", "caller", "return", "comparison":
			roles = append(roles, codeintel.Role(r))
		default:
			return "", fmt.Errorf("find_references: unknown role %q; valid roles: definition, implementation, caller, return, comparison", r)
		}
	}

	result, err := t.finder.References(ctx, in.Symbol, roles, limit)
	if err != nil {
		// Return availability errors as tool output so the model sees them.
		return t.marshalBudgeted(findReferencesResult{
			Symbol: in.Symbol,
			Error:  err.Error(),
		}), nil
	}

	return t.marshalBudgeted(findReferencesResult{
		Symbol:    result.Symbol,
		Locations: result.Locations,
		Complete:  result.Complete,
		Errors:    result.Errors,
		Truncated: result.Truncated,
	}), nil
}

// marshalBudgeted marshals r and guarantees the returned string never exceeds
// t.maxBytes (when set). When the full result is over budget, it binary
// searches the largest prefix of Locations whose marshaled size still fits -
// marshaled size is monotonically non-decreasing in the number of Locations
// kept, so this is O(log n) marshals of the whole result rather than
// dropping one location at a time and re-marshaling the whole remaining
// slice on every drop (O(n) marshals of an O(n) slice - O(n^2) total, which
// measured in the tens of seconds for a 10,000-location result). If the
// budget still can't be met with zero Locations (an oversized Symbol or
// Error string, both of which echo caller-supplied or workspace-derived text
// verbatim), it falls back to a minimal, always-bounded payload instead of
// returning oversized data. This is the one place every response path (nil
// analyzer, analyzer error, success) converges through, so the budget
// contract in Capability.MaxResultBytes cannot be bypassed by any of them.
func (t *findReferencesTool) marshalBudgeted(r findReferencesResult) string {
	data, err := json.Marshal(r)
	if err != nil {
		return `{"symbol":"","locations":[],"complete":false,"error":"find_references: marshal failed"}`
	}
	if t.maxBytes <= 0 || len(data) <= t.maxBytes {
		return string(data)
	}

	r.Truncated = true
	full := r.Locations

	lo, hi, best, bestData := 0, len(full), -1, ([]byte)(nil)
	for lo <= hi {
		mid := (lo + hi) / 2
		r.Locations = full[:mid]
		d, merr := json.Marshal(r)
		if merr != nil {
			return `{"symbol":"","locations":[],"complete":false,"error":"find_references: marshal failed"}`
		}
		if len(d) <= t.maxBytes {
			best, bestData = mid, d
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best >= 0 {
		return string(bestData)
	}

	// Not even zero Locations fit: the Symbol and/or Error text itself is
	// oversized. Bound both explicitly rather than returning data larger
	// than the declared budget.
	minimal := findReferencesResult{
		Symbol:    truncateToBytes(r.Symbol, t.maxBytes/4),
		Complete:  r.Complete,
		Errors:    r.Errors,
		Truncated: true,
		Error:     truncateToBytes(r.Error, t.maxBytes/4),
	}
	data, err = json.Marshal(minimal)
	if err == nil && len(data) <= t.maxBytes {
		return string(data)
	}
	// t.maxBytes itself is smaller than the smallest valid response - return
	// the smallest valid payload we can produce rather than fail the call.
	return `{"symbol":"","locations":[],"complete":false,"truncated":true}`
}

// truncateToBytes returns s cut to at most max bytes, trimming back to the
// nearest valid UTF-8 boundary so the result never contains a partial
// multi-byte rune.
func truncateToBytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	b := []byte(s)[:max]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
