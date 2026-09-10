package cliautomations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// insertAutomationRun opens root's automations.db (the same path
// openAutomationStore/buildService uses) and inserts one run row
// directly, then closes the handle before returning - so the row
// exists durably on disk without leaving any lock the CLI command's own
// buildService call (invoked afterward, in the same test) would
// contend with. This lets runs_cmd_test.go seed deterministic
// StartedAt timestamps (second-granularity RFC3339, hours apart) for
// sort/limit assertions without depending on real wall-clock spacing
// between fired runs.
func insertAutomationRun(t *testing.T, root string, r storage.AutomationRun) {
	t.Helper()
	db, err := storage.OpenSQLite(workspace.NamespacePath(root, "automations.db"))
	if err != nil {
		t.Fatalf("open automations db: %v", err)
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
