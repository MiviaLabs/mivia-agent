package delivery

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// fakeRepo implements only the workflowledger.Repository surface the plan
// readers use (ListStepAttempts + LoadContent); every other method is the nil
// embedded interface and panics if touched, so a test that reaches one fails
// loudly instead of silently passing on stubbed state.
type fakeRepo struct {
	workflowledger.Repository
	attempts []workflowledger.StepAttempt
	content  map[string][]byte
}

func (f *fakeRepo) ListStepAttempts(_ context.Context, _ string) ([]workflowledger.StepAttempt, error) {
	return f.attempts, nil
}

func (f *fakeRepo) LoadContent(_ context.Context, ref string) ([]byte, error) {
	data, ok := f.content[ref]
	if !ok {
		return nil, workflowledger.ErrContentNotFound
	}
	return data, nil
}

// TestLoadStackPlanOutputUsesLatestSucceededDecompose pins that the plan
// readers select the LATEST succeeded decompose attempt that produced an
// output, not the first. The decompose_repair loop (chunk_plan_validate
// rejected -> decompose re-runs, compiler synthesis MaxIterations 3) leaves a
// plan run with MULTIPLE succeeded decompose attempts; the first holds the
// plan the deterministic gate REJECTED and the last holds the accepted plan.
// LoadStackPlanOutput must hand the driver the accepted plan, and
// DecomposedChunks (used by the delivery gate) must agree with it, or the
// stack is seeded/driven/verified against chunks the gate refused while the
// delivery gate counts different chunks.
func TestLoadStackPlanOutputUsesLatestSucceededDecompose(t *testing.T) {
	rejected := `{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"a","title":"rejected-a","files":["a.go"],"est_diff_lines":900,"tests":true},{"id":"b","title":"rejected-b","files":["b.go"],"est_diff_lines":900,"tests":true}],"has_more":false}}`
	accepted := `{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"c","title":"accepted","files":["c.go"],"est_diff_lines":40,"tests":true}],"has_more":false}}`
	repo := &fakeRepo{
		attempts: []workflowledger.StepAttempt{
			{AttemptID: "wfa-1", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: "ref:rejected"},
			// A succeeded attempt that produced no output artifact must never
			// shadow the accepted plan (both readers filter on OutputRef).
			{AttemptID: "wfa-2", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 2, Status: workflowledger.AttemptStatusSucceeded},
			{AttemptID: "wfa-3", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 3, Status: workflowledger.AttemptStatusSucceeded, OutputRef: "ref:accepted"},
		},
		content: map[string][]byte{
			"ref:rejected": []byte(rejected),
			"ref:accepted": []byte(accepted),
		},
	}

	data, err := LoadStackPlanOutput(context.Background(), repo, "wfr-plan")
	if err != nil {
		t.Fatalf("LoadStackPlanOutput: %v", err)
	}
	if string(data) != accepted {
		t.Fatalf("LoadStackPlanOutput returned the stale (gate-rejected) decompose plan; want the latest accepted plan")
	}

	n, ok := DecomposedChunks(context.Background(), repo, "wfr-plan")
	if !ok || n != 1 {
		t.Fatalf("DecomposedChunks = (%d, %v), want (1, true) from the accepted plan", n, ok)
	}
}

// TestLoadStackPlanOutputNoSucceededDecompose pins the fail-closed negative
// path: a plan run with no succeeded decompose output is an error for
// LoadStackPlanOutput and "not applicable" for DecomposedChunks, never a
// zero-chunk stack.
func TestLoadStackPlanOutputNoSucceededDecompose(t *testing.T) {
	repo := &fakeRepo{
		attempts: []workflowledger.StepAttempt{
			{AttemptID: "wfa-1", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 1, Status: workflowledger.AttemptStatusFailed, OutputRef: "ref:failed"},
		},
		content: map[string][]byte{"ref:failed": []byte(`{"stack_mode":"multi"}`)},
	}
	if _, err := LoadStackPlanOutput(context.Background(), repo, "wfr-plan"); err == nil {
		t.Fatalf("LoadStackPlanOutput succeeded with no succeeded decompose output; want an error")
	}
	if n, ok := DecomposedChunks(context.Background(), repo, "wfr-plan"); ok {
		t.Fatalf("DecomposedChunks = (%d, true), want (0, false) with no succeeded decompose output", n)
	}
}

// TestDecomposedChunksSkipsSucceededAttemptWithoutOutput pins the selection
// rule of DecomposedChunks: the LATEST succeeded decompose attempt with an
// OutputRef is authoritative. A later succeeded attempt that produced no
// output artifact must not shadow the recorded plan - the pre-fix loop took
// the last succeeded attempt regardless of OutputRef and returned (0, false)
// here, misclassifying the plan run as "not a stacking plan run" for the
// delivery gate.
func TestDecomposedChunksSkipsSucceededAttemptWithoutOutput(t *testing.T) {
	plan := `{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"a","title":"a","files":["a.go"],"est_diff_lines":40,"tests":true}],"has_more":false}}`
	repo := &fakeRepo{
		attempts: []workflowledger.StepAttempt{
			{AttemptID: "wfa-1", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: "ref:plan"},
			{AttemptID: "wfa-2", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 2, Status: workflowledger.AttemptStatusSucceeded},
		},
		content: map[string][]byte{"ref:plan": []byte(plan)},
	}
	n, ok := DecomposedChunks(context.Background(), repo, "wfr-plan")
	if !ok || n != 1 {
		t.Fatalf("DecomposedChunks = (%d, %v), want (1, true): a later succeeded attempt without an output must not shadow the recorded plan", n, ok)
	}
}

// TestDecomposedChunksSelectsLatestAttemptByAttemptNo pins that DecomposedChunks
// selects the authoritative decompose plan by AttemptNo, exactly like
// LoadStackPlanOutput, and never by iteration order. ListStepAttempts ordering
// by event sequence is a storage implementation detail, not part of the
// Repository contract, so a repository that returns attempts out of AttemptNo
// order must not make the delivery gate count chunks from a different plan
// than the driver seeds and drives. The pre-fix loop took the last matching
// attempt in slice order: with the accepted plan (AttemptNo 3) listed BEFORE
// the rejected plan (AttemptNo 1), it counted the rejected plan's chunks here
// while LoadStackPlanOutput kept serving the accepted plan.
func TestDecomposedChunksSelectsLatestAttemptByAttemptNo(t *testing.T) {
	rejected := `{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"a","title":"rejected-a","files":["a.go"],"est_diff_lines":900,"tests":true},{"id":"b","title":"rejected-b","files":["b.go"],"est_diff_lines":900,"tests":true}],"has_more":false}}`
	accepted := `{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"c","title":"accepted","files":["c.go"],"est_diff_lines":40,"tests":true}],"has_more":false}}`
	repo := &fakeRepo{
		// Iteration order deliberately does NOT follow AttemptNo: the accepted
		// plan (AttemptNo 3) comes first and the rejected plan (AttemptNo 1)
		// last, so a slice-order selection picks the wrong plan.
		attempts: []workflowledger.StepAttempt{
			{AttemptID: "wfa-3", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 3, Status: workflowledger.AttemptStatusSucceeded, OutputRef: "ref:accepted"},
			{AttemptID: "wfa-1", RunID: "wfr-plan", StepID: DecomposeStepID, AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: "ref:rejected"},
		},
		content: map[string][]byte{
			"ref:rejected": []byte(rejected),
			"ref:accepted": []byte(accepted),
		},
	}

	n, ok := DecomposedChunks(context.Background(), repo, "wfr-plan")
	if !ok || n != 1 {
		t.Fatalf("DecomposedChunks = (%d, %v), want (1, true): must count the latest decompose attempt by AttemptNo (the accepted plan), not the last in iteration order", n, ok)
	}

	// The two readers must agree on which plan is authoritative even when the
	// repository returns attempts out of AttemptNo order.
	data, err := LoadStackPlanOutput(context.Background(), repo, "wfr-plan")
	if err != nil {
		t.Fatalf("LoadStackPlanOutput: %v", err)
	}
	if string(data) != accepted {
		t.Fatalf("LoadStackPlanOutput returned the rejected plan; want the accepted plan")
	}
}

// TestTopologicalOrderPinsDependencyOrder covers the success path: deps come
// first, ties break lexicographically, and a diamond resolves once.
func TestTopologicalOrderPinsDependencyOrder(t *testing.T) {
	chunks := []ChunkPlan{
		{ID: "d", DependsOn: []string{"b", "c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "a"},
	}
	order, err := TopologicalOrder(chunks)
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}
	want := []string{"a", "b", "c", "d"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// TestTopologicalOrderRejectsDuplicateChunkIDs pins the fail-closed negative
// path for duplicated ids. Before the fix, two chunks sharing a zero-indegree
// id both landed in the ready queue, the loop emitted the id twice, and
// len(order) == len(chunks) still held, so TopologicalOrder silently returned
// a duplicated order ([a a]) with no error - duplicating the wave entry,
// inflating the "k/N" stack_part, and admitting the same chunk twice. A
// duplicated id with dependencies previously fell into the misleading
// "contains a cycle" branch instead of naming the duplicate. Both must now
// fail closed with a duplicate-id error.
func TestTopologicalOrderRejectsDuplicateChunkIDs(t *testing.T) {
	cases := []struct {
		name   string
		chunks []ChunkPlan
	}{
		{name: "zero-indegree duplicate", chunks: []ChunkPlan{
			{ID: "a"},
			{ID: "a"},
		}},
		{name: "duplicate with dependencies", chunks: []ChunkPlan{
			{ID: "a", DependsOn: []string{"b"}},
			{ID: "a"},
			{ID: "b"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := TopologicalOrder(tc.chunks); err == nil {
				t.Fatalf("TopologicalOrder accepted duplicate chunk id %q; want an error", "a")
			}
		})
	}
}

// TestTopologicalOrderRejectsCycleAndUnknownDep pins the other fail-closed
// inputs of the ordering contract, so the duplicate rejection cannot regress
// the existing cycle/unknown-dep guards.
func TestTopologicalOrderRejectsCycleAndUnknownDep(t *testing.T) {
	cycle := []ChunkPlan{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}
	if _, err := TopologicalOrder(cycle); err == nil {
		t.Fatalf("TopologicalOrder accepted a cyclic graph; want an error")
	}
	unknown := []ChunkPlan{
		{ID: "a", DependsOn: []string{"ghost"}},
	}
	if _, err := TopologicalOrder(unknown); err == nil {
		t.Fatalf("TopologicalOrder accepted an unknown dependency; want an error")
	}
}

// chunksFromSeed decodes a fuzz seed into a chunk list: each ':'-separated
// part is one chunk whose first character is its id (one of a-d) and whose
// remaining characters are its dependencies. Unknown characters are dropped,
// so the generator can only exercise the ordering contract's own inputs.
func chunksFromSeed(data []byte) []ChunkPlan {
	var chunks []ChunkPlan
	for _, part := range strings.Split(string(data), ":") {
		if part == "" {
			continue
		}
		id := part[:1]
		if !strings.ContainsRune("abcd", rune(id[0])) {
			continue
		}
		c := ChunkPlan{ID: id}
		for _, d := range part[1:] {
			if strings.ContainsRune("abcd", d) {
				c.DependsOn = append(c.DependsOn, string(d))
			}
		}
		chunks = append(chunks, c)
	}
	return chunks
}

// FuzzTopologicalOrder pins the ordering contract under adversarial chunk
// lists: no panic, and a nil error must mean the order is a permutation of
// the chunk ids - every id exactly once, none missing, none duplicated. The
// pre-fix duplicate bug returned a nil error with the id emitted twice; the
// seeds cover the valid diamond, a dependency chain, duplicates, a cycle, and
// an unknown dependency. Run with: go test -fuzz=FuzzTopologicalOrder
// -fuzztime=30s ./internal/workflows/stacking
func FuzzTopologicalOrder(f *testing.F) {
	for _, seed := range []string{
		"a:b:c:",       // three independent chunks
		"b:a::",        // b depends on a
		"a:a::",        // duplicate id, zero indegree
		"a:b:b:a:",     // cycle a <-> b
		"a:bd:",        // b depends on undefined d
		"d:c:b:a::",    // chain
		"a:c:d:b:c:a:", // diamond with duplicate deps
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		chunks := chunksFromSeed(data)
		order, err := TopologicalOrder(chunks)
		if err != nil {
			return
		}
		if len(order) != len(chunks) {
			t.Fatalf("order %v has %d entries for %d chunks", order, len(order), len(chunks))
		}
		seen := make(map[string]bool, len(order))
		for _, id := range order {
			if seen[id] {
				t.Fatalf("chunk id %q appears more than once in order %v", id, order)
			}
			seen[id] = true
		}
		for _, c := range chunks {
			if !seen[c.ID] {
				t.Fatalf("chunk id %q missing from order %v", c.ID, order)
			}
		}
	})
}

// TestStatusIsTerminalCoversCanceled pins the terminal vocabulary: a canceled
// chunk (a dependent given up on after its dependency failed) is terminal, so
// no drive pass re-admits or re-marks it.
func TestStatusIsTerminalCoversCanceled(t *testing.T) {
	for _, s := range []string{StatusMerged, StatusFailed, StatusSkipped, StatusCanceled} {
		if !StatusIsTerminal(s) {
			t.Fatalf("StatusIsTerminal(%q) = false, want true", s)
		}
	}
	for _, s := range []string{StatusPlanned, StatusQueued, StatusBlocked, StatusRunning, StatusImplemented, StatusReviewed, StatusPublished, StatusReopened} {
		if StatusIsTerminal(s) {
			t.Fatalf("StatusIsTerminal(%q) = true, want false", s)
		}
	}
	if StatusIsAdmissiblePre(StatusCanceled) {
		t.Fatal("StatusIsAdmissiblePre(canceled) = true; a canceled chunk must never be admitted")
	}
}
