package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// A gate failure whose Check.Failures line names a write-blocklisted path
// must never reach the declared repair step: no workflow agent can write
// that path, so dispatching repair would only burn an attempt before
// failing with the same message this pre-check gives immediately.
// Regression: wfr-inv-0db263bf6f08df3f30862788d9c67d28 spent a full
// repair_preflight_structure attempt (~340s) on a go-structure violation
// naming a blocklisted file, then failed anyway with the identical cause.
func TestBlockedGateFailureNeverReachesRepair(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-blocked-gate", InitialStep: "preflight_structure",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8},
		Steps: []definition.Step{
			{ID: "preflight_structure", Kind: "evidence_gate", Verifier: "go-structure", OnFailure: "repair"},
			{ID: "repair", Kind: "agent", Agent: "dev"},
		},
		Transitions: []definition.Transition{
			{From: "preflight_structure", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "preflight_structure", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair", To: "preflight_structure", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "go-structure", result: verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{{
			Name: "go-structure", Status: "failed", Class: "source",
			Failures: []string{
				"NOTE comment block: internal/workflows/definition/step_defaults.go L5-30 (26 lines, soft 25) - consider docs/ for long rationale.",
			},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{}}
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-blocked-gate", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetWritePathBlocklist([]string{"internal/workflows/definition/step_defaults.go"}); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatal("run error = nil, want the blocked-path cause to reach the caller")
	}
	if !strings.Contains(err.Error(), "internal/workflows/definition/step_defaults.go") {
		t.Fatalf("run error = %v, want it to name the blocked path", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %q, want failed", got.Status)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("repair step ran %d time(s), want 0: a blocked-path gate failure must never dispatch repair: %+v", len(runner.calls), runner.calls)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := latestAttempt(attempts, "preflight_structure")
	if !ok || gate.Status != workflowledger.AttemptStatusFailed || gate.ToStepID != "failure" {
		t.Fatalf("gate attempt = %+v, want failed routed to the terminal failure, not the un-honored repair target", gate)
	}
}

// A gate failure whose Failures lines name only non-blocklisted paths must
// still reach the declared repair step: the pre-check must not over-match.
func TestUnblockedGateFailureStillReachesRepair(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-unblocked-gate", InitialStep: "preflight_structure",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8},
		Steps: []definition.Step{
			{ID: "preflight_structure", Kind: "evidence_gate", Verifier: "go-structure", OnFailure: "repair"},
			{ID: "repair", Kind: "agent", Agent: "dev"},
		},
		Transitions: []definition.Transition{
			{From: "preflight_structure", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "preflight_structure", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair", To: "preflight_structure", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "go-structure", result: verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{{
			Name: "go-structure", Status: "failed", Class: "source",
			Failures: []string{
				"NOTE comment block: internal/cli/context_usage_events_integration_test.go L114-139 (26 lines, soft 25) - consider docs/ for long rationale.",
			},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{}}
	runner.outputsByStepCall[repairCallKey(1)] = json.RawMessage(`{"summary":"repaired"}`)
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-unblocked-gate", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	// Blocklist a DIFFERENT path than the one the failure names.
	if err := ctrl.SetWritePathBlocklist([]string{"internal/workflows/definition/step_defaults.go"}); err != nil {
		t.Fatal(err)
	}
	_, _ = ctrl.Run(context.Background())
	if len(runner.calls) == 0 {
		t.Fatal("the repair step never ran; an unblocked gate failure must still reach its declared repair target")
	}
}

// blockedPathsFromGateFailures must dedupe a path repeated across multiple
// failure lines and across multiple failed checks, and must ignore paths
// from a check that PASSED (only "failed" checks are scanned).
func TestBlockedPathsFromGateFailuresDedupesAcrossChecksAndLines(t *testing.T) {
	ctrl := &LinearController{WritePathBlocklist: []string{"internal/mcp", "scripts"}}
	result := verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{
			{
				Name: "go-structure", Status: "failed",
				Failures: []string{
					"NOTE comment block: internal/mcp/gateway.go L1-30 (30 lines, soft 25) - consider docs/ for long rationale.",
					"WARN file LOC: internal/mcp/gateway.go has 900 lines (soft max 500). Consider splitting soon.",
				},
			},
			{
				Name: "go-vet", Status: "failed",
				Failures: []string{
					"NOTE comment block: scripts/check_go_structure.py L1-30 (30 lines, soft 25) - consider docs/ for long rationale.",
				},
			},
			{
				// PASSED check: its Failures (if any leaked through) must be ignored.
				Name: "go-test", Status: "passed",
				Failures: []string{"internal/mcp/unrelated.go should also be ignored"},
			},
		},
	}
	got := ctrl.blockedPathsFromGateFailures(result)
	want := []string{"internal/mcp/gateway.go", "scripts/check_go_structure.py"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("blockedPathsFromGateFailures() = %v, want %v (deduped, passed-check paths excluded)", got, want)
	}
}

// A failure line naming no blocklisted path must return nil, not an empty
// slice that a caller might mistake for "checked and found none blocked".
func TestBlockedPathsFromGateFailuresReturnsNilWhenNoneBlocked(t *testing.T) {
	ctrl := &LinearController{WritePathBlocklist: []string{"internal/mcp"}}
	result := verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{
			{Name: "go-structure", Status: "failed", Failures: []string{
				"NOTE comment block: internal/cli/foo.go L1-30 (30 lines, soft 25) - consider docs/ for long rationale.",
			}},
		},
	}
	if got := ctrl.blockedPathsFromGateFailures(result); len(got) != 0 {
		t.Fatalf("blockedPathsFromGateFailures() = %v, want none", got)
	}
}
