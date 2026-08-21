package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestExecuteWorkflowRunsJSONMatchesToolShape pins the wire contract: `workflow
// runs --json` prints exactly ledger.ListRunsView, the same shape
// workflow_list_runs already returns to the model - a desktop-app caller
// polling this command gets a stable, already-tested JSON structure rather
// than a second hand-rolled one that could drift.
func TestExecuteWorkflowRunsJSONMatchesToolShape(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-JSON0001")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout bytes.Buffer
	if err := executeWorkflowRunsJSON(root, config, "", 20, &stdout); err != nil {
		t.Fatalf("executeWorkflowRunsJSON: %v", err)
	}

	var view ledger.ListRunsView
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatalf("output is not valid ListRunsView JSON: %v (%s)", err, stdout.String())
	}
	if view.Count != 1 || view.Runs[0].RunID != run {
		t.Fatalf("view = %+v, want one run %q", view, run)
	}
}

// TestExecuteWorkflowRunsJSONHonorsStatusFilter pins that --status filtering
// applies identically to the JSON path as the text path.
func TestExecuteWorkflowRunsJSONHonorsStatusFilter(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-JSON0002")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout bytes.Buffer
	if err := executeWorkflowRunsJSON(root, config, "failed", 20, &stdout); err != nil {
		t.Fatalf("executeWorkflowRunsJSON: %v", err)
	}
	var view ledger.ListRunsView
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Count != 0 {
		t.Fatalf("status=failed filter on a pending run = %+v, want 0 runs", view)
	}
	_ = run
}

// TestExecuteWorkflowStatusJSONMatchesToolShape pins the same contract for
// `workflow status <id> --json` against ledger.StatusView.
func TestExecuteWorkflowStatusJSONMatchesToolShape(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-JSON0003")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout bytes.Buffer
	if err := executeWorkflowStatusJSON(run, root, config, &stdout); err != nil {
		t.Fatalf("executeWorkflowStatusJSON: %v", err)
	}
	var view ledger.StatusView
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatalf("output is not valid StatusView JSON: %v (%s)", err, stdout.String())
	}
	if view.RunID != run {
		t.Fatalf("view.RunID = %q, want %q", view.RunID, run)
	}
}

// TestExecuteWorkflowStatusJSONUnknownRunErrors pins that an unknown run ID
// fails closed rather than printing a partial/empty JSON object.
func TestExecuteWorkflowStatusJSONUnknownRunErrors(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-JSON0004")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout bytes.Buffer
	err := executeWorkflowStatusJSON("wfr-does-not-exist", root, config, &stdout)
	if err == nil {
		t.Fatal("expected an error for an unknown run ID")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on error", stdout.String())
	}
}

// TestRunWorkflowCommandRunsJSONFlag pins the dispatch-layer wiring of
// `workflow runs --json` end to end through runWorkflowCommandRuns.
func TestRunWorkflowCommandRunsJSONFlag(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-JSON0005")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout, stderr bytes.Buffer
	if err := runWorkflowCommandRuns([]string{"--json"}, root, config, &stdout, &stderr); err != nil {
		t.Fatalf("runWorkflowCommandRuns: %v", err)
	}
	if !strings.Contains(stdout.String(), run) {
		t.Fatalf("output %q does not contain run %q", stdout.String(), run)
	}
}

// TestRunWorkflowCommandRunsRejectsJSONWithWatch pins that --json and
// --watch cannot be combined - --watch's line-per-change text protocol and
// --json's one-shot structured snapshot are different, incompatible
// contracts, not a flag combination worth quietly picking one.
func TestRunWorkflowCommandRunsRejectsJSONWithWatch(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-JSON0006")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout, stderr bytes.Buffer
	err := runWorkflowCommandRuns([]string{"--json", "--watch"}, root, config, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want a mutually-exclusive error", err)
	}
}

// TestRunWorkflowCommandStatusJSONFlag pins the dispatch-layer wiring of
// `workflow status <id> --json` end to end.
func TestRunWorkflowCommandStatusJSONFlag(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-JSON0007")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout, stderr bytes.Buffer
	if err := runWorkflowCommandStatus([]string{"--json", run}, root, config, &stdout, &stderr); err != nil {
		t.Fatalf("runWorkflowCommandStatus: %v", err)
	}
	var view ledger.StatusView
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatalf("output is not valid StatusView JSON: %v (%s)", err, stdout.String())
	}
	if view.RunID != run {
		t.Fatalf("view.RunID = %q, want %q", view.RunID, run)
	}
}
