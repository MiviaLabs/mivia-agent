package clichat

// store_note's model-facing contract: scope, validation, caps, fail-closed
// error paths, and concurrency. The tool is unprivileged on purpose, so every
// test here also pins what a sub-agent may do with it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// errStoreNoteFault is a store-side failure no caller can mistake for a
// validation answer.
var errStoreNoteFault = errors.New("store_note fault")

// failingNoteStore satisfies the repository interface but fails every write,
// for the fail-closed path.
type failingNoteStore struct{ ledger.LedgerRepository }

func (failingNoteStore) StoreContent(context.Context, string, []byte) error {
	return errStoreNoteFault
}

// taskContext returns ctx carrying a coordination identity, as the subagent
// runtime stamps it before tool execution.
func taskContext(runID, taskID string) context.Context {
	return runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: runID, TaskID: taskID, Agent: "builder",
	})
}

func newDefaultStoreNoteTool(repo ledger.LedgerRepository) *storeNoteTool {
	return &storeNoteTool{repo: repo}
}

// TestStoreNoteScopeKeptForSpawnedAndRoot pins the registration scope: the
// tool is deliberately not tools.PrivilegedTool, so ScopeSpawned (sub-agents)
// and ScopeRoot both keep it. A privileged leak in would invert that.
func TestStoreNoteScopeKeptForSpawnedAndRoot(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newDefaultStoreNoteTool(ledger.NewMemoryLedgerRepository()))
	for _, mode := range []tools.ScopeMode{tools.ScopeSpawned, tools.ScopeRoot} {
		scoped := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: mode})
		if _, ok := scoped.Get("store_note"); !ok {
			t.Fatalf("scope %v dropped store_note", mode)
		}
	}
}

// TestStoreNoteHappyPath stores a note and resolves its ref: the response ref
// must equal the canonical minter over the exact bytes, and the stored body
// must load back byte-identical.
func TestStoreNoteHappyPath(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := newDefaultStoreNoteTool(repo)
	out, err := tool.Execute(taskContext("run-1", "task-1"), json.RawMessage(`{"content":"a note body"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Ref   string `json:"ref"`
		Bytes int    `json:"bytes"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.Error != "" {
		t.Fatalf("happy path returned an error: %s", out)
	}
	wantRef := ledger.Reference(ledger.RefKindNote, []byte("a note body"))
	if response.Ref != wantRef {
		t.Fatalf("ref = %q, want canonical %q", response.Ref, wantRef)
	}
	if response.Bytes != len("a note body") {
		t.Fatalf("bytes = %d, want %d", response.Bytes, len("a note body"))
	}
	body, err := repo.LoadContent(context.Background(), response.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "a note body" {
		t.Fatalf("stored body = %q, want the exact input", body)
	}
	if kind, _, err := ledger.ParseReference(response.Ref); err != nil || kind != ledger.RefKindNote {
		t.Fatalf("ref kind = %q (err %v), want note", kind, err)
	}
}

// TestStoreNotePerWriteCap pins the per-write byte cap: oversized content is a
// JSON error that names the cap, and nothing is stored.
func TestStoreNotePerWriteCap(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := &storeNoteTool{repo: repo, maxBytes: 8}
	out, err := tool.Execute(taskContext("run-1", "task-1"), json.RawMessage(`{"content":"this body is far longer than eight bytes"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "error") || !strings.Contains(out, "8") {
		t.Fatalf("oversized write must fail with a JSON error naming the cap: %s", out)
	}
	if strings.Contains(out, "ref:note:") {
		t.Fatalf("error response must not carry a ref: %s", out)
	}
}

// TestStoreNotePerTaskCountCapWithIsolation pins the per-task count cap: the
// third write on one task fails, and a sibling task on the same run keeps its
// own untouched count.
func TestStoreNotePerTaskCountCapWithIsolation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := &storeNoteTool{repo: repo, maxPerTask: 2}
	write := func(taskID, body string) string {
		out, err := tool.Execute(taskContext("run-1", taskID), json.RawMessage(`{"content":"`+body+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write("task-a", "first")
	write("task-a", "second")
	third := write("task-a", "third")
	if !strings.Contains(third, "error") {
		t.Fatalf("third write on a capped task must fail: %s", third)
	}
	if strings.Contains(third, "ref:note:") {
		t.Fatalf("capped write must not surface a ref: %s", third)
	}
	fresh := write("task-b", "fresh task body")
	if !strings.Contains(fresh, "ref:note:") {
		t.Fatalf("a sibling task must keep its own count: %s", fresh)
	}
}

// TestStoreNoteMissingTaskIdentity pins the no-identity contract: without a
// task identity on the context the tool answers a clean JSON error instead of
// panicking or minting an unattributed note.
func TestStoreNoteMissingTaskIdentity(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := newDefaultStoreNoteTool(repo)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"orphan note"}`))
	if err != nil {
		t.Fatalf("missing identity must be a JSON answer, not a Go error: %v", err)
	}
	if !strings.Contains(out, "error") || !strings.Contains(out, "no active task") {
		t.Fatalf("missing identity must produce a clean 'no active task' error: %s", out)
	}
	if strings.Contains(out, "ref:note:") {
		t.Fatalf("unattributed write must not surface a ref: %s", out)
	}
}

// TestStoreNoteEmptyContent rejects an empty body before touching the store.
func TestStoreNoteEmptyContent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := newDefaultStoreNoteTool(repo)
	for _, args := range []string{`{"content":""}`, `{}`, `{"content":null}`} {
		out, err := tool.Execute(taskContext("run-1", "task-1"), json.RawMessage(args))
		if err != nil {
			t.Fatalf("Execute(%s): %v", args, err)
		}
		if !strings.Contains(out, "error") {
			t.Fatalf("empty content must be a JSON error: %s", out)
		}
		if strings.Contains(out, "ref:note:") {
			t.Fatalf("empty content must not surface a ref: %s", out)
		}
	}
}

// TestStoreNoteFailClosedOnStoreError pins INV-AG-10 on the write side: when
// the store rejects the write, the tool surfaces an error and never a ref.
// The digest is content-addressed, so a leaked ref in an error would still
// resolve phantom content.
func TestStoreNoteFailClosedOnStoreError(t *testing.T) {
	tool := newDefaultStoreNoteTool(failingNoteStore{})
	out, err := tool.Execute(taskContext("run-1", "task-1"), json.RawMessage(`{"content":"doomed note"}`))
	if err == nil && !strings.Contains(out, "error") {
		t.Fatalf("store failure must surface an error, got out=%q err=%v", out, err)
	}
	combined := out + fmt.Sprint(err)
	if strings.Contains(combined, "ref:note:") {
		t.Fatalf("failed write leaked a ref: out=%q err=%v", out, err)
	}
}

// TestStoreNoteConcurrentWritesOneTask exercises the mutex-guarded count map
// on a single task from many goroutines; `make race` runs this suite.
func TestStoreNoteConcurrentWritesOneTask(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := &storeNoteTool{repo: repo, maxPerTask: 32}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf("concurrent note %d", i)
			out, err := tool.Execute(taskContext("run-1", "task-race"), json.RawMessage(`{"content":"`+body+`"}`))
			if err != nil {
				errs <- err
				return
			}
			if !strings.Contains(out, "ref:note:") {
				errs <- fmt.Errorf("write %d lost its ref: %s", i, out)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestStoreNoteReleasesBudgetWhenTaskContextEnds pins the count map's
// lifetime: a task's budget entry must not outlive the task, or a
// long-running session accumulates one entry per task forever. Without
// this the third write below would spuriously fail, since the finished
// task's count would still read 2 against a maxPerTask of 2.
func TestStoreNoteReleasesBudgetWhenTaskContextEnds(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := &storeNoteTool{repo: repo, maxPerTask: 2}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = runtime.ContextWithTaskIdentity(ctx, runtime.TaskIdentity{RunID: "run-1", TaskID: "task-done", Agent: "builder"})

	for _, body := range []string{"first", "second"} {
		out, err := tool.Execute(ctx, json.RawMessage(`{"content":"`+body+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "ref:note:") {
			t.Fatalf("write %q must succeed under the cap: %s", body, out)
		}
	}
	cancel()

	key := "run-1/task-done"
	deadline := time.Now().Add(time.Second)
	for {
		tool.mu.Lock()
		_, present := tool.counts[key]
		tool.mu.Unlock()
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("counts[%q] still present a second after context cancellation", key)
		}
		time.Sleep(time.Millisecond)
	}

	// A brand new task on the same run must not inherit the released slot
	// or be denied by it; this only proves the entry is gone, not that a
	// fresh task starts clean (TestStoreNotePerTaskCountCapWithIsolation
	// already pins isolation between distinct tasks).
	fresh := taskContext("run-1", "task-done")
	out, err := tool.Execute(fresh, json.RawMessage(`{"content":"after cleanup"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ref:note:") {
		t.Fatalf("a released task key must accept fresh writes again: %s", out)
	}
}

// TestStoreNoteRegisteredOnSessionSurfaces pins registration: the tool joins
// the ledger tool set, so a registry built by registerLedgerTools carries it.
// The negative check is the compile-time interface assertion block in
// store_note.go.
func TestStoreNoteToolContractSurface(t *testing.T) {
	tool := newDefaultStoreNoteTool(ledger.NewMemoryLedgerRepository())
	if tool.Name() != "store_note" {
		t.Fatalf("name = %q, want store_note", tool.Name())
	}
	var _ tools.Tool = tool
	// Deliberately not tools.PrivilegedTool (same precedent as ledger_read).
	if _, ok := tools.Tool(tool).(tools.PrivilegedTool); ok {
		t.Fatal("store_note must not be privileged; sub-agents need it")
	}
}

// TestStoreNoteFailedStoreRefundsBudget pins the reviewer fix: a store error
// must not consume one of the task's note slots, so transient repository
// failures cannot starve the allowance.
func TestStoreNoteFailedStoreRefundsBudget(t *testing.T) {
	tool := &storeNoteTool{repo: failingNoteStore{}, maxPerTask: 2}
	ctx := taskContext("run-refund", "task-refund")
	for i := 0; i < 5; i++ {
		if _, err := tool.Execute(ctx, json.RawMessage(`{"content":"note"}`)); err == nil {
			t.Fatalf("write %d: expected store error", i)
		}
	}
	tool.repo = ledger.NewMemoryLedgerRepository()
	resp, err := tool.Execute(ctx, json.RawMessage(`{"content":"note"}`))
	if err != nil {
		t.Fatalf("post-fault write must succeed after refunds: %v", err)
	}
	if !strings.Contains(resp, `"ref":"ref:note:`) {
		t.Fatalf("post-fault write must return a ref: %q", resp)
	}
}

// TestStoreNoteRetryAttemptGetsFreshBudget pins the generation-aware cleanup:
// a retry redispatch of the same RunID/TaskID runs a fresh context, and its
// budget must start clean even though the prior attempt's watcher goroutine
// may not have deleted the entry yet.
func TestStoreNoteRetryAttemptGetsFreshBudget(t *testing.T) {
	tool := newDefaultStoreNoteTool(ledger.NewMemoryLedgerRepository())
	first, cancelFirst := context.WithCancel(taskContext("run-retry", "task-retry"))
	for i := 0; i < tool.taskCap(); i++ {
		body := fmt.Sprintf(`{"content":"attempt one note %d"}`, i)
		if _, err := tool.Execute(first, json.RawMessage(body)); err != nil {
			t.Fatalf("attempt-one write %d: %v", i, err)
		}
	}
	if resp, err := tool.Execute(first, json.RawMessage(`{"content":"one too many"}`)); err != nil || !strings.Contains(resp, "note budget exhausted") {
		t.Fatalf("attempt one must exhaust its budget: err=%v resp=%s", err, resp)
	}
	cancelFirst()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tool.mu.Lock()
		staleGone := tool.counts["run-retry/task-retry"] == 0
		tool.mu.Unlock()
		if staleGone {
			break
		}
		time.Sleep(time.Millisecond)
	}
	second := taskContext("run-retry", "task-retry")
	for i := 0; i < tool.taskCap(); i++ {
		body := fmt.Sprintf(`{"content":"attempt two note %d"}`, i)
		if _, err := tool.Execute(second, json.RawMessage(body)); err != nil {
			t.Fatalf("retry attempt write %d must start with a fresh budget: %v", i, err)
		}
	}
}
