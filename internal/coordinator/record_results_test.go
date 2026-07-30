package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// storeContentFailingRepo is a ledger repository whose content writes always
// fail. Everything else behaves like the in-memory repository.
type storeContentFailingRepo struct {
	*ledger.MemoryLedgerRepository
}

var errStoreContentUnavailable = errors.New("content store unavailable")

func (storeContentFailingRepo) StoreContent(_ context.Context, _ string, _ []byte) error {
	return errStoreContentUnavailable
}

func TestStoreContentFailureBlocksRef(t *testing.T) {
	repo := storeContentFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"secret":"data"}`)}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "test"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	task, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	// The invariant: a reference handed to the model resolves, or it is not
	// handed to the model. The content write failed, so no reference may be
	// recorded on the task.
	if task.OutputRef != "" {
		t.Fatalf("output ref %q recorded despite failed content write", task.OutputRef)
	}

	// The dropped reference must not be silent: the failure surfaces on the run.
	if result.Err == nil {
		t.Fatal("content store failure was silent, want run error")
	}
	if !strings.Contains(result.Err.Error(), errStoreContentUnavailable.Error()) {
		t.Fatalf("run error = %v, want it to mention the content store failure", result.Err)
	}
}

// TestRecoveredFailedTaskWithoutRefStillReportsError pins that a recovered
// non-successful task always carries an Err, even when no error content
// reference exists. persistResultContent blanks the ref when the content write
// fails, so gating the synthesized error on the ref made a failed task replay
// with Err == nil: a caller (and the model reading the join result) then saw
// neither an error nor an error_ref for a task that demonstrably failed.
func TestRecoveredFailedTaskWithoutRefStillReportsError(t *testing.T) {
	results := resultsFromSnapshots([]ledger.TaskSnapshot{
		{TaskID: "ok", Status: string(ledger.TaskStatusCompleted)},
		{TaskID: "boom", Status: string(ledger.TaskStatusFailed), ErrorRef: ""},
		{TaskID: "slow", Status: string(ledger.TaskStatusTimedOut), ErrorRef: ""},
	})
	if len(results) != 3 {
		t.Fatalf("results = %+v, want 3", results)
	}
	if results[0].Err != nil {
		t.Errorf("completed task Err = %v, want nil", results[0].Err)
	}
	for _, i := range []int{1, 2} {
		got := results[i]
		if got.Err == nil {
			t.Fatalf("task %q (status %q) Err = nil, want a non-nil failure description",
				got.TaskID, got.Status)
		}
		if !strings.Contains(got.Err.Error(), got.Status) {
			t.Errorf("task %q error %q does not name the status %q", got.TaskID, got.Err, got.Status)
		}
		// The message must not present a reference clause when no ref exists.
		if strings.Contains(got.Err.Error(), "(error content reference ") {
			t.Errorf("task %q error %q claims a reference it does not have", got.TaskID, got.Err)
		}
	}
}

func TestResultReferencesUseCanonicalFullDigest(t *testing.T) {
	outputRef, errorRef := resultReferences(subagents.Result{Output: []byte("payload")})
	if errorRef != "" {
		t.Fatalf("error ref = %q, want empty for a result with no error", errorRef)
	}
	want := ledger.Reference(ledger.RefKindOutput, []byte("payload"))
	if outputRef != want {
		t.Fatalf("output ref = %q, want canonical %q", outputRef, want)
	}
	// Regression proof that the digest[:8] truncation is gone: the digest
	// segment is a full 64-hex-character SHA-256, so the ref is the key the
	// content is actually stored under.
	_, digest, err := ledger.ParseReference(outputRef)
	if err != nil {
		t.Fatalf("ParseReference(%q) = %v, want canonical reference", outputRef, err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest %q has %d chars, want 64 (truncated digest regression)", digest, len(digest))
	}
}
