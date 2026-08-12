# ADLC Plan — Context Degradation Final Recommendations

Status: COMPLETE — all waves shipped, reviewed (APPROVE each), gates green
Branch: master (do NOT create branches; do NOT delete/reset/stash — a concurrent agent works on this tree)
Commits: `9123879f` feat(agent): add remainder-ref elision and ref-only result tier · (final) fix(cli): forward ref_only_tools through skill handlers

## Objective

Implement all final recommendations from the context-degradation review, per the
ADLC (`.mivia/rules/05-adlc-agentic-development-lifecycle.md`), on master, with
the workspace structure gate (`scripts/check_go_structure.py --strict`) enforced
at every commit. Reviewers must APPROVE each wave before the next starts.

## Final recommendations → waves

| # | Recommendation | Wave |
|---|---|---|
| R1 | Compaction elision must preserve recoverability: spool the full elided body to the remainder store and name the ref in the notice (`read_output`) | Wave 2 |
| R2 | New agent-loop tier: tools in `ref_only_tools` never inline results ≥ `batch_degrade_floor_bytes`; whole body spooled, ref-only notice, notice-only charge | Wave 3 |
| R3 | Remainder store contract: idempotent, content-addressed memory store + documented `ContentStore` idempotency requirement | Wave 3 |
| R4 | Wire `ref_only_tools` through every CLI entry point (root loop, dispatcher, multi-step, skill, agent-switch, chat REPL) | Wave 3 |
| R5 | Docs: config option, notice formats, storage contract, recovery flow | Wave 4 |

Wave 1 (prep) was the original ADLC R0 audit + review scope (cleanup of
oversized files created by the R0 implementation, shippable-example test fixes).

## Wave execution log

### Wave 1 — R0 cleanup (APPROVED)
- w1a RED tests: `internal/contextmgr/planner_elision_test.go`, `internal/config/batch_result_budget_test.go`
- w1b prod: `internal/contextmgr/planner_elision.go` — cost-precision + comment fixes
- w1c prod: `internal/config/tools_config.go` + shipped-example test fix + tools_config_test.go (already carried `ref_only_tools`)
- w1d prod: `internal/workflows/controller/panel_step.go` guard + tests
- w1e test: `internal/chat/batch_result_budget_wiring_test.go`
- w1g completion, w1r review: **APPROVE**
- Gate: `go build ./...` + `go test -race` config/chat/agent PASS

### Wave 2 — R1 elide-with-ref (APPROVED)
- w2a RED tests (9): spool full body + ref notice; nil-spool / NewSpool(nil) / store-failure / empty-principal → plain notice (byte-identical); principal-scoped load (ErrDenied cross-session); two-hop artifact chain; determinism across prepares; StructuralPreparationManager seam
- w2b prod `contracts.go`: `PrepareInput.Spool *remainder.Spool`
- w2c prod `planner.go`: `PlanInput.Spool` + `PlanInput.Principal`; purity comment updated; Plan→planCompact threading
- w2d prod `structural.go`: forward Spool+Principal at the sole production seam
- w2e prod `planner_elision.go`: spool-through-seam elision; notice gains `; remainder: <ref> — use read_output to fetch the full body`; empty ref keeps plain notice (INV-AG-10); kept 3-arg/1-arg seams for the fuzz test (delegate to spool-capable core)
- w2f prod `internal/agent/context.go`: `input.Spool = opts.RemainderSpool`
- w2g completion, w2r review: **APPROVE** (recommendation: document ContentStore idempotency → done in w3f)
- Gate: `go build ./...` PASS

### Wave 3 — R2/R3/R4 ref-only tier + storage + CLI chain (APPROVED)
- w3a RED tests (8): spools whole body; below-floor inline; non-member unchanged (strict membership); ephemeral skipped; nil-spool / store-failure / empty-principal → no-ref fallback notice; budget-exhausted still ref-only
- w3b prod `options.go`: `Options.RefOnlyTools` doc comment (field existed from w1c)
- w3c prod `shape_batch.go`: ref-only tier at top of tier ladder — `slices.Contains(opts.RefOnlyTools, name)` && `totalN >= BatchDegradeFloorBytes` && !ephemeral → spool whole body → notice `[tool result for <name> elided to a remainder ref (original ~N KiB): <ref> — use read_output to fetch the full body]`; fallback `[tool result for <name> elided; original ~N KiB]`; notice-only charge
- w3d prod CLI chain (internal/cli + internal/subagents): SessionDispatcherOpts → resultBudgets → registerMultiStepHandler → MultiStepHandler → loopOptions → agent.Options; model_binding, agent_switch, chat_repl, session
- w3e tests: wiring tests (config → Session → snapshot → agent.Options root path)
- w3f prod `internal/remainder`: MemoryStore idempotent (content-addressed dedupe); `ContentStore.StoreContent` idempotency documented; memory_store_test.go
- w3g completion, w3r review: **APPROVE**
- Gate: `go build ./...` + agent/chat/remainder/config/cli/subagents tests PASS

### Wave 3 follow-up (done, committed)
- Skill-path gap (found by w3e): `registerSkillHandlers` now forwards `RefOnlyTools: budgets.refOnlyTools` to the skill `MultiStepHandler` (dispatcher_handlers.go) — the CLI chain is complete at every entry point
- Structure split (done, committed): planner_elision_spool_test.go, shape_batch_refonly.go/_test.go, tools_config helpers, agent_switch helper — `check_go_structure.py --strict --worktree` exit 0

### Wave 4 — R5 docs (done, committed)
- `docs/product/config.md`: new "Ref-only tools" section — `[tools] ref_only_tools`, exact notice formats, notice-only charge, ephemeral never spooled, INV-AG-10 fallback
- `docs/architecture/overview.md`: "Context compaction and elision recoverability" — elide-with-ref notice + `read_output` recovery, principal-scoped refs
- `docs/architecture/embedded-persistence.md`: "Remainder storage contract" — content-addressed refs, `StoreContent` idempotency, MemoryStore, grants, sentinel errors
- This file: final results appended

## Known edges (accepted, documented)
- `BatchResultBudgetBytes <= 0` → shaping passthrough, ref-only tier inert (existing architectural gate)
- Pass-1-truncated ref-only results spool the pass-1 artifact (two-hop fetch), mirroring tier 3
- Determinism of refs requires an idempotent store (contract documented + memory store tested)

## Final gates (Wave 4 end) — ALL PASS
- `go build ./...` → exit 0
- `go test -count=1 ./...` → all 49 packages ok (incl. agent 11.7s, cli 59.8s, tools 23.4s, workflows/controller 9.6s)
- `scripts/check_go_structure.py --strict --worktree` → exit 0, zero warnings
- `go vet ./...` → clean
- Commit on master with conforming message (`type(scope): imperative subject ≤72 chars`, pre-commit invariant gates green)

## Outcome
All final recommendations implemented, each wave validated (spec validation → RED tests → GREEN prod → completion → independent reviewer APPROVE), gates enforced at every commit, docs aligned with shipped behavior, structure debt split out. Residual risks: none open; two-hop fetch edge and budget-off passthrough are documented behaviors, not defects.
