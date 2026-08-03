# 51.07 - CLOSED (merged): pageable truncated remainders

**Status:** **CLOSED - MERGED.** ADLC Step 0 ran 2026-08-03 and found the
design already shipped. Superseded by
`.mivia/plans/archived/tools/01-read-output-pageable-remainders.md`, which
is marked IMPLEMENTED and which itself said "if 51/07 ships its own
affordance, close this plan as merged" - the reverse happened.
**Do not implement from this document.**
**Date:** 2026-08-02, closed 2026-08-03
**Part of:** program `51` (`00-overview.md`).

## 0. Step 0 disposition

Panel verdict: **DO-NOT-BUILD** (the plan proposes building what exists).

| Finding | Severity | Disposition |
|---------|----------|-------------|
| Entire §4.1/§4.2 design shipped in `304f42d`: spool (`internal/remainder/spool.go:96`), the exact proposed notice (`internal/remainder/notice.go:14`), reader (`internal/cli/read_output.go:37`), wiring (`internal/agent/loop_scheduler.go:88`) | BLOCK | **Accepted.** Close as merged. Rebuilding risks a second, divergent notice grammar. |
| §6.1 (new tool vs `read_file` parameter) presented as open | BLOCK | **Closed by code:** decided as a **new tool**, `read_output`. |
| §4.3 "storage layer already owns lifecycle and deletion" | BLOCK, **false** | **Accepted.** There is no reclamation; `TestContentStoreIsNeverReclaimed` (`internal/ledger/content_retention_test.go:55`) asserts the opposite as intended. `Spool.MarkExpired` (`spool.go:187`) has no production caller. |
| §2 "plan 48 converts destroy to truncation" | STALE | **Accepted.** `48` is archived with item F still TODO; `internal/runtime/dispatcher.go:471` still calls `fail(overCeilingError(...))`. `07` shipped at a *different* seam (the agent-loop result cap), not the dispatcher ceiling. |
| §6.4 "under uncapped defaults truncation is rare, so value is concentrated in capped deployments" | LOW, **premise false** | **Accepted.** The cap that triggers spooling is the agent-loop `MaxToolResultChars`/capability cap (`internal/agent/loop_limits.go:36`), not the operator ceiling. Truncation is common under the uncapped default. Had Step 0 trusted §6.4 it would have reached the wrong conclusion. |
| §6.2 (redaction reachable from the spool point?) | MEDIUM | **Closed, but by accident not design:** `run_command` redacts before returning (`internal/tools/run.go:216`), so the spool stores redacted bytes. `read_output` applies **no** redaction on load. Recorded as defect §8.3 in the overview. |
| Attack "is spooling a disk-growth vector?" | BLOCK | **Confirmed: nothing bounds it.** See §2 below - this is the one genuinely unbuilt piece. |
| Ephemeral bodies are capped with a **nil** spool so a ref cannot outlive the scrub (`loop_scheduler.go:60-70`) | - | Noted so a future change does not regress it. |

## 1. What shipped

| Plan section | Shipped at |
|--------------|-----------|
| §4.1 spool on truncation | `internal/remainder/spool.go:96`, `internal/agent/loop_scheduler.go:88` |
| §4.1 notice naming the reference | `internal/remainder/notice.go:14`, `CapWithSpoolRef` at `:39` |
| §4.2 reading a remainder | `internal/cli/read_output.go:19` |
| §4.4 dedup by digest | `internal/contentref` + `StoreContent`/`LoadContent` (`internal/ledger/repository.go:102`) |
| INV-CE-07-A/B/C | `internal/remainder/spool_test.go`, `notice_paths_test.go` |

## 2. Residual work, re-parented

Three items survive this closure. **None of them is a paging plan.**

| # | Item | Owner |
|---|------|-------|
| R1 | Bound and reclaim the `content` table: a real `MarkExpired` caller so `ErrExpired` (`read_output.go:137`) becomes reachable, plus a size or age cap | New plan, or plan `48` residual - it owns truncation/storage semantics |
| R2 | Close the INV-CE-C hole at tool-*internal* truncation sites (`internal/tools/search.go:139`, `read.go:183`) which cut bytes inside `Execute`, so the loop has nothing over-cap left to spool - or amend INV-CE-C | Plan `48` residual |
| R3 | Apply `redact.Text` on the `read_output` load path, for symmetry with `ledger_read` (`internal/cli/ledger_tools.go:131`) | Bug fix, needs an owner now |

R1 is the largest and the only one that is genuinely unbuilt design.
Overview §8.3 and §8.4 record R3 and R1 as shipped-code defects.

## 3. Why this document is kept

As the record of a Step 0 that returned "already built". The scheduling
lesson is worth more than the plan was: `07` was drafted from a baseline
five days stale, and the panel's first act was to discover that
`.mivia/plans/archived/tools/01` had already shipped the same design with
the same notice format. Re-verify baselines against HEAD before challenging
a plan, not after.
