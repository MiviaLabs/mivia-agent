package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// definitionResolver is the analyzer capability this tool needs.
type definitionResolver interface {
	Definition(ctx context.Context, symbol string) (codeintel.Definition, error)
}

type goToDefinitionTool struct {
	ws       *workspace.Root
	resolver definitionResolver
	maxBytes int
	// secretPathPatterns/secretPathExceptions are the same operator policy
	// every content-returning tool applies. This tool reads declaration text
	// live from disk, so without them it answered questions about files
	// read_file refuses to open.
	secretPathExceptions []string
	secretPathPatterns   []string
}

type goToDefinitionArgs struct {
	Symbol string `json:"symbol"`
}

// goToDefinitionResult is the model-facing shape. Source is the declaration
// text read from disk at the reported span, never a retained parse of it, so
// what the model sees is what the file says right now.
type goToDefinitionResult struct {
	Symbol          string `json:"symbol"`
	Kind            string `json:"kind,omitempty"`
	Package         string `json:"package,omitempty"`
	Receiver        string `json:"receiver,omitempty"`
	Path            string `json:"path,omitempty"`
	Line            int    `json:"line,omitempty"`
	EndLine         int    `json:"end_line,omitempty"`
	Signature       string `json:"signature,omitempty"`
	Source          string `json:"source,omitempty"`
	SourceTruncated bool   `json:"source_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (t *goToDefinitionTool) Name() string { return "go_to_definition" }

func (t *goToDefinitionTool) Description() string {
	return "Jump to where a named symbol is declared. " +
		"Params: symbol (required, e.g. 'FuncName', 'pkgname.FuncName', 'ClassName.methodName', or 'full/import/path.FuncName'). " +
		"Returns the declaration's file, line span, one-line signature and source text (bounded to 40 lines). " +
		"Prefer this over reading a whole file to find one declaration. " +
		"Returns analysis unavailable when the workspace language has no analyzer backend."
}

func (t *goToDefinitionTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"symbol": map[string]any{
			"type":        "string",
			"description": "Symbol to locate (e.g. 'FuncName', 'pkgname.FuncName', 'ClassName.methodName', or 'full/import/path.FuncName')",
		},
	}, []string{"symbol"})
}

func (t *goToDefinitionTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, MaxResultBytes: t.maxBytes}
}

// ResultBudgetBytes declares the self-truncation budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *goToDefinitionTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *goToDefinitionTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in goToDefinitionArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	in.Symbol = strings.TrimSpace(in.Symbol)
	if in.Symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	if t.resolver == nil {
		return t.marshal(goToDefinitionResult{Symbol: in.Symbol, Error: "go_to_definition: no analyzer available"}), nil
	}

	def, err := t.resolver.Definition(ctx, in.Symbol)
	if err == nil && isSecretPath(def.Path, t.secretPathExceptions, t.secretPathPatterns) {
		// Refuse as an ANSWER, not a call failure, matching this tool's
		// not-found/ambiguous handling - and never echo the path back.
		return t.marshal(goToDefinitionResult{Symbol: in.Symbol, Error: "reading secret-like path is blocked"}), nil
	}
	if err != nil {
		// Unavailable / not found / ambiguous are all answers the model needs
		// to read, not call failures.
		return t.marshal(goToDefinitionResult{Symbol: in.Symbol, Error: err.Error()}), nil
	}

	return t.marshal(goToDefinitionResult{
		Symbol:          def.Symbol,
		Kind:            string(def.Kind),
		Package:         def.Package,
		Receiver:        def.Receiver,
		Path:            relativizePath(t.ws, def.Path),
		Line:            def.Line,
		EndLine:         def.EndLine,
		Signature:       def.Signature,
		Source:          def.Source,
		SourceTruncated: def.SourceTruncated,
	}), nil
}

// marshal enforces the declared budget. The variable-length part here is the
// source text, so an over-budget result drops source LINES from the end -
// keeping the span, signature and path, which are what the model navigates by.
func (t *goToDefinitionTool) marshal(r goToDefinitionResult) string {
	lines := []string(nil)
	if r.Source != "" {
		lines = strings.Split(r.Source, "\n")
	}
	return budgetedJSON(t.maxBytes, len(lines), func(keep int, truncated bool) any {
		out := r
		out.Source = strings.Join(lines[:keep], "\n")
		if truncated {
			out.SourceTruncated = true
		}
		return out
	}, `{"symbol":"","source_truncated":true}`)
}

// relativizePath rewrites an absolute workspace path to the relative form the
// file tools speak. Paths outside the workspace (or with no workspace at all)
// are returned unchanged rather than turned into a ".." walk.
func relativizePath(ws *workspace.Root, abs string) string {
	if ws == nil || ws.Abs == "" || abs == "" || !filepath.IsAbs(abs) {
		return abs
	}
	rel := ws.Rel(abs)
	if rel == "" || strings.HasPrefix(rel, "..") {
		return abs
	}
	return rel
}
