package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// cleanImplementJSON is a schema-plausible implement output that touches no
// write-blocklisted path.
func cleanImplementJSON(addressed ...string) json.RawMessage {
	a := "[]"
	if len(addressed) > 0 {
		b, _ := json.Marshal(addressed)
		a = string(b)
	}
	return json.RawMessage(`{"summary":"implemented","files_changed":["a.go"],"inspected":["a.go"],"addressed_findings":` + a + `,"pr_title":"fix","pr_summary":"fixes the bug"}`)
}

func reviewJSON(verdict string, findings ...string) json.RawMessage {
	items := "[]"
	if len(findings) > 0 {
		parts := make([]string, 0, len(findings))
		for _, f := range findings {
			parts = append(parts, `{"id":"`+f+`","severity":"high","reason":"`+f+`"}`)
		}
		items = "[" + strings.Join(parts, ",") + "]"
	}
	return json.RawMessage(`{"verdict":"` + verdict + `","findings":` + items + `}`)
}

// blockedController builds a repair-loop controller whose write blocklist
// protects .mivia/workflows and .mivia/policy, exactly like a workspace that
// configures write_path_blocklist for its workflow directory.
func blockedController(t *testing.T, runner *scriptedRunner, runID string) *LinearController {
	t.Helper()
	wf := repairWorkflow(t, 30, 16)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetWritePathBlocklist([]string{".mivia/workflows", ".mivia/policy"}); err != nil {
		t.Fatal(err)
	}
	return ctrl
}

// TestBlockedImplementFailsRunInsteadOfRoutingToReview is the core regression:
// an implement step that records a refused write (blocked_paths) must fail the
// run with an honest blocked cause. It must NOT be routed to review, must not
// advance the repair loop counter, and must not burn the loop into a
// misattributed "review made no progress" failure.
func TestBlockedImplementFailsRunInsteadOfRoutingToReview(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"implemented","files_changed":["a.go"],"inspected":["a.go"],"addressed_findings":[],"pr_title":"fix","pr_summary":"fixes the bug","blocked_paths":[".mivia/workflows/bug-fix.toml"]}`),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-impl")
	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflow blocked") || !strings.Contains(err.Error(), ".mivia/workflows/bug-fix.toml") {
		t.Fatalf("run error = %v, want a blocked cause naming the refused path", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.StepID == "review" {
			t.Fatal("review ran after a blocked implement; the run must fail at the implement step")
		}
	}
	counters, _ := ctrl.Repo.GetLoopCounters(context.Background(), ctrl.RunID)
	for _, lc := range counters {
		if lc.Iterations != 0 {
			t.Fatalf("loop counter %q advanced to %d after a blocked implement", lc.LoopName, lc.Iterations)
		}
	}
}

// TestBlockedFilesChangedFailsRun covers the agent that claims a blocklisted
// file in files_changed without the blocked_paths field: no workflow agent can
// change a host-blocklisted path, so a claim of one is a blocked signal.
func TestBlockedFilesChangedFailsRun(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"implemented","files_changed":[".mivia/policy/deploy.toml"],"inspected":["a.go"],"addressed_findings":[],"pr_title":"fix","pr_summary":"fixes the bug"}`),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-files")
	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflow blocked") || !strings.Contains(err.Error(), ".mivia/policy/deploy.toml") {
		t.Fatalf("run error = %v, want a blocked cause naming the claimed path", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.StepID == "review" {
			t.Fatal("review ran after a blocked implement")
		}
	}
}

// TestReviewDemandingBlockedPathFailsInsteadOfLooping covers the deadlock
// shape seen in production: the reviewer keeps demanding an edit to a
// host-blocklisted path that no workflow agent can perform. The review step
// must fail honestly as blocked instead of looping implement -> review.
func TestReviewDemandingBlockedPathFailsInsteadOfLooping(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": cleanImplementJSON(),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"edit .mivia/workflows/bug-fix.toml to lower max_bytes to 16000. This must be executed by a process with write access to .mivia/workflows.","required":"edit .mivia/workflows/bug-fix.toml to lower max_bytes to 16000"}]}`),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-review")
	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflow blocked") || !strings.Contains(err.Error(), ".mivia/workflows") {
		t.Fatalf("run error = %v, want a blocked cause naming the demanded path", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want exactly implement#1 and review#1 (the blocked review must not loop back to implement)", len(runner.calls))
	}
}

// TestMentionOfBlockedPathWithoutDemandRoutesNormally is the false-positive
// guard: a review finding that merely mentions a blocklisted path (no demand
// verb) must route on the loop back-edge exactly as before.
func TestMentionOfBlockedPathWithoutDemandRoutesNormally(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": cleanImplementJSON(),
		"review#1":    reviewJSON("changes_requested", "R0-f1"),
		"implement#2": cleanImplementJSON("R0-f1"),
		"review#2":    reviewJSON("approved"),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-mention")
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v, want a succeeded run", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	implements := 0
	for _, call := range runner.calls {
		if call.StepID == "implement" {
			implements++
		}
	}
	if implements != 2 {
		t.Fatalf("implement calls = %d, want 2 (the loop back-edge must still work)", implements)
	}
}

// TestFindingEvidenceQuotingBlockedPathRoutesNormally is the production
// false-positive guard: a review finding whose evidence merely QUOTES a
// blocklisted path (doc content mentioning ".mivia/policy/deploy.toml") while
// its required field demands a PLAN correction must route on the loop
// back-edge exactly as before, not fail the run as blocked. Before the fix,
// json.Marshal merged evidence and required onto one line, so the quoted path
// token plus the demand verb "Correct" was misread as a demand to write the
// blocked path.
func TestFindingEvidenceQuotingBlockedPathRoutesNormally(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": cleanImplementJSON(),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-1","severity":"low","claim":"Plan falsely claims docs/product/overview.md is identical to upstream master.","evidence":"Local docs/product/overview.md lines 27-30: .mivia/policy/deploy.toml and --agent <name>; upstream URL https://raw.githubusercontent.com/MiviaLabs/mivia-agent/master/docs/product/overview.md lines 27-30: .mivia/policy/.toml (missing <name> placeholder).","reason":"The plan claims content-identical to upstream master, but the local doc differs, as confirmed by fetch_url of the upstream URL.","required":"Correct the plan's step 2 to remove the false claim of identity to upstream master, or provide valid evidence supporting the claim."}]}`),
		"implement#2": cleanImplementJSON("R0-1"),
		"review#2":    reviewJSON("approved"),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-evidence-quote")
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v, want a succeeded run", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	implements := 0
	for _, call := range runner.calls {
		if call.StepID == "implement" {
			implements++
		}
	}
	if implements != 2 {
		t.Fatalf("implement calls = %d, want 2 (the loop back-edge must still work)", implements)
	}
}

// TestReviewDemandingTestFixturePathRoutesNormally pins the smoke regression:
// a review finding whose required text describes a TEST FIXTURE (a helper
// that creates a temporary directory containing ".mivia/workflows" and writes
// fixture TOML files into it) describes the future test's layout, not a
// demand to edit the host's .mivia/workflows. Before the fix, the detector
// misread "creates ... with .mivia/workflows, writes workflow TOML files" as
// a write demand and failed the run as blocked instead of routing on the
// repair-loop back-edge.
func TestReviewDemandingTestFixturePathRoutesNormally(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": cleanImplementJSON(),
		"review#1":    json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-1","severity":"medium","reason":"The test plan references helpers writeWorkflowFixture and runWorkflowsWithIO that do not exist in the tree.","required":"Add writeWorkflowFixture to the new internal/cli/workflows_command_json_test.go (or shared test helpers) that creates a temporary directory with .mivia/workflows, writes workflow TOML files from test definitions, and returns the workspace root."}]}`),
		"implement#2": cleanImplementJSON("R0-1"),
		"review#2":    reviewJSON("approved"),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-fixture")
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v, want a succeeded run (the fixture description must route on the loop back-edge)", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	implements := 0
	for _, call := range runner.calls {
		if call.StepID == "implement" {
			implements++
		}
	}
	if implements != 2 {
		t.Fatalf("implement calls = %d, want 2 (the loop back-edge must still work)", implements)
	}
}

// TestZeroProgressStillFailsWhenNothingBlocked pins the existing stall guard:
// with the blocklist installed but no blocked signal anywhere, identical
// review findings must still fail with the zero-progress cause, not a blocked
// cause.
func TestZeroProgressStillFailsWhenNothingBlocked(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": cleanImplementJSON(),
		"review#1":    reviewJSON("changes_requested", "R0-f1"),
		"implement#2": cleanImplementJSON("R0-f1"),
		"review#2":    reviewJSON("changes_requested", "R0-f1"),
		"implement#3": cleanImplementJSON("R0-f1"),
		"review#3":    reviewJSON("changes_requested", "R0-f1"),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-noprogress")
	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "review made no progress") {
		t.Fatalf("run error = %v, want the zero-progress cause", err)
	}
	if strings.Contains(err.Error(), "workflow blocked") {
		t.Fatalf("run error = %v, must not be a blocked cause when nothing is blocked", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
}

// TestBlockedImplementWithEmptyBlocklistStillFails covers the explicit
// blocked_paths field working even when the controller has no blocklist of its
// own (the agent itself recorded the host refusal).
func TestBlockedImplementWithEmptyBlocklistStillFails(t *testing.T) {
	wf := repairWorkflow(t, 30, 16)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"implemented","files_changed":["a.go"],"inspected":["a.go"],"addressed_findings":[],"pr_title":"fix","pr_summary":"fixes the bug","blocked_paths":[".mivia/workflows/bug-fix.toml"]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-blocked-emptybl", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflow blocked") {
		t.Fatalf("run error = %v, want a blocked cause from the recorded blocked_paths", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
}

// TestBlockedPathsSelfReportOutsideBlocklistIsIgnored is the regression for
// the false-self-report bug: a step can self-report blocked_paths for a file
// that is NOT actually in the controller's write blocklist - by conflating
// "out of my task scope" with "the host refused this write", or by
// hallucinating a refusal that never happened at the tool-call boundary.
// With a real blocklist configured, that false claim must not be trusted: the
// run must proceed normally instead of terminating as host-blocked over a
// path the host never blocked.
func TestBlockedPathsSelfReportOutsideBlocklistIsIgnored(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"implemented","files_changed":["a.go"],"inspected":["a.go"],"addressed_findings":[],"pr_title":"fix","pr_summary":"fixes the bug","blocked_paths":["internal/cli/workflow_deliver.go"]}`),
		"review#1":    reviewJSON("approved"),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-falsereport")
	got, err := ctrl.Run(context.Background())
	if err != nil && strings.Contains(err.Error(), "workflow blocked") {
		t.Fatalf("run error = %v, must not treat an out-of-blocklist self-report as a blocked cause", err)
	}
	if err != nil {
		t.Fatalf("run error = %v, want success", err)
	}
	if got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %v, want succeeded", got.Status)
	}
}

// TestBlockedPathsSelfReportInsideBlocklistStillFails pins the companion
// case: a self-reported path that DOES fall under the configured blocklist
// must still fail the run, exactly like TestBlockedImplementWithEmptyBlocklistStillFails
// pins the empty-blocklist case. The fix must validate false claims away
// without weakening a genuine one.
func TestBlockedPathsSelfReportInsideBlocklistStillFails(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"implemented","files_changed":["a.go"],"inspected":["a.go"],"addressed_findings":[],"pr_title":"fix","pr_summary":"fixes the bug","blocked_paths":[".mivia/workflows/bug-fix.toml"]}`),
	}}
	ctrl := blockedController(t, runner, "wfr-blocked-realreport")
	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflow blocked") {
		t.Fatalf("run error = %v, want a blocked cause from the recorded blocked_paths", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
}
