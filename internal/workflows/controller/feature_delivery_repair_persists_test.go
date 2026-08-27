package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// repairChangeSummarySchema is the change-summary-v1 shape the implement and
// repair_pr_metadata steps validate against (mirrors
// .mivia/workflows/schemas/change-summary-v1.json): pr_title and pr_summary
// are required, so a repair step that rewrites the title MUST carry it in its
// recorded output or its attempt fails schema validation.
var repairChangeSummarySchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"summary", "files_changed", "inspected", "addressed_findings", "pr_title", "pr_summary"},
	"properties": map[string]any{
		"summary":            map[string]any{"type": "string", "minLength": 1},
		"files_changed":      map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "minLength": 1}},
		"inspected":          map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "minLength": 1}},
		"addressed_findings": map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}},
		"pr_title":           map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"pr_summary":         map[string]any{"type": "string", "minLength": 1},
	},
}

func repairChangeSummaryJSON(title, summary string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"pr_title":           title,
		"pr_summary":         summary,
		"summary":            "repair",
		"files_changed":      []string{"a.go"},
		"inspected":          []string{"a.go"},
		"addressed_findings": []string{},
	})
	return b
}

// featureDeliveryRepairChain compiles a feature-delivery-shaped workflow: the
// implement step's change summary is reviewed by the panel, the integration
// gate and the five preflight gates, and only then does the run reach the
// delivery terminal. A PR-metadata delivery rejection routes back to
// repair_pr_metadata, whose success re-enters review_panel — the exact chain
// the shipped feature-delivery.toml re-executes between a metadata repair and
// the next delivery attempt.
func featureDeliveryRepairChain(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "feature-delivery-repair-chain", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OutputSchema: "schemas/change-summary-v1.json"},
			{ID: "review_panel", Kind: "agent_gate", Agent: "rev", OutputSchema: "schemas/review-panel-v1.json"},
			{ID: "review_integration", Kind: "agent_gate", Agent: "rev", OutputSchema: "schemas/review-v1.json"},
			{ID: "test_validate", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "verify", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "code_validate", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "preflight_validate", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "preflight_structure", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "repair_pr_metadata", Kind: "agent", Agent: "dev", OutputSchema: "schemas/change-summary-v1.json",
				Context: []definition.ContextBinding{{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review_panel", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review_panel", To: "review_integration", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"host_verdict": "approved"}}},
			{From: "review_integration", To: "test_validate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "test_validate", To: "verify", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "verify", To: "code_validate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "code_validate", To: "preflight_validate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "preflight_validate", To: "preflight_structure", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "preflight_structure", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "repair_pr_metadata", To: "review_panel", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
			OnPRMetadataFailure: "repair_pr_metadata",
			MaxRepairs:          5,
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestPRMetadataRepairPersistsThroughFullChain pins the delivery repair-loop
// contract end to end at the controller level: implement produces a
// non-conforming pr_title, the run reaches delivery_pending, the delivery
// rejection re-opens the run at repair_pr_metadata, the repair step rewrites
// the metadata, and the run re-executes the WHOLE post-repair chain
// (review_panel -> review_integration -> the five gates -> success). The next
// delivery must then resolve the FIXED title — not the old one — and the run
// must NOT re-churn the loop (no new wf-delivery failure attempt, exactly one
// repair). A metadata repair that actually fixed the cause must persist, so a
// stuck loop cannot keep burning max_repairs.
func TestPRMetadataRepairPersistsThroughFullChain(t *testing.T) {
	exerciseRepairChainPersistence(t, workflowledger.NewMemoryRepository())
}

func TestPRMetadataRepairPersistsThroughFullChainSQLite(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	exerciseRepairChainPersistence(t, workflowledger.NewStorageRepository(store))
}

// exerciseRepairChainPersistence drives the delivery-repair loop once: the
// first run ends delivery_pending with the OLD non-conforming title, the
// repair step rewrites the metadata, the chain re-runs, and the resolved
// summary must then be the FIXED title with no delivery re-churn.
func exerciseRepairChainPersistence(t *testing.T, repo workflowledger.Repository) {
	t.Helper()
	ctrl, runner := repairChainController(t, repo)
	runRepairChainFirstPhase(t, repo, ctrl, runner)
	runRepairChainSecondPhase(t, repo, ctrl, runner)
}

// repairChainController builds the controller for the delivery-repair chain.
// implement produces the OLD non-conforming title, so the first delivery must
// reject the metadata and re-open the run at repair_pr_metadata.
func repairChainController(t *testing.T, repo workflowledger.Repository) (*LinearController, *linearRunner) {
	t.Helper()
	ctx := context.Background()
	compiled := featureDeliveryRepairChain(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{
		"implement":          repairChangeSummaryJSON("textutil: add TruncateEllipsis rune-safe ellipsis truncation", "Adds the helper. Needed for delivery."),
		"review_panel":       json.RawMessage(`{"host_verdict":"approved"}`),
		"review_integration": json.RawMessage(`{"verdict":"approved"}`),
	}}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: compiled.Digest})
	if err != nil {
		t.Fatal(err)
	}
	changeSummary := StepRuntime{Agent: agents.ResolvedAgent{Name: "dev"}, Schema: repairChangeSummarySchema}
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement":          changeSummary,
		"review_panel":       {Agent: agents.ResolvedAgent{Name: "rev"}},
		"review_integration": {Agent: agents.ResolvedAgent{Name: "rev"}},
		"repair_pr_metadata": changeSummary,
	}, map[string]any{"task": "x"}, "wfr-repair-chain", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(stubVerifierProfile{name: "go-default", checks: []definition.Check{{Name: "workspace-dir", Status: "passed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return ctrl, runner
}

// runRepairChainFirstPhase runs implement -> full chain -> delivery_pending,
// asserts the OLD title resolves as the change summary, then re-opens the run
// at repair_pr_metadata exactly as ReopenForRepair does after a metadata
// refusal from delivery.
func runRepairChainFirstPhase(t *testing.T, repo workflowledger.Repository, ctrl *LinearController, runner *linearRunner) {
	t.Helper()
	ctx := context.Background()
	got, err := ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("first run = %+v, err = %v, want delivery_pending", got, err)
	}
	summary, err := delivery.ResolveLatestChangeSummary(ctx, repo, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary["pr_title"].(string) != "textutil: add TruncateEllipsis rune-safe ellipsis truncation" {
		t.Fatalf("resolved pr_title before repair = %v, want the non-conforming title", summary["pr_title"])
	}
	refusal := &delivery.PRMetadataError{Reason: `delivery: pr_title "textutil: add TruncateEllipsis rune-safe ellipsis truncation" does not match pattern "^(?P<type>feat|fix|docs|chore|refactor|test)(\((?P<scope>[a-z0-9-]+)\))?!?: .+$"`}
	if err := delivery.ReopenForRepair(ctx, repo, ctrl.RunID, "repair_pr_metadata", delivery.MaxDeliveryRepairs, refusal, &discardWriter{}); err != nil {
		t.Fatalf("ReopenForRepair: %v", err)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if wfCount := countAttempts(attempts, delivery.DeliveryRepairStepID); wfCount != 1 {
		t.Fatalf("wf-delivery attempts after reopen = %d, want 1", wfCount)
	}
}

// runRepairChainSecondPhase swaps in the FIXED title for the repair step,
// re-runs the chain, and asserts the resolved summary is the repaired title
// and the loop did NOT re-churn (exactly one delivery rejection attempt and
// one repair, with the panel and gates re-executed).
func runRepairChainSecondPhase(t *testing.T, repo workflowledger.Repository, ctrl *LinearController, runner *linearRunner) {
	t.Helper()
	ctx := context.Background()
	runner.mu.Lock()
	runner.outputs["repair_pr_metadata"] = repairChangeSummaryJSON("feat(textutil): add TruncateEllipsis rune-safe ellipsis truncation", "Adds the helper. Needed for delivery.")
	runner.mu.Unlock()
	got, err := ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("second run = %+v, err = %v, want delivery_pending", got, err)
	}
	summary, err := delivery.ResolveLatestChangeSummary(ctx, repo, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("no change summary resolved after the repair")
	}
	if title := summary["pr_title"].(string); title != "feat(textutil): add TruncateEllipsis rune-safe ellipsis truncation" {
		t.Fatalf("resolved pr_title after repair = %q, want the repaired title", title)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if wfCount := countAttempts(attempts, delivery.DeliveryRepairStepID); wfCount != 1 {
		t.Fatalf("wf-delivery attempts after repair = %d, want 1 (the run must not re-churn the delivery loop)", wfCount)
	}
	if repairCount := countAttempts(attempts, "repair_pr_metadata"); repairCount != 1 {
		t.Fatalf("repair_pr_metadata attempts = %d, want 1", repairCount)
	}
	if panelCount := countAttempts(attempts, "review_panel"); panelCount != 2 {
		t.Fatalf("review_panel attempts = %d, want 2 (the chain must re-run after the repair)", panelCount)
	}
	if structureCount := countAttempts(attempts, "preflight_structure"); structureCount != 2 {
		t.Fatalf("preflight_structure attempts = %d, want 2 (the chain must re-run after the repair)", structureCount)
	}
}

func countAttempts(attempts []workflowledger.StepAttempt, stepID string) int {
	n := 0
	for _, a := range attempts {
		if a.StepID == stepID {
			n++
		}
	}
	return n
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// featureDeliveryRepairChainWithReviewLoop is the feature-delivery shape with
// a REAL review loop: the panel re-enters implement on changes_requested, so
// implement runs TWICE before delivery ever runs. That gives implement a
// higher AttemptNo than the later repair step (the controller numbers attempts
// per step and restarts at 1 for repair_pr_metadata), which is exactly the
// real-run shape that shadowed a metadata repair's fixed title under the old
// highest-AttemptNo resolution.
func featureDeliveryRepairChainWithReviewLoop(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "feature-delivery-repair-chain-review-loop", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OutputSchema: "schemas/change-summary-v1.json"},
			{ID: "review_panel", Kind: "agent_gate", Agent: "rev", OutputSchema: "schemas/review-panel-v1.json"},
			{ID: "review_integration", Kind: "agent_gate", Agent: "rev", OutputSchema: "schemas/review-v1.json"},
			{ID: "test_validate", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "verify", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "code_validate", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "preflight_validate", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "preflight_structure", Kind: "evidence_gate", Verifier: "go-default"},
			{ID: "repair_pr_metadata", Kind: "agent", Agent: "dev", OutputSchema: "schemas/change-summary-v1.json",
				Context: []definition.ContextBinding{{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review_panel", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review_panel", To: "review_integration", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"host_verdict": "approved"}}},
			{From: "review_panel", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"host_verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: 8},
			{From: "review_integration", To: "test_validate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "test_validate", To: "verify", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "verify", To: "code_validate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "code_validate", To: "preflight_validate", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "preflight_validate", To: "preflight_structure", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "preflight_structure", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "passed"}}},
			{From: "repair_pr_metadata", To: "review_panel", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
			OnPRMetadataFailure: "repair_pr_metadata",
			MaxRepairs:          5,
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// runPRMetadataReentryFirstPhase runs the chain to delivery_pending with the
// review loop's re-entry (implement#2 exists BEFORE delivery #1), asserts the
// OLD title still resolves as the change summary, then re-opens the run at
// repair_pr_metadata exactly as ReopenForRepair does after a metadata refusal
// from delivery.
func runPRMetadataReentryFirstPhase(t *testing.T, repo workflowledger.Repository, ctrl *LinearController) {
	t.Helper()
	ctx := context.Background()
	oldTitle := "textutil: add TruncateEllipsis rune-safe ellipsis truncation"

	got, err := ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("first run = %+v, err = %v, want delivery_pending", got, err)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if n := countAttempts(attempts, "implement"); n != 2 {
		t.Fatalf("implement attempts = %d, want 2 (review-loop re-entry)", n)
	}
	summary, err := delivery.ResolveLatestChangeSummary(ctx, repo, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary["pr_title"].(string) != oldTitle {
		t.Fatalf("resolved pr_title before repair = %v, want the non-conforming title", summary["pr_title"])
	}

	// Delivery rejects the metadata; the run re-opens at repair_pr_metadata
	// (what ReopenForRepair + the engine/CLI do after a PRMetadataError).
	refusal := &delivery.PRMetadataError{Reason: `delivery: pr_title "` + oldTitle + `" does not match pattern "^(?P<type>feat|fix|docs|chore|refactor|test)(\((?P<scope>[a-z0-9-]+)\))?!?: .+$"`}
	if err := delivery.ReopenForRepair(ctx, repo, ctrl.RunID, "repair_pr_metadata", delivery.MaxDeliveryRepairs, refusal, &discardWriter{}); err != nil {
		t.Fatalf("ReopenForRepair: %v", err)
	}
	attempts, err = repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if wfCount := countAttempts(attempts, delivery.DeliveryRepairStepID); wfCount != 1 {
		t.Fatalf("wf-delivery attempts after reopen = %d, want 1", wfCount)
	}
}

// runPRMetadataReentrySecondPhase re-runs the chain after the repair and
// asserts the next delivery resolves the FIXED title — the regression. The
// repair step's AttemptNo is 1 while implement#2's is 2, so recency (the
// repair recorded LAST) must win over the highest-AttemptNo rule. It also
// asserts no re-churn: exactly one delivery rejection attempt and one repair,
// with the panel and gates re-executed.
func runPRMetadataReentrySecondPhase(t *testing.T, repo workflowledger.Repository, ctrl *LinearController) {
	t.Helper()
	ctx := context.Background()
	fixedTitle := "feat(textutil): add TruncateEllipsis rune-safe ellipsis truncation"

	// Run 2: the repair fixes the title, then the chain re-runs before the
	// next delivery — the exact re-execution the shipped feature-delivery
	// TOML performs after a metadata repair.
	got, err := ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("second run = %+v, err = %v, want delivery_pending", got, err)
	}
	summary, err := delivery.ResolveLatestChangeSummary(ctx, repo, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("no change summary resolved after the repair")
	}
	if title := summary["pr_title"].(string); title != fixedTitle {
		t.Fatalf("resolved pr_title after repair = %q, want the repaired title %q", title, fixedTitle)
	}

	// No re-churn: exactly one delivery rejection attempt and one repair,
	// and the chain re-ran (panel ran 3 times: two in run 1, one in run 2).
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if wfCount := countAttempts(attempts, delivery.DeliveryRepairStepID); wfCount != 1 {
		t.Fatalf("wf-delivery attempts after repair = %d, want 1 (the run must not re-churn the delivery loop)", wfCount)
	}
	if repairCount := countAttempts(attempts, "repair_pr_metadata"); repairCount != 1 {
		t.Fatalf("repair_pr_metadata attempts = %d, want 1", repairCount)
	}
	if panelCount := countAttempts(attempts, "review_panel"); panelCount != 3 {
		t.Fatalf("review_panel attempts = %d, want 3 (two in run 1, one after the repair)", panelCount)
	}
	if structureCount := countAttempts(attempts, "preflight_structure"); structureCount != 2 {
		t.Fatalf("preflight_structure attempts = %d, want 2 (the chain must re-run after the repair)", structureCount)
	}
}

// runPRMetadataRepairSurvivesImplementReviewReentry is the RED-before-fix
// regression for the live delivery loop: the review loop makes implement run
// twice BEFORE delivery #1, so implement#2 has AttemptNo=2 while the later
// repair step has AttemptNo=1, and the next delivery must still resolve the
// FIXED title. See the two phase helpers for the assertions and
// TestPRMetadataRepairSurvivesImplementReviewReentry for the memory/sqlite
// wrappers.
func runPRMetadataRepairSurvivesImplementReviewReentry(t *testing.T, repo workflowledger.Repository) {
	t.Helper()
	ctx := context.Background()
	oldTitle := "textutil: add TruncateEllipsis rune-safe ellipsis truncation"
	fixedTitle := "feat(textutil): add TruncateEllipsis rune-safe ellipsis truncation"
	compiled := featureDeliveryRepairChainWithReviewLoop(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		// Run 1: the panel re-enters implement once (changes_requested), so
		// implement#2 exists BEFORE delivery #1 — the real-run shape.
		"implement#1":          repairChangeSummaryJSON(oldTitle, "Adds the helper. Needed for delivery."),
		"review_panel#1":       json.RawMessage(`{"host_verdict":"changes_requested"}`),
		"implement#2":          repairChangeSummaryJSON(oldTitle, "Adds the helper. Needed for delivery."),
		"review_panel#2":       json.RawMessage(`{"host_verdict":"approved"}`),
		"review_integration#1": json.RawMessage(`{"verdict":"approved"}`),
		// Run 2 (after the delivery rejection): the repair fixes the title,
		// then the whole chain re-runs.
		"repair_pr_metadata#1": repairChangeSummaryJSON(fixedTitle, "Adds the helper. Needed for delivery."),
		"review_panel#3":       json.RawMessage(`{"host_verdict":"approved"}`),
		"review_integration#2": json.RawMessage(`{"verdict":"approved"}`),
	}}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: compiled.Digest})
	if err != nil {
		t.Fatal(err)
	}
	changeSummary := StepRuntime{Agent: agents.ResolvedAgent{Name: "dev"}, Schema: repairChangeSummarySchema}
	ctrl, err := NewLinearController(repo, runner, compiled, map[string]StepRuntime{
		"implement":          changeSummary,
		"review_panel":       {Agent: agents.ResolvedAgent{Name: "rev"}},
		"review_integration": {Agent: agents.ResolvedAgent{Name: "rev"}},
		"repair_pr_metadata": changeSummary,
	}, map[string]any{"task": "x"}, "wfr-repair-reentry", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cat := definition.NewCatalogue()
	if err := cat.Register(stubVerifierProfile{name: "go-default", checks: []definition.Check{{Name: "workspace-dir", Status: "passed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}

	runPRMetadataReentryFirstPhase(t, repo, ctrl)
	runPRMetadataReentrySecondPhase(t, repo, ctrl)
}

// TestPRMetadataRepairSurvivesImplementReviewReentry runs
// runPRMetadataRepairSurvivesImplementReviewReentry against the memory and
// sqlite repositories.
func TestPRMetadataRepairSurvivesImplementReviewReentry(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		runPRMetadataRepairSurvivesImplementReviewReentry(t, workflowledger.NewMemoryRepository())
	})
	t.Run("sqlite", func(t *testing.T) {
		store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		runPRMetadataRepairSurvivesImplementReviewReentry(t, workflowledger.NewStorageRepository(store))
	})
}
