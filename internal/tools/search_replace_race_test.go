package tools

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// TestSearchReplaceConcurrentEditsCanLoseAnUpdate is the counterpart to
// TestSearchReplaceReapplyingIdenticalEditDoesNotDuplicate: that test covers
// two IDENTICAL edits (fixed by the alreadyApplied check), this one covers
// two DIFFERENT edits to the same file. search_replace reads the whole file,
// computes the replacement in memory, and writes the whole file back with no
// lock across the two steps. Two concurrent calls that both read before
// either writes will each compute their replacement against the same
// original content; whichever writes last overwrites the file with a
// version that never saw the other call's change, silently dropping it -
// the lost-update counterpart to the duplication bug, and not fixed by the
// idempotency check (both new_string values are genuinely new, so neither
// call short-circuits).
//
// afterSearchReplaceRead is a test-only seam (see write.go) used here to
// force a deterministic interleave - both calls block until they have both
// read the file, then are released to write at the same time - rather than
// relying on natural scheduling to occasionally hit the window.
func TestSearchReplaceConcurrentEditsCanLoseAnUpdate(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.go",
		"content": "func foo() {\n\treturn one\n}\n",
	})

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var readCount atomic.Int32
	afterSearchReplaceRead = func() {
		if readCount.Add(1) <= 2 {
			entered <- struct{}{}
			<-release
		}
	}
	t.Cleanup(func() { afterSearchReplaceRead = nil })

	edits := []map[string]any{
		{"path": "f.go", "old_string": "return one", "new_string": "return two"},
		{"path": "f.go", "old_string": "func foo() {", "new_string": "func foo() {\n\textra()"},
	}
	start := make(chan struct{})
	errs := make(chan error, len(edits))
	for _, edit := range edits {
		edit := edit
		go func() {
			<-start
			_, err := reg.Execute(context.Background(), "search_replace", mustJSON(t, edit))
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < len(edits); i++ {
		<-entered
	}
	close(release)
	for i := 0; i < len(edits); i++ {
		if err := <-errs; err != nil {
			t.Fatalf("search_replace call %d failed: %v", i, err)
		}
	}

	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.go"})
	hasFirst := strings.Contains(got, "return two")
	hasSecond := strings.Contains(got, "extra()")
	if !hasFirst || !hasSecond {
		t.Fatalf("lost update: one of two concurrent edits to different parts of the file was dropped (return two present=%v, extra() present=%v), content=%q", hasFirst, hasSecond, got)
	}
}
