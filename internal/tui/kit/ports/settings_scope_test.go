package ports

import "testing"

// TestScopeIotaOrderStable pins the numeric order: serialized scopes (and
// any persisted int) must not shift when new scopes append. Kill mutation:
// reorder the Scope iota block.
func TestScopeIotaOrderStable(t *testing.T) {
	if ScopeUser != 0 || ScopeProject != 1 || ScopeBuiltin != 2 {
		t.Fatalf("scope iota shifted: user=%d project=%d builtin=%d", ScopeUser, ScopeProject, ScopeBuiltin)
	}
	if ScopeBuiltin.String() != "builtin" {
		t.Fatalf("ScopeBuiltin.String() = %q, want builtin", ScopeBuiltin.String())
	}
}
