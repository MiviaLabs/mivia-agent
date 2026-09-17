package agent

// Tests for newTurnShapeWrapper, the sole construction site for
// turnShapeWrapper (S6b of the approved S6 plan). See sdk_shaping.go's
// doc comment on newTurnShapeWrapper for the constructor's contract.
// Mirrors refonly_shim_constructor_test.go's pattern for newRefOnlyShim
// (S6a, already merged).

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// turnShapeCtorSentinelInner is a named stub inner tool used only to
// give the newTurnShapeWrapper constructor tests a distinct,
// identity-checkable value for the inner field.
type turnShapeCtorSentinelInner struct{ name string }

func (c *turnShapeCtorSentinelInner) Name() string { return c.name }
func (c *turnShapeCtorSentinelInner) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{}, nil
}

// turnShapeCtorSentinels holds one set of distinct-per-field sentinel
// constructor arguments, shared by TestNewTurnShapeWrapperSetsEveryField
// and TestNewTurnShapeWrapperOnDegrade so both tests construct
// newTurnShapeWrapper from identical inputs. cap shadows the builtin
// exactly as the production call sites do.
type turnShapeCtorSentinels struct {
	inner     *turnShapeCtorSentinelInner
	budget    int
	counter   *turnShapeCounter
	env       shapeEnv
	ephemeral bool
	toolName  string
	turn      *sdkTurnState
	cap       int
}

// newTurnShapeCtorSentinelValues builds one turnShapeCtorSentinels. The
// counter's previewReserve is set away from its zero-value default so
// counter's identity (not merely a freshly-constructed zero value) is
// what the field-equality check in assertTurnShapeWrapperFields proves.
func newTurnShapeCtorSentinelValues() turnShapeCtorSentinels {
	counter := newTurnShapeCounter()
	counter.previewReserve = 424242
	spool := remainder.NewSpool(nil)
	return turnShapeCtorSentinels{
		inner:     &turnShapeCtorSentinelInner{name: "inner-sentinel"},
		budget:    7777,
		counter:   counter,
		env:       newShapeEnv(spool, "env-principal-sentinel"),
		ephemeral: true,
		toolName:  "turn-name-sentinel", // distinct from inner.Name()
		turn:      &sdkTurnState{},
		cap:       8888, // shadows the builtin exactly as the production sites do
	}
}

// assertTurnShapeWrapperFields walks turnShapeWrapper's fields by
// reflection and asserts each one holds its matching sentinel from s.
// Direct field access does the actual value checks (this test lives in
// package agent, so it can read turnShapeWrapper's unexported fields
// directly); reflection only enumerates the struct's field names, so a
// field added to turnShapeWrapper without a matching case here fails
// the test instead of silently escaping it. onDegrade is checked only
// for non-nil: its invocation contract is proven separately by
// TestNewTurnShapeWrapperOnDegrade, since closures compare unequal.
func assertTurnShapeWrapperFields(t *testing.T, got *turnShapeWrapper, s turnShapeCtorSentinels) {
	t.Helper()
	visited := make(map[string]bool)
	typ := reflect.TypeOf(*got)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		switch name {
		case "inner":
			if sdktools.Tool(got.inner) != sdktools.Tool(s.inner) {
				t.Errorf("field inner = %v, want %v", got.inner, s.inner)
			}
		case "budget":
			if got.budget != s.budget {
				t.Errorf("field budget = %v, want %v", got.budget, s.budget)
			}
		case "counter":
			if got.counter != s.counter {
				t.Errorf("field counter = %v, want %v", got.counter, s.counter)
			}
		case "env":
			// shapeEnv carries a []string field (refOnlyTools), so it is
			// not comparable with ==; reflect.DeepEqual proves the same
			// per-field content instead.
			if !reflect.DeepEqual(got.env, s.env) {
				t.Errorf("field env = %+v, want %+v", got.env, s.env)
			}
		case "ephemeral":
			if got.ephemeral != s.ephemeral {
				t.Errorf("field ephemeral = %v, want %v", got.ephemeral, s.ephemeral)
			}
		case "toolName":
			if got.toolName != s.toolName {
				t.Errorf("field toolName = %v, want %v", got.toolName, s.toolName)
			}
		case "turn":
			if got.turn != s.turn {
				t.Errorf("field turn = %v, want %v", got.turn, s.turn)
			}
		case "cap":
			if got.cap != s.cap {
				t.Errorf("field cap = %v, want %v", got.cap, s.cap)
			}
		case "onDegrade":
			if got.onDegrade == nil {
				t.Fatal("field onDegrade is nil")
			}
		default:
			t.Errorf("unrecognized field %q on turnShapeWrapper; test does not know how to check it", name)
		}
		visited[name] = true
	}

	wantFields := map[string]bool{
		"inner": true, "budget": true, "counter": true, "env": true,
		"ephemeral": true, "toolName": true, "turn": true, "cap": true,
		"onDegrade": true,
	}
	if !reflect.DeepEqual(visited, wantFields) {
		t.Errorf("visited fields = %v, want %v (a struct field was added or removed without updating this test)", visited, wantFields)
	}
}

// TestNewTurnShapeWrapperSetsEveryField asserts newTurnShapeWrapper
// copies every constructor argument into the matching struct field,
// one sentinel per field, and that the struct has no field the test
// does not know about (a new field must fail this test, not escape
// it). The onDegrade field's invocation contract is checked separately
// by TestNewTurnShapeWrapperOnDegrade.
func TestNewTurnShapeWrapperSetsEveryField(t *testing.T) {
	s := newTurnShapeCtorSentinelValues()
	onDegrade := func(charged, budget int) {}

	got := newTurnShapeWrapper(s.inner, s.budget, s.counter, s.env, s.ephemeral, s.toolName, s.turn, s.cap, onDegrade)
	if got == nil {
		t.Fatal("newTurnShapeWrapper returned nil")
	}
	assertTurnShapeWrapperFields(t, got, s)
}

// TestNewTurnShapeWrapperOnDegrade asserts the onDegrade field, once
// set, is invoked with exactly the (charged, budget) pair the caller
// passed to it - closures compare unequal, so the field's identity is
// proven by invocation, not equality (matches the constructor's doc
// comment) - and that the production onDegrade closure shape shared by
// both call sites (applyTurnShaping, wrapTurnShaping), which captures
// Options and delegates to emitBatchShapingRow, drives the real event
// path end-to-end: a heartbeat Event whose Detail names the exact
// charged/budget totals, not merely an arbitrary func value.
func TestNewTurnShapeWrapperOnDegrade(t *testing.T) {
	s := newTurnShapeCtorSentinelValues()

	var recordedCharged, recordedBudget int
	recording := newTurnShapeWrapper(s.inner, s.budget, s.counter, s.env, s.ephemeral, s.toolName, s.turn, s.cap,
		func(charged, budget int) { recordedCharged, recordedBudget = charged, budget })
	recording.onDegrade(11, 22)
	if recordedCharged != 11 || recordedBudget != 22 {
		t.Errorf("onDegrade invocation recorded (%d, %d), want (11, 22)", recordedCharged, recordedBudget)
	}

	var captured Event
	opts := Options{OnEvent: func(e Event) { captured = e }}
	prodWrapper := newTurnShapeWrapper(s.inner, s.budget, s.counter, s.env, s.ephemeral, s.toolName, s.turn, s.cap,
		func(charged, budget int) { emitBatchShapingRow(opts, charged, budget) })
	prodWrapper.onDegrade(11, 22)
	if captured.Kind != EventHeartbeat {
		t.Errorf("Event.Kind = %v, want %v", captured.Kind, EventHeartbeat)
	}
	const wantDetail = "tool batch budget: 1 of 1 results degraded · 11/22 bytes charged"
	if captured.Detail != wantDetail {
		t.Errorf("Event.Detail = %q, want %q", captured.Detail, wantDetail)
	}
}

// TestTurnShapeConstructorIsSoleSite asserts that "&turnShapeWrapper{"
// appears exactly once across internal/agent's non-test .go files, and
// that the single occurrence lives inside the body of
// newTurnShapeWrapper. This pins turnShapeWrapper construction to a
// single call site so every caller (applyTurnShaping and
// wrapTurnShaping) shares one place that sets every field. Mirrors
// TestRefOnlyConstructorIsSoleSite (S6a, already merged).
func TestTurnShapeConstructorIsSoleSite(t *testing.T) {
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

		// Map each CompositeLit for &turnShapeWrapper{...} to its
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
			if !ok || ident.Name != "turnShapeWrapper" {
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
		t.Fatalf("want exactly one &turnShapeWrapper{ construction site, got %d: %v", len(found), parts)
	}

	if found[0].fn != "newTurnShapeWrapper" {
		t.Fatalf("sole &turnShapeWrapper{ site is %s:%d, inside function %q; want it inside newTurnShapeWrapper",
			found[0].file, found[0].line, found[0].fn)
	}
}
