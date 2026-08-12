# Investigation: bug-fix workflow runs failed at the review step

**Date:** 2026-08-12
**Runs:** wfr-CSR7NWYQUPLS3F6U, wfr-O2ISPM6PR2SRRKLF (both `bug-fix`, started 09:42:57/09:42:59Z)
**Verdict:** Not a review failure. A harness deadlock — task requires writing a path that
workflow agents are blocked from writing — is being misattributed to the review gate.

---

## Failure signature (identical in both runs)

- hunt → triage (confirmed) → fix_plan → implement → **review: changes_requested** → implement
  (repair) → **review: changes_requested** → implement (repair) → **review attempt 3: FAILED**
- Both runs carry the **same** `error_ref` digest (`sha256:7ce524…`) and the same `error_text`:
  `review made no progress across rounds (identical findings set); run failed`
- Both runs show loop `review_repair` iterations = 2. Identical digests across two independent
  runs = deterministic harness behavior, not model variance.

## Root-cause chain (grounded)

1. **The task itself** was to fix `.mivia/workflows/bug-fix.toml`: lower `inputs.task` /
   `inputs.scope` `max_bytes` from 1048576 to 16000 (1048576 exceeds the engine's 262144-byte
   step-context render cap `maxStepContextBytes`, internal/workflows/controller/agent_step.go).
2. **`.mivia/workflows` is on the write-path blocklist** for workflow agent steps
   (.mivia/mivia.toml:428-437): `write_file`/`search_replace`/`multi_edit`/`delete_file` refuse it.
   The implementer (workflow-engineer agent) therefore **cannot edit bug-fix.toml**.
3. The implementer acknowledged the block ("implementation summary confirms step 1 of the
   approved plan … was blocked and not applied" — review evidence, finding R1-1).
4. The reviewer correctly and consistently returned `changes_requested` with the same finding
   R1-1 each round — **the reviewer did its job**.
5. The harness **no-progress guard** (internal/workflows/controller/linear_execution_helpers.go:101-109
   → `failReviewNoProgress` in linear_convergence.go:125) then degraded review attempt 3 to
   Failed with the "review made no progress" cause and routed to `failure`.

So: implementer blocked → reviewer repeats the valid finding → guard kills the run and **blames
review**.

## What should be improved

### 1. Pre-flight admission check for un-writable task targets (best fix)
Before (or early in) a workflow run, detect that the task/plan requires editing a path on the
`write_path_blocklist` and fail fast with an honest diagnostic, e.g.
"task requires write access to .mivia/workflows/bug-fix.toml, which is write-blocklisted for
workflow agents; execute this change from the root session or a host-owned process."
Today the run burns ~25–30 minutes looping before the guard stops it.

### 2. The no-progress guard must distinguish "spinning" from "blocked"
`reviewMadeNoProgress` only compares finding-id sets. When the prior implement attempt's
change-summary records a blocked write (or the worktree diff is empty while findings demand a
specific file edit), fail with a **distinct blocked cause naming the path** — not
"review made no progress". Consider a `blocked_paths` field in the change-summary output schema
(schemas/change-summary-v1.json) that the controller checks before degrading the review.

### 3. Review gates should never be the failure sink for execution deadlocks
A review is a verdict gate (approved / changes_requested). When a loop cannot converge because
of an external constraint (permissions, missing resource), the run should settle terminal as
**blocked** (or route to a root-intervention step) with the review findings preserved as
evidence — never attribute the deadlock to the reviewer.

### 4. Document and route workflow-definition changes through the root
`.mivia/workflows`, `.mivia/policy`, `.mivia/agents` etc. are root-owned surfaces by design.
Fix-the-workflow tasks should be executed by the root orchestrator / host (which is not subject
to the agent write blocklist), not submitted as `bug-fix`/`feature-delivery` runs. Add a note to
the workflow docs (and optionally a root-run `workflow-admin` variant) so this class of task is
routed correctly from the start.

### 5. (Optional, org decision) Scoped self-repair for workflow definitions
If the org wants agents to repair workflow definitions in-loop, add an explicit,
narrowly-scoped escape hatch (e.g. a dedicated `workflow-admin` agent/skill with write access to
`.mivia/workflows` under a separate gate). The blocklist comment says "choose deliberately" — the
current choice is to block, so the default resolution is (4), not this.

## Immediate unblock (offered, not done)
The underlying bug (max_bytes 1048576 > 262144 render cap in bug-fix.toml) can be fixed by the
root session — I can edit `.mivia/workflows/bug-fix.toml` directly. Awaiting your go-ahead.

## Note on the third failure
wfr-VBFFPHMNREYQ4Z4V (18 h ago) also failed, but at the **hunt** step with a different
`error_ref` — separate cause, not the review/harness pattern above.
