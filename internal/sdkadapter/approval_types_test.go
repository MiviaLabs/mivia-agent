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
	if _, ok := s.Lookup(StandingKey{Name: "x", Class: tools.ExecutionWrite}); ok {
		t.Fatal("zero-value Lookup hit on an empty cache")
	}
	s.Allow(StandingKey{Name: "x", Class: tools.ExecutionWrite})
	if approved, ok := s.Lookup(StandingKey{Name: "x", Class: tools.ExecutionWrite}); !ok || !approved {
		t.Fatalf("after Allow: (%v,%v), want (true,true)", approved, ok)
	}
	s.Deny(StandingKey{Name: "x", Class: tools.ExecutionWrite})
	if approved, ok := s.Lookup(StandingKey{Name: "x", Class: tools.ExecutionWrite}); !ok || approved {
		t.Fatalf("after Deny: (%v,%v), want (false,true) — Deny must flip and cross-delete the allow entry", approved, ok)
	}
	s.Allow(StandingKey{Name: "x", Class: tools.ExecutionWrite})
	if approved, ok := s.Lookup(StandingKey{Name: "x", Class: tools.ExecutionWrite}); !ok || !approved {
		t.Fatalf("after re-Allow: (%v,%v), want (true,true) — Allow must cross-delete the deny entry", approved, ok)
	}
	var nilPtr *ApprovalStanding
	nilPtr.Allow(StandingKey{Name: "x", Class: tools.ExecutionWrite})
	nilPtr.Deny(StandingKey{Name: "x", Class: tools.ExecutionWrite})
	if _, ok := nilPtr.Lookup(StandingKey{Name: "x", Class: tools.ExecutionWrite}); ok {
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
					s.Allow(StandingKey{Name: name, Class: tools.ExecutionWrite})
				} else {
					s.Deny(StandingKey{Name: name, Class: tools.ExecutionWrite})
				}
				s.Lookup(StandingKey{Name: name, Class: tools.ExecutionWrite})
			}
		}(w)
	}
	wg.Wait()
	if _, ok := s.Lookup(StandingKey{Name: "tool", Class: tools.ExecutionWrite}); !ok {
		t.Fatal("after concurrent writes the entry must exist")
	}
}
