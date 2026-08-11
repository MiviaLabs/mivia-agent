package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestSpawnAgentWaitRunReturnsTaskOutput(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"output":"completed analysis"}`), nil
	})); err != nil {
		t.Fatal(err)
	}

	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-spawn", TurnID: "turn-1"})
	out, err := (&spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo, skillReg: nil, agentReg: testAgentRegistry(t, "oneshot")}).Execute(ctx, json.RawMessage(`{
		"tasks":[{"id":"t1","agent":"oneshot","prompt":"analyze"}],"wait":"run"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		TaskResults []struct {
			TaskID string         `json:"task_id"`
			Output map[string]any `json:"output"`
		} `json:"task_results"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.TaskResults) != 1 || response.TaskResults[0].TaskID != "t1" || response.TaskResults[0].Output["output"] != "completed analysis" {
		t.Fatalf("task_results=%+v, want completed task output", response.TaskResults)
	}
}

// TestModelVisibleRefsUseCanonicalMinter guards the invariant that a reference
// handed to the model is the canonical, resolvable form: every model-visible ref
// must be byte-identical to what ledger.Reference mints, and must parse back via
// ledger.ParseReference. A second minting implementation would break both.
func TestModelVisibleRefsUseCanonicalMinter(t *testing.T) {
	output := []byte(`{"output":"canonical minter check"}`)
	failure := errors.New("handler exploded")
	results := []subagents.Result{
		{TaskID: "t-ok", Status: "completed", Output: json.RawMessage(output)},
		{TaskID: "t-err", Status: "failed", Err: failure},
	}
	wantOutputRef := ledger.Reference(ledger.RefKindOutput, output)
	wantErrorRef := ledger.Reference(ledger.RefKindError, []byte(failure.Error()))

	assertCanonical := func(t *testing.T, ref string) {
		t.Helper()
		kind, digest, err := ledger.ParseReference(ref)
		if err != nil {
			t.Fatalf("ledger.ParseReference(%q) error = %v, want nil", ref, err)
		}
		if len(digest) != 64 {
			t.Fatalf("ledger.ParseReference(%q) kind=%q digest len = %d, want 64", ref, kind, len(digest))
		}
	}

	modelResults := modelTaskResults(nil, results, 4096)
	if len(modelResults) != 2 {
		t.Fatalf("modelTaskResults len = %d, want 2", len(modelResults))
	}
	if modelResults[0].OutputRef != wantOutputRef {
		t.Fatalf("modelTaskResults output_ref = %q, want %q", modelResults[0].OutputRef, wantOutputRef)
	}
	if modelResults[1].ErrorRef != wantErrorRef {
		t.Fatalf("modelTaskResults error_ref = %q, want %q", modelResults[1].ErrorRef, wantErrorRef)
	}
	assertCanonical(t, modelResults[0].OutputRef)
	assertCanonical(t, modelResults[1].ErrorRef)

	// dispatch_tasks mints refs on its own encode path; it must agree exactly.
	var encoded []struct {
		TaskID    string `json:"task_id"`
		OutputRef string `json:"output_ref"`
		ErrorRef  string `json:"error_ref"`
	}
	raw := (&dispatchTasksTool{}).encodeResults(nil, results)
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		t.Fatalf("encodeResults produced unparsable JSON %q: %v", raw, err)
	}
	if len(encoded) != 2 {
		t.Fatalf("encodeResults tasks = %+v, want 2", encoded)
	}
	if encoded[0].OutputRef != wantOutputRef {
		t.Fatalf("encodeResults output_ref = %q, want %q", encoded[0].OutputRef, wantOutputRef)
	}
	if encoded[1].ErrorRef != wantErrorRef {
		t.Fatalf("encodeResults error_ref = %q, want %q", encoded[1].ErrorRef, wantErrorRef)
	}
	assertCanonical(t, encoded[0].OutputRef)
	assertCanonical(t, encoded[1].ErrorRef)
}

// TestSpawnResultPayloadRecoveredRunUsesStoredRefs covers spawn_agent's
// idempotent-replay path (wait=run on a run recovered from the ledger).
// Recovered results carry no Output, and their Err is prose that merely mentions
// the stored reference, so minting refs from those values produced an error_ref
// that was a digest of the sentence and no output_ref at all. The model must be
// handed the ledger's own references.
//
// Regression: INV-AG-10
func TestSpawnResultPayloadRecoveredRunUsesStoredRefs(t *testing.T) {
	const storedOutputRef = "ref:output:stored-output-key"
	const storedErrorRef = "ref:error:stored-error-key"
	snap := ledger.RunSnapshot{
		RunID: "run-replay", DisplayName: "replay", Status: ledger.RunStatusFailed,
		Tasks: []ledger.TaskSnapshot{
			{RunID: "run-replay", TaskID: "t1", Status: string(ledger.TaskStatusCompleted), OutputRef: storedOutputRef},
			{RunID: "run-replay", TaskID: "t2", Status: string(ledger.TaskStatusFailed), ErrorRef: storedErrorRef},
		},
	}
	// Mirrors coordinator.resultsFromSnapshots: no Output, and an Err that is
	// prose about the recovery rather than the recorded failure text.
	completed := &coordinator.RunResult{Snapshot: snap, Results: []subagents.Result{
		{TaskID: "t1", Status: string(ledger.TaskStatusCompleted),
			Provenance: runtime.Metadata{Kind: "recovered", Status: string(ledger.TaskStatusCompleted)}},
		{TaskID: "t2", Status: string(ledger.TaskStatusFailed),
			Err:        errors.New("recovered task t2: failed (error content reference " + storedErrorRef + ")"),
			Provenance: runtime.Metadata{Kind: "recovered", Status: string(ledger.TaskStatusFailed)}},
	}}

	var response struct {
		TaskResults []struct {
			TaskID    string `json:"task_id"`
			OutputRef string `json:"output_ref"`
			ErrorRef  string `json:"error_ref"`
		} `json:"task_results"`
	}
	out := spawnResultPayload(snap, completed, 4096, nil)
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if len(response.TaskResults) != 2 {
		t.Fatalf("task_results = %+v, want 2", response.TaskResults)
	}
	if response.TaskResults[0].OutputRef != storedOutputRef {
		t.Fatalf("output_ref = %q, want the snapshot's stored ref %q", response.TaskResults[0].OutputRef, storedOutputRef)
	}
	if response.TaskResults[1].ErrorRef != storedErrorRef {
		t.Fatalf("error_ref = %q, want the snapshot's stored ref %q", response.TaskResults[1].ErrorRef, storedErrorRef)
	}
}

func TestRunTaskResultsWithRepoAttachesMessages(t *testing.T) {
	result := &coordinator.RunResult{
		Snapshot: ledger.RunSnapshot{RunID: "r1", Tasks: []ledger.TaskSnapshot{
			{RunID: "r1", TaskID: "t1", Status: "completed"},
		}},
		Results: []subagents.Result{{TaskID: "t1", Status: "completed"}},
	}
	got := runTaskResultsWithRepo(ledger.NewMemoryLedgerRepository(), result, 4096)
	if len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("got=%+v", got)
	}
	if runTaskResultsWithRepo(nil, nil, 4096) != nil {
		t.Fatal("nil result")
	}
	result.Results[0].Provenance.Kind = "recovered"
	got = runTaskResultsWithRepo(ledger.NewMemoryLedgerRepository(), result, 4096)
	if len(got) != 1 {
		t.Fatalf("recovered path: %+v", got)
	}
}

func TestJoinRunTool_RecoveredRunUsesPersistedTaskResults(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	const runID = "cli-recovered-run"
	if err := repo.CreateRun(context.Background(), "cli-recovered", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	tasks := []ledger.TaskSnapshot{
		{RunID: runID, TaskID: "task-a", Status: string(ledger.TaskStatusCompleted), OutputRef: "ref:output:7", Version: 1,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-a", TaskID: "task-a", RunID: runID, AttemptNum: 1, Status: string(ledger.TaskStatusCompleted)}}},
		{RunID: runID, TaskID: "task-b", Status: string(ledger.TaskStatusFailed), ErrorRef: "ref:error:deadbeef", Version: 1,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-b", TaskID: "task-b", RunID: runID, AttemptNum: 1, Status: string(ledger.TaskStatusFailed)}}},
	}
	for _, task := range tasks {
		if err := repo.CreateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	c := coordinator.New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	d := runtime.New(runtime.Policy{})
	h, err := c.Spawn(context.Background(), nil, "cli-recovered")
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: d, principal: orchestrationPrincipal{sessionID: "session-recovered"}})
	defer runHandles.Delete(runID)

	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-recovered"})
	out, err := (&joinRunTool{dispatcher: d, repo: repo}).Execute(ctx, json.RawMessage(`{"run_id":"cli-recovered-run"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		TaskResults []struct {
			TaskID    string `json:"task_id"`
			OutputRef string `json:"output_ref"`
			ErrorRef  string `json:"error_ref"`
		} `json:"task_results"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.TaskResults) != 2 {
		t.Fatalf("task results = %+v", response.TaskResults)
	}
	refs := make(map[string]struct{ output, failure string }, len(response.TaskResults))
	for _, task := range response.TaskResults {
		refs[task.TaskID] = struct{ output, failure string }{task.OutputRef, task.ErrorRef}
	}
	if refs["task-a"].output != "ref:output:7" {
		t.Fatalf("completed persisted result = %+v", refs["task-a"])
	}
	if refs["task-b"].failure != "ref:error:deadbeef" {
		t.Fatalf("failed persisted result = %+v", refs["task-b"])
	}
}

func TestOrchestrationLifecycleTools_RejectCrossSessionHandleAccess(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	origin := runtime.New(runtime.Policy{})
	other := runtime.New(runtime.Policy{})
	if err := repo.CreateRun(context.Background(), "cli-session-scoped", ledger.RunSnapshot{
		RunID: "run-session-scoped", Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(origin, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "cli-session-scoped")
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-session-scoped"
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: origin, principal: orchestrationPrincipal{sessionID: "origin"}})
	defer runHandles.Delete(runID)
	args := json.RawMessage(`{"run_id":"run-session-scoped"}`)

	tests := []struct {
		name string
		tool tools.Tool
	}{
		{name: "inspect", tool: &inspectAgentTool{dispatcher: other, repo: repo}},
		{name: "join", tool: &joinRunTool{dispatcher: other, repo: repo}},
		{name: "cancel", tool: &cancelRunTool{dispatcher: other, repo: repo}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
			out, err := tt.tool.Execute(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if out != `{"error":"unknown run_id"}` {
				t.Fatalf("cross-session access returned %q", out)
			}
		})
	}
}

// TestRunHandleNotAccessibleToOtherOwner reproduces the same-dispatcher IDOR:
// independent callers currently share a dispatcher and repository, so the
// lifecycle tools must not authorize solely on those two identities.
func TestRunHandleNotAccessibleToOtherOwner(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := repo.CreateRun(context.Background(), "cli-owner-scoped", ledger.RunSnapshot{
		RunID: "run-owner-scoped", Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "cli-owner-scoped")
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-owner-scoped"
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "owner-a"}})
	defer runHandles.Delete(runID)

	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner-a"})
	otherCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner-b"})
	_ = ownerCtx
	for name, tool := range map[string]tools.Tool{
		"inspect": &inspectAgentTool{dispatcher: dispatcher, repo: repo},
		"join":    &joinRunTool{dispatcher: dispatcher, repo: repo},
		"cancel":  &cancelRunTool{dispatcher: dispatcher, repo: repo},
	} {
		out, err := tool.Execute(otherCtx, json.RawMessage(`{"run_id":"run-owner-scoped"}`))
		if err != nil {
			t.Fatal(err)
		}
		if out != `{"error":"unknown run_id"}` {
			t.Fatalf("foreign owner %s accessed run: %s", name, out)
		}
	}
}

func TestCancelRunCannotCancelForeignRun(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})
	spawn := &spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo, skillReg: nil, agentReg: testAgentRegistry(t, "oneshot")}
	out, err := spawn.Execute(ownerCtx, json.RawMessage(`{"tasks":[{"id":"t1","agent":"oneshot","prompt":"work"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	<-started
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
	cancelOut, err := (&cancelRunTool{dispatcher: dispatcher, repo: repo}).Execute(foreignCtx, json.RawMessage(`{"run_id":"`+response.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cancelOut != `{"error":"unknown run_id"}` {
		t.Fatalf("foreign cancel response = %s", cancelOut)
	}
	close(release)
	joined, err := (&joinRunTool{dispatcher: dispatcher, repo: repo}).Execute(ownerCtx, json.RawMessage(`{"run_id":"`+response.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined, `"status":"completed"`) {
		t.Fatalf("foreign cancel affected run: %s", joined)
	}
}

func TestDispatcherCloseUnregistersCompletedOrchestrationHandle(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "close-unregister")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the completed-run cleanup path deterministically: wait for the run
	// to finish before closing the dispatcher, so the assertion tests the
	// unregister-on-close behavior, not a race with the pool.
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("oneshot run did not complete")
	}
	record := &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "session"}, retention: time.Hour}
	storeOrchestrationHandle(snap.RunID, record)
	dispatcher.Close()
	if _, ok := runHandles.Load(snap.RunID); ok {
		t.Fatal("dispatcher close retained a completed orchestration handle")
	}
}

// TestDispatcherCloseRetainsActiveOrchestrationHandle pins the surface-rebuild
// fix: a dispatcher close while the run is still in flight must NOT unregister
// the handle, because surface rebuilds (tool admission, /agent, /model)
// replace the dispatcher while the session continues. A rebuild that dropped
// the handle made every in-flight background run unreachable as "unknown
// run_id". The same-session caller must reach it through a rebuilt dispatcher;
// a foreign session must still be refused.
func TestDispatcherCloseRetainsActiveOrchestrationHandle(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	if err := dispatcher.Register(runtime.Subagent, "blocker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "blocker"}}, "retain-active")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	record := &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "session"}, retention: time.Hour}
	storeOrchestrationHandle(snap.RunID, record)
	t.Cleanup(func() { runHandles.Delete(snap.RunID) })
	dispatcher.Close()
	if _, ok := runHandles.Load(snap.RunID); !ok {
		t.Fatal("dispatcher close dropped an active orchestration handle")
	}
	// The same session reaches the run through a rebuilt dispatcher instance.
	rebuilt := runtime.New(runtime.Policy{})
	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session"})
	if got, errJSON := accessibleOrchestrationHandle(ownerCtx, snap.RunID, rebuilt, repo); got == nil || errJSON != "" {
		t.Fatalf("active handle unreachable after rebuild: record=%v errJSON=%q", got, errJSON)
	}
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
	if _, errJSON := accessibleOrchestrationHandle(foreignCtx, snap.RunID, rebuilt, repo); errJSON != errJSONUnknownRunID {
		t.Fatalf("foreign session reached the run: errJSON=%q", errJSON)
	}
	close(release)
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("run did not complete after release")
	}
}

func TestRunHandleAccessibleToAncestor(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "ancestor-access")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(snap.RunID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "shared-session"}})
	defer runHandles.Delete(snap.RunID)

	parentCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "shared-session"})
	out, err := (&inspectAgentTool{dispatcher: dispatcher, repo: repo}).Execute(parentCtx, json.RawMessage(`{"run_id":"`+snap.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == `{"error":"unknown run_id"}` {
		t.Fatal("parent could not access child run")
	}
}

func TestUnauthorizedAndUnknownAreIndistinguishable(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "indistinguishable")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(snap.RunID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "owner"}})
	defer runHandles.Delete(snap.RunID)
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
	tool := &inspectAgentTool{dispatcher: dispatcher, repo: repo}
	unauthorized, err := tool.Execute(foreignCtx, json.RawMessage(`{"run_id":"`+snap.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := tool.Execute(foreignCtx, json.RawMessage(`{"run_id":"run-does-not-exist"}`))
	if err != nil {
		t.Fatal(err)
	}
	if unauthorized != unknown || unknown != `{"error":"unknown run_id"}` {
		t.Fatalf("unauthorized=%q unknown=%q", unauthorized, unknown)
	}
}

func TestTaskDepthPropagates(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{MaxDepth: 8})
	depths := make(chan int, 1)
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		depths <- req.Depth
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "depth-session", TurnID: "turn-1", Depth: 1})
	_, err := (&spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo, skillReg: nil, agentReg: testAgentRegistry(t, "oneshot")}).Execute(ctx, json.RawMessage(`{"tasks":[{"id":"t1","agent":"oneshot","prompt":"work"}],"wait":"run"}`))
	if err != nil {
		t.Fatal(err)
	}
	if depth := <-depths; depth != 2 {
		t.Fatalf("task depth = %d, want 2", depth)
	}
}

func TestSpawnAgentIdempotencyKeyDedupesAcrossTurns(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	tool := &spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo, agentReg: testAgentRegistry(t, "worker")}
	args := json.RawMessage(`{"tasks":[{"id":"task-1","agent":"worker","prompt":"requested work"}],"idempotency_key":"key"}`)
	first, err := tool.Execute(runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session", TurnID: "turn:1"}), args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Execute(runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session", TurnID: "turn:2"}), args)
	if err != nil {
		t.Fatal(err)
	}
	var firstResult, secondResult struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(first), &firstResult); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(second), &secondResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.RunID == "" || secondResult.RunID != firstResult.RunID {
		t.Fatalf("run IDs = %q, %q; want one non-empty reused run ID", firstResult.RunID, secondResult.RunID)
	}
}
