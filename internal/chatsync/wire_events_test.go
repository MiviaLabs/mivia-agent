package chatsync

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sourceWireTypeConstants parses every non-test Go file in this package and
// returns the value of each `Type<Name> = "..."` constant, keyed by constant
// name. It reads the SOURCE rather than the package's exported values because
// the defect it guards is a constant that exists but was never wired into the
// event table: at run time such a constant is indistinguishable from any other
// string, so only the declaration site can reveal it.
func sourceWireTypeConstants(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		collectWireTypeConstants(t, file, out)
	}
	if len(out) == 0 {
		t.Fatal("found no Type* string constants in the package source")
	}
	return out
}

func collectWireTypeConstants(t *testing.T, file *ast.File, out map[string]string) {
	t.Helper()
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			name := value.Names[0].Name
			if !strings.HasPrefix(name, "Type") {
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			unquoted, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", name, err)
			}
			out[name] = unquoted
		}
	}
}

func goTypeName(model any) string {
	return reflect.TypeOf(model).Name()
}

// TestEveryWireTypeConstantIsInTheEventTable asserts that the event table is
// the single definition site: a Type* constant that no spec names is a wire
// type the contract snapshot, the SSE-safety check, and any web client would
// all silently miss.
func TestEveryWireTypeConstantIsInTheEventTable(t *testing.T) {
	constants := sourceWireTypeConstants(t)
	inTable := map[string]bool{}
	for _, spec := range WireEventSpecs() {
		inTable[spec.Type] = true
	}

	var missing []string
	for name, value := range constants {
		if !inTable[value] {
			missing = append(missing, name+" = "+strconv.Quote(value))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("wire type constants absent from the event table:\n  %s", strings.Join(missing, "\n  "))
	}

	declared := map[string]bool{}
	for _, value := range constants {
		declared[value] = true
	}
	for _, spec := range WireEventSpecs() {
		if !declared[spec.Type] {
			t.Errorf("event table names %q, which no Type* constant declares", spec.Type)
		}
	}
}

// TestKnownWireTypesIsDerivedFromTheEventTable pins the derivation so the
// exported list can never be edited into disagreement with the table.
func TestKnownWireTypesIsDerivedFromTheEventTable(t *testing.T) {
	specs := WireEventSpecs()
	if len(KnownWireTypes) != len(specs) {
		t.Fatalf("KnownWireTypes has %d entries, event table has %d", len(KnownWireTypes), len(specs))
	}
	for i, spec := range specs {
		if KnownWireTypes[i] != spec.Type {
			t.Errorf("KnownWireTypes[%d] = %q, event table has %q", i, KnownWireTypes[i], spec.Type)
		}
	}
}

// TestEventTableBindsEachTypeToADistinctPayload asserts the table carries a
// payload model per type and never reuses one struct for two types, which
// would make the recorded shape of one type a copy of another's.
func TestEventTableBindsEachTypeToADistinctPayload(t *testing.T) {
	seenType := map[string]bool{}
	seenPayload := map[string]bool{}
	for _, spec := range WireEventSpecs() {
		if spec.Payload == nil {
			t.Errorf("%s has no payload model", spec.Type)
			continue
		}
		if seenType[spec.Type] {
			t.Errorf("event table lists %q twice", spec.Type)
		}
		seenType[spec.Type] = true

		name := goTypeName(spec.Payload)
		if seenPayload[name] {
			t.Errorf("payload model %s is bound to more than one wire type", name)
		}
		seenPayload[name] = true
	}
}
