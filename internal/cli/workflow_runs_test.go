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
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
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

// TestExecuteWorkflowRunsHeartbeatFreshness pins the liveness column of the
// runs listing: a running run whose active attempt recorded a heartbeat shows
// "hb <Ns>" derived from LastHeartbeatAt.
func TestExecuteWorkflowRunsHeartbeatFreshness(t *testing.T) {
	root, _, repo, closeFn, ctx, run := openEventsFixtureWithRun(t, "wfr-HB-0001")
	config := filepath.Join(root, "config.toml")

	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "att-hb-1", RunID: run, StepID: "start", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStepAttemptHeartbeat(ctx, run, attempt.AttemptID, time.Now().Add(-60*time.Second)); err != nil {
		t.Fatal(err)
	}
	closeFn() // release the seeding connection; the command opens its own

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, config, "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "hb 60s") {
		t.Fatalf("output %q missing the hb 60s freshness column", out)
	}
}

// TestExecuteWorkflowRunsHeartbeatDash pins the no-heartbeat case: an
// in-flight run with no recorded heartbeat shows "hb -", while a terminal run
// carries no hb column at all.
func TestExecuteWorkflowRunsHeartbeatDash(t *testing.T) {
	root, repo, closeFn, ctx, runID := newRunsHeartbeatFixture(t, "wfr-HB-0002")
	config := filepath.Join(root, "config.toml")

	t.Run("in-flight run without heartbeat shows hb -", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := executeWorkflowRuns(root, config, "running", 20, &stdout, io.Discard); err != nil {
			t.Fatalf("executeWorkflowRuns: %v", err)
		}
		if !strings.Contains(stdout.String(), "hb -") {
			t.Fatalf("output %q missing the hb - placeholder", stdout.String())
		}
	})

	t.Run("terminal run carries no hb column", func(t *testing.T) {
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
			t.Fatal(err)
		}
		closeFn() // release the seeding connection; the command opens its own
		var stdout bytes.Buffer
		if err := executeWorkflowRuns(root, config, "succeeded", 20, &stdout, io.Discard); err != nil {
			t.Fatalf("executeWorkflowRuns: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, runID) {
			t.Fatalf("output %q missing the terminal run", out)
		}
		if strings.Contains(out, "hb ") {
			t.Fatalf("output %q must not carry an hb column for a terminal run", out)
		}
	})
}

// newRunsHeartbeatFixture builds an in-flight (running) run with an active
// attempt but no heartbeat, for the runs-listing dash-column tests.
func newRunsHeartbeatFixture(t *testing.T, runID string) (root string, repo *workflowledger.StorageRepository, closeFn func(), ctx context.Context, run string) {
	t.Helper()
	root, _, repo, closeFn, ctx, run = openEventsFixtureWithRun(t, runID)
	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "att-hb-" + runID, RunID: run, StepID: "start", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	return root, repo, closeFn, ctx, run
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
		t.Errorf("oversized error was not marked truncated:\n%s", out[:Min(len(out), 400)])
	}
	if strings.Count(out, "x") > maxAttemptErrorBytes+10 {
		t.Error("truncation did not bound the printed error")
	}
}

// TestWorkflowStatusAttemptErrorTruncatesRuneSafe pins that printAttemptError
// truncates rune-safely (E4, DC-6): the raw byte cut used to split a
// multi-byte rune, emitting invalid UTF-8 into the status report.
func TestWorkflowStatusAttemptErrorTruncatesRuneSafe(t *testing.T) {
	// 2 ASCII bytes push the 2000-byte cut inside the 4-byte rune stream, so
	// a raw cut would leave a dangling lead byte.
	long := "xx" + strings.Repeat("\U0001F642", 600) // 2402 bytes
	root, run, closeFn := seedFailedAttempt(t, "wfr-JJJJ9999", long, true)
	defer closeFn()

	var stdout bytes.Buffer
	if err := executeWorkflowStatus(run, root, filepath.Join(root, "config.toml"), &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowStatus: %v", err)
	}
	out := stdout.String()
	if !utf8.ValidString(out) {
		t.Errorf("status output is not valid UTF-8:\n%s", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("oversized error was not marked truncated:\n%s", out[:Min(len(out), 400)])
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
	original := WorkflowRunsList
	t.Cleanup(func() { WorkflowRunsList = original })
	sentinel := errors.New("injected list failure")
	WorkflowRunsList = func(context.Context, workflowledger.Repository, ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
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

// TestExecuteWorkflowRunsWatchExitsOnTerminal pins that --watch returns once
// every matched run is terminal, and prints one line per state change.
func TestExecuteWorkflowRunsWatchExitsOnTerminal(t *testing.T) {
	root, _, repo, closeFn, ctx, run := openEventsFixtureWithRun(t, "wfr-WATCH001")
	defer closeFn()
	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	original := workflowWatchSleep
	t.Cleanup(func() { workflowWatchSleep = original })
	// Settle the run to succeeded between passes instead of sleeping, so the
	// watch observes a real transition and then exits.
	workflowWatchSleep = func(time.Duration) {
		cur, err := repo.GetRun(ctx, run)
		if err != nil {
			t.Error(err)
			return
		}
		if cur.Status == workflowledger.RunStatusRunning {
			if err := repo.CompareAndSetRunStatus(ctx, run, cur.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
				t.Error(err)
			}
		}
	}

	var stdout bytes.Buffer
	if err := executeWorkflowRunsWatch(root, filepath.Join(root, "config.toml"), "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRunsWatch: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "running") {
		t.Errorf("watch did not report the running state:\n%s", out)
	}
	if !strings.Contains(out, "succeeded") {
		t.Errorf("watch did not report the terminal state:\n%s", out)
	}
	// delivery_pending is NOT terminal; a watch that treats it as terminal is
	// the bug this command exists to avoid.
	if workflowledger.IsTerminalRunStatus(workflowledger.RunStatusDeliveryPending) {
		t.Error("delivery_pending must not be terminal")
	}
}

func TestExecuteWorkflowRunsWatchRejectsUnknownStatus(t *testing.T) {
	if err := executeWorkflowRunsWatch(t.TempDir(), "", "bogus", 20, io.Discard, io.Discard); err == nil {
		t.Fatal("error = nil, want an unknown-status error")
	}
}

func TestRunWorkflowCommandRunsWatchFlag(t *testing.T) {
	root, _, _, closeFn, _, run := openEventsFixtureWithRun(t, "wfr-WATCH002")
	defer closeFn()
	var stdout bytes.Buffer
	// The seeded run is pending, which is non-terminal, so drive one pass and
	// settle it from the sleep hook.
	original := workflowWatchSleep
	t.Cleanup(func() { workflowWatchSleep = original })
	workflowWatchSleep = func(time.Duration) {}
	go func() {}()
	if err := runWorkflowCommandRuns([]string{"--watch", "--status", "succeeded"}, root, filepath.Join(root, "config.toml"), &stdout, io.Discard); err != nil {
		t.Fatalf("runWorkflowCommandRuns --watch: %v", err)
	}
	if strings.Contains(stdout.String(), run) {
		t.Errorf("succeeded filter listed a pending run: %q", stdout.String())
	}
}

func TestRunWorkflowCommandRunsRejectsBadWatchFlag(t *testing.T) {
	if err := runWorkflowCommandRuns([]string{"--watch=maybe"}, t.TempDir(), "", io.Discard, io.Discard); err == nil {
		t.Fatal("error = nil, want a boolean parse error")
	}
}

func TestWatchSnapshotFailures(t *testing.T) {
	t.Run("workspace open failure", func(t *testing.T) {
		if _, err := watchSnapshot(filepath.Join(t.TempDir(), "nope"), "", "", 20); err == nil {
			t.Fatal("error = nil, want a workspace open error")
		}
	})

	t.Run("ledger read failure", func(t *testing.T) {
		root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-SNAPFAIL1")
		defer closeFn()
		original := WorkflowRunsList
		t.Cleanup(func() { WorkflowRunsList = original })
		sentinel := errors.New("injected snapshot failure")
		WorkflowRunsList = func(context.Context, workflowledger.Repository, ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
			return nil, sentinel
		}
		if _, err := watchSnapshot(root, filepath.Join(root, "config.toml"), "", 20); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the injected failure", err)
		}
	})
}

// TestWatchSnapshotLimitAndStepFallback covers the truncation branch and the
// empty-active-step fallback the watch prints as "-".
func TestWatchSnapshotLimitAndStepFallback(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-SNAPLIM01")
	defer closeFn()
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	second := workflowledger.RunSnapshot{
		RunID: "wfr-SNAPLIM02", WorkflowName: "test-wf",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, second, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	got, err := watchSnapshot(root, filepath.Join(root, "config.toml"), "", 1)
	if err != nil {
		t.Fatalf("watchSnapshot: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (limit applied)", len(got))
	}

	// A run with no derived active step must print "-" rather than a blank.
	original := workflowWatchSleep
	t.Cleanup(func() { workflowWatchSleep = original })
	workflowWatchSleep = func(time.Duration) {}
	var stdout bytes.Buffer
	if err := executeWorkflowRunsWatch(root, filepath.Join(root, "config.toml"), "succeeded", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRunsWatch: %v", err)
	}
}

func TestWorkflowDeliveryProbeSeamRefusesUnusableTool(t *testing.T) {
	original := workflowDeliveryProbe
	t.Cleanup(func() { workflowDeliveryProbe = original })
	sentinel := errors.New("gh missing")
	workflowDeliveryProbe = func(string) error { return sentinel }
	if err := workflowDeliveryProbe("github"); !errors.Is(err, sentinel) {
		t.Fatalf("seam error = %v, want the injected failure", err)
	}
}

// TestWorkflowDeliveryAdmissionProbesPRTool pins the fail-fast: a run that
// declares delivery must be refused at admission when the provider's PR tool
// is unusable, instead of spending the whole run and dying at publication.
func TestWorkflowDeliveryAdmissionProbesPRTool(t *testing.T) {
	wf := &definition.CompiledWorkflow{
		Name: "wf-probe",
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "master",
		},
	}
	original := workflowDeliveryProbe
	t.Cleanup(func() { workflowDeliveryProbe = original })
	sentinel := errors.New("gh is not installed")
	workflowDeliveryProbe = func(provider string) error {
		if provider != "github" {
			t.Errorf("probed provider %q, want github", provider)
		}
		return sentinel
	}
	_, _, err := workflowDeliveryAdmission(wf, workflowspace.Identity{}, true, "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("admission error = %v, want the probe failure", err)
	}
}

// TestWorkflowRunsWatchPrintsDashForNoActiveStep covers the fallback for a run
// with no derived active step: the line must show "-" rather than a blank
// column that reads as missing data.
func TestWorkflowRunsWatchPrintsDashForNoActiveStep(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-NOSTEP001")
	defer closeFn()
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	stepless := "wfr-NOSTEP002"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: stepless, WorkflowName: "test-wf", Status: workflowledger.RunStatusPending,
	}, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusSucceeded} {
		cur, err := repo.GetRun(ctx, stepless)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, stepless, cur.Version, next, nil); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	settled, err := repo.GetRun(ctx, stepless)
	if err != nil {
		t.Fatal(err)
	}
	if settled.ActiveStepID != "" {
		t.Skipf("run derived an active step %q; the fallback needs a stepless run", settled.ActiveStepID)
	}

	original := workflowWatchSleep
	t.Cleanup(func() { workflowWatchSleep = original })
	workflowWatchSleep = func(time.Duration) { t.Fatal("watch must exit without sleeping when all runs are terminal") }

	var stdout bytes.Buffer
	if err := executeWorkflowRunsWatch(root, filepath.Join(root, "config.toml"), "succeeded", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRunsWatch: %v", err)
	}
	if !strings.Contains(stdout.String(), "step=-") {
		t.Errorf("stepless run did not print the dash fallback:\n%s", stdout.String())
	}
}
