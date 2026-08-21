package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func seedRunningAttempt(t *testing.T, repo ledger.Repository, runID string) {
	seedRunningAttemptWithOutput(t, repo, runID, []byte(`{"ok":true,"verdict":"approved"}`))
}

func seedRunningAttemptWithOutput(t *testing.T, repo ledger.Repository, runID string, out []byte) {
	t.Helper()
	ctx := context.Background()
	snapshot, err := ledger.MarshalSnapshot(ledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, ledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: ledger.SnapshotDigest(snapshot),
		InputDigest:    ledger.InputDigest(map[string]string{"task": "build"}),
		Status:         ledger.RunStatusPending, ActiveStepID: "one",
		StartedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, ledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := ledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: ledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	ref := "sha256:" + ledger.DigestHex(out)
	if err := repo.StoreContent(ctx, ref, out); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	decision, _ := json.Marshal(map[string]any{"selected": map[string]any{"output": map[string]any{"verdict": "approved"}}})
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, ledger.AttemptOutcome{
		Status: ledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: ledger.DigestHex(out),
		ToStepID: "two", TransitionIndex: 0, MatchDigest: "md", DecisionJSON: decision,
		CoordinatorRunID: "coord-1", TaskID: "task-1", EvidenceJSON: []byte(`[{"name":"task"}]`),
	}); err != nil {
		t.Fatal(err)
	}
}

func testService(t *testing.T, repo ledger.Repository, engine ledger.Engine) *ledger.Service {
	t.Helper()
	svc, err := ledger.NewService(ledger.ServiceOptions{
		Engine: engine,
		Repo: func(context.Context) (ledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestStatusFromLedger(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-status-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	outStr, err := findTool(t, svc, ledger.ToolWorkflowStatus).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var status ledger.StatusView
	if err := json.Unmarshal([]byte(outStr), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID != runID || status.Workflow != "two-step" || status.Status != "running" {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Attempts) != 1 || status.Attempts[0].Verdict != "approved" || status.Attempts[0].ToStep != "two" {
		t.Fatalf("attempts = %+v", status.Attempts)
	}
}

func TestEventsFromLedger(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-events-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	evOut, err := findTool(t, svc, ledger.ToolWorkflowEvents).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	var page ledger.EventsPage
	if err := json.Unmarshal([]byte(evOut), &page); err != nil {
		t.Fatal(err)
	}
	if page.Count < 1 || page.Events[0].Kind == "" {
		t.Fatalf("events = %+v", page)
	}
	for _, ev := range page.Events {
		if strings.Contains(ev.Detail, "api_key") || strings.Contains(ev.Detail, "sk-") {
			t.Fatalf("event detail leaks secret material: %q", ev.Detail)
		}
	}
}

func TestInspectValidatesOffsetAndLimit(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-page-validate"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	if _, err := svc.Inspect(ctx, runID, "one", 1, -1, 0); err == nil || !strings.Contains(err.Error(), "offset") {
		t.Fatalf("negative offset error = %v, want offset validation", err)
	}
	if _, err := svc.Inspect(ctx, runID, "one", 1, 0, -1); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("negative limit error = %v, want limit validation", err)
	}
}

func TestInspectClampsLimitAndDefaultsPageSize(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-page-clamp"
	// Larger than the page ceiling so the paging path is active.
	blob := bytes.Repeat([]byte("a"), ledger.DefaultInspectPageBytes+64)
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	// limit 0 defaults to DefaultInspectPageBytes.
	view, err := svc.Inspect(ctx, runID, "one", 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OutputText) != ledger.DefaultInspectPageBytes {
		t.Fatalf("default page length = %d, want %d", len(view.OutputText), ledger.DefaultInspectPageBytes)
	}
	if view.OutputNextOffset != ledger.DefaultInspectPageBytes {
		t.Fatalf("default page next offset = %d, want %d", view.OutputNextOffset, ledger.DefaultInspectPageBytes)
	}

	// A limit above the page ceiling clamps to DefaultInspectPageBytes.
	view, err = svc.Inspect(ctx, runID, "one", 1, 0, ledger.DefaultInspectPageBytes*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OutputText) != ledger.DefaultInspectPageBytes {
		t.Fatalf("clamped page length = %d, want %d", len(view.OutputText), ledger.DefaultInspectPageBytes)
	}
	if view.OutputNextOffset != ledger.DefaultInspectPageBytes {
		t.Fatalf("clamped page next offset = %d, want %d", view.OutputNextOffset, ledger.DefaultInspectPageBytes)
	}
	if view.Output != nil {
		t.Fatalf("paged view unexpectedly carries full parsed output: %#v", view.Output)
	}
	if view.OutputBytes != len(blob) {
		t.Fatalf("OutputBytes = %d, want %d", view.OutputBytes, len(blob))
	}
}

func TestInspectPagesOutputTextWithNextOffset(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-page"
	blob := bytes.Repeat([]byte("abcdefghij"), 30) // 300 bytes
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	view, err := svc.Inspect(ctx, runID, "one", 1, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.OutputText != string(blob[0:100]) {
		t.Fatalf("page 0 text = %q, want %q", view.OutputText, blob[0:100])
	}
	if view.OutputOffset != 0 || view.OutputBytes != len(blob) {
		t.Fatalf("page 0 framing = offset %d bytes %d, want 0/%d", view.OutputOffset, view.OutputBytes, len(blob))
	}
	if view.OutputNextOffset != 100 {
		t.Fatalf("page 0 next offset = %d, want 100", view.OutputNextOffset)
	}
	if view.Output != nil {
		t.Fatalf("paged page 0 unexpectedly carries full parsed output: %#v", view.Output)
	}

	view, err = svc.Inspect(ctx, runID, "one", 1, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.OutputText != string(blob[100:200]) {
		t.Fatalf("page 1 text = %q, want %q", view.OutputText, blob[100:200])
	}
	if view.OutputOffset != 100 || view.OutputNextOffset != 200 {
		t.Fatalf("page 1 framing = offset %d next %d, want 100/200", view.OutputOffset, view.OutputNextOffset)
	}

	// Final page: OutputNextOffset is 0 (exhausted) and omitted by omitempty.
	view, err = svc.Inspect(ctx, runID, "one", 1, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.OutputText != string(blob[200:300]) {
		t.Fatalf("page 2 text = %q, want %q", view.OutputText, blob[200:300])
	}
	if view.OutputOffset != 200 || view.OutputNextOffset != 0 {
		t.Fatalf("page 2 framing = offset %d next %d, want 200/0", view.OutputOffset, view.OutputNextOffset)
	}
}

func TestInspectRefusesAbovePageableCeiling(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-ceiling"
	blob := bytes.Repeat([]byte("x"), ledger.MaxPageableBytes+1)
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	svc := testService(t, repo, nil)
	ctx := context.Background()

	_, err := svc.Inspect(ctx, runID, "one", 1, 0, 0)
	if err == nil {
		t.Fatal("inspect of an artifact above the pageable ceiling: expected refusal")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("ceiling refusal = %q, want ceiling wording", err.Error())
	}
	if !strings.Contains(err.Error(), "sha256:") {
		t.Fatalf("ceiling refusal = %q, want the output ref named", err.Error())
	}
}

func TestInspectBudgetGuardHalvesPageOnce(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-budget"
	blob := bytes.Repeat([]byte("a"), ledger.DefaultInspectPageBytes*2)
	seedRunningAttemptWithOutput(t, repo, runID, blob)
	// A tight inspect budget that a full default page (64 KiB of text) would
	// exceed but a halved page (32 KiB) fits: the guard must halve once and
	// rebuild, never fail closed on framing.
	ctx := context.Background()
	svc, err := ledger.NewService(ledger.ServiceOptions{
		Repo: func(context.Context) (ledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
		InspectBudgetBytes: 48 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Inspect(ctx, runID, "one", 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := ledger.DefaultInspectPageBytes / 2
	if len(view.OutputText) != want {
		t.Fatalf("budget-guarded page length = %d, want %d (page halved once)", len(view.OutputText), want)
	}
	if view.OutputNextOffset != want {
		t.Fatalf("budget-guarded next offset = %d, want %d", view.OutputNextOffset, want)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 48<<10 {
		t.Fatalf("budget-guarded view marshals to %d bytes, still over budget %d", len(encoded), 48<<10)
	}
}

func TestInspectFromLedger(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	insOut, err := findTool(t, svc, ledger.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect ledger.InspectView
	if err := json.Unmarshal([]byte(insOut), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.CoordinatorRunID != "coord-1" || inspect.TaskID != "task-1" {
		t.Fatalf("inspect identity = %+v", inspect)
	}
	if inspect.Output == nil || inspect.Transition == nil || inspect.Transition.ToStep != "two" {
		t.Fatalf("inspect detail = %+v", inspect)
	}
}

func TestInspectRedactsConfiguredOutput(t *testing.T) {
	policy, err := redact.Compile([]string{`secret-[a-z0-9]+`}, []string{"api_key"}, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	repo := ledger.NewMemoryRepository()
	runID := "wfr-inspect-redaction-1"
	seedRunningAttemptWithOutput(t, repo, runID, []byte(`{"api_key":"test-secret-placeholder","note":"secret-abc123"}`))
	svc := testService(t, repo, nil)
	insOut, err := findTool(t, svc, ledger.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(`{"run_id":"`+runID+`","step":"one","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var inspect ledger.InspectView
	if err := json.Unmarshal([]byte(insOut), &inspect); err != nil {
		t.Fatal(err)
	}
	output, ok := inspect.Output.(map[string]any)
	if !ok {
		t.Fatalf("inspect output type = %T, want object", inspect.Output)
	}
	if output["api_key"] != "[redacted]" || output["note"] != "[redacted]" {
		t.Fatalf("inspect output = %#v, want configured redaction", output)
	}
	if strings.Contains(insOut, "test-secret-placeholder") || strings.Contains(insOut, "secret-abc123") {
		t.Fatalf("inspect result contains unredacted output: %s", insOut)
	}
}

func TestListRunsFromLedger(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-list-1"
	seedRunningAttempt(t, repo, runID)
	svc := testService(t, repo, nil)
	listOut, err := findTool(t, svc, ledger.ToolWorkflowListRuns).Execute(
		context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var list ledger.ListRunsView
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count < 1 || list.Runs[0].RunID != runID {
		t.Fatalf("list = %+v", list)
	}
}

// TestListRunsHugeLimitDoesNotOverflow: the list_runs schema admits limit up to
// MaxInt64 with no maximum, so offset+limit can wrap negative, which used to
// slice runs[1:negative] and panic. A huge limit must clamp to the remainder
// after offset, never panic.
// TestListRunsIncludesActiveStepAndHeartbeat pins the wire contract a
// desktop-app live run list needs: active_step and last_heartbeat_at on
// each RunListItem, without a second per-run workflow_status round trip.
func TestListRunsIncludesActiveStepAndHeartbeat(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	ctx := context.Background()
	runID := "wfr-list-heartbeat-1"
	snapshot, err := ledger.MarshalSnapshot(ledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, ledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: ledger.SnapshotDigest(snapshot),
		InputDigest:    ledger.InputDigest(map[string]string{"task": "build"}),
		Status:         ledger.RunStatusPending, ActiveStepID: "one",
		StartedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, ledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := ledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: ledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	heartbeatAt := time.Date(2026, 8, 6, 12, 5, 0, 0, time.UTC)
	if err := repo.SetStepAttemptHeartbeat(ctx, runID, attempt.AttemptID, heartbeatAt); err != nil {
		t.Fatal(err)
	}

	svc := testService(t, repo, nil)
	list, err := svc.ListRuns(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 {
		t.Fatalf("list.Count = %d, want 1", list.Count)
	}
	item := list.Runs[0]
	if item.ActiveStep != "one" {
		t.Fatalf("ActiveStep = %q, want %q", item.ActiveStep, "one")
	}
	if item.LastHeartbeatAt != heartbeatAt.UTC().Format(time.RFC3339) {
		t.Fatalf("LastHeartbeatAt = %q, want %q", item.LastHeartbeatAt, heartbeatAt.UTC().Format(time.RFC3339))
	}
}

// TestListRunsOmitsHeartbeatForTerminalRuns pins that a finished run's list
// item carries no heartbeat column, matching the CLI text listing's own
// runHeartbeatFreshness gate (internal/cli/workflow_runs.go) - a terminal
// run's last attempt heartbeat is stale by definition and would mislead a
// live-view caller into rendering a "still beating" pulse for a dead run.
func TestListRunsOmitsHeartbeatForTerminalRuns(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	runID := "wfr-list-terminal-1"
	seedRunningAttempt(t, repo, runID)
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, ledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	svc := testService(t, repo, nil)
	list, err := svc.ListRuns(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Runs[0].LastHeartbeatAt != "" {
		t.Fatalf("list = %+v, want one terminal run with no heartbeat", list)
	}
}

func TestListRunsHugeLimitDoesNotOverflow(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	seedRunningAttempt(t, repo, "wfr-list-huge-1")
	seedRunningAttempt(t, repo, "wfr-list-huge-2")
	svc := testService(t, repo, nil)
	ctx := context.Background()
	all, err := svc.ListRuns(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListRuns(ctx, "", math.MaxInt, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Count != all.Count-1 {
		t.Fatalf("ListRuns(MaxInt, 1) Count = %d, want %d (remainder after offset 1)", page.Count, all.Count-1)
	}
}

// TestListRunsRejectsUnknownStatusFilter is the regression test for the
// silent false-negative: the caller-supplied status filter was cast straight
// to ledger.RunStatus with no validation, so a typo like
// "succeeeded" filtered to zero rows and returned Count:0 with a nil error -
// an agent reads 'no runs' for a misspelled filter. ListRuns must reject an
// unknown status with an explicit error, mirroring the CLI twin
// (internal/cli/workflow_runs.go workflowRunStatuses).
func TestListRunsRejectsUnknownStatusFilter(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	seedRunningAttempt(t, repo, "wfr-list-status-1")
	svc := testService(t, repo, nil)
	ctx := context.Background()

	if _, err := svc.ListRuns(ctx, "succeeeded", 0, 0); err == nil {
		t.Fatal("ListRuns with an unknown status filter must return an error, not silently filter to zero rows")
	}
}

// TestListRunsAcceptsEveryKnownStatusFilter pins that every ledger RunStatus
// value is accepted as a status filter (matching the CLI's accepted set), so
// strict validation never rejects a legitimate filter.
func TestListRunsAcceptsEveryKnownStatusFilter(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	seedRunningAttempt(t, repo, "wfr-list-status-1")
	svc := testService(t, repo, nil)
	ctx := context.Background()
	for _, status := range []ledger.RunStatus{
		ledger.RunStatusPending,
		ledger.RunStatusRunning,
		ledger.RunStatusWaitingApproval,
		ledger.RunStatusDeliveryPending,
		ledger.RunStatusSucceeded,
		ledger.RunStatusFailed,
		ledger.RunStatusCanceled,
		ledger.RunStatusTimedOut,
		ledger.RunStatusDeliveryFailed,
	} {
		if _, err := svc.ListRuns(ctx, string(status), 0, 0); err != nil {
			t.Fatalf("ListRuns(%q) = %v, want no error for a known status", status, err)
		}
	}
}

// TestListRunsSurfacesDeliveryClaim pins the delivery-liveness fields added
// alongside the desktop app's heartbeat fix: RunListItem.DeliveryClaimHeld
// is false for a delivery_pending run nobody is delivering (parked,
// distinct from "actively working"), and true with a non-empty
// DeliveryClaimAt once a delivery attempt holds the run's execution claim.
func TestListRunsSurfacesDeliveryClaim(t *testing.T) {
	repo := ledger.NewMemoryRepository()
	ctx := context.Background()
	const runID = "wfr-list-claim"
	snapshot, err := ledger.MarshalSnapshot(ledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, ledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: ledger.SnapshotDigest(snapshot),
		InputDigest:    ledger.InputDigest(map[string]string{"task": "build"}),
		Status:         ledger.RunStatusPending,
		StartedAt:      time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, next := range []ledger.RunStatus{
		ledger.RunStatusRunning,
		ledger.RunStatusDeliveryPending,
	} {
		run, getErr := repo.GetRun(ctx, runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if casErr := repo.CompareAndSetRunStatus(ctx, runID, run.Version, next, nil); casErr != nil {
			t.Fatal(casErr)
		}
	}
	svc := testService(t, repo, nil)

	findRow := func(view ledger.ListRunsView) ledger.RunListItem {
		t.Helper()
		for _, item := range view.Runs {
			if item.RunID == runID {
				return item
			}
		}
		t.Fatalf("ListRuns did not return run %q", runID)
		return ledger.RunListItem{}
	}

	before, err := svc.ListRuns(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if row := findRow(before); row.DeliveryClaimHeld || row.DeliveryClaimAt != "" {
		t.Fatalf("row before any claim = %+v, want DeliveryClaimHeld=false and DeliveryClaimAt=\"\"", row)
	}

	if err := repo.ClaimRun(ctx, runID, "delivery-worker"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.ListRuns(ctx, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(after)
	if !row.DeliveryClaimHeld {
		t.Fatal("DeliveryClaimHeld = false while a claim is held, want true")
	}
	if row.DeliveryClaimAt == "" {
		t.Fatal("DeliveryClaimAt is empty while a claim is held, want a timestamp")
	}
}

func TestDeliverWithoutAllowPublishRefuses(t *testing.T) {
	svc := testService(t, ledger.NewMemoryRepository(), &stubEngine{})
	out, err := findTool(t, svc, ledger.ToolWorkflowDeliver).Execute(
		context.Background(), json.RawMessage(`{"run_id":"wfr-x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result ledger.DeliverResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Refused || result.Reason == "" {
		t.Fatalf("deliver without allow_publish = %+v", result)
	}
}

func TestRunRequiresWorkflowName(t *testing.T) {
	svc := testService(t, ledger.NewMemoryRepository(), &stubEngine{})
	_, err := findTool(t, svc, ledger.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "workflow name") {
		t.Fatalf("error = %v, want workflow name required", err)
	}
}

func TestToolDescriptionsAreGeneric(t *testing.T) {
	svc := testService(t, ledger.NewMemoryRepository(), nil)
	bias := []string{"go test", "cmd/mivia", "github.com/MiviaLabs", "*.go", "golang"}
	for _, tool := range ledger.Tools(svc) {
		text := tool.Description() + "\n" + flattenDescs(tool.Parameters())
		for _, b := range bias {
			if strings.Contains(strings.ToLower(text), strings.ToLower(b)) {
				t.Errorf("%s description contains language bias %q", tool.Name(), b)
			}
		}
		if tool.ResultBudgetBytes() <= 0 {
			t.Errorf("%s missing result budget", tool.Name())
		}
	}
}

func findTool(t *testing.T, svc *ledger.Service, name string) ledger.Tool {
	t.Helper()
	for _, tool := range ledger.Tools(svc) {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func flattenDescs(v any) string {
	var b strings.Builder
	switch x := v.(type) {
	case map[string]any:
		if d, ok := x["description"].(string); ok {
			b.WriteString(d)
			b.WriteByte('\n')
		}
		for _, child := range x {
			b.WriteString(flattenDescs(child))
		}
	case []any:
		for _, child := range x {
			b.WriteString(flattenDescs(child))
		}
	}
	return b.String()
}

type stubEngine struct{}

func (stubEngine) Start(context.Context, ledger.StartRequest) (ledger.StartResult, error) {
	return ledger.StartResult{RunID: "wfr-stub", Status: "running"}, nil
}
func (stubEngine) Cancel(context.Context, string) (ledger.CancelResult, error) {
	return ledger.CancelResult{RunID: "wfr-stub", Status: "canceled"}, nil
}
func (stubEngine) Deliver(context.Context, string, bool) (ledger.DeliverResult, error) {
	return ledger.DeliverResult{RunID: "wfr-stub", Status: "succeeded"}, nil
}
func (stubEngine) Delete(context.Context, string, bool) (ledger.DeleteResult, error) {
	return ledger.DeleteResult{RunID: "wfr-stub", Status: "succeeded", Deleted: true}, nil
}
