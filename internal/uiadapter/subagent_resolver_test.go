package uiadapter

import (
	"context"
	"sync"
	"testing"
)

// fakeContentResolver is a minimal toolCallContentResolver stand-in for
// tests. fn lets a test observe that resolver().LoadContent actually reaches
// the wired implementation, since interface values otherwise don't compare
// cleanly.
type fakeContentResolver struct {
	fn func(ctx context.Context, ref string) ([]byte, error)
}

func (f fakeContentResolver) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	return f.fn(ctx, ref)
}

// TestSubagentThreads_ResolverDefaultsNil pins the zero-value behavior: a
// SubagentThreads never given a resolver reports nil, so a later slice's
// caller can safely treat that as "no content resolver wired" rather than
// dereferencing a non-nil-but-useless value.
func TestSubagentThreads_ResolverDefaultsNil(t *testing.T) {
	threads := NewSubagentThreads()
	if got := threads.resolver(); got != nil {
		t.Fatalf("expected resolver() to be nil before SetContentResolver, got %#v", got)
	}
}

// TestSubagentThreads_SetContentResolverThenResolver pins that
// SetContentResolver actually wires the field resolver() reads back - proven
// by running the fake's LoadContent through the value resolver() returns
// and checking the wired fn ran, since interface values don't compare
// cleanly in general.
func TestSubagentThreads_SetContentResolverThenResolver(t *testing.T) {
	threads := NewSubagentThreads()

	var ran bool
	fake := fakeContentResolver{fn: func(ctx context.Context, ref string) ([]byte, error) {
		ran = true
		return []byte(ref), nil
	}}

	threads.SetContentResolver(fake)

	got := threads.resolver()
	if got == nil {
		t.Fatal("expected resolver() to be non-nil after SetContentResolver")
	}
	out, err := got.LoadContent(context.Background(), "ref-marker")
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if !ran {
		t.Fatal("expected the wired fn to have run")
	}
	if string(out) != "ref-marker" {
		t.Fatalf("got %q, want %q", out, "ref-marker")
	}
}

// TestSubagentThreads_ContentResolverConcurrencySafe proves
// SubagentThreads.mu actually guards the new contentResolver field: 20
// goroutines alternately call SetContentResolver and resolver()
// concurrently. Run with -race; a missing/insufficient lock around the new
// field would be flagged by the race detector.
func TestSubagentThreads_ContentResolverConcurrencySafe(t *testing.T) {
	threads := NewSubagentThreads()
	fakeA := fakeContentResolver{fn: func(context.Context, string) ([]byte, error) { return nil, nil }}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				threads.SetContentResolver(fakeA)
			} else {
				_ = threads.resolver()
			}
		}(i)
	}
	wg.Wait()
}
