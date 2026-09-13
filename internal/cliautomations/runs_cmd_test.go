package cliautomations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// automationStorePath resolves the SAME on-disk store path
// openAutomationStore/buildService (automations.go) resolves for root:
// re-load root's own config and run it through the identical resolver,
// rather than hardcoding a filename here, so this helper cannot drift
// from production's own path once a fixture's [subagents] store_path
// changes.
func automationStorePath(t *testing.T, root string) string {
	t.Helper()
	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig(%q, \"\"): %v", root, err)
	}
	return clichat.ContextStorePath(gotRoot, res.Subagents)
}

// insertAutomationRun opens root's automation/session store (the same
// path openAutomationStore/buildService uses) and inserts one run row
// directly, then closes the handle before returning - so the row
// exists durably on disk without leaving any lock the CLI command's own
// buildService call (invoked afterward, in the same test) would
// contend with. This lets runs_cmd_test.go seed deterministic
// StartedAt timestamps (second-granularity RFC3339, hours apart) for
// sort/limit assertions without depending on real wall-clock spacing
// between fired runs.
func insertAutomationRun(t *testing.T, root string, r storage.AutomationRun) {
	t.Helper()
	db, err := storage.OpenSQLite(automationStorePath(t, root))
	if err != nil {
		t.Fatalf("open automation store: %v", err)
	}
	defer db.Close()
	if err := db.InsertAutomationRun(context.Background(), r); err != nil {
		t.Fatalf("insert automation run: %v", err)
	}
}

// seedTwoAutomations writes a fixture (via writeAutomationsFixture, so
// config + one automation already exists) and then overwrites
// automations.toml with two enabled automations, idA and idB, each
// with a single StepPrompt step - runs_cmd_test.go's own multi-
// automation fixture.
func seedTwoAutomations(t *testing.T, idA, idB string) string {
	t.Helper()
	root := writeAutomationsFixture(t, idA)
	specA := automation.Spec{ID: idA, Name: idA, Enabled: true, Steps: []automation.Step{{Kind: automation.StepPrompt, Prompt: "hi"}}}
	specB := automation.Spec{ID: idB, Name: idB, Enabled: true, Steps: []automation.Step{{Kind: automation.StepPrompt, Prompt: "hi"}}}
	if err := automation.SaveSpecs(ports.ScopeProject, root, []automation.Spec{specA, specB}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}
	return root
}

// TestRunsCommandFiltersByAutomation proves --automation restricts the
// listed runs to exactly that automation's own history.
func TestRunsCommandFiltersByAutomation(t *testing.T) {
	root := seedTwoAutomations(t, "auto-a", "auto-b")
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-a-1", AutomationID: "auto-a", State: "succeeded", StartedAt: base.Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-b-1", AutomationID: "auto-b", State: "succeeded", StartedAt: base.Format(time.RFC3339)})

	stdout, _ := captureOutput(t, func() {
		if err := runRunsCommand([]string{"--workspace", root, "--automation", "auto-a"}); err != nil {
			t.Fatalf("runRunsCommand: %v", err)
		}
	})
	if !strings.Contains(stdout, "run-a-1") {
		t.Fatalf("runs --automation auto-a output = %q, want run-a-1 listed", stdout)
	}
	if strings.Contains(stdout, "run-b-1") {
		t.Fatalf("runs --automation auto-a output = %q, want run-b-1 excluded", stdout)
	}
}

// TestRunsCommandHonorsLimit proves --limit truncates a single
// automation's run list to the N most recent.
func TestRunsCommandHonorsLimit(t *testing.T) {
	root := writeAutomationsFixture(t, "limit-me")
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-1", AutomationID: "limit-me", State: "succeeded", StartedAt: base.Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-2", AutomationID: "limit-me", State: "succeeded", StartedAt: base.Add(time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-3", AutomationID: "limit-me", State: "succeeded", StartedAt: base.Add(2 * time.Hour).Format(time.RFC3339)})

	stdout, _ := captureOutput(t, func() {
		if err := runRunsCommand([]string{"--workspace", root, "--automation", "limit-me", "--limit", "2"}); err != nil {
			t.Fatalf("runRunsCommand: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("runs --limit 2 output = %q, want exactly 2 lines", stdout)
	}
	if !strings.Contains(stdout, "run-3") || !strings.Contains(stdout, "run-2") {
		t.Fatalf("runs --limit 2 output = %q, want the two most recent runs (run-3, run-2)", stdout)
	}
	if strings.Contains(stdout, "run-1") {
		t.Fatalf("runs --limit 2 output = %q, want run-1 (oldest) excluded", stdout)
	}
}

// TestRunsCommandAcrossAllAutomationsSortedByStart proves the no-
// --automation path concatenates every automation's runs client-side
// and sorts the combined slice by StartedAt descending.
func TestRunsCommandAcrossAllAutomationsSortedByStart(t *testing.T) {
	root := seedTwoAutomations(t, "auto-a", "auto-b")
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-a-1", AutomationID: "auto-a", State: "succeeded", StartedAt: base.Add(1 * time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-a-2", AutomationID: "auto-a", State: "succeeded", StartedAt: base.Add(3 * time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-b-1", AutomationID: "auto-b", State: "succeeded", StartedAt: base.Add(2 * time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "run-b-2", AutomationID: "auto-b", State: "succeeded", StartedAt: base.Add(4 * time.Hour).Format(time.RFC3339)})

	stdout, _ := captureOutput(t, func() {
		if err := runRunsCommand([]string{"--workspace", root, "--limit", "10"}); err != nil {
			t.Fatalf("runRunsCommand: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 {
		t.Fatalf("runs (all automations) output = %q, want 4 lines", stdout)
	}
	wantOrder := []string{"run-b-2", "run-a-2", "run-b-1", "run-a-1"}
	for i, want := range wantOrder {
		if !strings.HasPrefix(lines[i], want+"\t") {
			t.Fatalf("runs (all automations) line %d = %q, want it to start with %q (descending StartedAt)", i, lines[i], want)
		}
	}
}

// TestRunsCommandEmptyPrintsNoRuns proves an automation with zero
// recorded runs prints the documented "no runs recorded" line.
func TestRunsCommandEmptyPrintsNoRuns(t *testing.T) {
	root := writeAutomationsFixture(t, "no-runs-yet")
	stdout, _ := captureOutput(t, func() {
		if err := runRunsCommand([]string{"--workspace", root}); err != nil {
			t.Fatalf("runRunsCommand: %v", err)
		}
	})
	if !strings.Contains(stdout, "no runs recorded") {
		t.Fatalf("runs output = %q, want \"no runs recorded\"", stdout)
	}
}

func TestRunsCommandRejectsPositionalArgs(t *testing.T) {
	root := writeAutomationsFixture(t, "runs-positional")
	if err := runRunsCommand([]string{"--workspace", root, "unexpected"}); err == nil {
		t.Fatal("runRunsCommand with an unexpected positional arg: got nil error, want rejection")
	}
}

func TestRunsCommandParseFlagsErrorPropagates(t *testing.T) {
	if err := runRunsCommand([]string{"--workspace"}); err == nil {
		t.Fatal("runRunsCommand with --workspace missing its value: got nil error")
	}
}

func TestRunsCommandBuildServiceErrorPropagates(t *testing.T) {
	err := runRunsCommand([]string{"--workspace", filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("runRunsCommand against a nonexistent workspace: got nil error")
	}
}

// TestRunsCommandMalformedLimitErrors covers --limit's own
// strconv.Atoi error branch: a non-integer value is rejected rather
// than silently falling back to the default.
func TestRunsCommandMalformedLimitErrors(t *testing.T) {
	root := writeAutomationsFixture(t, "runs-bad-limit")
	err := runRunsCommand([]string{"--workspace", root, "--limit", "not-a-number"})
	if err == nil {
		t.Fatal("runRunsCommand with a malformed --limit: got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("runRunsCommand malformed --limit error = %q, want it naming --limit", err.Error())
	}
}

// TestRunsCommandAutomationFlagMissingValueErrors covers
// flagValueSeam's own "requires a value" error branch for --automation:
// the flag given with nothing (or another flag) after it, rather than
// a malformed value.
func TestRunsCommandAutomationFlagMissingValueErrors(t *testing.T) {
	if err := runRunsCommand([]string{"--automation"}); err == nil {
		t.Fatal("runRunsCommand with --automation missing its value: got nil error, want rejection")
	}
}

// TestRunsCommandLimitFlagMissingValueErrors covers flagValueSeam's own
// "requires a value" error branch for --limit specifically (distinct
// from TestRunsCommandMalformedLimitErrors, which covers strconv.Atoi's
// error on a present-but-non-integer value).
func TestRunsCommandLimitFlagMissingValueErrors(t *testing.T) {
	if err := runRunsCommand([]string{"--limit"}); err == nil {
		t.Fatal("runRunsCommand with --limit missing its value: got nil error, want rejection")
	}
}

// TestRunsCommandAcrossAllAutomationsTruncatesToLimit proves the
// no---automation cross-automation aggregation path's own truncation
// branch (`if len(runs) > limit { runs = runs[:limit] }`): more runs
// exist across automations than --limit allows, and the printed output
// must still be exactly `limit` rows, the most recent ones.
func TestRunsCommandAcrossAllAutomationsTruncatesToLimit(t *testing.T) {
	root := seedTwoAutomations(t, "trunc-a", "trunc-b")
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	insertAutomationRun(t, root, storage.AutomationRun{ID: "trunc-a-1", AutomationID: "trunc-a", State: "succeeded", StartedAt: base.Add(1 * time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "trunc-a-2", AutomationID: "trunc-a", State: "succeeded", StartedAt: base.Add(3 * time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "trunc-b-1", AutomationID: "trunc-b", State: "succeeded", StartedAt: base.Add(2 * time.Hour).Format(time.RFC3339)})
	insertAutomationRun(t, root, storage.AutomationRun{ID: "trunc-b-2", AutomationID: "trunc-b", State: "succeeded", StartedAt: base.Add(4 * time.Hour).Format(time.RFC3339)})

	stdout, _ := captureOutput(t, func() {
		if err := runRunsCommand([]string{"--workspace", root, "--limit", "2"}); err != nil {
			t.Fatalf("runRunsCommand: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("runs (all automations) --limit 2 output = %q, want exactly 2 lines (4 runs exist, truncation must apply)", stdout)
	}
	if !strings.HasPrefix(lines[0], "trunc-b-2\t") || !strings.HasPrefix(lines[1], "trunc-a-2\t") {
		t.Fatalf("runs (all automations) --limit 2 output = %q, want the two most recent runs (trunc-b-2, trunc-a-2)", stdout)
	}
}

// TestRunsCommandRejectsNonPositiveLimit covers --limit's sign contract,
// which TestRunsCommandMalformedLimitErrors does not reach: a negative
// integer parses cleanly through strconv.Atoi and then landed in the
// cross-automation truncation as `runs[:limit]`, panicking the CLI with
// "slice bounds out of range" (the guard `len(runs) > limit` is true even
// for an empty slice when limit is negative). Zero is refused for the
// sibling reason: storage reads limit <= 0 as "unlimited", so `--limit 0`
// would print the entire history instead of nothing.
func TestRunsCommandRejectsNonPositiveLimit(t *testing.T) {
	for _, argv := range [][]string{{"--limit=-1"}, {"--limit", "-3"}, {"--limit", "0"}} {
		args := append([]string{"--workspace", writeAutomationsFixture(t, "runs-limit-sign")}, argv...)
		err := runRunsCommand(args)
		if err == nil {
			t.Fatalf("runRunsCommand %v: got nil error, want a --limit rejection", argv)
		}
		if !strings.Contains(err.Error(), "--limit") {
			t.Fatalf("runRunsCommand %v error = %q, want it naming --limit", argv, err.Error())
		}
	}
}
