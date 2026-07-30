package tools

import (
	"context"
	"encoding/json"
	"fmt"

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

func (t *findReferencesTool) Name() string { return "find_references" }

func (t *findReferencesTool) Description() string {
	return "Resolve references to a named symbol across the codebase. " +
		"Returns classified locations (definition, implementation, caller, return, comparison). " +
		"Params: symbol (required, e.g. 'ClassName.methodName' or 'FunctionName'); " +
		"roles (optional, filter by role type); limit (optional, max results, default 50). " +
		"Returns analysis unavailable when the workspace language has no analyzer backend."
}

func (t *findReferencesTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"symbol": map[string]any{"type": "string", "description": "Symbol name to resolve (e.g. 'ClassName.methodName' or 'FunctionName')"},
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

func (t *findReferencesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.finder == nil {
		return "", fmt.Errorf("find_references: no analyzer available; analysis unavailable")
	}
	var in findReferencesArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = t.limit
	}
	if limit <= 0 {
		limit = 50
	}

	// Convert string roles to codeintel.Role values.
	var roles []codeintel.Role
	for _, r := range in.Roles {
		roles = append(roles, codeintel.Role(r))
	}

	result, err := t.finder.References(ctx, in.Symbol, roles, limit)
	if err != nil {
		// Return availability errors as tool output so the model sees them.
		out := fmt.Sprintf(`{"symbol":%q,"locations":[],"complete":false,"error":%q}`, in.Symbol, err.Error())
		return out, nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(data), nil
}
