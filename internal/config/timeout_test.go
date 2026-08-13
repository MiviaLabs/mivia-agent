package config

import (
	"testing"
	"time"
)

func TestEffectiveTimeoutSec(t *testing.T) {
	if got := EffectiveTimeoutSec(0); got != DefaultOrchestrationTimeoutSec {
		t.Fatalf("zero config: got %d want %d", got, DefaultOrchestrationTimeoutSec)
	}
	if got := EffectiveTimeoutSec(120); got != 120 {
		t.Fatalf("configured: got %d want 120", got)
	}
	if got := EffectiveTimeoutSec(60, 0, 300, 90); got != 300 {
		t.Fatalf("max override: got %d want 300", got)
	}
	if got := EffectiveTimeoutSec(600, 60); got != 600 {
		t.Fatalf("shorter override must not shrink the configured floor: got %d want 600", got)
	}
	if got := EffectiveTimeoutSec(0, 60); got != DefaultOrchestrationTimeoutSec {
		t.Fatalf("floor wins over smaller override: got %d want %d", got, DefaultOrchestrationTimeoutSec)
	}
	if got := EffectiveTimeoutSec(43200, 90000); got != 90000 {
		t.Fatalf("larger override still raises: got %d want 90000", got)
	}
	if got := EffectiveTimeoutSec(0, 0); got != DefaultOrchestrationTimeoutSec {
		t.Fatalf("all zero: got %d want ceiling", got)
	}
}

// TestEffectiveTimeoutSecClampsOverflow pins the overflow guard for a huge
// model-supplied timeout_seconds. 10^10 s parses from JSON and fits int64, but
// time.Duration(10^10)*time.Second = 10^19 ns > MaxInt64 (9.22e18), which wraps
// negative and collapses the whole-call budget below the operator floor.
// EffectiveTimeoutSec must clamp to MaxTimeoutSeconds so every downstream
// time.Duration(n)*time.Second stays positive while raise-only semantics are
// preserved up to 10 years.
func TestEffectiveTimeoutSecClampsOverflow(t *testing.T) {
	huge := 10_000_000_000 // 10^10 s: fits int64, parses from JSON, overflows ns
	if got := EffectiveTimeoutSec(0, huge); got != MaxTimeoutSeconds {
		t.Fatalf("EffectiveTimeoutSec(0, huge) = %d, want MaxTimeoutSeconds %d (not %d)", got, MaxTimeoutSeconds, huge)
	}
	if got := EffectiveTimeoutSec(600, huge); got != MaxTimeoutSeconds {
		t.Fatalf("EffectiveTimeoutSec(600, huge) = %d, want MaxTimeoutSeconds %d", got, MaxTimeoutSeconds)
	}
	if d := time.Duration(EffectiveTimeoutSec(0, huge)) * time.Second; d <= 0 {
		t.Fatalf("clamped timeout overflows time.Duration: %v", d)
	}
}

// TestRequestedTimeoutSec pins the key behavior change: an explicit positive
// timeout_seconds IS the budget — it is not floored to the configured default
// or DefaultOrchestrationTimeoutSec. This is what lets the root orchestrator
// bound a dispatch_tasks/delegate/spawn_agent call to a shorter window than
// the 12h default. When explicit is 0 or negative, the configured default
// applies via EffectiveTimeoutSec (backward-compatible fallback). Per-task
// overrides may still raise the budget above the explicit value.
func TestRequestedTimeoutSec(t *testing.T) {
	// Explicit positive is honored directly, not floored.
	if got := RequestedTimeoutSec(43200, 600); got != 600 {
		t.Fatalf("explicit 600 with configured 43200: got %d want 600 (not floored to default)", got)
	}
	// Explicit 0 falls back to the configured default.
	if got := RequestedTimeoutSec(43200, 0); got != 43200 {
		t.Fatalf("explicit 0 with configured 43200: got %d want 43200", got)
	}
	// Explicit 0 with unconfigured default falls back to 12h.
	if got := RequestedTimeoutSec(0, 0); got != DefaultOrchestrationTimeoutSec {
		t.Fatalf("both zero: got %d want %d", got, DefaultOrchestrationTimeoutSec)
	}
	// Negative explicit treated as 0 (fallback to configured).
	if got := RequestedTimeoutSec(600, -1); got != 600 {
		t.Fatalf("explicit -1 with configured 600: got %d want 600", got)
	}
	// Per-task override can raise above the explicit batch value.
	if got := RequestedTimeoutSec(43200, 600, 900); got != 900 {
		t.Fatalf("task override raises above explicit: got %d want 900", got)
	}
	// Per-task override below the explicit batch value does not shrink it.
	if got := RequestedTimeoutSec(43200, 600, 300); got != 600 {
		t.Fatalf("smaller task override must not shrink explicit budget: got %d want 600", got)
	}
	// Huge explicit clamps to MaxTimeoutSeconds (overflow safety).
	huge := 10_000_000_000
	if got := RequestedTimeoutSec(43200, huge); got != MaxTimeoutSeconds {
		t.Fatalf("huge explicit: got %d want MaxTimeoutSeconds %d", got, MaxTimeoutSeconds)
	}
	if d := time.Duration(RequestedTimeoutSec(0, huge)) * time.Second; d <= 0 {
		t.Fatalf("clamped timeout overflows time.Duration: %v", d)
	}
}
