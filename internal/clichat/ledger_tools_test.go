// Package cli - tests for the read-only execution-history tools.
package clichat

import (
	"context"
	"encoding/json"
	"errors"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// storeLedgerContent stores data under its canonical reference and returns it.
func storeLedgerContent(t *testing.T, repo ledger.LedgerRepository, data []byte) string {
	t.Helper()
	ref := ledger.Reference(ledger.RefKindOutput, data)
	if ref == "" {
		t.Fatalf("canonical reference for %d bytes was empty", len(data))
	}
	if err := repo.StoreContent(context.Background(), ref, data); err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestLedgerReadWorksOnMemoryBackend proves the tools need no particular
// storage backend: the memory repository satisfies the same read contract the
// durable one does, so ledger_read is written against the interface only.
func TestLedgerReadWorksOnMemoryBackend(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	const body = "recorded task output"
	ref := storeLedgerContent(t, repo, []byte(body))

	tool := &ledgerReadTool{repo: repo}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"ref":"`+ref+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status        string `json:"status"`
		Ref           string `json:"ref"`
		Kind          string `json:"kind"`
		Bytes         int    `json:"bytes"`
		Truncated     bool   `json:"truncated"`
		Content       string `json:"content"`
		ContentIsData bool   `json:"content_is_data"`
		Note          string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.Status != "ok" || response.Content != body {
		t.Fatalf("ledger_read returned %s", out)
	}
	if response.Ref != ref || response.Kind != ledger.RefKindOutput {
		t.Fatalf("reference metadata wrong: %s", out)
	}
	if response.Bytes != len(body) || response.Truncated {
		t.Fatalf("size metadata wrong: %s", out)
	}
	if !response.ContentIsData || response.Note == "" {
		t.Fatalf("untrusted-data framing missing: %s", out)
	}
}

// ledgerReadResponse is the decoded ledger_read envelope.
type ledgerReadResponse struct {
	Status  string `json:"status"`
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
	Error   string `json:"error"`
}

// runOneTaskForModel runs a single task through a real coordinator and returns
// the repository plus the results exactly as the model receives them from
// spawn_agent/dispatch_tasks. Nothing here mints a reference: the refs under
// test come from cliorchestrate.ModelTaskResults, the same function the tools use.
func runOneTaskForModel(t *testing.T, out json.RawMessage, handlerErr error) (ledger.LedgerRepository, []cliorchestrate.ModelTaskResultForTest) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "oneshot", handlerFunc(
		func(context.Context, runtime.Request) (json.RawMessage, error) {
			return out, handlerErr
		})); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	results := cliorchestrate.ModelTaskResults(result.Snapshot.Tasks, result.Results, 4096)
	if len(results) != 1 {
		t.Fatalf("expected 1 model-visible task result, got %d", len(results))
	}
	return repo, results
}

// TestModelVisibleOutputRefResolves is the regression proof for the
// truncated-digest defect: the coordinator persisted task output under a
// 16-hex-character key while the model was handed the full 64-character
// reference, so every output_ref the model saw was a dead pointer.
//
// The loop is closed end to end on purpose. The reference comes only from
// cliorchestrate.ModelTaskResults - what the model actually receives - and is resolved only
// through ledger_read, the agent-facing tool. A test that minted its own
// reference, or that read content by a key it computed itself, would agree
// with itself and prove nothing.
//
// Regression: INV-AG-10
func TestModelVisibleOutputRefResolves(t *testing.T) {
	const body = `{"finding":"model-visible output ref must resolve"}`
	repo, results := runOneTaskForModel(t, json.RawMessage(body), nil)

	modelRef := results[0].OutputRef
	if modelRef == "" {
		t.Fatal("model-visible task result carried no output_ref")
	}

	out, err := (&ledgerReadTool{repo: repo}).Execute(
		context.Background(), json.RawMessage(`{"ref":"`+modelRef+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response ledgerReadResponse
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.Status != "ok" {
		t.Fatalf("output_ref handed to the model did not resolve through ledger_read: %s", out)
	}
	if response.Content != body {
		t.Fatalf("resolved content = %q, want the task output %q", response.Content, body)
	}
	if response.Ref != modelRef || response.Kind != ledger.RefKindOutput {
		t.Fatalf("reference metadata wrong: %s", out)
	}
}

// TestModelVisibleErrorRefResolves closes the same loop for a failing task:
// the error_ref the model is handed must resolve to the recorded failure text.
//
// Regression: INV-AG-10
func TestModelVisibleErrorRefResolves(t *testing.T) {
	const detail = "handler refused: recorded failure detail"
	repo, results := runOneTaskForModel(t, nil, errors.New(detail))

	modelRef := results[0].ErrorRef
	if modelRef == "" {
		t.Fatal("model-visible task result carried no error_ref")
	}
	if results[0].Error == "" {
		t.Fatal("model-visible task result carried no error text")
	}

	out, err := (&ledgerReadTool{repo: repo}).Execute(
		context.Background(), json.RawMessage(`{"ref":"`+modelRef+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response ledgerReadResponse
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.Status != "ok" {
		t.Fatalf("error_ref handed to the model did not resolve through ledger_read: %s", out)
	}
	if response.Content != results[0].Error {
		t.Fatalf("resolved content = %q, want the reported error %q", response.Content, results[0].Error)
	}
	if !strings.Contains(response.Content, detail) {
		t.Fatalf("recorded failure detail missing from %q", response.Content)
	}
	if response.Kind != ledger.RefKindError {
		t.Fatalf("reference metadata wrong: %s", out)
	}
}

// TestLedgerReadRejectsMalformedRef asserts a bad key shape is reported as
// malformed and never as not_found: not_found must only ever mean "the bytes
// are absent", or the tool cannot be trusted to prove a dead pointer.
func TestLedgerReadRejectsMalformedRef(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	tool := &ledgerReadTool{repo: repo}
	digest := strings.Repeat("a", 64)

	// The reference the not_found path produces, for comparison.
	absent := ledger.Reference(ledger.RefKindOutput, []byte("never stored"))
	notFound, err := tool.Execute(context.Background(), json.RawMessage(`{"ref":"`+absent+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		ref  string
	}{
		{name: "missing prefix", ref: "not-a-reference"},
		{name: "unknown kind", ref: "ref:sideband:" + digest},
		{name: "wrong digest length", ref: "ref:output:abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"ref": tt.ref})
			if err != nil {
				t.Fatal(err)
			}
			out, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Error  string `json:"error"`
				Detail string `json:"detail"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatalf("unmarshal %s: %v", out, err)
			}
			if response.Error != "malformed reference" || response.Detail == "" {
				t.Fatalf("expected malformed response, got %s", out)
			}
			if response.Status == "not_found" {
				t.Fatalf("malformed ref reported as not_found: %s", out)
			}
			if out == notFound {
				t.Fatalf("malformed response is textually identical to not_found: %s", out)
			}
		})
	}
}

func TestLedgerReadReportsNotFoundForAbsentContent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := ledger.Reference(ledger.RefKindError, []byte("was never stored"))
	tool := &ledgerReadTool{repo: repo}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"ref":"`+ref+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status string `json:"status"`
		Ref    string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.Status != "not_found" || response.Ref != ref {
		t.Fatalf("expected not_found for absent content, got %s", out)
	}
}

// TestLedgerReadRedactsOutput installs a real redaction policy first: with no
// policy configured redact.Text is the identity function, so the assertion
// below would pass trivially and prove nothing.
func TestLedgerReadRedactsOutput(t *testing.T) {
	const secret = "sk-live-abcdef0123456789"
	policy, err := redact.Compile([]string{`sk-live-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte("the token is "+secret+" so use it"))
	out, err := (&ledgerReadTool{repo: repo}).Execute(context.Background(), json.RawMessage(`{"ref":"`+ref+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("secret survived redaction: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected redaction placeholder in %s", out)
	}
}

func TestLedgerReadTruncatesLargeContent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	body := strings.Repeat("x", 500)
	ref := storeLedgerContent(t, repo, []byte(body))

	out, err := (&ledgerReadTool{repo: repo, maxBytes: 64}).Execute(
		context.Background(), json.RawMessage(`{"ref":"`+ref+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Truncated bool   `json:"truncated"`
		Bytes     int    `json:"bytes"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if !response.Truncated {
		t.Fatalf("expected truncated=true for %d bytes at cap 64: %s", len(body), out)
	}
	if response.Bytes != len(body) {
		t.Fatalf("bytes must report the original length, got %d", response.Bytes)
	}
	if len(response.Content) != 64 {
		t.Fatalf("content length = %d, want 64", len(response.Content))
	}
}

// TestListRunEventsRejectsUnknownKind uses an OWNED, accessible run on purpose.
// An unregistered run_id short-circuits at the ownership gate, so the assertion
// would hold for the wrong reason: deleting the kind check entirely would still
// leave the response as {"error":"unknown run_id"} and the test green. With a
// run the caller can actually read, the only thing separating the unknown-kind
// error from an events envelope is the kind check itself.
func TestListRunEventsRejectsUnknownKind(t *testing.T) {
	const runID = "run-ledger-events-unknown-kind"
	repo, dispatcher := ownedRunFixture(t, "cli-ledger-events-unknown-kind", runID, "owner")
	if err := repo.AppendEvent(context.Background(), ledger.LifecycleEvent{
		ID: "ev-1", RunID: runID, Kind: "run_created",
	}); err != nil {
		t.Fatal(err)
	}

	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})
	tool := &listRunEventsTool{dispatcher: dispatcher, repo: repo}

	// Control: the same run read without a kind filter does return an envelope,
	// so the rejection below cannot be an artifact of an inaccessible run.
	accessible, err := tool.Execute(ownerCtx, json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(accessible, `"count":1`) {
		t.Fatalf("fixture run is not readable by its owner, so the kind assertion would be vacuous: %s", accessible)
	}

	out, err := tool.Execute(ownerCtx, json.RawMessage(`{"run_id":"`+runID+`","kind":"task_qeued"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "unknown run_id") {
		t.Fatalf("request was rejected by the ownership gate, not the kind check: %s", out)
	}
	var response struct {
		Error    string   `json:"error"`
		Accepted []string `json:"accepted"`
		Events   []any    `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.Error != "unknown kind" {
		t.Fatalf("expected unknown kind error, got %s", out)
	}
	if len(response.Accepted) != len(lifecycleEventKinds) {
		t.Fatalf("accepted list must name every kind, got %s", out)
	}
	// A typo must not look like "this run simply had no such events".
	if response.Events != nil {
		t.Fatalf("unknown kind returned an event list: %s", out)
	}
	if strings.Contains(out, `"count"`) {
		t.Fatalf("unknown kind returned a result envelope: %s", out)
	}
}

// expectedLifecycleEventKinds is an INDEPENDENT transcription of the kind
// vocabulary, written out here rather than read from lifecycleEventKinds.
// Comparing the schema enum against the same slice it is built from is
// tautological; comparing it against this literal means any change to the
// production vocabulary has to be consciously mirrored into the test.
var expectedLifecycleEventKinds = []string{
	"run_created",
	"task_created",
	"task_running",
	"task_blocked",
	"task_completed",
	"task_failed",
	"task_timed_out",
	"task_canceled",
	"task_cancel_requested",
	"task_retry_pending",
	"task_retry_queued",
	"task_interrupted_unrecoverable",
}

// TestListRunEventsKindEnumMatchesSchema guards against drift between the
// vocabulary advertised to the model and the vocabulary the tool accepts.
func TestListRunEventsKindEnumMatchesSchema(t *testing.T) {
	tool := &listRunEventsTool{dispatcher: runtime.New(runtime.Policy{}), repo: ledger.NewMemoryLedgerRepository()}
	props, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties map")
	}
	kindProp, ok := props["kind"].(map[string]any)
	if !ok {
		t.Fatal("schema has no kind property")
	}
	enum, ok := kindProp["enum"].([]string)
	if !ok {
		t.Fatalf("kind enum has type %T, want []string", kindProp["enum"])
	}
	if len(enum) != len(lifecycleEventKinds) {
		t.Fatalf("enum has %d entries, lifecycleEventKinds has %d", len(enum), len(lifecycleEventKinds))
	}
	for i, want := range lifecycleEventKinds {
		if enum[i] != want {
			t.Fatalf("enum[%d] = %q, want %q", i, enum[i], want)
		}
		if !knownLifecycleEventKind(want) {
			t.Fatalf("runtime validation rejects advertised kind %q", want)
		}
	}
	if knownLifecycleEventKind("task_queued") {
		t.Fatal("task_queued must not be accepted: nothing emits it")
	}
	// Independent check: the advertised enum must equal the literal list above.
	if len(enum) != len(expectedLifecycleEventKinds) {
		t.Fatalf("enum has %d entries, expected %d: %v",
			len(enum), len(expectedLifecycleEventKinds), enum)
	}
	for i, want := range expectedLifecycleEventKinds {
		if enum[i] != want {
			t.Fatalf("enum[%d] = %q, expected %q", i, enum[i], want)
		}
	}
}

// ownedRunFixture registers a run in cliorchestrate.RunHandlesForTest owned by principalSession and
// returns the repository, dispatcher, and run ID. Setup mirrors
// TestRunHandleNotAccessibleToOtherOwner in orchestrate_lifecycle_test.go.
func ownedRunFixture(t *testing.T, key, runID, principalSession string) (ledger.LedgerRepository, *runtime.Dispatcher) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := repo.CreateRun(context.Background(), key, ledger.RunSnapshot{
		RunID: runID, Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, key)
	if err != nil {
		t.Fatal(err)
	}
	cliorchestrate.StoreTestRunHandle(runID, c, h, repo, dispatcher, principalSession)
	t.Cleanup(func() { cliorchestrate.RunHandlesForTest.Delete(runID) })
	return repo, dispatcher
}

// TestListRunEventsRequiresRunOwnership pins INV-AG-9: a foreign run and a
// nonexistent run must be indistinguishable, so the tool leaks nothing about
// which run IDs exist.
func TestListRunEventsRequiresRunOwnership(t *testing.T) {
	const runID = "run-ledger-events-owned"
	repo, dispatcher := ownedRunFixture(t, "cli-ledger-events-owned", runID, "owner-a")

	tool := &listRunEventsTool{dispatcher: dispatcher, repo: repo}
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner-b"})
	unauthorized, err := tool.Execute(foreignCtx, json.RawMessage(`{"run_id":"`+runID+`"}`))
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

func TestListRunEventsReturnsOrderedMetadataWithoutPayload(t *testing.T) {
	const runID = "run-ledger-events-ordered"
	repo, dispatcher := ownedRunFixture(t, "cli-ledger-events-ordered", runID, "owner")

	appended := []ledger.LifecycleEvent{
		{ID: "ev-1", RunID: runID, Kind: "run_created"},
		{ID: "ev-2", RunID: runID, Kind: "task_created", TaskID: "t1", Payload: []byte(`{"secret":"do not leak"}`)},
		{ID: "ev-3", RunID: runID, Kind: "task_completed", TaskID: "t1", AttemptID: "a1"},
	}
	for _, event := range appended {
		if err := repo.AppendEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})
	tool := &listRunEventsTool{dispatcher: dispatcher, repo: repo}
	out, err := tool.Execute(ownerCtx, json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		RunID     string `json:"run_id"`
		Count     int    `json:"count"`
		Truncated bool   `json:"truncated"`
		Events    []struct {
			ID        string `json:"id"`
			Sequence  uint64 `json:"sequence"`
			Kind      string `json:"kind"`
			TaskID    string `json:"task_id"`
			AttemptID string `json:"attempt_id"`
			CreatedAt string `json:"created_at"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if response.RunID != runID || response.Count != 3 || response.Truncated {
		t.Fatalf("envelope wrong: %s", out)
	}
	for i, event := range response.Events {
		if event.Kind != appended[i].Kind || event.ID != appended[i].ID {
			t.Fatalf("event %d out of order: %s", i, out)
		}
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, i+1)
		}
		if event.CreatedAt == "" {
			t.Fatalf("event %d has no timestamp: %s", i, out)
		}
	}
	if response.Events[1].TaskID != "t1" || response.Events[2].AttemptID != "a1" {
		t.Fatalf("task/attempt metadata missing: %s", out)
	}
	if strings.Contains(out, "payload") || strings.Contains(out, "do not leak") {
		t.Fatalf("event payload leaked into the response: %s", out)
	}

	// A limit smaller than the event count reports truncation rather than
	// silently returning a short list.
	limited, err := tool.Execute(ownerCtx, json.RawMessage(`{"run_id":"`+runID+`","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(limited, `"truncated":true`) || !strings.Contains(limited, `"count":2`) {
		t.Fatalf("limited response = %s", limited)
	}

	// Kind filtering narrows the stream without erroring.
	filtered, err := tool.Execute(ownerCtx, json.RawMessage(`{"run_id":"`+runID+`","kind":"task_completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filtered, `"count":1`) || !strings.Contains(filtered, "task_completed") {
		t.Fatalf("filtered response = %s", filtered)
	}
}

// TestLedgerToolsAreUnprivilegedAndReachSubAgents asserts the two properties
// subagents.MultiStepHandler.restrictedRegistry filters on: it drops a tool
// that implements tools.PrivilegedTool, and it drops a fixed name denylist.
// That method is unexported in another package, so the rule is asserted here
// by the same two conditions rather than by calling it.
func TestLedgerToolsAreUnprivilegedAndReachSubAgents(t *testing.T) {
	// Mirrors the denylist in internal/subagents/multi_step.go.
	subAgentDenylist := map[string]bool{
		"delegate": true, "dispatch_tasks": true,
		"spawn_agent": true, "inspect_agents": true,
		"join_run": true, "cancel_run": true,
	}
	reg := tools.NewRegistry()
	dispatcher := runtime.New(runtime.Policy{})
	if _, err := registerLedgerTools(dispatcher, reg, ledger.NewMemoryLedgerRepository(), 0, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ledger_read", "list_run_events", "read_output"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s missing from the registry", name)
		}
		if _, privileged := tool.(tools.PrivilegedTool); privileged {
			t.Fatalf("%s must not implement tools.PrivilegedTool", name)
		}
		if subAgentDenylist[name] {
			t.Fatalf("%s is on the sub-agent denylist", name)
		}
	}
	// registerSessionTool must keep rejecting unprivileged tools; these two
	// deliberately go through registerLedgerTools instead.
	if err := registerSessionTool(dispatcher, tools.NewRegistry(), &ledgerReadTool{}); err == nil {
		t.Fatal("registerSessionTool accepted an unprivileged tool")
	}
	// Re-registering must fail rather than shadow an existing name.
	if _, err := registerLedgerTools(dispatcher, reg, ledger.NewMemoryLedgerRepository(), 0, nil); err == nil {
		t.Fatal("duplicate registration was accepted")
	}
}

// TestLedgerReadKeepsFramingUnderResultCap pins the defence against a
// tail-truncated envelope.
//
// json.Marshal of a map emits keys alphabetically, which placed "content" first
// and "content_is_data"/"note" after it. capToolResult
// (internal/agent/loop_limits.go) trims the END of an oversized tool body
// whenever an operator configures [tools] max_tool_result_bytes, so any large
// payload lost exactly the framing that marks the bytes as untrusted and left
// invalid JSON behind - and a sub-agent controls its own recorded output, so
// it controlled whether that happened.
//
// The field ORDER is the actual fix, so it is asserted directly: a future
// switch back to a map would silently reintroduce the bug while every value
// assertion still passed.
func TestLedgerReadKeepsFramingUnderResultCap(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte(strings.Repeat("A", 8192)))

	out, err := (&ledgerReadTool{repo: repo, maxBytes: 256}).Execute(
		context.Background(), json.RawMessage(`{"ref":"`+ref+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	var response struct {
		Status        string `json:"status"`
		Truncated     bool   `json:"truncated"`
		Bytes         int    `json:"bytes"`
		Content       string `json:"content"`
		ContentIsData bool   `json:"content_is_data"`
		Note          string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("result is not valid JSON: %v (%s)", err, out)
	}
	if response.Status != "ok" || !response.Truncated || response.Bytes != 8192 {
		t.Fatalf("envelope wrong: %s", out)
	}
	if !response.ContentIsData || response.Note != contentIsDataNote {
		t.Fatalf("untrusted-data framing missing or altered: %s", out)
	}

	// Field order: "content" must be marshalled LAST, after every framing field.
	// Then a tail cut can only remove recorded content.
	notePos := strings.Index(out, `"note"`)
	contentPos := strings.Index(out, `"content"`)
	framePos := strings.Index(out, `"content_is_data"`)
	if notePos < 0 || contentPos < 0 || framePos < 0 {
		t.Fatalf("expected note, content_is_data and content keys in %s", out)
	}
	if contentPos < notePos || contentPos < framePos {
		t.Fatalf("content must be encoded after the framing fields (content=%d note=%d content_is_data=%d): %s",
			contentPos, notePos, framePos, out)
	}
}
