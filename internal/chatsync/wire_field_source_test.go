package chatsync

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A string that leaves this machine must pass through applyTruncation.
//
// truncate.go states that as an invariant - "Every free-text field on this
// wire is routed through applyTruncation, which is why this is applied there
// and not at each of its twenty-six call sites" - and the invariant has been
// false twice. Both times the shape was identical: a payload field assigned
// DIRECTLY from the raw bus event, while the same string was sanitised and
// bounded into a local variable a couple of lines away.
//
//   - tool.ended's Status was built from ev.Detail. A NUL reached the wire,
//     where the receiving column rejects it and takes the whole hundred-event
//     batch with it, and a long detail produced a 393 KB payload against a
//     64 KiB bound.
//   - before that, the hook row's Output was ev.Output, which carries an
//     operator diagnostic naming the hook's absolute path.
//
// Both regression suites missed both, for the same reason: each enumerates the
// shapes its author was thinking about. TestNoWireFieldCarriesANulByte lists
// event shapes; TestNoEventExceedsTheStoredPayloadBound lists them again. A
// field nobody listed is invisible to both.
//
// So this gate does not enumerate. It reads the payload structs from the
// recorded vocabulary, asks reflection which of their fields are strings, and
// then reads this package's own source to find any of those fields assigned
// from the event. It is an authoring-time check on the mechanism rather than
// another behavioural test over remembered examples, and it is the layer that
// can see the defect: no test can drive a field it does not know exists.

// eventParamNames are the conventional names for the bus event in this
// package's projector functions. A field assigned from one of these is coming
// straight off the wire's INPUT, which is exactly what must not happen.
var eventParamNames = map[string]bool{"ev": true, "event": true}

// TestNoWirePayloadStringComesStraightFromTheEvent is the gate.
func TestNoWirePayloadStringComesStraightFromTheEvent(t *testing.T) {
	stringFields := wireStringFields()
	if len(stringFields) == 0 {
		t.Fatal("no wire payload string fields were found; the reflection is " +
			"wrong, not the code")
	}
	exempt := loadWireFieldExemptions(t)

	fset := token.NewFileSet()
	var findings []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			typeName := compositeTypeName(lit.Type)
			fields, tracked := stringFields[typeName]
			if !tracked {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || !fields[key.Name] {
					continue
				}
				if !readsTheEventDirectly(kv.Value) {
					continue
				}
				qualified := typeName + "." + key.Name
				if _, allowed := exempt[qualified]; allowed {
					continue
				}
				findings = append(findings, fmt.Sprintf("%s at %s:%d",
					qualified, path, fset.Position(kv.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(findings) > 0 {
		t.Errorf("wire string fields assigned straight from the bus event: %v\n"+
			"Route the value through applyTruncation, which removes the code "+
			"points the receiving column cannot store and bounds the field in "+
			"the bytes that column counts. A field that skips it can carry a NUL "+
			"- which rejects the whole batch it travels in - or exceed the "+
			"payload bound and do the same. If a field genuinely must pass "+
			"through raw, declare it in %s with a reason.",
			findings, wireFieldExemptionPath)
	}
}

// readsTheEventDirectly reports whether expr reads a field off the bus event
// WITHOUT the choke point in between.
//
// An applyTruncation call is the choke point, so its whole subtree is skipped:
// reading the event there is the correct input, not a bypass. Everything else
// counts - `ev.Detail`, `toolEndStatus(ev.Detail)` and `string(ev.Content)`
// all reach the wire with the raw value.
func readsTheEventDirectly(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "applyTruncation" {
				return false
			}
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if base, ok := sel.X.(*ast.Ident); ok && eventParamNames[base.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

func compositeTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// wireStringFields returns, per payload type name, the set of its string
// fields - including those it inherits from an embedded Envelope, and those of
// any struct reachable from it.
//
// It reads the RECORDED vocabulary rather than a list written here, so a
// payload minted tomorrow is covered without anyone remembering this file.
func wireStringFields() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, spec := range WireEventSpecs() {
		collectStringFields(reflect.TypeOf(spec.Payload), out)
	}
	return out
}

func collectStringFields(t reflect.Type, out map[string]map[string]bool) {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return
	}
	if _, seen := out[t.Name()]; seen {
		return
	}
	fields := map[string]bool{}
	out[t.Name()] = fields
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.String:
			fields[f.Name] = true
			// An embedded struct's fields are also settable on the outer
			// literal, so they belong to both sets.
			if f.Anonymous {
				collectStringFields(f.Type, out)
			}
		case reflect.Struct:
			collectStringFields(f.Type, out)
			if f.Anonymous {
				for name := range out[ft.Name()] {
					fields[name] = true
				}
			}
		}
	}
}

const wireFieldExemptionPath = "../../.mivia/policy/wire-field-sources.json"

// loadWireFieldExemptions reads the declared exceptions. An entry needs a
// REASON: a bare allowlist decays into a place to put anything inconvenient,
// which is the shape of every gate this repository has had to fix twice.
func loadWireFieldExemptions(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(wireFieldExemptionPath)
	if err != nil {
		t.Fatalf("read %s: %v", wireFieldExemptionPath, err)
	}
	var policy struct {
		Exempt map[string]string `json:"exempt"`
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatalf("parse %s: %v", wireFieldExemptionPath, err)
	}
	for field, reason := range policy.Exempt {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s exempts %s with an empty reason; an exemption that says "+
				"nothing is a bypass with extra steps", wireFieldExemptionPath, field)
		}
	}
	return policy.Exempt
}
