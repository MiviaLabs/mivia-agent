# 06 — Role–skill binding

**Status:** Design-ready.
**Date:** 2026-07-29
**Commit:** `feat(agent): restrict which skills a role may invoke`
**Depends on:** `05` (role model, parser). **Blocks:** nothing.
**Blast radius:** MODERATE.

---

## 1. What `skills = [...]` means

mivia skills are **not** context injections. `internal/skills/loader.go:64-83` registers each skill as a `Definition` whose `Run` is a single `completer.Chat`; `RegisterAllAsSubagents` (`skills.go:214-229`) exposes them as `Kind=Subagent` handlers and `d.Allow`s every one globally.

The reference implementation's `skills:` field preloads full skill *content* into the subagent's context. **mivia cannot copy that semantic**, because a mivia skill is a delegated one-shot with its own completer call and its own untrusted-content wrapper (`loader.go:74-75`).

**Decision: `skills` is an invocation allowlist, not a preload.**

> *"This role may invoke these skills as subagents."* Omitted ⇒ all skills (consistent with `tools`). Explicit `[]` ⇒ none.

### Why not preload

Preloading duplicates the same text into every turn of every role that lists it, and it **drops the untrusted-content wrapper** that `loader.go:74-75` deliberately applies. If preload is wanted later it needs (a) `Definition.Body` retained by the loader and (b) a separate field name. **Reserve `preload_skills`; do not overload `skills`.**

## 2. Enforcement point — and its honest scope

Global `d.Allow(Subagent, name)` cannot be narrowed per role: there is one `Policy.Allow` map per dispatcher, shared by every handler, and `Register` auto-allows (`dispatcher.go:162-168`). So the gate lives where a skill name becomes a task:

1. `dispatchTasksTool.buildTasks` (`internal/cli/dispatch.go:214-237`) — reject `pt.Handler` when it names a skill absent from the active role's allowlist. The tool already looks the name up in `t.skillReg` at `:220-224`, so this is ~4 lines.
2. `spawnAgentTool.Execute` (`internal/cli/orchestrate.go:162-175`) — same check on `pt.Name`.

> **Scope caveat, stated plainly in docs and in the field description.** Both tools are in the mandatory denylist, so they exist **only in the root agent's registry**. Therefore `skills` is enforced for the root role (`mivia chat --agent X`) and is *structurally* satisfied for spawned roles — a spawned role has no way to reach a skill at all. Do **not** describe this as a general capability gate. Describe it as: *"restricts which skills the root orchestrator may fan out to under this role."*

Getting this wording right matters more than the code: an overclaimed security boundary is worse than an honest narrow one.

## 3. Companion gate: skill tools ⊆ role tools

With `Definition.Tools` populated (plan `05` P2 — the parser rewrite), `roles.Resolve` can enforce that each allowlisted skill's declared `Tools` is a subset of the role's effective tools, reusing `skills.Select` (`internal/skills/skills.go:51-65`).

**This check is vacuous until P2 lands.** `internal/skills/loader.go` never sets `Definition.Tools` today, so `Select`'s subset loop (`skills.go:60-64`) iterates an empty slice and passes for every skill. The predecessor plan listed this gate as "RESOLVED — enforced at load time, Phase 1"; it would have shipped as a no-op.

**Do not claim this check in docs before P2.**

Timing: the gate runs at Layer B (`05` §7), not config-load — the skill registry does not exist until `skills.LoadMarkdown` runs. The predecessor plan placed it at config-load, which is impossible for the same reason tool-name validation is.

## 4. Changes

| Site | File | Change |
|---|---|---|
| Skill allowlist gate | `internal/cli/dispatch.go:214-237` | +6 LOC; reject non-allowlisted skill names |
| Same for spawn | `internal/cli/orchestrate.go:162-175` | +6 LOC (file is 393; keep minimal) |
| Subset gate | `internal/roles/validate.go` | reuse `skills.Select` once P2 lands |
| ~~Skill union~~ | — | **Dropped.** Was "embedded + workspace, workspace wins" per the closed plan `03`. There is no embedded corpus: skills come from the workspace only, so there is nothing to union and `06` gains a dependency-free start |

## 5. Verification

```bash
go test ./internal/roles/... ./internal/skills/... ./internal/cli/... -race
make verify && make invariants
```

**Tests:**

- `TestRoleSkillAllowlist_RootOnly` — root role cannot dispatch a non-allowlisted skill; a spawned role has no dispatch tool at all (asserts the §2 caveat is true, not just documented).
- `TestRoleSkillAllowlist_OmittedAllowsAll` and `_EmptyAllowsNone` — the nil-vs-empty distinction. Implementation must test `len()==0` intent explicitly; omitted and `[]` mean *different* things here, unlike `can_spawn` in the predecessor plan.
- `TestSkillToolsSubsetOfRoleTools` — requires P2; skip with an explicit `t.Skip("requires Definition.Tools; see 05 P2")` until then rather than passing vacuously.
- ~~`TestSkillUnionWorkspaceWins`~~ — dropped with the skill union above; workspace skills are the only skills.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Drop the allowlist check in `buildTasks` | `TestRoleSkillAllowlist_RootOnly` |
| M2 | Treat `skills: []` as "all" | `TestRoleSkillAllowlist_EmptyAllowsNone` |
| M3 | Populate `Definition.Tools` but skip the subset gate | `TestSkillToolsSubsetOfRoleTools` |

**Rollback criterion:** if the root-only scope makes the field more confusing than useful, drop `skills` from the v1 schema entirely rather than shipping a field whose enforcement most users will never reach. It returns when a spawned role can hold a delegation tool.
