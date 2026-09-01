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
	"time"
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

// ---------------------------------------------------------------------------
// events section of api/contracts/chat-sessions.v1.json
//
// The Go event table is the AUTHORING site: this repository's projector emits
// these events and the API stores their payloads as opaque jsonb, so nothing
// upstream defines their shape. The contract file is the PUBLISHED mirror a
// web client vendors, and the test below makes the two provably equal. There
// is no build-time code generation in either direction: the shipping binary
// never reads the JSON, and the JSON is never written by a tool.
// ---------------------------------------------------------------------------

type contractWireField struct {
	Kind     string `json:"kind"`
	Optional bool   `json:"optional"`
	Ref      string `json:"ref,omitempty"`
}

type contractWireShape struct {
	GoType string                       `json:"goType"`
	Fields map[string]contractWireField `json:"fields"`
}

type contractEvents struct {
	Envelope contractWireShape            `json:"envelope"`
	Objects  map[string]contractWireShape `json:"objects"`
	Types    map[string]contractWireShape `json:"types"`
}

// wireObjectModels lists the shared sub-objects a payload can carry. They are
// recorded once instead of being inlined into every type that embeds them.
func wireObjectModels() map[string]any {
	return map[string]any{
		"agentOrigin": AgentOrigin{},
		"truncation":  Truncation{},
		"truncField":  TruncField{},
	}
}

// wireRefNames inverts wireObjectModels: Go type name -> contract key.
func wireRefNames() map[string]string {
	out := map[string]string{}
	for key, model := range wireObjectModels() {
		out[reflect.TypeOf(model).Name()] = key
	}
	return out
}

func wireKind(typ reflect.Type) string {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(time.Time{}) {
		return "string"
	}
	switch typ.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int64, reflect.Uint64, reflect.Float64:
		return "number"
	case reflect.Slice:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	case reflect.Interface:
		return "any"
	default:
		return typ.Kind().String()
	}
}

// wireRef names the objects entry that describes a field. For a map field it
// describes the map's VALUES, which is how trunc.fields is read.
func wireRef(typ reflect.Type) string {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Map {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(time.Time{}) {
		return ""
	}
	return wireRefNames()[typ.Name()]
}

// describeWireShape derives the recorded shape of one model. The embedded
// Envelope is skipped: it is recorded once under events.envelope rather than
// repeated into all fifteen types.
func describeWireShape(t *testing.T, model any) contractWireShape {
	t.Helper()
	typ := reflect.TypeOf(model)
	shape := contractWireShape{GoType: typ.Name(), Fields: map[string]contractWireField{}}
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Anonymous {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no json tag", typ.Name(), field.Name)
		}
		name, opts, _ := strings.Cut(tag, ",")
		shape.Fields[name] = contractWireField{
			Kind:     wireKind(field.Type),
			Optional: strings.Contains(opts, "omitempty"),
			Ref:      wireRef(field.Type),
		}
	}
	return shape
}

func assertWireShapeEqual(t *testing.T, label string, got, want contractWireShape) {
	t.Helper()
	if got.GoType != want.GoType {
		t.Errorf("%s: Go model is %s, contract records %s", label, got.GoType, want.GoType)
	}
	if !reflect.DeepEqual(sortedKeys(got.Fields), sortedKeys(want.Fields)) {
		t.Errorf("%s: field names differ\n  go:       %v\n  contract: %v",
			label, sortedKeys(got.Fields), sortedKeys(want.Fields))
		return
	}
	for name, wantField := range want.Fields {
		if got.Fields[name] != wantField {
			t.Errorf("%s.%s: go %+v, contract %+v", label, name, got.Fields[name], wantField)
		}
	}
}

// TestWireEnvelopeMatchesContractSnapshot pins the envelope every payload
// embeds. A web client reads v, at and turn on every frame.
func TestWireEnvelopeMatchesContractSnapshot(t *testing.T) {
	events := loadChatSessionsContract(t).Events
	assertWireShapeEqual(t, "envelope", describeWireShape(t, Envelope{}), events.Envelope)
}

// TestWireSharedObjectsMatchContractSnapshot pins the shared sub-objects.
// trunc.fields is how a client learns a string field was cut, which is the
// only reliable truncation signal: the budget that cut it is not on the wire.
func TestWireSharedObjectsMatchContractSnapshot(t *testing.T) {
	events := loadChatSessionsContract(t).Events
	models := wireObjectModels()
	if !reflect.DeepEqual(sortedKeys(models), sortedKeys(events.Objects)) {
		t.Fatalf("shared object names differ\n  go:       %v\n  contract: %v",
			sortedKeys(models), sortedKeys(events.Objects))
	}
	for key, model := range models {
		assertWireShapeEqual(t, "objects."+key, describeWireShape(t, model), events.Objects[key])
	}
}

// TestWireEventPayloadsMatchContractSnapshot is the drift gate between the Go
// event table and the artifact a web client vendors. It fails on a type the
// contract omits, a type the contract invents, and any payload field whose
// name, kind, optionality, or object reference has changed.
func TestWireEventPayloadsMatchContractSnapshot(t *testing.T) {
	events := loadChatSessionsContract(t).Events
	specs := WireEventSpecs()

	goTypes := make([]string, 0, len(specs))
	for _, spec := range specs {
		goTypes = append(goTypes, spec.Type)
	}
	sort.Strings(goTypes)
	if !reflect.DeepEqual(goTypes, sortedKeys(events.Types)) {
		t.Fatalf("recorded event types differ from the Go event table\n  go:       %v\n  contract: %v",
			goTypes, sortedKeys(events.Types))
	}

	for _, spec := range specs {
		assertWireShapeEqual(t, "types."+spec.Type,
			describeWireShape(t, spec.Payload), events.Types[spec.Type])
	}
}
