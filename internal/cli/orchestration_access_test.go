package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// accessHelperFixture holds a registered orchestration handle owned by one session.
type accessHelperFixture struct {
	repo       ledger.LedgerRepository
	dispatcher *runtime.Dispatcher
	runID      string
	record     *orchestrationHandle
	ownerCtx   context.Context
	foreignCtx context.Context
}

func newAccessHelperFixture(t *testing.T) *accessHelperFixture {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "access-helper")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	const ownerSession = "owner-session"
	record := &orchestrationHandle{
		coord:      c,
		handle:     h,
		repo:       repo,
		dispatcher: dispatcher,
		principal:  orchestrationPrincipal{sessionID: ownerSession},
	}
	runHandles.Store(snap.RunID, record)
	t.Cleanup(func() { runHandles.Delete(snap.RunID) })
	return &accessHelperFixture{
		repo:       repo,
		dispatcher: dispatcher,
		runID:      snap.RunID,
		record:     record,
		ownerCtx:   runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: ownerSession}),
		foreignCtx: runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "foreign-session"}),
	}
}

func TestAccessibleOrchestrationHandleEmptyRunID(t *testing.T) {
	f := newAccessHelperFixture(t)
	got, errJSON := accessibleOrchestrationHandle(f.ownerCtx, "", f.dispatcher, f.repo)
	if got != nil {
		t.Fatalf("record = %v, want nil", got)
	}
	if errJSON != errJSONRunIDRequired {
		t.Fatalf("errJSON = %q, want %q", errJSON, errJSONRunIDRequired)
	}
	if errJSON != `{"error":"run_id is required"}` {
		t.Fatalf("wire value drifted: %q", errJSON)
	}
}

func TestAccessibleOrchestrationHandleUnknownID(t *testing.T) {
	f := newAccessHelperFixture(t)
	got, errJSON := accessibleOrchestrationHandle(f.ownerCtx, "run-does-not-exist", f.dispatcher, f.repo)
	if got != nil {
		t.Fatalf("record = %v, want nil", got)
	}
	if errJSON != errJSONUnknownRunID {
		t.Fatalf("errJSON = %q, want %q", errJSON, errJSONUnknownRunID)
	}
	if errJSON != `{"error":"unknown run_id"}` {
		t.Fatalf("wire value drifted: %q", errJSON)
	}
}

func TestAccessibleOrchestrationHandleWrongType(t *testing.T) {
	f := newAccessHelperFixture(t)
	const badID = "run-wrong-type"
	runHandles.Store(badID, "not-a-handle")
	t.Cleanup(func() { runHandles.Delete(badID) })

	got, errJSON := accessibleOrchestrationHandle(f.ownerCtx, badID, f.dispatcher, f.repo)
	if got != nil {
		t.Fatalf("record = %v, want nil", got)
	}
	if errJSON != errJSONUnknownRunID {
		t.Fatalf("errJSON = %q, want %q", errJSON, errJSONUnknownRunID)
	}
}

func TestAccessibleOrchestrationHandleForeignPrincipal(t *testing.T) {
	f := newAccessHelperFixture(t)
	got, errJSON := accessibleOrchestrationHandle(f.foreignCtx, f.runID, f.dispatcher, f.repo)
	if got != nil {
		t.Fatalf("record = %v, want nil", got)
	}
	if errJSON != errJSONUnknownRunID {
		t.Fatalf("errJSON = %q, want %q", errJSON, errJSONUnknownRunID)
	}
}

// INV-AG-9: unknown and inaccessible runs must return the identical error string.
func TestAccessibleOrchestrationHandleIndistinguishable(t *testing.T) {
	f := newAccessHelperFixture(t)
	_, unknownJSON := accessibleOrchestrationHandle(f.ownerCtx, "run-does-not-exist", f.dispatcher, f.repo)
	_, foreignJSON := accessibleOrchestrationHandle(f.foreignCtx, f.runID, f.dispatcher, f.repo)
	if unknownJSON != foreignJSON {
		t.Fatalf("INV-AG-9 violated: unknown=%q foreign=%q", unknownJSON, foreignJSON)
	}
	if unknownJSON != errJSONUnknownRunID {
		t.Fatalf("expected errJSONUnknownRunID, got %q", unknownJSON)
	}
	if unknownJSON != `{"error":"unknown run_id"}` {
		t.Fatalf("wire value drifted: %q", unknownJSON)
	}
}

func TestAccessibleOrchestrationHandleAccessible(t *testing.T) {
	f := newAccessHelperFixture(t)
	got, errJSON := accessibleOrchestrationHandle(f.ownerCtx, f.runID, f.dispatcher, f.repo)
	if errJSON != "" {
		t.Fatalf("errJSON = %q, want empty", errJSON)
	}
	if got == nil {
		t.Fatal("record is nil, want non-nil")
	}
	if got.handle == nil {
		t.Fatal("record.handle is nil")
	}
	if got != f.record {
		t.Fatal("returned record is not the stored orchestration handle")
	}
}
