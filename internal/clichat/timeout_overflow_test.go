package clichat

import (
	"math"
	"testing"
)

// TestTimeoutConversionsSaturateInsteadOfOverflow pins saturation on absurd
// configured seconds values at this package's config-to-Duration sites. A
// bare multiply by time.Second overflows to a negative Duration above
// ~9.2e9 seconds. totalTaskTimeout is the riskiest site: a negative total
// budget arms an already-expired context, so every subagent task would die
// at admission.
func TestTimeoutConversionsSaturateInsteadOfOverflow(t *testing.T) {
	huge := int(math.MaxInt64)

	if got := totalTaskTimeout(huge); got <= 0 {
		t.Fatalf("totalTaskTimeout(%d) = %v; want a positive saturated budget", huge, got)
	}
	if got := requestTimeout(huge); got <= 0 {
		t.Fatalf("requestTimeout(%d) = %v; want a positive saturated deadline", huge, got)
	}

	// The opt-out and default paths must keep their meaning.
	if got := totalTaskTimeout(-1); got != 0 {
		t.Fatalf("totalTaskTimeout(-1) = %v; want 0 (explicit opt-out)", got)
	}
	if got := totalTaskTimeout(0); got <= 0 {
		t.Fatalf("totalTaskTimeout(0) = %v; want the compiled default", got)
	}
}
