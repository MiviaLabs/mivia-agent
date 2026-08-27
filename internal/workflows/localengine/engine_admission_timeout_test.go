package localengine

import (
	"testing"
	"time"
)

// TestAdmissionFetchTimeoutDefaults pins the fresh-admission origin-fetch
// bound: a zero or negative DeliveryTimeout falls back to 2 minutes, mirroring
// the deliver path (engine_deliver.go); a positive value passes through. The
// bound is what makes a hung or offline origin fail closed instead of blocking
// engine run creation forever.
func TestAdmissionFetchTimeoutDefaults(t *testing.T) {
	e := &Engine{}
	if got := e.admissionFetchTimeout(); got != 2*time.Minute {
		t.Fatalf("zero DeliveryTimeout bound = %v, want 2m", got)
	}
	e.DeliveryTimeout = -1
	if got := e.admissionFetchTimeout(); got != 2*time.Minute {
		t.Fatalf("negative DeliveryTimeout bound = %v, want 2m", got)
	}
	e.DeliveryTimeout = 250 * time.Millisecond
	if got := e.admissionFetchTimeout(); got != 250*time.Millisecond {
		t.Fatalf("positive DeliveryTimeout bound = %v, want 250ms", got)
	}
}
