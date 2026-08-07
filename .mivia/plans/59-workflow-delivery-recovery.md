# 59 - Workflow delivery recovery: base divergence and stranded runs

**Status**: Revised after Step 0 challenge round 1 (BLOCK, 9 findings adopted).
Pending re-review.

## Goal

Make `feature-delivery` delivery recover automatically when the base branch
advances while a run is in flight, and give operators a real recovery path for
runs stranded in `delivery_failed`. PRs must be produced for completed work,
even when master moved mid-run.

## Root cause (confirmed 2026-08-08, run wfr-QEQ5OSYSG5EY7VCE; auditor
reproduced every claim)

- All workflow runs execute in **linked git worktrees**
  (`gitdir: <main>/.git/worktrees/<run>`), which SHARE `refs/heads/*` and
  `refs/remotes/*` with the main repository (git-worktree(1); confirmed by
  `git rev-parse` in the run's worktree returning the main repo's `master`).
- Delivery eligibility (`internal/workflows/delivery/deliver.go`,
  `verifyEligibility` steps 6 and 6b) demands the base refs still EQUAL the
  admitted base commit:
  - step 6: `refs/heads/master^{commit}` == admitted `BaseCommit` (L104-107);
  - step 6b: `refs/remotes/origin/master^{commit}` == admitted
    `OriginBaseCommit` (L110-128).
- In a linked worktree these refs are the MAIN repo's live refs. Any commit to
  master during a run moves them, so the check fires a permanent
  `RefusalError` and the engine settles the run to `delivery_failed`
  (`localengine/engine_deliver.go`).
- Observed for wfr-QEQ5OSYSG5EY7VCE (admitted 20:25:55Z at base a5ef1418):
  config commit 92dabc8 landed 20:37:53Z, host auto-pushed it ("update by
  push" in the origin/master reflog, 20:42:05Z), delivery refused 20:44:42Z.
  The run had completed the ENTIRE pipeline with all gates approved; only the
  delivery phase failed.
- `delivery_failed` is irreversible: `workflow deliver` refuses any run that
  is not `delivery_pending`, `workflow resume` treats terminal runs as a
  no-op, and `ValidRunTransition` has no outgoing edge from `delivery_failed`
  (`ledger/types.go:44-46`). A spurious refusal permanently strands runs whose
  worktree and branch are intact.

## Confirmed defects

1. Local base-pin equality (step 6) is invalid in linked worktrees: it reads
   the main repo's live branch, not the admitted base.
2. Remote base-pin equality (step 6b) refuses forward advancement of the
   remote base. Industry practice (Dependabot auto-rebase, Renovate
   `rebaseStalePrs`/`rebaseWhen: auto`) treats an advanced base as a normal
   condition; only a base REWRITE that drops the admitted commit is permanent.
3. No origin refresh happens before eligibility ("fetch and retry" is
   documented but nothing fetches).
4. `delivery_failed` has no recovery path (no transition edge, three entry
   guards refuse, both settle paths hard-code `delivery_pending`).
5. The CLI delivery claim path force-releases held claims under a per-process
   flock that does not serialize two hosts; the engine tool path takes no
   flock at all -> cross-host double-publish race (reviewer AR-1, reproduced
   as real: with a shared worktree both commits can land).
6. A failed agent step (hooks run's plan_review provider error) leaves the
   run terminal with no resume path. Related but separate; not fixed here.

## Locked boundaries

- Workflow agents do not use `run_command`, commit, push, or read secrets.
- Host-owned delivery stays host-owned: fixed argv, pinned git env, no TOML
  commands. `mode = "draft"`, `base = "master"` unchanged.
- Idempotency and PR ownership reuse (existing pushed/pending record logic)
  are preserved; a retry never creates a second PR.
- Refusals remain permanent ONLY for genuinely permanent conditions:
  rewritten base (admitted origin base not an ancestor of the current base),
  worktree loss, foreign PR with mismatched draft state, origin remote change,
  commit-message policy rejection.
- A refusal never writes over a prior attempt's commit/tree/diff/PR identity.
- Two hosts must never publish the same run concurrently (rollback criterion).

## Design (revised after Step 0 round 1)

### A. Eligibility: ancestry instead of equality (deliver.go verifyEligibility)

- Keep: worktree HEAD on the `wf/` branch (step 4); admitted `BaseCommit` is
  an ancestor of HEAD (step 5); origin remote matches the admitted URL
  (step 7, L129-145).
- Remove: step 6 (local `refs/heads/<base>` equality). AR-8: loses no real
  protection - the PR base is the remote base; step 5 + commitOrResume's
  parent chain already guarantee the delivered branch contains the admitted
  base. Pin in tests: when `OriginBaseCommit == ""` (no tracking ref at
  admission), the fallback pin is the local `BaseCommit` (deliver.go L70-75);
  a remote rewrite retaining the local base still passes ancestry - harmless,
  documented, tested so nobody re-adds an equality check later.
- Replace step 6b with ancestry, with fetch ordered and pinned per AR-4/AR-5/
  AR-6:
  1. Step 7 (origin URL equality vs admitted URL) runs FIRST.
  2. UNCONDITIONAL bounded single-ref fetch on every (re-)eligibility attempt,
     from the ADMITTED URL, never the mutable `origin` name:
     `git fetch --no-tags <admitted-url> +<base>:refs/remotes/origin/<base>`
     (force-updating refspec; AR-6: staleness is undetectable without
     fetching, and a rewrite leaves the tracking ref at the OLD tip which
     would pass ancestry).
  3. The rewrite is detected ONLY by the post-fetch ancestry check (F-1: a
     force-update fetch never errors on non-fast-forward, so there is no
     "fetch errors because the remote rewrote history" branch). Classify:
     transport/network errors -> recoverable (run stays `delivery_pending`);
     the remote base ref does not exist (deleted base branch) -> PERMANENT
     refusal (it can never satisfy ancestry); non-ancestor after fetch ->
     PERMANENT refusal with reason recorded.
  4. If the admitted `OriginBaseCommit` is an ancestor of the fetched remote
     base -> PROCEED: create the PR against the current base (GitHub computes
     base..head at creation; the delivered branch still contains exactly the
     validated change).
- Post-create base verification (AR-7, closes the TOCTOU; pinned route F-2):
  extend `PRClient` with a base-read capability: `gh pr view --json
  baseRefOid` returning the PR's actual base commit. After find/create, if
  the admitted `OriginBaseCommit` is NOT an ancestor of that base -> the
  attempt is marked failed and the run settles PERMANENT to `delivery_failed`
  with the reason recorded (the PR may already exist; this check prevents the
  success settle, it cannot unpublish). Extend `PRRef`, `GitHubCLI`, and every
  fake (`fakePRClient`, `prRecorder`, localengine mocks) in wave 2.
  Alternative rejected for now (extra pinned fetch per attempt).
- Document that forward advancement may yield a PR needing a rebase or
  showing a merge conflict; that is normal GitHub life for out-of-date
  branches (Dependabot/Renovate behavior).

### B. `delivery_failed` re-eligibility (engine + CLI + tool)

- Transition table (`ledger/types.go ValidRunTransition`): add outgoing edges
  `delivery_failed -> delivery_pending` and `delivery_failed ->
  delivery_failed` (self-loop) with tests. AR-2: re-eligibility is a versioned
  CAS `delivery_failed -> delivery_pending`, then the EXISTING settle paths
  run unchanged (AR-3: both success-settle sites CAS only from
  `delivery_pending`, so the intermediate CAS makes them correct as-is).
- Terminal invariant (F-3): `delivery_failed` STAYS a terminal status
  (`IsTerminalRunStatus(delivery_failed)=true` is load-bearing for
  `workflow_cleanup.go:44`, invocation-key replay in `workflow_tool_engine.go:86`,
  and `settleRunFailure`). The two recovery edges are an explicit carve-out:
  update the doc comment at `types.go:26` and the terminal-rejection test
  (`types_test.go:64-73`) to exclude exactly these two edges; do not
  un-terminalize the status.
- CAS placement (F-4): `delivery.Deliver` performs the
  `delivery_failed -> delivery_pending` CAS as the SINGLE enforcing site, so
  all three entry paths share it and cannot diverge. The self-loop edge is
  used defensively by the refusal settle when the run is already
  `delivery_failed` (a still-refused re-eligibility); pinned with a test.
- All THREE entry guards accept `delivery_failed` (AR-9):
  `delivery.Deliver` (deliver.go:57-63, the enforcing guard),
  `engine.Deliver` (engine_deliver.go:36-39), CLI `deliverRunWithStore`.
  Each re-runs eligibility from current state; eligible -> deliver (existing
  idempotency/ownership paths apply); still refused -> print the reason and
  stay `delivery_failed` (self-loop CAS).
- Cross-host publish fence (AR-1): replace CLI `claimWorkflowDeliveryRun`
  force-release (ClearRunClaim + re-claim) with lease-based takeover
  (`TakeoverExpiredRunClaim`, `DefaultClaimLease`), mirroring
  `claimWorkflowResumeHandoff` (workflow_resume.go). Held + unexpired =
  "another deliverer is live, retry"; add `--force` for explicit bypass.
  Add a two-repository (two-host) integration test asserting serialized
  publish. Engine `claimDelivery` already never clears held claims; keep.
- Refusal reason surfacing: extend the `workflow deliver` CLI/tool and
  `localengine` settle paths to durably record the refusal reason via
  `recordAutoDeliveryFailure` (auditor note: today only the end-of-run
  auto-delivery path persists it; the explicit CLI/engine paths settle to
  `delivery_failed` without a reason). Add reason + timestamp to status
  output.
- `workflow resume` keeps refusing `delivery_pending` (deliver is the path);
  it now also refuses `delivery_failed` with a pointer to
  `workflow deliver --allow-publish` (recovery is a delivery concern, not a
  body re-run).

### C. Contract and docs

- Update the committed-workflow contract test if it asserts `delivery_failed`
  is irreversible; assert re-eligibility is the recovery path.
- Update the workflow user guide delivery section (statuses, retry command,
  when a PR targets an advanced base).
- No workflow TOML change: recovery is engine/host behavior.

## Waves

1. Eligibility revision (A) with unit tests: forward-advance accepted,
   rewrite refused, unconditional pinned-URL fetch ordered after step 7,
   fetch-rewrite = permanent vs transport = recoverable, local refs/heads
   move no longer refuses (linked-worktree fixture), `OriginBaseCommit == ""`
   fallback pinned (AR-8).
2. Re-eligibility (B): transition edges + tests; all three entry guards;
   lease-based claim takeover + two-host serialization test; refusal reason
   persistence on CLI/engine paths. Integration tests (mock git/gh):
   delivery_failed -> deliver -> succeeded; still-refused stays
   delivery_failed; transient stays delivery_pending; no double PR on retry.
3. Contract test updates + user guide (C). Run `make verify`, `make race`.
4. Recovery run: re-deliver wfr-QEQ5OSYSG5EY7VCE via the fixed path (worktree
   intact, feature changes present) -> one draft PR; AR-7 post-create base
   check observed. Watch the three in-flight runs (events, subagents, ledger)
   deliver against the advanced base. Report every PR URL.

## Challenge disposition (Step 0 rounds 1-2)

Round 1: reviewer returned changes_requested (BLOCK); auditor confirmed the
root cause in full. All nine findings adopted (AR-1..AR-9).
Round 2: re-review returned approve-in-substance; no redesign required. Five
plan-precision items pinned: F-1 (fetch-error classification, deleted-base
permanent), F-2 (AR-7 route = PRClient baseRefOid extension, wave 2, settle
permanent), F-3 (delivery_failed stays terminal; two edges are a documented
carve-out), F-4 (CAS single site = delivery.Deliver; self-loop defensive),
F-5 (stale citations corrected: deliver.go 92-94/164/177/189; wave-3 test
pointers named; `TestDeliverBaseMoved` and
`TestWorkflowDeliverRefusalPrintsDeliveryFailedStatus` rewritten in waves
1-2; engine error text `engine_deliver.go:100-103` updated in wave 2).

## Rollback criterion

Stop and revert if re-eligibility can publish a PR whose BRANCH does not
contain the admitted base commit, if a rewritten-base run ever settles
succeeded (AR-7 regression), or if two hosts can publish the same run
concurrently (AR-1 regression).
