# Workflow stack settle: known gaps

## Status

This document lists gaps in stack-settle automation that are not fixed yet.
The drive-before-delivery ordering itself is shipped — see
[Workflow architecture: Stacking](workflows.md#stacking-small-pr-delivery)
for what already runs. Everything below this point is still open.

A live incident on 2026-08-17 surfaced the gaps: a stacked plan run parked at
`delivery_pending` for more than one hour after all its chunk PRs merged. A
host `workflow deliver` call settled the run only after a retry. The lock was
not the cause. The cause was the absence of any automatic completion path.

## Problem statement

A stacked plan run parks at `delivery_pending` when its chunk PRs merge out of
band. It parks because no live process observes the merge. It settles only
when a host process runs `workflow deliver` or `stack drive`.

A failed chunk also parks the parent. The drive halts on a failed chunk and
leaves the stack resumable. No path settles the plan run as failed.

The workflow execution lock adds friction. `workflow deliver` aborts after a
5-second wait with an opaque "lock is busy" error. The lock is not the cause
of the parked run. It makes the failure mode worse.

## Evidence

The incident run is `wfr-3AQWNJUQDC5ZNGGR` (workflow `feature-delivery`,
base `dev`).

1. The plan run finished every step at 13:27 and parked at
   `delivery_pending`.
2. The drive delivered chunk c1 as PR #205 and chunk c2 as PR #206.
3. Both PRs merged out of band. The user or another agent merged them.
4. No process polled the stack. The task ledger never saw the merges.
5. The integration run was never admitted. The parent stayed parked.
6. `workflow deliver` refused while PR #206 was open. It reported
   "not fully driven yet".
7. After PR #206 merged, two deliver attempts failed on
   "lock Git exclude: lock is busy". The third attempt settled the run at
   14:38:59.

The pattern is known in the repo. See
`internal/cli/workflow_tool_engine_delivery_repair_test.go`, comment at line
475: "workflow deliver on 'lock is busy' (observed: plan runs parked 50+
min)".

## Root causes

| Id | Defect | Evidence |
|----|--------|----------|
| D1 | No autonomous settle. The drive loop lives inside a live process. Out-of-band merges are invisible. | `internal/workflows/localengine/engine_stack.go` line 143, `markMergedChunks` |
| D2 | Failed chunks park the parent. The drive halts on a failed chunk. | `internal/workflows/localengine/engine_stack_settle.go`, `stackHasProgress` |
| D3 | Lock friction. The execution flock has a 5-second wait and an opaque error. | `internal/cli/workflow_resume_lock.go` line 66, `internal/cli/workflow_tool_engine.go` line 33 |
| D4 | Status opacity. `delivery_pending` shows no blocking cause. | `internal/cli/stack_admit_integration.go` line 78 |
| D5 | Host and API parity gap. The `workflow_run` tool admits and returns. Nothing drives the stack after that. | `maybeDriveSettledStack` call sites, `scripts/run-delivery-workflow.sh` |

## Design goals

1. Merging the last chunk PR settles the plan run within one poll interval.
   No host action is needed.
2. A terminal chunk failure settles the plan run as failed.
3. `workflow deliver` never fails with "lock is busy". It waits, or it
   reports the lock holder.
4. Every `delivery_pending` run explains why it waits.

## Proposed changes

### P0-1: Durable completion sweep

Add an always-on reconcile that recomputes stack state for every
`delivery_pending` plan run. The reconcile reads the task ledger and the PR
oracle. It runs from the engine reconcile loop and the session recovery
sweep. It is idempotent and CAS-guarded, so it survives process death.

The sweep settles a plan run when every chunk merged. It admits the
integration run first, waits for it to settle, then settles the plan run.
This keeps the current drive-before-delivery semantics. See decision A.

### P0-2: PR-merge oracle as a first-class trigger

Move `markMergedChunks` out of the drive loop into the sweep. The sweep polls
GitHub for published chunks of `delivery_pending` plan runs. A merged PR
flips the task status from `published` to `merged` in the ledger. This makes
`stackDriveCompleted` true. Every settle path then finishes.

### P0-3: Deliver drives instead of refusing

`workflow deliver` on an advanceable stack admits the remaining waves and the
integration run. It waits with the existing backoff, then settles. It refuses
only on external-action blockers, such as an approve grant or a failed chunk.
The refusal names the exact blocker, such as the PR number or the chunk id.

### P1: Failed chunks resolve the parent

A terminal chunk failure settles the plan run as failed. The failure record
carries the failing chunk and its `error_ref`. The sweep cancels queued
dependents. The plan run never stays `delivery_pending` for an uncompletable
stack.

The failed chunk PR stays open by default. A new stacking knob controls this.
See the `failed_pr_policy` section.

### P2: Lock hygiene

Scope the execution flock to the actual git-exclude marker writes and step
admission. `workflow deliver` and the settle path do not need it. Settle is
one ledger CAS plus one oracle read. Both are already safe under concurrency.

If the flock stays on the deliver path, raise the wait to 60 seconds. Use
exponential backoff with jitter. On timeout, report who holds the flock and
for how long. Use flock `F_GETLK` to read the owner. Reclaim stale holders
whose process died. Fix the misleading "Git exclude" name in the busy error.

### P3: Status transparency

Compute a blocking cause for every `delivery_pending` run. Show the cause in
`workflow status`, in the deliver refusal, and in the sidebar dot.

Examples:

- "waiting for PR #206 (chunk c2) to merge"
- "all chunks merged - awaiting integration run"
- "chunk c1 failed (ref ...) - stack failed"

### P4: Host and API parity

The engine reconcile drives API-admitted stacks to completion. No CLI watcher
is needed. Add a `workflow drive` root tool that mirrors `workflow deliver`.
Fix `scripts/run-delivery-workflow.sh` to print and background the drive
command for multi-chunk plans.

## Config: `failed_pr_policy`

New stacking knob in the workflow `[stacking]` table.

| Key | Type | Values | Default | Meaning |
|-----|------|--------|---------|---------|
| `failed_pr_policy` | string | `leave_open`, `close` | `leave_open` | What the stack driver does with a failed chunk's published PR |

- `leave_open`: keep the PR open. A human reviews and closes it. This is the
  default.
- `close`: close the PR when the chunk fails terminally.

The knob lives on `definition.Stacking`
(`internal/workflows/definition/types.go`). The compiler validates the value
(`internal/workflows/compiler/stacking.go`). It follows the `merge_policy`
pattern: a string enum with a global default.

## Files to change

| File | Change |
|------|--------|
| `internal/workflows/localengine/engine_stack.go` | Extract `markMergedChunks`; call the sweep |
| `internal/workflows/localengine/engine_stack_settle.go` | Add failure settle; apply `failed_pr_policy` |
| `internal/workflows/definition/types.go` | Add `FailedPRPolicy` to `Stacking` |
| `internal/workflows/compiler/stacking.go` | Validate `failed_pr_policy` enum and default |
| `internal/cli/workflow_resume_lock.go` | Fix lock wait, holder report, stale reclaim |
| `internal/cli/workflow_tool_engine.go` | Raise lock wait; report holder |
| `internal/cli/stack_admit_integration.go` | Name the exact blocker in refusals |
| `internal/cli/workflow_deliver.go` | Drive an advanceable stack before settling |
| `internal/cli/workflows_sidebar.go` | Show blocking cause in the dot |
| `scripts/run-delivery-workflow.sh` | Print and background the drive command |
| `docs/architecture/workflows.md` | Document the new knob and the sweep |

## Tests to add

- Engine: out-of-band merges settle the plan run with zero deliver or drive
  calls. The oracle stub flips the merge; the sweep CASes the run to
  `succeeded`.
- Engine: a terminal chunk failure settles the plan run as failed and
  cancels dependents.
- Engine: `failed_pr_policy = "close"` closes the failed chunk PR;
  `leave_open` keeps it open.
- CLI: deliver on an advanceable stack drives then settles.
- CLI: deliver on a failed chunk refuses with the blocker name.
- CLI: deliver under lock contention waits and reports the holder.
- Compiler: `failed_pr_policy` invalid values fail; the default fills.
- Contract: the shipped workflow tomls compile with `soft_lines = 350`.

## Decisions

- A. All chunks merged: admit the integration run, wait for it to settle,
  then settle the plan run. Confirmed. The sweep keeps the current
  drive-before-delivery semantics.
- B. Failed chunk PR policy is a config option. The default is `leave_open`.
  Confirmed.
- C. Sweep cadence: poll `delivery_pending` plan runs every 30 seconds by
  default. The cadence is a tunable constant.

## Non-goals

- No change to the drive-before-delivery ordering.
- No change to `merge_policy` semantics.
- No change to the chunk admission contract.
- No new ADR files. ADRs are prohibited in this repository.
