package sdkadapter

import (
	"testing"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestAccumulatorNewReturnsNonNil confirms NewAccumulator always
// returns a usable Accumulator, not a nil pointer.
func TestAccumulatorNewReturnsNonNil(t *testing.T) {
	acc := NewAccumulator()
	if acc == nil {
		t.Fatal("NewAccumulator returned nil")
	}
}

// TestAccumulatorRecordTotalRoundTrip pins the Record -> Total
// round-trip: each of the four Usage fields is summed across two
// Record calls, and Total returns the sum.
func TestAccumulatorRecordTotalRoundTrip(t *testing.T) {
	acc := NewAccumulator()
	first := sdkshape.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, CachedTokens: 5}
	second := sdkshape.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 11, CachedTokens: 2}
	if err := acc.Record("sid", first); err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if err := acc.Record("sid", second); err != nil {
		t.Fatalf("Record second: %v", err)
	}
	got, ok := acc.Total("sid")
	if !ok {
		t.Fatal("Total: want true, got false")
	}
	want := sdkshape.Usage{PromptTokens: 17, CompletionTokens: 23, TotalTokens: 41, CachedTokens: 7}
	if got != want {
		t.Fatalf("Total = %+v, want %+v", got, want)
	}
}

// TestAccumulatorTotalUnknownSessionReturnsFalse confirms that Total
// for a never-recorded sessionID returns the zero Usage with ok=false.
func TestAccumulatorTotalUnknownSessionReturnsFalse(t *testing.T) {
	acc := NewAccumulator()
	got, ok := acc.Total("never-recorded")
	if ok {
		t.Fatal("Total: want false, got true")
	}
	if got != (sdkshape.Usage{}) {
		t.Fatalf("Total: got %+v, want zero Usage", got)
	}
}

// TestAccumulatorResetClearsSession confirms Reset wipes the session's
// running total: a Record then Reset leaves Total returning
// (zero, false), and a second Reset is a no-op that returns nil.
func TestAccumulatorResetClearsSession(t *testing.T) {
	acc := NewAccumulator()
	u := sdkshape.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CachedTokens: 4}
	if err := acc.Record("sid", u); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := acc.Reset("sid"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got, ok := acc.Total("sid")
	if ok {
		t.Fatal("Total after Reset: want false, got true")
	}
	if got != (sdkshape.Usage{}) {
		t.Fatalf("Total after Reset: got %+v, want zero Usage", got)
	}
	if err := acc.Reset("sid"); err != nil {
		t.Fatalf("second Reset on cleared session: %v", err)
	}
}
