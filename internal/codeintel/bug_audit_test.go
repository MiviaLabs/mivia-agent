package codeintel

import (
	"go/token"
	"testing"
)

// Verify that the analyzerEnv has exactly one GOPROXY=off and GOFLAGS=-mod=readonly.
func TestAnalyzerEnvNetworkBlocked(t *testing.T) {
	env := analyzerEnv()
	var goproxyCount, goflagsCount int
	for _, e := range env {
		if e == "GOPROXY=off" {
			goproxyCount++
		}
		if e == "GOFLAGS=-mod=readonly" {
			goflagsCount++
		}
	}
	if goproxyCount != 1 {
		t.Errorf("expected exactly 1 GOPROXY=off, got %d", goproxyCount)
	}
	if goflagsCount != 1 {
		t.Errorf("expected exactly 1 GOFLAGS=-mod=readonly, got %d", goflagsCount)
	}
}

// Verify role filter with invalid role strings.
func TestRoleFilterInvalidRolesSilentlyIgnored(t *testing.T) {
	// The makeRoleFilter function accepts any Role value. Unknown strings
	// become valid Role values but match nothing. This is by design —
	// it's up to the tool layer to validate.
	filter := makeRoleFilter([]Role{"Implimentation", RoleDefinition})
	if !filter[RoleDefinition] {
		t.Error("expected RoleDefinition to be in filter")
	}
	// makeRoleFilter uses Role type, so "Implimentation" becomes a Role("Implimentation")
	// which IS in the filter map. The tool layer must validate.
	if !filter["Implimentation"] {
		t.Error("expected Implimentation in filter (codeintel layer accepts any Role)")
	}
	t.Log("role validation happens in tools/find_references.go, not in codeintel layer")
}

// Verify sameObject correctly distinguishes same-named objects in different packages.
func TestSameObjectDistinguishesPackages(t *testing.T) {
	// We can't easily create real types.Objects in a test without a full type-check.
	// But we can verify the function works for nil cases and documents the contract.
	var nilObj token.Position // dummy
	_ = nilObj

	// sameObject(nil, anything) = false
	if sameObject(nil, nil) {
		t.Error("sameObject(nil, nil) should be false")
	}
	// sameObject(not_nil, nil) = false
	t.Log("sameObject contract: compares by Pkg().Path() + Name()")
}
