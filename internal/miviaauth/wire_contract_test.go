package miviaauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// This file is the drift gate that replaced the generated types' freshness
// check. It compares this package against api/contracts/auth.v1.json, a
// hand-maintained record of the mivia API's /v1/auth surface.
//
// The value is that ONE SIDE OF THE COMPARISON IS NOT THIS PACKAGE. A field
// renamed in wire.go, a route repointed in client.go, or a nullable response
// field quietly made non-pointer all fail here, because the expected value
// lives in a file someone had to edit on purpose. Do not add a regeneration
// mode: a test that can rewrite its own expectations proves nothing.
//
// What it cannot do is notice the API changing under the recorded contract.
// See api/contracts/README.md.

type contractField struct {
	Kind     string `json:"kind"`
	Nullable bool   `json:"nullable"`
}

type contractStruct struct {
	DTO               string                   `json:"dto"`
	FieldsNotModelled []string                 `json:"dtoFieldsNotModelled"`
	Fields            map[string]contractField `json:"fields"`
}

type contractRoute struct {
	Name           string `json:"name"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	Auth           string `json:"auth"`
	SuccessStatus  int    `json:"successStatus"`
	RequestStruct  string `json:"requestStruct"`
	ResponseStruct string `json:"responseStruct"`
}

type authContract struct {
	Routes  []contractRoute           `json:"routes"`
	Structs map[string]contractStruct `json:"structs"`
}

func loadAuthContract(t *testing.T) authContract {
	t.Helper()
	path := filepath.Join("..", "..", "api", "contracts", "auth.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var c authContract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(c.Routes) == 0 || len(c.Structs) == 0 {
		t.Fatalf("%s parsed to an empty contract -- the test itself is broken, not a real pass", path)
	}
	return c
}

// goWireStructs maps each recorded struct name to a zero value of the Go type
// that models it. A struct recorded in the contract with no entry here fails
// the test: that is how a newly recorded DTO is forced to grow a Go model
// instead of being silently ignored.
func goWireStructs() map[string]any {
	return map[string]any{
		"loginRequest":    loginRequest{},
		"refreshRequest":  refreshRequest{},
		"revokeRequest":   revokeRequest{},
		"authUser":        authUser{},
		"sessionResponse": sessionResponse{},
		"okResponse":      okResponse{},
		"meResponse":      meResponse{},
		"errorEnvelope":   errorEnvelope{},
	}
}

// TestWireStructsMatchContractSnapshot asserts that every recorded struct has
// a Go model whose JSON field names, value kinds, and nullability are exactly
// what the contract records.
func TestWireStructsMatchContractSnapshot(t *testing.T) {
	contract := loadAuthContract(t)
	models := goWireStructs()

	for name, want := range contract.Structs {
		model, ok := models[name]
		if !ok {
			t.Errorf("contract records struct %q but no Go type models it", name)
			continue
		}
		got := describeGoStruct(t, model)

		if diff := sortedKeys(got); !reflect.DeepEqual(diff, sortedKeys(want.Fields)) {
			t.Errorf("%s: JSON field names differ\n  go:       %v\n  contract: %v",
				name, diff, sortedKeys(want.Fields))
			continue
		}
		for field, wantField := range want.Fields {
			gotField := got[field]
			if gotField.Nullable != wantField.Nullable {
				t.Errorf("%s.%s: nullable = %v, contract records %v",
					name, field, gotField.Nullable, wantField.Nullable)
			}
			if !kindMatches(gotField.Kind, wantField.Kind) {
				t.Errorf("%s.%s: Go value kind %q does not satisfy contract kind %q",
					name, field, gotField.Kind, wantField.Kind)
			}
		}
	}

	for name := range models {
		if _, ok := contract.Structs[name]; !ok {
			t.Errorf("Go models wire struct %q that the contract does not record", name)
		}
	}
}

// TestDeliberatelyUnmodelledDTOFieldsStayUnmodelled holds the contract's
// documented exceptions to their word.
//
// Two structs model fewer fields than their DTO declares, each for a stated
// reason: revokeRequest omits sessionId because the CLI never revokes another
// session by id, and errorEnvelope omits timestamp and path because it is
// read only to classify a failure. Adding one of those fields to Go without
// removing it from the exception list would leave the record describing a
// decision that is no longer true, which is exactly the drift this file
// exists to catch.
func TestDeliberatelyUnmodelledDTOFieldsStayUnmodelled(t *testing.T) {
	contract := loadAuthContract(t)
	models := goWireStructs()

	for name, recorded := range contract.Structs {
		if len(recorded.FieldsNotModelled) == 0 {
			continue
		}
		model, ok := models[name]
		if !ok {
			continue
		}
		got := describeGoStruct(t, model)
		for _, field := range recorded.FieldsNotModelled {
			if _, present := got[field]; present {
				t.Errorf("%s now models %q, which the contract records as deliberately unmodelled (%s); update api/contracts/auth.v1.json",
					name, field, recorded.DTO)
			}
		}
	}
}

// describeGoStruct reads a struct's json tags by reflection into the same
// shape the contract records. A field is nullable when it is modelled as a
// pointer, which is how meResponse.displayName carries the DTO's
// `string | null`.
func describeGoStruct(t *testing.T, model any) map[string]contractField {
	t.Helper()
	typ := reflect.TypeOf(model)
	out := map[string]contractField{}
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no json tag; every wire field must name its key explicitly",
				typ.Name(), field.Name)
		}
		name, opts, _ := strings.Cut(tag, ",")
		if opts != "" {
			t.Fatalf("%s.%s carries json options %q; wire structs send and accept exact field sets, so omitempty and friends are not allowed",
				typ.Name(), field.Name, opts)
		}
		fieldType := field.Type
		nullable := fieldType.Kind() == reflect.Pointer
		if nullable {
			fieldType = fieldType.Elem()
		}
		out[name] = contractField{Kind: goKindName(fieldType), Nullable: nullable}
	}
	return out
}

func goKindName(t reflect.Type) string {
	if t == reflect.TypeOf(time.Time{}) {
		// The API sends expiresAt as an ISO-8601 string; time.Time is the
		// decode target, not a different wire kind.
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int64, reflect.Float64:
		return "number"
	case reflect.Struct, reflect.Map:
		return "object"
	case reflect.Slice:
		return "array"
	default:
		return t.Kind().String()
	}
}

// kindMatches allows a contract kind to name alternatives with "|", which
// errorEnvelope.message needs: the API types it as `string | string[]` and
// the Go model normalizes both into one string.
func kindMatches(got, want string) bool {
	for _, alt := range strings.Split(want, "|") {
		if got == strings.TrimSpace(alt) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestClientRoutesMatchContractSnapshot asserts every recorded route's method
// and path are what the client actually sends, that the path carries the /v1
// version prefix and no /api segment, and that bearer-gated routes send an
// Authorization header while public ones do not.
//
// It drives the real Client against an httptest server rather than reading
// string constants, so a route that is recorded correctly but built wrongly
// still fails.
func TestClientRoutesMatchContractSnapshot(t *testing.T) {
	contract := loadAuthContract(t)
	for _, route := range contract.Routes {
		t.Run(route.Name, func(t *testing.T) {
			rec := newRouteRecorder(t, route)
			rec.call(t, route.Name)

			if rec.method != route.Method {
				t.Errorf("method = %q, contract records %q", rec.method, route.Method)
			}
			if rec.path != route.Path {
				t.Errorf("path = %q, contract records %q", rec.path, route.Path)
			}
			if !strings.HasPrefix(rec.path, "/v1/") {
				t.Errorf("path %q lacks the /v1 version prefix", rec.path)
			}
			if strings.HasPrefix(rec.path, "/api/") || strings.Contains(rec.path, "/api/v") {
				t.Errorf("path %q carries an /api segment; the API sets no global prefix", rec.path)
			}
			hasAuth := rec.authorization != ""
			wantAuth := route.Auth == "bearer"
			if hasAuth != wantAuth {
				t.Errorf("Authorization header present = %v, contract records auth %q", hasAuth, route.Auth)
			}
		})
	}
}
