package cli

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestStackRunAutoPublishAllowed pins the policy-derivation half of the
// sweep/session publish gate (reachable-bug audit finding 1): a stack chunk
// or integration run's InvocationKey resolves back to its plan run, whose
// merge_policy decides whether an unattended path may publish it.
func TestStackRunAutoPublishAllowed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  string
		allowed bool
	}{
		{"approve policy withholds auto-publish", "approve", false},
		{"auto policy allows auto-publish", "auto", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := workflowledger.NewMemoryRepository()
			t.Cleanup(func() { _ = repo.Close() })

			toml := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "`+tc.policy+`"`, 1)

			planRun, planRaw := stackingResumeSnapshot(t, toml, map[string]string{"task": "add feature"})
			planRun.RunID = "wfr-gate-plan-" + tc.policy
			planRun.Status = workflowledger.RunStatusPending
			if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
				t.Fatal(err)
			}

			chunkRun, chunkRaw := stackingResumeSnapshot(t, toml, map[string]string{
				"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
			})
			chunkRun.RunID = "wfr-gate-chunk-" + tc.policy
			chunkRun.Status = workflowledger.RunStatusPending
			chunkRun.InvocationKey = planRun.RunID + ":c1"
			if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
				t.Fatal(err)
			}

			isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, chunkRun.RunID)
			if !isStackRun {
				t.Fatalf("isStackRun = false, want true for a chunk run's invocation key")
			}
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v for merge_policy=%s", allowed, tc.allowed, tc.policy)
			}
		})
	}
}

// TestStackRunAutoPublishAllowedIntegrationRun proves the gate applies to
// the integration chunk id the same way it applies to a regular chunk id.
func TestStackRunAutoPublishAllowedIntegrationRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	toml := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)
	planRun, planRaw := stackingResumeSnapshot(t, toml, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-gate-plan-integration"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	intRun, intRaw := stackingResumeSnapshot(t, toml, map[string]string{"task": "add feature", "stack_mode": "single"})
	intRun.RunID = "wfr-gate-integration"
	intRun.Status = workflowledger.RunStatusPending
	intRun.InvocationKey = planRun.RunID + ":" + stackIntegrationChunkID
	if err := repo.CreateRun(ctx, intRun, intRaw); err != nil {
		t.Fatal(err)
	}

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, intRun.RunID)
	if !isStackRun {
		t.Fatalf("isStackRun = false, want true for the integration run's invocation key")
	}
	if allowed {
		t.Fatalf("allowed = true under merge_policy=approve, want false")
	}
}

// TestStackRunAutoPublishAllowedNonStackRun proves the gate never fires for
// the plan run itself (empty InvocationKey): its own publication is
// authorized separately by delivery.deliver_plan_run, not this predicate.
func TestStackRunAutoPublishAllowedNonStackRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	run, raw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	run.RunID = "wfr-gate-plan-nonstack"
	run.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, run, raw); err != nil {
		t.Fatal(err)
	}

	isStackRun, _ := stackRunAutoPublishAllowed(ctx, repo, run.RunID)
	if isStackRun {
		t.Fatalf("isStackRun = true for a plan run (empty invocation key), want false")
	}
}

func TestStackRunAutoPublishAllowedUnknownRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, "wfr-does-not-exist")
	if isStackRun || allowed {
		t.Fatalf("isStackRun=%v allowed=%v for an unknown run, want false/false (fail closed)", isStackRun, allowed)
	}
}

// TestStackRunAutoPublishAllowedOrdinaryRunWithColonInInvocationKey pins a
// false-positive regression: agenttools.StartRequest.InvocationKey is a
// fully caller-supplied, unconstrained string used for idempotent retries of
// the ORDINARY (non-stacking) workflow_run tool - nothing requires it to
// avoid colons. A caller using e.g. "release:v1.2" as their invocation key
// must not have their run silently withheld from auto-publish forever just
// because stackIDFromChunkInvocationKey's colon heuristic misreads the
// prefix as a stack id. isStackRun must require the parsed prefix to
// actually resolve to a real stacking-enabled plan run, not just contain a
// colon.
func TestStackRunAutoPublishAllowedOrdinaryRunWithColonInInvocationKey(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	run, raw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	run.RunID = "wfr-ordinary-colon-key"
	run.Status = workflowledger.RunStatusPending
	run.InvocationKey = "release:v1.2"
	if err := repo.CreateRun(ctx, run, raw); err != nil {
		t.Fatal(err)
	}

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, run.RunID)
	if isStackRun {
		t.Fatalf("isStackRun = true for an ordinary run whose caller-supplied invocation key happens to contain a colon, want false")
	}
	if allowed {
		t.Fatalf("allowed = true, want false (fail closed alongside isStackRun=false)")
	}
}

// TestStackRunAutoPublishAllowedInvocationKeyPrefixesARealNonStackingRun
// covers the adversarial variant: the colon-split prefix happens to name a
// REAL run, but one whose compiled workflow has no [stacking] table. The
// gate must still resolve isStackRun=false, not treat that unrelated run's
// existence as proof of a stack.
func TestStackRunAutoPublishAllowedInvocationKeyPrefixesARealNonStackingRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	const plainTOML = `version = 1
name = "plain"
initial_step = "one"

[inputs.task]
type = "string"
required = true

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	unrelated, unrelatedRaw := stackingResumeSnapshot(t, plainTOML, map[string]string{"task": "x"})
	unrelated.RunID = "wfr-unrelated-plain"
	unrelated.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, unrelated, unrelatedRaw); err != nil {
		t.Fatal(err)
	}

	run, raw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	run.RunID = "wfr-borrows-unrelated-prefix"
	run.Status = workflowledger.RunStatusPending
	run.InvocationKey = unrelated.RunID + ":whatever"
	if err := repo.CreateRun(ctx, run, raw); err != nil {
		t.Fatal(err)
	}

	isStackRun, _ := stackRunAutoPublishAllowed(ctx, repo, run.RunID)
	if isStackRun {
		t.Fatalf("isStackRun = true when the colon-split prefix names a real but non-stacking run, want false")
	}
}

// TestStackRunAutoPublishAllowedInvocationKeyEmbedsRealPlanRunID covers the
// hardest false-positive: the colon-split prefix resolves to a REAL stacking
// plan run, but the run itself carries no stack-mode inputs. The gate must
// read the run's own admission evidence (its snapshot inputs), not the
// invocation-key prefix alone, so this non-stacking run is not withheld.
func TestStackRunAutoPublishAllowedInvocationKeyEmbedsRealPlanRunID(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	// Seed a real stacking plan run.
	tomlApprove := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)
	planRun, planRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-real-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	// A non-stacking run whose caller-supplied invocation key embeds the
	// real plan-run id. Its snapshot inputs carry no stack_mode.
	run, raw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	run.RunID = "wfr-ordinary-embedding"
	run.InvocationKey = planRun.RunID + ":c1"
	seedDeliveryPendingRun(t, repo, run, raw)

	isStackRun, _ := stackRunAutoPublishAllowed(ctx, repo, run.RunID)
	if isStackRun {
		t.Fatalf("isStackRun = true for a non-stacking run whose invocation key embeds a real plan-run id, want false")
	}
}

// TestStackRunAutoPublishAllowedChunkRunOwnInputs keeps the existing
// positive path: a real chunk run whose OWN snapshot carries stack_mode=chunk
// with chunk and stack_part is correctly identified as a stack run and
// withheld under merge_policy=approve.
func TestStackRunAutoPublishAllowedChunkRunOwnInputs(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	tomlApprove := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)

	planRun, planRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-own-inputs-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/2",
	})
	chunkRun.RunID = "wfr-own-inputs-chunk"
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	seedDeliveryPendingRun(t, repo, chunkRun, chunkRaw)

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, chunkRun.RunID)
	if !isStackRun {
		t.Fatalf("isStackRun = false for a real chunk run with stack_mode inputs, want true")
	}
	if allowed {
		t.Fatalf("allowed = true under merge_policy=approve, want false")
	}
}

func TestStackIDFromChunkInvocationKey(t *testing.T) {
	cases := []struct {
		key     string
		wantID  string
		wantOK  bool
		comment string
	}{
		{"wfr-abc123:c1", "wfr-abc123", true, "chunk key"},
		{"wfr-abc123:integration", "wfr-abc123", true, "integration key"},
		{"wfr-abc123:decompose:2", "wfr-abc123", true, "decompose continuation key"},
		{"", "", false, "plan run's own empty key"},
		{":leading-colon", "", false, "empty stack id must not match"},
	}
	for _, tc := range cases {
		gotID, gotOK := stackIDFromChunkInvocationKey(tc.key)
		if gotOK != tc.wantOK || (gotOK && gotID != tc.wantID) {
			t.Fatalf("%s: stackIDFromChunkInvocationKey(%q) = (%q, %v), want (%q, %v)", tc.comment, tc.key, gotID, gotOK, tc.wantID, tc.wantOK)
		}
	}
}

// TestStackRunAutoPublishAllowedMissingPlanRunFailsClosed covers the F2
// residual: when a chunk run's own evidence proves it is stack-shaped, but
// the plan run it points at has been deleted, the gate must fail closed
// (isStackRun=true, allowed=false) so the run stays parked instead of being
// auto-published by callers that fall through to allowPublish=true.
func TestStackRunAutoPublishAllowedMissingPlanRunFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	tomlApprove := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)

	planRun, planRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-f2-missing-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-f2-missing-chunk"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}

	// Delete the plan run after the chunk row was admitted.
	if err := repo.DeleteRun(ctx, planRun.RunID); err != nil {
		t.Fatal(err)
	}

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, chunkRun.RunID)
	if !isStackRun {
		t.Fatalf("isStackRun = false for a stack-shaped chunk run, want true (the missing plan run must fail closed, not fall through)")
	}
	if allowed {
		t.Fatalf("allowed = true for a stack-shaped chunk run with missing plan run, want false")
	}
	if !stackRunPublishWithheld(ctx, repo, chunkRun.RunID, true) {
		t.Fatal("stackRunPublishWithheld = false for missing plan run, want true")
	}
}

// TestStackRunAutoPublishAllowedCorruptPlanRunFailsClosed covers the F2
// residual when the plan run row exists but its admitted snapshot is corrupt
// or otherwise unresumable: the gate must still withhold auto-publication.
func TestStackRunAutoPublishAllowedCorruptPlanRunFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	planRun := workflowledger.RunSnapshot{
		RunID: "wfr-f2-corrupt-plan", WorkflowName: "mini-stack",
		SnapshotDigest: "sha256:bad", Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, planRun, []byte("not a valid snapshot")); err != nil {
		t.Fatal(err)
	}

	tomlApprove := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)
	chunkRun, chunkRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-f2-corrupt-chunk"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, chunkRun.RunID)
	if !isStackRun {
		t.Fatalf("isStackRun = false for a stack-shaped chunk run with corrupt plan run, want true")
	}
	if allowed {
		t.Fatalf("allowed = true for corrupt plan run, want false")
	}
}

// TestStackRunAutoPublishAllowedMissingPlanRunAutoPolicyStillFailsClosed is the
// regression control: even when the missing plan run is from an auto-policy
// stack, the gate cannot derive allowed=true without resolving the policy, so
// it must withhold publication rather than assume auto.
func TestStackRunAutoPublishAllowedMissingPlanRunAutoPolicyFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	planRun, planRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-f2-missing-plan-auto"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-f2-missing-chunk-auto"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteRun(ctx, planRun.RunID); err != nil {
		t.Fatal(err)
	}

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, chunkRun.RunID)
	if !isStackRun || allowed {
		t.Fatalf("isStackRun=%v allowed=%v for missing auto-policy plan run, want true/false", isStackRun, allowed)
	}
}

// TestStackRunPublishWithheldLogsMissingPlanRun proves the F2 diagnostic
// branch: when a stack-shaped run's plan run is missing, stackRunPublishWithheld
// logs the distinct missing/unresolvable reason, not the generic grant-pause
// message.
func TestStackRunPublishWithheldLogsMissingPlanRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	tomlApprove := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)
	planRun, planRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-f2-log-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}
	chunkRun, chunkRaw := stackingResumeSnapshot(t, tomlApprove, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-f2-log-chunk"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, planRun.RunID); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() { log.SetOutput(prevOutput); log.SetFlags(prevFlags) })
	log.SetOutput(&buf)
	log.SetFlags(0)

	if !stackRunPublishWithheld(ctx, repo, chunkRun.RunID, false) {
		t.Fatal("stackRunPublishWithheld = false, want true")
	}
	if !strings.Contains(buf.String(), "missing or unresolvable") {
		t.Fatalf("log = %q, want missing/unresolvable message", buf.String())
	}
}

// TestPlanMissingOrCorruptDirect covers the defensive branches of the helper
// used only for diagnostics.
func TestPlanMissingOrCorruptDirect(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	if planMissingOrCorrupt(ctx, repo, "wfr-does-not-exist") {
		t.Fatal("planMissingOrCorrupt = true for missing run, want false")
	}

	run, runRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	run.RunID = "wfr-no-colon"
	run.Status = workflowledger.RunStatusPending
	run.InvocationKey = "nocolon"
	if err := repo.CreateRun(ctx, run, runRaw); err != nil {
		t.Fatal(err)
	}
	if planMissingOrCorrupt(ctx, repo, run.RunID) {
		t.Fatal("planMissingOrCorrupt = true for run with no colon in key, want false")
	}

	planRun, planRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-f2-direct-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}
	chunkRun, chunkRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-f2-direct-chunk"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}
	if planMissingOrCorrupt(ctx, repo, chunkRun.RunID) {
		t.Fatal("planMissingOrCorrupt = true for resolvable plan run, want false")
	}
	if err := repo.DeleteRun(ctx, planRun.RunID); err != nil {
		t.Fatal(err)
	}
	if !planMissingOrCorrupt(ctx, repo, chunkRun.RunID) {
		t.Fatal("planMissingOrCorrupt = false for deleted plan run, want true")
	}
}

// TestStackRunAutoPublishAllowedStackShapedPrefixingNonStackingRunDoesNotPanic
// covers a defensive gap in the F2 fix: when a run's own snapshot is
// stack-shaped but its invocation-key prefix resolves to a real run whose
// workflow has no [stacking] table, the gate must fail closed instead of
// dereferencing a nil Stacking pointer.
func TestStackRunAutoPublishAllowedStackShapedPrefixingNonStackingRunDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	const plainTOML = `version = 1
name = "plain"
initial_step = "one"

[inputs.task]
type = "string"
required = true

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	unrelated, unrelatedRaw := stackingResumeSnapshot(t, plainTOML, map[string]string{"task": "x"})
	unrelated.RunID = "wfr-f2-unrelated-plain"
	unrelated.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, unrelated, unrelatedRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-f2-stackshaped-bad-prefix"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = unrelated.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}

	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, chunkRun.RunID)
	if !isStackRun {
		t.Fatalf("isStackRun = false for stack-shaped run with non-stacking prefix, want true (fail closed)")
	}
	if allowed {
		t.Fatalf("allowed = true for non-stacking prefix, want false")
	}
}
