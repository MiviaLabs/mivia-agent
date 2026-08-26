package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// toolCallEmittingHandler pulls the ToolCallSink off the task's own context
// (installed by contextForTask via runToolCallBuffer, not hand-rolled) and
// invokes it directly with distinguishable steps. This exercises the REAL
// per-task context path end to end: pool -> contextForTask -> sinkFor ->
// buffer -> recordTaskResult's flush -> ledger.
type toolCallEmittingHandler struct {
	toolName string
}

func (h toolCallEmittingHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	sink, ok := subagents.ToolCallSinkFrom(ctx)
	if !ok {
		return nil, errNoSinkOnContext
	}
	sink(subagents.ToolCallStep{ToolCallID: h.toolName + "-call-1", Name: h.toolName, Kind: "start", Input: h.toolName + "-input"})
	sink(subagents.ToolCallStep{ToolCallID: h.toolName + "-call-1", Name: h.toolName, Kind: "end", Output: h.toolName + "-output"})
	return json.RawMessage(`{"ok":true}`), nil
}

var errNoSinkOnContext = &noSinkError{}

type noSinkError struct{}

func (*noSinkError) Error() string { return "no ToolCallSink on task context" }

// TestRecordTaskResultFlushesToolCallsToLedger is the end-to-end integration
// test for Part B chunk 4: a task dispatched through the real coordinator ->
// pool -> contextForTask path, whose handler emits tool-call steps via the
// sink pulled off its OWN context, must have those steps persisted to the
// ledger as a ToolCallsRef the coordinator can resolve back to the original
// steps (including ToolCallID).
func TestRecordTaskResultFlushesToolCallsToLedger(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller", toolCallEmittingHandler{toolName: "read_file"}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "toolcaller"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.ToolCallsRef == "" {
		t.Fatal("expected non-empty ToolCallsRef after task completion")
	}

	data, err := repo.LoadContent(context.Background(), snap.ToolCallsRef)
	if err != nil {
		t.Fatalf("LoadContent(%q): %v", snap.ToolCallsRef, err)
	}
	var steps []subagents.ToolCallStep
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatalf("unmarshal steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2: %+v", len(steps), steps)
	}
	if steps[0].Kind != "start" || steps[0].ToolCallID != "read_file-call-1" || steps[0].Name != "read_file" {
		t.Fatalf("steps[0] = %+v, want start/read_file-call-1/read_file", steps[0])
	}
	if steps[1].Kind != "end" || steps[1].ToolCallID != "read_file-call-1" {
		t.Fatalf("steps[1] = %+v, want end/read_file-call-1", steps[1])
	}
}

// TestRecordTaskResultToolCallsIsolatedAcrossConcurrentTasks dispatches two
// tasks in the same run batch, each emitting distinct, task-identifiable
// tool-call steps. Their stored refs must never cross-contaminate. Run under
// -race.
func TestRecordTaskResultToolCallsIsolatedAcrossConcurrentTasks(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller-a", toolCallEmittingHandler{toolName: "tool_alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(runtime.Subagent, "toolcaller-b", toolCallEmittingHandler{toolName: "tool_beta"}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 4})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "ta", Name: "toolcaller-a"},
		{ID: "tb", Name: "toolcaller-b"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	snapA, err := repo.GetTask(context.Background(), h.runID, "ta")
	if err != nil {
		t.Fatal(err)
	}
	snapB, err := repo.GetTask(context.Background(), h.runID, "tb")
	if err != nil {
		t.Fatal(err)
	}
	if snapA.ToolCallsRef == "" || snapB.ToolCallsRef == "" {
		t.Fatalf("expected both refs non-empty: a=%q b=%q", snapA.ToolCallsRef, snapB.ToolCallsRef)
	}
	if snapA.ToolCallsRef == snapB.ToolCallsRef {
		t.Fatalf("task A and B share the same ToolCallsRef: %q", snapA.ToolCallsRef)
	}

	dataA, err := repo.LoadContent(context.Background(), snapA.ToolCallsRef)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := repo.LoadContent(context.Background(), snapB.ToolCallsRef)
	if err != nil {
		t.Fatal(err)
	}

	var stepsA, stepsB []subagents.ToolCallStep
	if err := json.Unmarshal(dataA, &stepsA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dataB, &stepsB); err != nil {
		t.Fatal(err)
	}

	for _, s := range stepsA {
		if s.Name != "tool_alpha" {
			t.Fatalf("task A ref contains a non-alpha step: %+v", s)
		}
		if s.Name == "tool_beta" || s.Input == "tool_beta-input" || s.Output == "tool_beta-output" {
			t.Fatalf("task A ref contaminated with B content: %+v", s)
		}
	}
	for _, s := range stepsB {
		if s.Name != "tool_beta" {
			t.Fatalf("task B ref contains a non-beta step: %+v", s)
		}
		if s.Name == "tool_alpha" || s.Input == "tool_alpha-input" || s.Output == "tool_alpha-output" {
			t.Fatalf("task B ref contaminated with A content: %+v", s)
		}
	}
}

// toolCallStoreOutageRepo fails StoreContent only for tool-call-kind
// references ("ref:tool_calls:<hex>"), delegating everything else, so a run's
// output/error content still stores and resolves while the tool-call trace
// store alone fails.
type toolCallStoreOutageRepo struct {
	ledger.LedgerRepository
}

func (r *toolCallStoreOutageRepo) StoreContent(ctx context.Context, ref string, data []byte) error {
	if strings.HasPrefix(ref, "ref:"+ledger.RefKindToolCalls+":") {
		return errors.New("injected tool-calls store outage")
	}
	return r.LedgerRepository.StoreContent(ctx, ref, data)
}

// marshalBreakingHandler behaves like toolCallEmittingHandler but emits one
// extra step whose At is outside the RFC 3339 year range, so json.Marshal of
// the flushed []ToolCallStep fails at finalize (persistToolCalls' marshal
// branch), before any repo call.
type marshalBreakingHandler struct {
	toolName string
}

func (h marshalBreakingHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	sink, ok := subagents.ToolCallSinkFrom(ctx)
	if !ok {
		return nil, errNoSinkOnContext
	}
	sink(subagents.ToolCallStep{ToolCallID: h.toolName + "-call-1", Name: h.toolName, Kind: "start", Input: h.toolName + "-input"})
	sink(subagents.ToolCallStep{ToolCallID: h.toolName + "-call-1", Name: h.toolName, Kind: "end", Output: h.toolName + "-output"})
	sink(subagents.ToolCallStep{
		ToolCallID: h.toolName + "-call-2", Name: h.toolName, Kind: "start",
		Input: h.toolName + "-input", At: time.Date(20000, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	return json.RawMessage(`{"ok":true}`), nil
}

// quietHandler completes without emitting any tool-call steps at all.
type quietHandler struct{}

func (quietHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

// registerAndJoinToolCaller spawns a single-task run of name on repo and joins
// it. Join must return a nil error; run-level failures surface in
// RunResult.Err, never in Join's error return.
func registerAndJoinToolCaller(t *testing.T, repo ledger.LedgerRepository, name string, handler runtime.Handler) (*RunHandle, ledger.TaskSnapshot, *RunResult) {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, name, handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: name}}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, jerr := c.Join(context.Background(), h)
	if jerr != nil {
		t.Fatalf("Join returned error %v; completed runs surface failures via RunResult.Err", jerr)
	}
	if res == nil {
		t.Fatal("Join returned nil result")
	}
	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	return h, snap, res
}

// assertTaskRecordWithoutTrace pins the shared shape of both failure modes:
// the task itself completes cleanly, its output content still resolves, but no
// tool-calls reference is recorded and the dropped-trace failure is joined
// into the RUN-level error (RunResult.Err), while the task's own result error
// stays empty so the attribution stays at the seam that lost data.
func assertTaskRecordWithoutTrace(t *testing.T, repo ledger.LedgerRepository, snap ledger.TaskSnapshot, runRes *RunResult) {
	t.Helper()
	if len(runRes.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(runRes.Results))
	}
	if runRes.Results[0].Err != nil {
		t.Fatalf("task result carries an error it should not: %v", runRes.Results[0].Err)
	}
	if runRes.Err == nil {
		t.Fatal("expected RunResult.Err to carry the tool-call persistence failure")
	}
	if !strings.Contains(runRes.Err.Error(), "tool-call content") {
		t.Fatalf("RunResult.Err does not name the tool-call failure: %v", runRes.Err)
	}
	if snap.Status != "completed" {
		t.Fatalf("task status = %q, want completed (the run itself succeeded)", snap.Status)
	}
	if snap.ToolCallsRef != "" {
		t.Fatalf("ToolCallsRef = %q after failed persistence, want \"\" (drop-the-ref)", snap.ToolCallsRef)
	}
	if snap.OutputRef == "" {
		t.Fatal("OutputRef is empty; the outage must be scoped to tool-call refs")
	}
	data, err := repo.LoadContent(context.Background(), snap.OutputRef)
	if err != nil {
		t.Fatalf("LoadContent(OutputRef): %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("output round-trip = %s, want {\"ok\":true}", data)
	}
}

// TestRecordTaskResultToolCallsMarshalFailureSurfacesInRunResultErr drives the
// marshal branch of persistToolCalls (a step whose At cannot RFC-3339 encode).
// The flushed steps used to vanish silently into ""; they must now join their
// failure into the run error while the ref stays unset.
func TestRecordTaskResultToolCallsMarshalFailureSurfacesInRunResultErr(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	h, snap, runRes := registerAndJoinToolCaller(t, repo, "marshalbreaker", marshalBreakingHandler{toolName: "read_file"})
	assertTaskRecordWithoutTrace(t, repo, snap, runRes)
	_ = h
}

// TestRecordTaskResultToolCallsStoreOutageSurfacesInRunResultErr drives the
// StoreContent branch with an outage scoped to tool-call refs. Same contract:
// loud loss in RunResult.Err, dropped ref, everything else intact.
func TestRecordTaskResultToolCallsStoreOutageSurfacesInRunResultErr(t *testing.T) {
	outage := &toolCallStoreOutageRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	h, snap, runRes := registerAndJoinToolCaller(t, outage, "toolcaller", toolCallEmittingHandler{toolName: "read_file"})
	assertTaskRecordWithoutTrace(t, outage, snap, runRes)
	if got := len(h.toolCalls.flush("t1")); got != 0 {
		t.Fatalf("buffer holds %d steps after flush, want 0 (flush pops even when the store fails)", got)
	}
}

// TestRecordTaskResultNoToolCallsStoresEmptyRefAndCleanErr pins the no-trace
// contract through the full finalize path: a task emitting zero tool-call
// steps records an empty ToolCallsRef and contributes nothing to the run
// error.
func TestRecordTaskResultNoToolCallsStoresEmptyRefAndCleanErr(t *testing.T) {
	_, snap, runRes := registerAndJoinToolCaller(t, ledger.NewMemoryLedgerRepository(), "quiet", quietHandler{})
	if runRes.Err != nil {
		t.Fatalf("clean task polluted the run error: %v", runRes.Err)
	}
	if snap.ToolCallsRef != "" {
		t.Fatalf("ToolCallsRef = %q for a task with no tool calls, want \"\"", snap.ToolCallsRef)
	}
}
