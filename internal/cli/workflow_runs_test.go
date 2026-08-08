package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestExecuteWorkflowRunsListsRuns covers the gap that made an in-flight run
// unaddressable: `workflow run` prints run_id only at completion and the
// worktree name is lower-cased, so without this listing the ID of a running
// workflow cannot be recovered at all.
func TestExecuteWorkflowRunsListsRuns(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-AAAA1111")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, config, "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, run) {
		t.Errorf("output %q does not list run %q", out, run)
	}
	// The ID must appear with its original casing, which is the whole point.
	if !strings.Contains(out, "wfr-AAAA1111") {
		t.Errorf("output %q lost the run ID casing", out)
	}
	if !strings.Contains(out, "test-wf") {
		t.Errorf("output %q omits the workflow name", out)
	}
}

func TestExecuteWorkflowRunsStatusFilter(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-BBBB2222")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	t.Run("matching status lists the run", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := executeWorkflowRuns(root, config, "pending", 20, &stdout, io.Discard); err != nil {
			t.Fatalf("executeWorkflowRuns: %v", err)
		}
		if !strings.Contains(stdout.String(), run) {
			t.Errorf("pending filter dropped the pending run: %q", stdout.String())
		}
	})

	t.Run("other status lists nothing", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := executeWorkflowRuns(root, config, "succeeded", 20, &stdout, io.Discard); err != nil {
			t.Fatalf("executeWorkflowRuns: %v", err)
		}
		if !strings.Contains(stdout.String(), "no runs") {
			t.Errorf("output %q, want the no-runs notice", stdout.String())
		}
	})

	t.Run("unknown status is rejected", func(t *testing.T) {
		var stdout bytes.Buffer
		err := executeWorkflowRuns(root, config, "bogus", 20, &stdout, io.Discard)
		if err == nil {
			t.Fatal("executeWorkflowRuns error = nil, want unknown-status error")
		}
		// The message must name the accepted values, or the operator has to
		// read the source to find them.
		if !strings.Contains(err.Error(), "succeeded") {
			t.Errorf("error %q does not list the accepted statuses", err)
		}
	})
}

func TestExecuteWorkflowRunsLimit(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-CCCC3333")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, config, "", 1, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(stdout.String()), "\n"); lines != 0 {
		t.Errorf("limit 1 printed %d extra lines: %q", lines, stdout.String())
	}
}

func TestRunWorkflowCommandRunsRejectsPositional(t *testing.T) {
	var stdout bytes.Buffer
	err := runWorkflowCommandRuns([]string{"extra"}, t.TempDir(), "", &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("error = %v, want an unexpected-argument error", err)
	}
}

// seedFailedAttempt records one failed attempt carrying errorText behind a
// content ref, mirroring how the controller persists a failure. When store is
// false the ref is recorded without its blob, which is the unreadable-content
// case. The ref uses the sha256: shape the controller writes.
func seedFailedAttempt(t *testing.T, runID, errorText string, store bool) (string, string, func()) {
	t.Helper()
	root, _, repo, closeFn, ctx, run := openEventsFixtureWithRun(t, runID)
	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "att-one", RunID: run, StepID: "plan_review", AttemptNo: 1}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	storedAttempt, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	ref := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(errorText)))
	if store {
		if err := repo.StoreContent(ctx, ref, []byte(errorText)); err != nil {
			t.Fatal(err)
		}
	}
	outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusFailed, ErrorRef: ref}
	if err := repo.CompleteStepAttempt(ctx, run, attempt.AttemptID, storedAttempt.Version, outcome); err != nil {
		t.Fatal(err)
	}
	return root, run, closeFn
}

// TestWorkflowStatusPrintsAttemptError is the regression guard for the second
// observability gap: status printed only "error sha256:..." so diagnosing a
// failed run meant reading the sqlite content table out of band.
func TestWorkflowStatusPrintsAttemptError(t *testing.T) {
	const reason = "review made no progress across rounds (identical findings set); run failed"
	root, run, closeFn := seedFailedAttempt(t, "wfr-DDDD4444", reason, true)
	defer closeFn()

	var stdout bytes.Buffer
	if err := executeWorkflowStatus(run, root, filepath.Join(root, "config.toml"), &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowStatus: %v", err)
	}
	if !strings.Contains(stdout.String(), reason) {
		t.Errorf("status output does not contain the failure reason:\n%s", stdout.String())
	}
}

func TestWorkflowStatusAttemptErrorTruncates(t *testing.T) {
	long := strings.Repeat("x", maxAttemptErrorBytes+500)
	root, run, closeFn := seedFailedAttempt(t, "wfr-EEEE5555", long, true)
	defer closeFn()

	var stdout bytes.Buffer
	if err := executeWorkflowStatus(run, root, filepath.Join(root, "config.toml"), &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowStatus: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "truncated") {
		t.Errorf("oversized error was not marked truncated:\n%s", out[:min(len(out), 400)])
	}
	if strings.Count(out, "x") > maxAttemptErrorBytes+10 {
		t.Error("truncation did not bound the printed error")
	}
}

// TestWorkflowStatusAttemptErrorMissingContent pins the degrade path: a ref
// whose blob is absent must still print a report, not fail the command.
func TestWorkflowStatusAttemptErrorMissingContent(t *testing.T) {
	root, run, closeFn := seedFailedAttempt(t, "wfr-FFFF6666", "absent blob", false)
	defer closeFn()

	var stdout bytes.Buffer
	if err := executeWorkflowStatus(run, root, filepath.Join(root, "config.toml"), &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowStatus must not fail on an unreadable ref: %v", err)
	}
	if !strings.Contains(stdout.String(), "content unavailable") {
		t.Errorf("missing content was not reported:\n%s", stdout.String())
	}
}

// TestRunWorkflowCommandRunsFlags covers the arg parsing branches: both
// --limit spellings, a bad --limit, a bad --status, and the dispatch route.
func TestRunWorkflowCommandRunsFlags(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-GGGG7777")
	defer closeFn()
	config := filepath.Join(root, "config.toml")

	for _, args := range [][]string{
		{"--limit", "5"},
		{"--limit=5"},
		{"--status", "pending", "--limit", "5"},
	} {
		var stdout bytes.Buffer
		if err := runWorkflowCommandRuns(args, root, config, &stdout, io.Discard); err != nil {
			t.Fatalf("runWorkflowCommandRuns(%q): %v", args, err)
		}
		if !strings.Contains(stdout.String(), run) {
			t.Errorf("args %q did not list the run: %q", args, stdout.String())
		}
	}

	t.Run("bad limit", func(t *testing.T) {
		if err := runWorkflowCommandRuns([]string{"--limit", "nope"}, root, config, io.Discard, io.Discard); err == nil {
			t.Fatal("error = nil, want an integer parse error")
		}
	})

	t.Run("dispatch route", func(t *testing.T) {
		var stdout bytes.Buffer
		err := runWorkflowWithIO([]string{"runs", "--workspace", root, "--config", config}, &stdout, io.Discard)
		if err != nil {
			t.Fatalf("runWorkflowWithIO runs: %v", err)
		}
		if !strings.Contains(stdout.String(), run) {
			t.Errorf("dispatch did not reach the listing: %q", stdout.String())
		}
	})
}

// TestExecuteWorkflowRunsOpenFailure pins that an unopenable workspace is
// reported rather than printed as an empty listing.
func TestExecuteWorkflowRunsOpenFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	if err := executeWorkflowRuns(missing, "", "", 20, io.Discard, io.Discard); err == nil {
		t.Fatal("error = nil, want a workspace open error")
	}
}

// TestWorkflowStatusAttemptErrorEmpty covers a stored blob that is only
// whitespace: it must say so rather than print a blank line.
func TestWorkflowStatusAttemptErrorEmpty(t *testing.T) {
	root, run, closeFn := seedFailedAttempt(t, "wfr-HHHH8888", "   \n  ", true)
	defer closeFn()

	var stdout bytes.Buffer
	if err := executeWorkflowStatus(run, root, filepath.Join(root, "config.toml"), &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowStatus: %v", err)
	}
	if !strings.Contains(stdout.String(), "(empty)") {
		t.Errorf("whitespace-only error was not reported as empty:\n%s", stdout.String())
	}
}

func TestRunWorkflowCommandRunsStatusFlagWithoutValue(t *testing.T) {
	if err := runWorkflowCommandRuns([]string{"--status"}, t.TempDir(), "", io.Discard, io.Discard); err == nil {
		t.Fatal("error = nil, want a missing --status value error")
	}
}

func TestRunWorkflowWithIONoSubcommand(t *testing.T) {
	err := runWorkflowWithIO([]string{"--workspace", t.TempDir()}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "runs") {
		t.Fatalf("error = %v, want the subcommand list naming runs", err)
	}
}

func TestExecuteWorkflowRunsListFailure(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-IIII9999")
	defer closeFn()
	original := workflowRunsList
	t.Cleanup(func() { workflowRunsList = original })
	sentinel := errors.New("injected list failure")
	workflowRunsList = func(context.Context, workflowledger.Repository, ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
		return nil, sentinel
	}
	err := executeWorkflowRuns(root, filepath.Join(root, "config.toml"), "", 20, io.Discard, io.Discard)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
}

// TestExecuteWorkflowRunsTruncatesToLimit pins that --limit actually bounds
// the listing when more runs exist than the limit allows.
func TestExecuteWorkflowRunsTruncatesToLimit(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-JJJJ0001")
	defer closeFn()
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	second := workflowledger.RunSnapshot{
		RunID: "wfr-JJJJ0002", WorkflowName: "test-wf",
		Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}
	if err := repo.CreateRun(ctx, second, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, filepath.Join(root, "config.toml"), "", 1, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	if got := strings.Count(strings.TrimSpace(stdout.String()), "\n"); got != 0 {
		t.Errorf("limit 1 printed %d newline(s); want a single run line:\n%s", got, stdout.String())
	}
}

// TestFailedAttemptDiagnosticRef pins which ref carries a failed attempt's
// diagnostic. An agent gate writes ErrorRef; an evidence gate's verifier
// output IS the diagnostic and lands in OutputRef with ErrorRef empty, so
// reading only ErrorRef showed a bare digest for every failed test, verify,
// and preflight gate.
func TestFailedAttemptDiagnosticRef(t *testing.T) {
	cases := []struct {
		name    string
		attempt workflowledger.StepAttempt
		want    string
	}{
		{"agent gate uses ErrorRef", workflowledger.StepAttempt{
			Status: workflowledger.AttemptStatusFailed, ErrorRef: "sha256:err", OutputRef: "sha256:out",
		}, "sha256:err"},
		{"evidence gate falls back to OutputRef", workflowledger.StepAttempt{
			Status: workflowledger.AttemptStatusFailed, OutputRef: "sha256:out",
		}, "sha256:out"},
		{"succeeded attempt yields nothing", workflowledger.StepAttempt{
			Status: workflowledger.AttemptStatusSucceeded, OutputRef: "sha256:out",
		}, ""},
	}
	for _, tc := range cases {
		if got := failedAttemptDiagnosticRef(tc.attempt); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
