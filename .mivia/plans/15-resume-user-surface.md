# 15 — Give resume a user surface

**Status:** Design-ready; two open decisions (§4, §5).
**Date:** 2026-07-30
**Depends on:** `12` (implemented) and `13` §6 — **BLOCKER CLEARED 2026-07-30: `13` §6 is
implemented and registered as INV-AG-13.** §2's ordering requirement is satisfied, so this plan is
now implementable. Two decisions remain open (§4, §5).
**Why it matters:** `ResumeInterruptedRun` has **zero production callers** — verified 2026-07-30,
`internal/coordinator/types.go:52` declares it on the interface and nothing invokes it outside
tests. Plans `12` and `13` shipped the whole resume-and-fence machinery and no user can reach any
of it. This plan is what turns that into product.
**Blocks:** nothing.
**Blast radius:** MEDIUM — it makes a previously unreachable execution path
reachable by users, on purpose.

---

## 1. The gap

`ResumeInterruptedRun` works, and nothing calls it.

`12` fixed it: a resumed task now restores its `Input` and executes instead of
failing `invalid task input`. `496a126` then fixed two lifecycle bugs that made
resume actively destructive. But the production call count is still zero —
`grep -rn "ResumeInterruptedRun" internal/cli/ cmd/` returns nothing.

Everything around it exists:

| Surface | State |
|---|---|
| Detection | `Recover` reports interrupted runs at startup; `internal/cli/orchestration_state.go:141` and `dispatcher.go:44` already print `info: recovered interrupted run %s (%s)` to stderr |
| Enumeration | `Coordinator.ListInterruptedRuns` (`internal/coordinator/types.go:53`) exists and is used |
| Display | `runDashboard.backfillFromCoordinator` (`internal/cli/tui_run_dashboard.go:295`) already lists interrupted runs in the TUI |
| Action | **nothing** |

So mivia tells the user a run was interrupted, shows it in the dashboard, and
offers no way to do anything about it. The capability was repaired and left
unreachable.

## 2. Blocked on `13` §6 — do not implement first

`13` §1 establishes that nothing fences a run against a second process on the
same store, and `13` §5a establishes that two processes writing one run collide
on `UNIQUE(run_id, sequence)` — with `CompareAndSetTaskStatus` leaving one
instance permanently ahead of the store when its append is lost.

Today that hazard is theoretical *because nothing calls resume*. **This plan is
what makes it real.** Shipping a resume button before the fence converts a
latent design gap into a user-triggerable one: two mivia windows on one
workspace, both showing the same interrupted run, both offering a button.

Implement `13` §6 first. This is not a preference — the ordering is the whole
reason `13` was written before this.

## 3. Invariants the surface must preserve

1. **Resume runs under the resuming caller's authority, never the original's**
   (`12` §3). Nothing the surface does may re-derive identity from the ledger.
2. **A resumed handle must be owned.** `02` scopes run handles by principal via
   `storeOrchestrationHandle` (`internal/cli/orchestrate.go:53,212`), which
   records `principal` from the caller's context. `ResumeInterruptedRun` builds
   its handle with `newRunHandle(runID, "", …)` and it is **not** registered in
   the orchestration handle map — so today a resumed run cannot be inspected,
   joined or cancelled by any tool. The surface must register it with the
   resuming caller's principal, or resume produces a run nobody can control.
3. **Refusal must be legible.** With `13` §6 in place, "held by another
   executor" must read differently from "already terminal" and from "cannot be
   resumed" (`12`'s missing-`Input` case). Three distinct causes, three
   distinct messages.

## 4. Open decision: which surface

| | Option | Assessment |
|---|---|---|
| **A** | Slash command — `/resume <run-id>`, listing interrupted runs with no argument | Matches the existing `/save`, `/load`, `/list` family (`internal/cli/chat_slash_handlers.go`). Explicit, scriptable, no new privilege surface. Requires the user to know the run exists |
| **B** | TUI dashboard action — a key on an interrupted row | The dashboard already lists exactly these runs (`tui_run_dashboard.go:295`); the row is inches from the action. Discoverable. Only reachable in the TUI |
| **C** | Model-facing tool — `resume_run`, alongside `join_run`/`cancel_run` | Consistent with the existing orchestration tools and would get handle registration for free. **But** it hands the model the ability to restart work, and every one of those tools is `PrivilegedTool` precisely because they are session control. A model resuming a run the user abandoned is a bad default |
| **D** | Auto-offer at startup when `Recover` reports interrupted runs | Highest discoverability, worst blast radius: it prompts at the moment the user has least context, and an auto-resume default would re-execute work they may have deliberately killed |

**Recommendation: A + B** — the same code path behind an explicit command and a
dashboard key. C is rejected for v1: resume is a user decision, not an agent
one, and `06` §2's reasoning about privileged tools applies unchanged. D is
rejected outright; offering is fine, but the moment of least context is the
wrong moment for a default.

If C is ever wanted, it must be a `PrivilegedTool` so it cannot reach a nested
agent, and it must be gated separately from `join_run` — resuming is a bigger
action than joining.

## 5. Open decision: what a resumed run costs

Resume re-executes tasks that were interrupted, which means **re-spending model
budget on work that may have partially completed**. `12` restores `Budget`
clamped to live config, but nothing tells the user what they are about to spend.

| | Option |
|---|---|
| **i** | Resume immediately; report cost after |
| **ii** | Show what will re-run (task count, and prior `Attempts`) and confirm |
| **iii** | Dry-run flag that lists the plan without executing |

**Recommendation: ii.** The information is already in the ledger — after the
`496a126` attempt fix, `Attempts` records what already ran — and re-spend is
exactly the surprise a user should not discover afterwards.

## 6. Changes (assuming A + B, ii)

| # | File | Change |
|---|---|---|
| 1 | `internal/cli/chat_slash_handlers.go` | `/resume` with no argument lists interrupted runs; with a run ID, confirms then resumes |
| 2 | `internal/cli/tui_run_dashboard.go` | a key binding on an interrupted row, routed to the same handler as item 1 |
| 3 | `internal/cli/orchestration_state.go` | register the resumed handle with the resuming caller's principal, so `inspect_agents` / `join_run` / `cancel_run` work on it (§3.2) |
| 4 | `internal/cli/` (new, small) | one `resumeRun(ctx, runID)` used by both surfaces — do not implement the logic twice |
| 5 | `docs/product/agent.md` | document the command, and that resume re-executes and re-spends |

## 7. Verification

**Tests:**

- `TestResumeCommandListsInterruptedRuns` — no argument lists, does not execute.
- `TestResumeCommandRegistersHandleWithResumingPrincipal` — §3.2, the
  load-bearing one: after resume, `inspect_agents` from the resuming session can
  see the run, and a *different* principal cannot.
- `TestResumeCommandRefusesHeldRun` — with `13` §6 in place, a claimed run is
  refused with the held-by-another-executor message, distinct from terminal.
- `TestResumeCommandRefusesUnresumableRun` — the missing-`Input` case reports
  that cause, not a generic failure.
- `TestResumeConfirmationShowsWhatWillReRun` — §5.

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Skip handle registration | `TestResumeCommandRegistersHandleWithResumingPrincipal` |
| M2 | Register with the *persisted* principal instead of the resuming one | same test's negative half |
| M3 | Collapse the three refusal causes into one message | `TestResumeCommandRefusesHeldRun`, `…RefusesUnresumableRun` |

## 8. Rollback criterion

If resume proves to surprise users — re-running work they considered finished —
the fix is the confirmation detail in §5, not removing the surface. If it proves
*unsafe* (double execution despite `13` §6), remove the surface and leave the
API: an unreachable capability is recoverable, a duplicated side effect is not.
