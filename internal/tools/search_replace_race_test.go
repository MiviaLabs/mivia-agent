package tools

import (
	"context"
	"strings"
	"testing"
)

// TestSearchReplaceConcurrentEditsToDifferentPartsBothSurvive is the
// counterpart to TestSearchReplaceReapplyingIdenticalEditDoesNotDuplicate:
// that test covers two IDENTICAL edits (fixed by the alreadyApplied check),
// this one covers two DIFFERENT edits to the same file, run genuinely
// concurrently. Before the per-path lock in edit_lock.go, search_replace's
// unsynchronized read-modify-write let two such calls each read the same
// original content and race to write, silently dropping whichever wrote
// first (or, under raw concurrent O_TRUNC writes, corrupting the file
// outright - both observed while this test was RED). lockEditFile now
// serializes the two calls per path, so the second always reads the first's
// result: both edits must survive regardless of scheduling. Run several
// times since a race that is now fixed by a lock, rather than by removing
// the concurrency, should hold up under repetition, not just once.
func TestSearchReplaceConcurrentEditsToDifferentPartsBothSurvive(t *testing.T) {
	for i := 0; i < 20; i++ {
		_, reg := setupWS(t)
		mustExec(t, reg, "write_file", map[string]any{
			"path":    "f.go",
			"content": "func foo() {\n\treturn one\n}\n",
		})

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
		for j := 0; j < len(edits); j++ {
			if err := <-errs; err != nil {
				t.Fatalf("iteration %d: search_replace call %d failed: %v", i, j, err)
			}
		}

		got := mustExec(t, reg, "read_file", map[string]any{"path": "f.go"})
		hasFirst := strings.Contains(got, "return two")
		hasSecond := strings.Contains(got, "extra()")
		if !hasFirst || !hasSecond {
			t.Fatalf("iteration %d: lost update: one of two concurrent edits to different parts of the file was dropped (return two present=%v, extra() present=%v), content=%q", i, hasFirst, hasSecond, got)
		}
	}
}
