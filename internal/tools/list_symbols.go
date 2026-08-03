package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// symbolSearcher is the analyzer capability the workspace mode needs.
// Declared here so tests can substitute a fake without an interface in the
// analyzer package.
type symbolSearcher interface {
	Symbols(ctx context.Context, prefix string, limit int) (codeintel.SymbolResult, error)
}

// fileOutliner is the single-file capability, injected the same way. The
// production value is codeintel.FileOutline, which parses one file and needs
// no workspace analysis at all.
type fileOutliner func(path string) (codeintel.SymbolResult, error)

type listSymbolsTool struct {
	ws                   *workspace.Root
	searcher             symbolSearcher
	outline              fileOutliner
	maxBytes             int
	limit                int
	secretPathExceptions []string
	secretPathPatterns   []string
}

type listSymbolsArgs struct {
	Path         string `json:"path,omitempty"`
	SymbolPrefix string `json:"symbol_prefix,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

// listSymbolsResult is the tool's model-facing JSON shape. Every response path
// (file outline, workspace search, analysis unavailable) converges on it, so
// the byte budget cannot be bypassed by one of them.
type listSymbolsResult struct {
	Path         string             `json:"path,omitempty"`
	SymbolPrefix string             `json:"symbol_prefix,omitempty"`
	Symbols      []codeintel.Symbol `json:"symbols"`
	Complete     bool               `json:"complete"`
	Errors       int                `json:"errors,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
	Error        string             `json:"error,omitempty"`
}

func (t *listSymbolsTool) Name() string { return "list_symbols" }

func (t *listSymbolsTool) Description() string {
	return "Outline the declarations in one file, or search declarations across the codebase by name prefix. " +
		"Params: path (outline one file: every declaration in source order) OR symbol_prefix (search the whole codebase); " +
		"exactly one of the two; limit (optional, max results, default 50 for prefix search). " +
		"Each result carries name, kind, receiver, line span, exported flag and a one-line signature. " +
		"Prefer this over reading a whole file to find out what is in it. " +
		"Returns analysis unavailable when the workspace language has no analyzer backend."
}

func (t *listSymbolsTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Relative path of a single file to outline. Mutually exclusive with symbol_prefix.",
		},
		"symbol_prefix": map[string]any{
			"type":        "string",
			"description": "Name prefix to search for across the codebase (case-insensitive). Mutually exclusive with path.",
		},
		"limit": map[string]any{
			"type":        "integer",
			"description": "Maximum number of results (default 50 for prefix search; a file outline returns everything it can fit)",
		},
	}, []string{})
}

func (t *listSymbolsTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, MaxResultBytes: t.maxBytes}
}

// ResultBudgetBytes declares the self-truncation budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *listSymbolsTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *listSymbolsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in listSymbolsArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	in.Path = strings.TrimSpace(in.Path)
	in.SymbolPrefix = strings.TrimSpace(in.SymbolPrefix)

	switch {
	case in.Path == "" && in.SymbolPrefix == "":
		return "", fmt.Errorf("list_symbols: one of path or symbol_prefix is required")
	case in.Path != "" && in.SymbolPrefix != "":
		return "", fmt.Errorf("list_symbols: path and symbol_prefix are mutually exclusive; pass one")
	case in.Path != "":
		return t.outlineFile(in)
	default:
		return t.searchWorkspace(ctx, in)
	}
}

// outlineFile answers the single-file mode. It never consults the workspace
// analyzer, so it works while the snapshot is cold and in projects that do not
// type-check at all.
func (t *listSymbolsTool) outlineFile(in listSymbolsArgs) (string, error) {
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs), t.secretPathExceptions, t.secretPathPatterns) {
		return "", fmt.Errorf("reading secret-like path is blocked")
	}
	if _, err := requireRegularFile(abs); err != nil {
		return "", err
	}
	if t.outline == nil {
		return t.marshal(listSymbolsResult{Path: in.Path, Error: "list_symbols: no outline backend available"}), nil
	}

	res, err := t.outline(abs)
	if err != nil {
		return t.marshal(listSymbolsResult{Path: in.Path, Error: err.Error()}), nil
	}
	// The envelope already carries the path the caller asked for; repeating it
	// on every symbol is bytes the model pays for and learns nothing from.
	syms := stripPaths(res.Symbols)
	truncated := res.Truncated
	if in.Limit > 0 && len(syms) > in.Limit {
		syms, truncated = syms[:in.Limit], true
	}
	return t.marshal(listSymbolsResult{
		Path:      in.Path,
		Symbols:   syms,
		Complete:  res.Complete,
		Errors:    res.Errors,
		Truncated: truncated,
	}), nil
}

// searchWorkspace answers the prefix mode through the shared cached analyzer.
func (t *listSymbolsTool) searchWorkspace(ctx context.Context, in listSymbolsArgs) (string, error) {
	if t.searcher == nil {
		return t.marshal(listSymbolsResult{SymbolPrefix: in.SymbolPrefix, Error: "list_symbols: no analyzer available"}), nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = t.limit
	}
	res, err := t.searcher.Symbols(ctx, in.SymbolPrefix, limit)
	if err != nil {
		// Availability errors are tool OUTPUT, not tool failures: the model
		// must see "this workspace has no analyzer" rather than a call error.
		return t.marshal(listSymbolsResult{SymbolPrefix: in.SymbolPrefix, Error: err.Error()}), nil
	}
	return t.marshal(listSymbolsResult{
		SymbolPrefix: in.SymbolPrefix,
		Symbols:      t.relativize(res.Symbols),
		Complete:     res.Complete,
		Errors:       res.Errors,
		Truncated:    res.Truncated,
	}), nil
}

// stripPaths clears the per-symbol path for single-file results.
func stripPaths(syms []codeintel.Symbol) []codeintel.Symbol {
	out := make([]codeintel.Symbol, len(syms))
	copy(out, syms)
	for i := range out {
		out[i].Path = ""
	}
	return out
}

// relativize rewrites absolute analyzer paths to workspace-relative ones, the
// form every other file tool speaks and the form read_file takes back.
func (t *listSymbolsTool) relativize(syms []codeintel.Symbol) []codeintel.Symbol {
	if t.ws == nil {
		return syms
	}
	out := make([]codeintel.Symbol, len(syms))
	copy(out, syms)
	for i := range out {
		out[i].Path = relativizePath(t.ws, out[i].Path)
	}
	return out
}

// marshal enforces the declared budget on every response path.
func (t *listSymbolsTool) marshal(r listSymbolsResult) string {
	// A nil slice marshals to null; an empty result must read as an empty
	// LIST, not as a missing field the model has to guess about.
	full := r.Symbols
	if full == nil {
		full = []codeintel.Symbol{}
	}
	return budgetedJSON(t.maxBytes, len(full), func(keep int, truncated bool) any {
		out := r
		out.Symbols = full[:keep]
		if truncated {
			out.Truncated = true
		}
		return out
	}, `{"symbols":[],"complete":false,"truncated":true}`)
}
