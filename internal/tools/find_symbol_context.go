package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// findSymbolContextVersion is the output envelope's schema version. Bump it
// only for a breaking field-shape change.
const findSymbolContextVersion = 1

// Bounds and defaults for max_references and context_lines, matching
// inspect_repository's convention (Design, plan 66 follow-on #1): defaults
// apply when the field is absent from the request, and 0 is not itself a
// meaningful "explicit" max_references (a caller asking for zero references
// would get nothing useful), so an int with a <=0-means-default convention
// is enough here - unlike context_lines, where 0 is a legitimate explicit
// choice and is handled with a pointer field below.
const (
	findSymbolContextDefaultMaxReferences = 20
	findSymbolContextMinMaxReferences     = 1
	findSymbolContextMaxMaxReferences     = 100
	findSymbolContextDefaultContextLines  = 8
	findSymbolContextMinContextLines      = 0
	findSymbolContextMaxContextLines      = 10
)

// symbolContextResolver is the analyzer capability this tool needs: the
// definition and reference lookups the three existing nav tools already call
// against the one shared *codeintel.Analyzer. A superset of the existing
// definitionResolver and referenceFinder interfaces (Design, plan 66
// follow-on #1) rather than a new interface - *codeintel.Analyzer already
// satisfies it.
type symbolContextResolver interface {
	definitionResolver
	referenceFinder
}

type findSymbolContextTool struct {
	ws       *workspace.Root
	resolver symbolContextResolver
	maxBytes int
}

func (t *findSymbolContextTool) Name() string { return "find_symbol_context" }

func (t *findSymbolContextTool) Description() string {
	return "Resolve a named symbol's declaration and callers in one bounded call: its definition (file, line span, signature, source) plus classified reference locations. " +
		"Params: symbol (required, e.g. 'FuncName', 'pkgname.FuncName', 'ClassName.methodName', or 'full/import/path.FuncName'); " +
		"max_references (optional, 1-100, default 20); context_lines (optional, 0-10, default 8, bounds the declaration source kept). " +
		"Prefer this over separate list_symbols, go_to_definition, and find_references calls when you need a symbol's full evidence in one turn. " +
		"Returns analysis unavailable when the workspace language has no analyzer backend."
}

func (t *findSymbolContextTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"symbol": map[string]any{
			"type":        "string",
			"description": "Symbol to resolve (e.g. 'FuncName', 'pkgname.FuncName', 'ClassName.methodName', or 'full/import/path.FuncName')",
		},
		"max_references": map[string]any{
			"type":        "integer",
			"description": "Maximum number of reference locations to return (1-100, default 20)",
			"minimum":     float64(findSymbolContextMinMaxReferences),
			"maximum":     float64(findSymbolContextMaxMaxReferences),
		},
		"context_lines": map[string]any{
			"type":        "integer",
			"description": "Lines of declaration source to keep, from the definition's start (0-10, default 8)",
			"minimum":     float64(findSymbolContextMinContextLines),
			"maximum":     float64(findSymbolContextMaxContextLines),
		},
	}, []string{"symbol"})
}

// ResultBudgetBytes declares the self-truncation budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *findSymbolContextTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *findSymbolContextTool) Capability(args json.RawMessage) Capability {
	var in findSymbolContextArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return Capability{Class: ExecutionRead, MaxResultBytes: t.maxBytes}
	}
	symbol, maxRefs, contextLines := resolveFindSymbolContextBounds(in)
	return Capability{
		Class:          ExecutionRead,
		ResourceKey:    findSymbolContextResourceKey(symbol, maxRefs, contextLines),
		MaxResultBytes: t.maxBytes,
	}
}

type findSymbolContextArgs struct {
	Symbol        string `json:"symbol"`
	MaxReferences int    `json:"max_references,omitempty"`
	// ContextLines is a pointer so an explicit 0 (no source lines) can be
	// told apart from an absent field (fall back to the default of 8) - the
	// only field here whose default is not its zero value.
	ContextLines *int `json:"context_lines,omitempty"`
}

// resolveFindSymbolContextBounds applies defaults to an already-decoded
// args value, without validating bounds. Used by both Capability (best
// effort, never errors) and Execute (paired with validation).
func resolveFindSymbolContextBounds(in findSymbolContextArgs) (symbol string, maxReferences, contextLines int) {
	symbol = strings.TrimSpace(in.Symbol)
	maxReferences = in.MaxReferences
	if maxReferences <= 0 {
		maxReferences = findSymbolContextDefaultMaxReferences
	}
	contextLines = findSymbolContextDefaultContextLines
	if in.ContextLines != nil {
		contextLines = *in.ContextLines
	}
	return symbol, maxReferences, contextLines
}

func findSymbolContextResourceKey(symbol string, maxReferences, contextLines int) string {
	h := sha256.New()
	h.Write([]byte(symbol))
	h.Write([]byte{0x1e})
	h.Write([]byte(strconv.Itoa(maxReferences)))
	h.Write([]byte{0x1e})
	h.Write([]byte(strconv.Itoa(contextLines)))
	return "symctx:" + hex.EncodeToString(h.Sum(nil))
}

// findSymbolContextDefinition is the declaration half of the envelope. It
// mirrors codeintel.Definition minus the redundant top-level Symbol field
// (the envelope already carries "symbol").
type findSymbolContextDefinition struct {
	Kind            string `json:"kind,omitempty"`
	Package         string `json:"package,omitempty"`
	Receiver        string `json:"receiver,omitempty"`
	Path            string `json:"path,omitempty"`
	Line            int    `json:"line,omitempty"`
	EndLine         int    `json:"end_line,omitempty"`
	Signature       string `json:"signature,omitempty"`
	Source          string `json:"source,omitempty"`
	SourceTruncated bool   `json:"source_truncated,omitempty"`
}

// findSymbolContextReference is one classified reference location. It omits
// codeintel.Location's Symbol field - it is always the same name already
// carried by the envelope's top-level "symbol", so repeating it on every
// reference is pure cost.
type findSymbolContextReference struct {
	Path string         `json:"path"`
	Line int            `json:"line"`
	Role codeintel.Role `json:"role"`
}

type findSymbolContextProvenance struct {
	WorkspaceRoot string `json:"workspace_root"`
	MaxReferences int    `json:"max_references"`
	ContextLines  int    `json:"context_lines"`
}

// findSymbolContextOutput is the tool's model-facing JSON shape. Every
// response path (no analyzer, analyzer error, success) converges on it, so
// the byte budget and truncation contract cannot be bypassed by any of them.
type findSymbolContextOutput struct {
	Version            int                          `json:"version"`
	Symbol             string                       `json:"symbol"`
	Definition         *findSymbolContextDefinition `json:"definition,omitempty"`
	References         []findSymbolContextReference `json:"references"`
	ReferenceCount     int                          `json:"reference_count"`
	ReferenceTruncated bool                         `json:"reference_truncated"`
	SymbolAvailable    bool                         `json:"symbol_available"`
	Error              string                       `json:"error,omitempty"`
	Provenance         findSymbolContextProvenance  `json:"provenance"`
}

func (t *findSymbolContextTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in findSymbolContextArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if err := validateFindSymbolContextInput(in); err != nil {
		return "", err
	}
	symbol, maxReferences, contextLines := resolveFindSymbolContextBounds(in)

	out := findSymbolContextOutput{
		Version:    findSymbolContextVersion,
		Symbol:     symbol,
		References: []findSymbolContextReference{},
		Provenance: findSymbolContextProvenance{
			WorkspaceRoot: ".",
			MaxReferences: maxReferences,
			ContextLines:  contextLines,
		},
	}

	if t.resolver == nil {
		out.Error = "find_symbol_context: no analyzer available"
		return t.marshal(out), nil
	}

	def, defErr := t.resolver.Definition(ctx, symbol)
	if defErr != nil {
		out.SymbolAvailable = !errors.Is(defErr, codeintel.ErrUnavailable)
		out.Error = defErr.Error()
		return t.marshal(out), nil
	}
	out.SymbolAvailable = true
	defOut := applyContextWindow(def, t.ws, contextLines)
	out.Definition = &defOut

	refs, refErr := t.resolver.References(ctx, symbol, referenceRolesExcludingDefinition, findSymbolContextReferenceFetchLimit)
	if refErr != nil {
		out.Error = refErr.Error()
		return t.marshal(out), nil
	}

	sorted := sortedReferenceLocations(refs.Locations)
	kept := sorted
	truncated := false
	if len(kept) > maxReferences {
		kept, truncated = kept[:maxReferences], true
	}
	out.References = toFindSymbolContextReferences(t.ws, kept)
	out.ReferenceCount = len(out.References)
	out.ReferenceTruncated = truncated

	return t.marshal(out), nil
}

// referenceRolesExcludingDefinition asks Analyzer.References for every role
// except "definition" - the definition location is already reported,
// structured, via the envelope's own "definition" field, so repeating it as
// an unstructured reference would be a redundant duplicate.
var referenceRolesExcludingDefinition = []codeintel.Role{
	codeintel.RoleImplementation,
	codeintel.RoleCaller,
	codeintel.RoleReturn,
	codeintel.RoleComparison,
}

// findSymbolContextReferenceFetchLimit is the limit passed to
// Analyzer.References itself. It must comfortably exceed
// findSymbolContextMaxMaxReferences (100): Analyzer.References applies its
// OWN cap during an unordered map iteration (analyzer.go's collectLocations,
// ADLC Step 0 finding AR-1) with no sort step, so asking it for exactly
// max_references would let ITS nondeterministic truncation decide which
// locations survive. Asking for effectively-everything instead means this
// tool's own sort-then-truncate (sortedReferenceLocations, below) is what
// determines the final max_references-bounded set, which is deterministic.
const findSymbolContextReferenceFetchLimit = 10_000

// sortedReferenceLocations sorts by (path, line, role), matching
// inspect_repository's existing result ordering convention. This must run
// BEFORE max_references truncation (AR-1): Analyzer.References's own order
// is unspecified, so truncating an unsorted slice would make which
// references survive nondeterministic between two calls against identical
// workspace state.
func sortedReferenceLocations(locations []codeintel.Location) []codeintel.Location {
	out := make([]codeintel.Location, len(locations))
	copy(out, locations)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Role < b.Role
	})
	return out
}

func toFindSymbolContextReferences(ws *workspace.Root, locations []codeintel.Location) []findSymbolContextReference {
	out := make([]findSymbolContextReference, len(locations))
	for i, loc := range locations {
		out[i] = findSymbolContextReference{
			Path: relativizePath(ws, loc.Path),
			Line: loc.Line,
			Role: loc.Role,
		}
	}
	return out
}

// applyContextWindow bounds the declaration source kept in the output to at
// most contextLines lines from the definition's start, still within
// codeintel.MaxDefinitionLines (Definition itself already enforces that
// outer cap - no new read path is needed here, only a further slice of the
// text it already returned). contextLines == 0 means the caller asked for no
// source text at all, which is a choice, not a truncation: SourceTruncated
// stays false in that case unless the analyzer itself already cut the span.
func applyContextWindow(def codeintel.Definition, ws *workspace.Root, contextLines int) findSymbolContextDefinition {
	out := findSymbolContextDefinition{
		Kind:      string(def.Kind),
		Package:   def.Package,
		Receiver:  def.Receiver,
		Path:      relativizePath(ws, def.Path),
		Line:      def.Line,
		EndLine:   def.EndLine,
		Signature: def.Signature,
	}
	if contextLines <= 0 || def.Source == "" {
		return out
	}
	lines := strings.Split(def.Source, "\n")
	if len(lines) > contextLines {
		out.Source = strings.Join(lines[:contextLines], "\n")
		out.SourceTruncated = true
		return out
	}
	out.Source = def.Source
	out.SourceTruncated = def.SourceTruncated
	return out
}

func validateFindSymbolContextInput(in findSymbolContextArgs) error {
	if strings.TrimSpace(in.Symbol) == "" {
		return fmt.Errorf("symbol is required")
	}
	if in.MaxReferences != 0 && (in.MaxReferences < findSymbolContextMinMaxReferences || in.MaxReferences > findSymbolContextMaxMaxReferences) {
		return fmt.Errorf("max_references must be between %d and %d", findSymbolContextMinMaxReferences, findSymbolContextMaxMaxReferences)
	}
	if in.ContextLines != nil && (*in.ContextLines < findSymbolContextMinContextLines || *in.ContextLines > findSymbolContextMaxContextLines) {
		return fmt.Errorf("context_lines must be between %d and %d", findSymbolContextMinContextLines, findSymbolContextMaxContextLines)
	}
	return nil
}

// marshal enforces the declared budget. The variable-length part here is
// References - Source is already tightly bounded (<=10 lines by
// context_lines's own maximum), so it never meaningfully contributes to an
// over-budget envelope the way a large reference list can.
func (t *findSymbolContextTool) marshal(out findSymbolContextOutput) string {
	full := out.References
	if full == nil {
		full = []findSymbolContextReference{}
	}
	return budgetedJSON(t.maxBytes, len(full), func(keep int, truncated bool) any {
		o := out
		o.References = full[:keep]
		o.ReferenceCount = keep
		if truncated {
			o.ReferenceTruncated = true
		}
		return o
	}, `{"version":1,"symbol":"","references":[],"reference_count":0,"reference_truncated":true,"symbol_available":false,"error":"find_symbol_context: result exceeds byte budget"}`)
}
