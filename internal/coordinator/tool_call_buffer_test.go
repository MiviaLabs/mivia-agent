package coordinator

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestSinkForNilBufferAndLazyProgress pins two defensive arms of the sink
// factory: a nil buffer hands back a callable no-op sink, and a zero-value
// buffer lazily creates its progress map on the first event instead of
// panicking (the constructor pre-initializes the map; this path protects
// any future construction site that forgets to).
func TestSinkForNilBufferAndLazyProgress(t *testing.T) {
	var nilBuf *runToolCallBuffer
	nilBuf.sinkFor("t")(subagents.ToolCallStep{ToolCallID: "1", Name: "grep", Kind: "start"})

	b := &runToolCallBuffer{}
	b.sinkFor("t")(subagents.ToolCallStep{ToolCallID: "1", Name: "grep", Kind: "start", At: time.Now()})

	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.progress["t"]
	if p == nil {
		t.Fatal("first event on a zero-value buffer created no progress entry")
	}
	if p.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", p.ToolCalls)
	}
}

// TestRunToolCallBufferPerTaskIsolation verifies that sinkFor/flush demux
// concurrently-written steps per taskID with zero cross-contamination, under
// -race.
func TestRunToolCallBufferPerTaskIsolation(t *testing.T) {
	b := newRunToolCallBuffer()
	sinkA := b.sinkFor("a")
	sinkB := b.sinkFor("b")

	const n = 20
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			sinkA(subagents.ToolCallStep{ToolCallID: fmt.Sprintf("a-%d", i), Name: "toolA"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			sinkB(subagents.ToolCallStep{ToolCallID: fmt.Sprintf("b-%d", i), Name: "toolB"})
		}
	}()
	wg.Wait()

	gotA := b.flush("a")
	gotB := b.flush("b")

	if len(gotA) != n {
		t.Fatalf("flush(a) len = %d, want %d", len(gotA), n)
	}
	if len(gotB) != n {
		t.Fatalf("flush(b) len = %d, want %d", len(gotB), n)
	}
	for i, s := range gotA {
		if s.ToolCallID != fmt.Sprintf("a-%d", i) || s.Name != "toolA" {
			t.Fatalf("gotA[%d] = %+v, want ToolCallID=a-%d Name=toolA", i, s, i)
		}
	}
	for i, s := range gotB {
		if s.ToolCallID != fmt.Sprintf("b-%d", i) || s.Name != "toolB" {
			t.Fatalf("gotB[%d] = %+v, want ToolCallID=b-%d Name=toolB", i, s, i)
		}
		if strings.HasPrefix(s.ToolCallID, "a-") {
			t.Fatalf("gotB contains an A step: %+v", s)
		}
	}

	// A second flush must return empty (buffer cleared).
	if got := b.flush("a"); len(got) != 0 {
		t.Fatalf("second flush(a) = %v, want empty", got)
	}
}

// TestRunToolCallBufferStepCap verifies boundary behavior around
// bufferMaxStepsPerTask: pushing 0, 1, cap-1, cap, and cap+1 steps yields
// min(pushed, cap) steps back from flush.
func TestRunToolCallBufferStepCap(t *testing.T) {
	cases := []int{0, 1, bufferMaxStepsPerTask - 1, bufferMaxStepsPerTask, bufferMaxStepsPerTask + 1}
	for _, pushed := range cases {
		t.Run(fmt.Sprintf("pushed=%d", pushed), func(t *testing.T) {
			b := newRunToolCallBuffer()
			sink := b.sinkFor("t")
			for i := 0; i < pushed; i++ {
				sink(subagents.ToolCallStep{ToolCallID: fmt.Sprintf("s-%d", i)})
			}
			want := pushed
			if want > bufferMaxStepsPerTask {
				want = bufferMaxStepsPerTask
			}
			got := b.flush("t")
			if len(got) != want {
				t.Fatalf("pushed=%d: flush returned %d steps, want %d", pushed, len(got), want)
			}
		})
	}
}

// TestRunToolCallBufferByteCap verifies that steps whose cumulative
// Input+Output size would exceed bufferMaxBytesPerTask are dropped, while
// steps that keep the running total at or under the cap are kept.
func TestRunToolCallBufferByteCap(t *testing.T) {
	b := newRunToolCallBuffer()
	sink := b.sinkFor("t")

	// Fill to just under the cap with one big step.
	big := strings.Repeat("x", bufferMaxBytesPerTask-10)
	sink(subagents.ToolCallStep{ToolCallID: "big", Input: big})

	// A step that fits within the remaining 10 bytes is kept.
	sink(subagents.ToolCallStep{ToolCallID: "fits", Input: strings.Repeat("y", 10)})

	// A step that would push the total over the cap is dropped.
	sink(subagents.ToolCallStep{ToolCallID: "overflow", Input: "z"})

	got := b.flush("t")
	if len(got) != 2 {
		t.Fatalf("flush returned %d steps, want 2 (big, fits); got %+v", len(got), got)
	}
	if got[0].ToolCallID != "big" || got[1].ToolCallID != "fits" {
		t.Fatalf("unexpected steps: %+v", got)
	}
}

// TestRunToolCallBufferByteCapBoundaryValues verifies the byte cap at exact
// boundary totals: a single step whose Input size is bufferMaxBytesPerTask-1,
// bufferMaxBytesPerTask, or bufferMaxBytesPerTask+1 bytes is kept only when
// it does not exceed the cap ("+stepBytes > bufferMaxBytesPerTask" in
// sinkFor is a strict greater-than, so a step landing exactly on the cap is
// kept). This complements TestRunToolCallBufferStepCap's already-covered
// step-count boundaries (0/1/cap-1/cap/cap+1) with the analogous set for the
// independent byte-total cap, which chunk 4's TestRunToolCallBufferByteCap
// exercised only via a fits/overflow scenario, not these exact totals.
func TestRunToolCallBufferByteCapBoundaryValues(t *testing.T) {
	cases := []struct {
		name  string
		bytes int
		kept  bool
	}{
		{"zero", 0, true},
		{"one", 1, true},
		{"cap-1", bufferMaxBytesPerTask - 1, true},
		{"cap", bufferMaxBytesPerTask, true},
		{"cap+1", bufferMaxBytesPerTask + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newRunToolCallBuffer()
			sink := b.sinkFor("t")
			sink(subagents.ToolCallStep{ToolCallID: "s", Input: strings.Repeat("x", tc.bytes)})
			got := b.flush("t")
			if tc.kept && len(got) != 1 {
				t.Fatalf("bytes=%d: flush returned %d steps, want 1 (kept)", tc.bytes, len(got))
			}
			if !tc.kept && len(got) != 0 {
				t.Fatalf("bytes=%d: flush returned %d steps, want 0 (dropped)", tc.bytes, len(got))
			}
		})
	}
}

// TestRunToolCallBufferByteCapPoisonsOrphanedEnd is a RED test for Finding 2
// of the Part B hostile bug audit: the byte cap drops a step purely on its
// own marginal size, with no atomicity between a call's start and end. A
// large "start" can be dropped near the budget ceiling while a later, small
// "end" for the SAME ToolCallID still fits under the (now higher, post-drop)
// remaining budget and gets kept — producing a merged summary downstream
// with a real Output but empty Name/Input and Incomplete=false: a "false
// completeness" artifact. The fix must poison a ToolCallID once any of its
// raw events are capped, so a call is always fully present, fully absent, or
// correctly start-only (never end-only) in the buffer.
func TestRunToolCallBufferByteCapPoisonsOrphanedEnd(t *testing.T) {
	b := newRunToolCallBuffer()
	sink := b.sinkFor("t")

	// Fill to just under the cap so the next (large) step is dropped by the
	// byte cap.
	filler := strings.Repeat("x", bufferMaxBytesPerTask-10)
	sink(subagents.ToolCallStep{ToolCallID: "filler", Input: filler})

	// A large "start" for call "x" is dropped by the byte cap (only 10 bytes
	// of budget remain).
	sink(subagents.ToolCallStep{ToolCallID: "x", Kind: "start", Input: strings.Repeat("s", 50)})

	// A small "end" for the SAME call "x" would fit under the cap on its own
	// marginal size, but must be dropped too now that "x" is poisoned.
	sink(subagents.ToolCallStep{ToolCallID: "x", Kind: "end", Output: "y"})

	got := b.flush("t")
	for _, step := range got {
		if step.ToolCallID == "x" {
			t.Fatalf("flush(t) contains a step for poisoned call %q: %+v; the orphaned end must be dropped alongside its capped start", "x", step)
		}
	}
	if len(got) != 1 || got[0].ToolCallID != "filler" {
		t.Fatalf("flush(t) = %+v, want only the filler step", got)
	}
}

// TestRunToolCallBufferFlushNilSafe verifies flush is nil-safe.
func TestRunToolCallBufferFlushNilSafe(t *testing.T) {
	var b *runToolCallBuffer
	if got := b.flush("anything"); got != nil {
		t.Fatalf("nil buffer flush = %v, want nil", got)
	}
}
