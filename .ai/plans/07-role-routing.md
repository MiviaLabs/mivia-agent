# 07 — Role routing: task binding and handler registration

**Status:** Design-ready; two decisions to confirm (§2, §5).
**Date:** 2026-07-29
**Commits:** `feat(cli): route tasks to agent roles`, `feat(agent): register a handler per role`
**Depends on:** `02`, `05`. **Blocks:** `08`.
**Blast radius:** HIGH (privilege routing + resume).

---

## 1. Goal

A task selects a role; the spawned agent runs with that role's prompt and scoped tools.

## 2. Binding: one handler per role

Register one `MultiStepHandler` per role under the role name (`Kind=Subagent`) in `registerMultiStepHandler` (`internal/cli/dispatcher.go:120-143`).

> **Correction to the predecessor plan.** It justified this option as *"composes with the existing `dispatcher.Allow[Subagent][name]` gate."* That is false: `Dispatcher.Register` unconditionally sets `d.policy.Allow[k][name] = true` (`dispatcher.go:161-168`). **Registration *is* allowance**; there is no per-caller gate to compose with.
>
> The real reason to choose it: **this is exactly what `RegisterAllAsSubagents` already does for skills** (`skills.go:214-229`), so it adds no new pattern. Cost is N × (a few pointers + a small map) — trivial for realistic N.

The predecessor's stated migration trigger (">50 roles, or per-role providers") is dropped: per-role providers are cut from the program (`00` §4), and lazy materialization is a performance change, not an architectural fork.

## 3. Task binding

Add an explicit `role` JSON field as the primary binding — cleaner than overloading `handler`/`name`.

| Tool | Field today | Change |
|---|---|---|
| `dispatch_tasks` | `handler`, defaults `"multi_step"` (`internal/cli/dispatch.go:215-218`); tasks built at `:205-238` | add `role`; precedence `role` > `handler` > default |
| `spawn_agent` | `name`, **no default** (`internal/cli/orchestrate.go:136`, assigned `:172-173`) | add `role`; if `name` is empty and `role` set, use `role`. Document the asymmetry |

**Back-compat:** no `role` and no `handler` ⇒ `multi_step`, unchanged.

> Note the correct anchors. The predecessor plan cited `orchestrate.go:261-308` for `spawn_agent`; that range is `inspectAgentTool`. `spawnAgentTool.Execute` is `orchestrate.go:131-209`.

### Discoverability — use the schema, not prose

`dispatchTasksTool.Parameters()` (`internal/cli/dispatch.go:98-101`) defines `handler` as a free-form string. Make `role` an **`enum` of role names**. Providers enforce enums structurally; injected prose is a suggestion. The predecessor plan relied solely on runtime `Description()` injection.

Keep runtime injection of the role list + `description` as well (that is the routing hint), but the **compiled base text** must stay project/language-generic — enforced by `internal/tools/generic_surface_test.go`.

**Compiled text that actively fights roles:** `dispatch.go:66` currently reads *"Always set handler:\"multi_step\" for tool-using agents."* Ship roles without rewriting that and the model will route around them. This is compiled surface, so rule 60 applies to the replacement.

> **Rule-60 note.** The predecessor plan repeatedly self-declared runtime-injected role text "rule-60-exempt". No such exemption exists in `.ai/rules/60-*.md`, which draws no compiled-vs-runtime distinction for `Description()`. Rule 60's own "When editing tools" step says to update the rule when the generic contract changes, so a `chore(ai)` amendment is required — **but it ships with `05`**, which is the first plan to inject workspace text into a compiled description. By the time `07` runs, the amendment exists.

## 4. Namespace collisions

Role, handler, and skill names share the `Kind=Subagent` map. Reject collisions at Layer B (`05` §7) with the offending source path, so `Dispatcher.Register`'s `duplicate handler` error (`dispatcher.go:158-160`) is unreachable.

**Untrusted-content angle:** a repo can ship `.mivia/skills/researcher/SKILL.md` with `name: researcher`, colliding with a role and failing session startup — a denial of service driven by repo content. Reject or namespace with a clear error rather than `duplicate handler`. The predecessor's `TestNamespaceCollision` covered only the config-time case.

## 5. Resume and idempotency — decision required

Two coupled defects, both understated in the predecessor plan as "informational":

**Resume drops role-bearing fields.** `ResumeInterruptedRun` reconstructs tasks with only `ID`, `Name`, `DependsOn` (`internal/coordinator/recovery.go:99-103`); `ledger.TaskSnapshot` persists only `HandlerName` (`coordinator/spawn.go:127`). With `role` as a field **separate from** `handler`, a resumed task dispatches to plain `multi_step` — **the full-privilege default handler**. That is privilege escalation via crash-and-resume.

It survives only by accident when role == handler name, which is true under §2's registration model but is not guaranteed by the design.

**Idempotency keys.** `requestFingerprint` hashes the marshaled `[]subagents.Task` (`coordinator/spawn.go:82-89`), and `Spawn` returns the existing handle on key match (`:23-28`). If role is carried out-of-band, two runs from *different* roles produce the same fingerprint, and a low-privilege role can receive a high-privilege role's live handle.

**Options:**

| | Approach | Cost |
|---|---|---|
| **A** | Persist `role` on `TaskSnapshot`; include it in `requestFingerprint`; on resume re-resolve the role and **fail closed** if it no longer exists or resolves to different `EffectiveTools` | ledger schema change + migration |
| **B** | Constrain role name ≡ handler name permanently, so `HandlerName` already carries the role | no migration; forecloses ever separating them |

**Decision: B, unconditionally.** `02` §3d determined it performs no ledger migration, so the A-trigger never fires. Document the name-equivalence constraint as load-bearing so nobody "cleans it up" later.

> **Correction to the framing above.** `ResumeInterruptedRun` has **zero production callers** (`coordinator/types.go:50` decl, `recovery.go:77` def, `ledger/types.go:100` comment — that is the complete non-test grep), so the escalation is unreachable today. It is also weaker than stated even if reached: `recovery.go:99-103` drops `Input`, so `MultiStepHandler.Invoke` fails immediately with `invalid task input` (`multi_step.go:54-59`). The real defect is that **resume is broken**, not that resume escalates. Filed separately in `02` §3d.

Still add `TestResume_PreservesRole` and `TestIdempotency_CrossRoleHandleDenied` — the idempotency half (`spawn.go:82-89`) *is* live.

Also note `H8` (`05` §9): renaming a role invalidates in-flight resume regardless of choice.

## 6. Changes

| Site | File | Change |
|---|---|---|
| Handler registration | `internal/cli/dispatcher.go:120-143` | loop over `roles.Registry.Names()`; collision rejection |
| Task binding | `internal/cli/dispatch.go:205-238` | `role` field; enum in `Parameters()`; rewrite the `:66` sentence |
| Spawn binding | `internal/cli/orchestrate.go:131-209` | `role` field; reconcile `name` asymmetry |
| Role-scoped handler | `internal/subagents/multi_step.go` | `Role *roles.ResolvedRole`; when set, role prompt + `ScopedRegistry` |
| Rule amendment | — | **moved to `05`** (first plan to inject workspace text into a compiled description) |

**Avoid a permanent dual path.** The predecessor said "when `Role` is nil, current behavior" — two prompt-resolution and two registry-scoping paths forever, with the legacy one untested by any role test. Instead express `[subagents]` **as** a built-in role. `Role` is then never nil, there is one code path, and back-compat is proven by construction.

> **Name it once: `default`.** `05` §3 calls the compiled base role `default` (and `inherits` defaults to `"default"`); an earlier draft here called the same object `multi_step`. They are one role. Use `default`, and keep `multi_step` only as the back-compat *handler* name that `role` resolution falls through to.

## 7. Verification

```bash
go test ./internal/cli/... ./internal/subagents/... ./internal/coordinator/... -race
make verify && make invariants
```

**Tests:** `TestDispatch_RoleBinding` (role reaches a scoped handler; no-role task reaches `multi_step`) · `TestSpawnAgent_RoleField` · `TestNamespaceCollision_RoleVsSkill` (including the workspace-skill DoS case) · `TestResume_PreservesRole` · `TestIdempotency_CrossRoleHandleDenied` · `TestRoleEnumInParameters` · `TestConcurrentRoleHandlersShareRegistry` — N role handlers on one `*runtime.Dispatcher` under a `dispatch_tasks` fan-out, under `-race` (the predecessor had no concurrency test despite `make race` being gated).

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Ignore `role`, fall through to `handler` | `TestDispatch_RoleBinding` |
| M2 | Drop collision rejection at Layer B | `TestNamespaceCollision_RoleVsSkill` |
| M3 | Resume without re-resolving the role | `TestResume_PreservesRole` |
| M4 | Exclude role from `requestFingerprint` | `TestIdempotency_CrossRoleHandleDenied` |

**Rollback criterion:** if per-role handler registration causes namespace churn in practice, move to invoke-time role resolution via a `Role` field on `runtime.Request` — but only together with §5 option A, since that path makes the resume gap worse, not better.
