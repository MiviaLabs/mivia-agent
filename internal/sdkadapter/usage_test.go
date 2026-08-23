package sdkadapter

import (
	"context"
	"errors"
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

// TestAccumulatorRecordRejectsBlankSessionID confirms Record refuses a
// blank sessionID with an error wrapping ErrBlankSessionID. The test
// covers both "" and a whitespace-only string because the SDK's
// Record uses strings.TrimSpace before the check.
func TestAccumulatorRecordRejectsBlankSessionID(t *testing.T) {
	acc := NewAccumulator()
	u := sdkshape.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CachedTokens: 4}
	for _, sid := range []string{"", "   "} {
		err := acc.Record(sid, u)
		if err == nil {
			t.Fatalf("Record(%q): want error, got nil", sid)
		}
		if !errors.Is(err, ErrBlankSessionID) {
			t.Fatalf("Record(%q): error %v does not wrap ErrBlankSessionID", sid, err)
		}
	}
}

// TestWrapCompleterRejectsNilAccumulator confirms WrapCompleter fails
// construction when the Accumulator pointer is nil, so a caller can
// errors.Is the result against ErrNilAccumulator.
func TestWrapCompleterRejectsNilAccumulator(t *testing.T) {
	wrapped, err := WrapCompleter("sid", nil, &fakeUsageCompleter{})
	if err == nil {
		t.Fatal("WrapCompleter with nil acc: want error, got nil")
	}
	if !errors.Is(err, ErrNilAccumulator) {
		t.Fatalf("WrapCompleter with nil acc: error %v does not wrap ErrNilAccumulator", err)
	}
	if wrapped != nil {
		t.Fatal("WrapCompleter with nil acc: want nil Completer on error")
	}
}

// TestWrapCompleterRejectsBlankSessionID confirms WrapCompleter fails
// construction when the sessionID is blank. The construction-time
// check fires before the SDK's Record guard, so callers see the error
// at Wrap time.
func TestWrapCompleterRejectsBlankSessionID(t *testing.T) {
	acc := NewAccumulator()
	wrapped, err := WrapCompleter("", acc, &fakeUsageCompleter{})
	if err == nil {
		t.Fatal("WrapCompleter with blank sessionID: want error, got nil")
	}
	if !errors.Is(err, ErrBlankSessionID) {
		t.Fatalf("WrapCompleter with blank sessionID: error %v does not wrap ErrBlankSessionID", err)
	}
	if wrapped != nil {
		t.Fatal("WrapCompleter with blank sessionID: want nil Completer on error")
	}
}

// TestWrapCompleterChatRecordsUsage confirms a wrapped Completer whose
// inner Chat returns a populated Response.Usage causes that Usage to
// land in the Accumulator under the sessionID supplied at wrap time.
func TestWrapCompleterChatRecordsUsage(t *testing.T) {
	acc := NewAccumulator()
	want := sdkshape.Usage{PromptTokens: 50, CompletionTokens: 25, TotalTokens: 75, CachedTokens: 10}
	inner := &fakeUsageCompleter{chatUsage: want}
	wrapped, err := WrapCompleter("sid", acc, inner)
	if err != nil {
		t.Fatalf("WrapCompleter: %v", err)
	}
	if _, err := wrapped.Chat(context.Background(), sdkshape.Request{}); err != nil {
		t.Fatalf("wrapped Chat: %v", err)
	}
	got, ok := acc.Total("sid")
	if !ok {
		t.Fatal("Total after Chat: want true, got false")
	}
	if got != want {
		t.Fatalf("Total after Chat: got %+v, want %+v", got, want)
	}
}

// TestWrapCompleterChatErrorDoesNotRecord confirms a wrapped Completer
// whose inner Chat returns an error does not record any usage. A
// failure that polluted the running total would silently corrupt
// per-session budgets.
func TestWrapCompleterChatErrorDoesNotRecord(t *testing.T) {
	acc := NewAccumulator()
	inner := &fakeUsageCompleter{chatErr: errors.New("boom")}
	wrapped, err := WrapCompleter("sid", acc, inner)
	if err != nil {
		t.Fatalf("WrapCompleter: %v", err)
	}
	if _, err := wrapped.Chat(context.Background(), sdkshape.Request{}); err == nil {
		t.Fatal("wrapped Chat: want error, got nil")
	}
	got, ok := acc.Total("sid")
	if ok {
		t.Fatalf("Total after Chat error: want false, got true (%+v)", got)
	}
}

// fakeUsageCompleter satisfies provider.Completer for the WrapCompleter
// tests. Only Chat is exercised; ChatStream is stubbed to satisfy the
// interface. chatErr takes precedence over chatUsage when set, so a
// single fake covers both the record and the error paths.
type fakeUsageCompleter struct {
	chatUsage sdkshape.Usage
	chatErr   error
}

func (f *fakeUsageCompleter) Name() string { return "fake" }
func (f *fakeUsageCompleter) Chat(_ context.Context, _ sdkshape.Request) (sdkshape.Response, error) {
	if f.chatErr != nil {
		return sdkshape.Response{}, f.chatErr
	}
	return sdkshape.Response{Usage: f.chatUsage}, nil
}
func (f *fakeUsageCompleter) ChatStream(_ context.Context, _ sdkshape.Request) (<-chan sdkshape.Chunk, error) {
	ch := make(chan sdkshape.Chunk)
	close(ch)
	return ch, nil
}
