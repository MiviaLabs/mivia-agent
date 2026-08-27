package sdkadapter

import (
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestApprovalStandingAllowDenyLookupSemantics pins the cache's core
// contract on the zero value (not just NewApprovalStanding): Lookup
// miss, Allow, Deny flip, and the allow/deny cross-delete. Also pins
// nil-receiver safety.
func TestApprovalStandingAllowDenyLookupSemantics(t *testing.T) {
	var s ApprovalStanding
	if _, ok := s.Lookup("x"); ok {
		t.Fatal("zero-value Lookup hit on an empty cache")
	}
	s.Allow("x", tools.ExecutionWrite)
	if approved, ok := s.Lookup("x"); !ok || !approved {
		t.Fatalf("after Allow: (%v,%v), want (true,true)", approved, ok)
	}
	s.Deny("x", tools.ExecutionWrite)
	if approved, ok := s.Lookup("x"); !ok || approved {
		t.Fatalf("after Deny: (%v,%v), want (false,true) — Deny must flip and cross-delete the allow entry", approved, ok)
	}
	s.Allow("x", tools.ExecutionWrite)
	if approved, ok := s.Lookup("x"); !ok || !approved {
		t.Fatalf("after re-Allow: (%v,%v), want (true,true) — Allow must cross-delete the deny entry", approved, ok)
	}
	var nilPtr *ApprovalStanding
	nilPtr.Allow("x", tools.ExecutionWrite)
	nilPtr.Deny("x", tools.ExecutionWrite)
	if _, ok := nilPtr.Lookup("x"); ok {
		t.Fatal("nil Lookup must miss, not panic")
	}
}

// TestApprovalStandingConcurrentAccess exercises mixed Allow/Deny/Lookup
// from concurrent goroutines; the mutex must hold under -race.
func TestApprovalStandingConcurrentAccess(t *testing.T) {
	s := NewApprovalStanding()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				name := "tool"
				if (w+i)%2 == 0 {
					s.Allow(name, tools.ExecutionWrite)
				} else {
					s.Deny(name, tools.ExecutionExternal)
				}
				s.Lookup(name)
			}
		}(w)
	}
	wg.Wait()
	if _, ok := s.Lookup("tool"); !ok {
		t.Fatal("after concurrent writes the entry must exist")
	}
}
