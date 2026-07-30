package codeintel

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRoleValidValues(t *testing.T) {
	roles := []Role{RoleDefinition, RoleImplementation, RoleCaller, RoleReturn, RoleComparison}
	for _, r := range roles {
		got := r.String()
		if got == "" {
			t.Fatalf("Role %q has empty String()", string(r))
		}
	}
}

func TestResultJSONRoundTrip(t *testing.T) {
	orig := Result{
		Symbol: "storage.Store",
		Locations: []Location{
			{Path: "internal/storage/store.go", Line: 10, Symbol: "storage.Store", Role: RoleDefinition},
			{Path: "internal/storage/memory.go", Line: 25, Symbol: "storage.Memory", Role: RoleImplementation},
		},
		Complete:  true,
		Errors:    0,
		Truncated: false,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Symbol != orig.Symbol {
		t.Fatalf("Symbol = %q, want %q", got.Symbol, orig.Symbol)
	}
	if len(got.Locations) != len(orig.Locations) {
		t.Fatalf("len(Locations) = %d, want %d", len(got.Locations), len(orig.Locations))
	}
	if got.Complete != orig.Complete {
		t.Fatalf("Complete = %v, want %v", got.Complete, orig.Complete)
	}
}

func TestResultOmitEmptyFields(t *testing.T) {
	r := Result{Symbol: "foo", Locations: nil, Complete: true}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// Errors=0 and Truncated=false should be omitted from JSON (omitempty).
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["errors"]; ok {
		t.Error("errors=0 should be omitted from JSON")
	}
	if _, ok := raw["truncated"]; ok {
		t.Error("truncated=false should be omitted from JSON")
	}
}

func TestErrUnavailable(t *testing.T) {
	if !errors.Is(ErrUnavailable, ErrUnavailable) {
		t.Fatal("ErrUnavailable should wrap itself")
	}
	if ErrUnavailable.Error() == "" {
		t.Fatal("ErrUnavailable.Error() should not be empty")
	}
}
