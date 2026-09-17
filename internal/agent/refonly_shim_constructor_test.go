package agent

// Tests for newRefOnlyShim, the sole construction site for
// refOnlyShim (S6a of the approved S6 plan). See refonly_shim.go's
// doc comment on newRefOnlyShim for the constructor's contract.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// ctorSentinelInner is a named stub inner tool used only to give
// TestNewRefOnlyShimSetsEveryField a distinct, identity-checkable
// value for the inner field.
type ctorSentinelInner struct{ name string }

func (c *ctorSentinelInner) Name() string { return c.name }
func (c *ctorSentinelInner) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{}, nil
}

// ctorSentinelSchema is a stub schema tool whose ParameterSchema
// returns a unique byte slice, giving the test an identity-checkable
// value for the schema field distinct from the inner tool.
type ctorSentinelSchema struct{ schema []byte }

func (c *ctorSentinelSchema) ParameterSchema() []byte { return c.schema }
func (c *ctorSentinelSchema) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	return sdktools.InOut{}, nil
}

// TestNewRefOnlyShimSetsEveryField asserts newRefOnlyShim copies
// every constructor argument into the matching struct field, one
// sentinel per field, and that the struct has no field the test
// does not know about (a new field must fail this test, not escape
// it).
func TestNewRefOnlyShimSetsEveryField(t *testing.T) {
	inner := &ctorSentinelInner{name: "inner-sentinel"}
	schema := &ctorSentinelSchema{schema: []byte("schema-sentinel-bytes")}
	spool := remainder.NewSpool(nil)
	names := []string{"ref-name-sentinel"}
	const floor = 4242 // deliberately NOT BatchDegradeFloorBytes
	const principal = "principal-sentinel"
	const ephemeral = true
	turn := &sdkTurnState{}

	got := newRefOnlyShim(inner, schema, spool, names, floor, principal, ephemeral, turn)
	if got == nil {
		t.Fatal("newRefOnlyShim returned nil")
	}

	// Direct field access is used for the actual value checks (this
	// test lives in package agent, so it can read refOnlyShim's
	// unexported fields directly; reflect.Value.Interface panics on
	// unexported fields even from within the defining package).
	// Reflection is used only to enumerate the struct's field names,
	// so a field added to refOnlyShim without a matching case here
	// fails the test instead of silently escaping it.
	visited := make(map[string]bool)
	typ := reflect.TypeOf(*got)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		switch name {
		case "inner":
			if sdktools.Tool(got.inner) != sdktools.Tool(inner) {
				t.Errorf("field inner = %v, want %v", got.inner, inner)
			}
		case "schema":
			if sdktools.SchemaTool(got.schema) != sdktools.SchemaTool(schema) {
				t.Errorf("field schema = %v, want %v", got.schema, schema)
			}
		case "spool":
			if got.spool != spool {
				t.Errorf("field spool = %v, want %v", got.spool, spool)
			}
		case "names":
			if !slices.Equal(got.names, names) {
				t.Errorf("field names = %v, want %v", got.names, names)
			}
		case "floor":
			if got.floor != floor {
				t.Errorf("field floor = %v, want %v", got.floor, floor)
			}
		case "principal":
			if got.principal != principal {
				t.Errorf("field principal = %v, want %v", got.principal, principal)
			}
		case "ephemeral":
			if got.ephemeral != ephemeral {
				t.Errorf("field ephemeral = %v, want %v", got.ephemeral, ephemeral)
			}
		case "turn":
			if got.turn != turn {
				t.Errorf("field turn = %v, want %v", got.turn, turn)
			}
		default:
			t.Errorf("unrecognized field %q on refOnlyShim; test does not know how to check it", name)
		}
		visited[name] = true
	}

	wantFields := map[string]bool{
		"inner": true, "schema": true, "spool": true, "names": true,
		"floor": true, "principal": true, "ephemeral": true, "turn": true,
	}
	if !reflect.DeepEqual(visited, wantFields) {
		t.Errorf("visited fields = %v, want %v (a struct field was added or removed without updating this test)", visited, wantFields)
	}
}

// TestRefOnlyConstructorIsSoleSite asserts that "&refOnlyShim{" appears
// exactly once across internal/agent's non-test .go files, and that
// the single occurrence lives inside the body of newRefOnlyShim. This
// pins refOnlyShim construction to a single call site so every caller
// (the registry route applyRefOnlyShim and the deferred route
// wrapRefOnly) shares one place that sets every field.
func TestRefOnlyConstructorIsSoleSite(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	type occurrence struct {
		file string
		line int
		fn   string // enclosing function name, "" if none
	}
	var found []occurrence

	fset := token.NewFileSet()
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fileNode, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", path, err)
		}

		// Map each CompositeLit for &refOnlyShim{...} to its
		// enclosing FuncDecl, if any, by walking the whole file and
		// tracking the current function.
		var currentFn string
		ast.Inspect(fileNode, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok {
				currentFn = fd.Name.Name
			}
			unary, ok := n.(*ast.UnaryExpr)
			if !ok || unary.Op != token.AND {
				return true
			}
			lit, ok := unary.X.(*ast.CompositeLit)
			if !ok {
				return true
			}
			ident, ok := lit.Type.(*ast.Ident)
			if !ok || ident.Name != "refOnlyShim" {
				return true
			}
			pos := fset.Position(unary.Pos())
			found = append(found, occurrence{file: path, line: pos.Line, fn: currentFn})
			return true
		})
	}

	if len(found) != 1 {
		var parts []string
		for _, o := range found {
			parts = append(parts, filepath.Base(o.file)+":"+strconv.Itoa(o.line)+" (in "+o.fn+")")
		}
		t.Fatalf("want exactly one &refOnlyShim{ construction site, got %d: %v", len(found), parts)
	}

	if found[0].fn != "newRefOnlyShim" {
		t.Fatalf("sole &refOnlyShim{ site is %s:%d, inside function %q; want it inside newRefOnlyShim",
			found[0].file, found[0].line, found[0].fn)
	}
}
