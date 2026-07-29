# 16 — Make workspace skills discoverable

**Status:** Design-ready; one open decision (§4).
**Date:** 2026-07-30
**Depends on:** nothing. **Blocks:** nothing. **Composes with:** `06` (§6).
**Blast radius:** LOW — additive; no behaviour changes for a workspace with no skills.

---

## 1. The gap

Workspace skills are loaded, registered and invocable — and the model has no way
to learn they exist.

`skills.LoadMarkdown` reads `.mivia/skills/<name>/SKILL.md` and registers each as
a `Subagent` handler, so `dispatch_tasks{handler: "bug-audit"}` works **today**.
But nothing tells the model that `bug-audit` is a name it may use:

- `Definition` (`internal/skills/skills.go`) has **no `Description` field**. The
  loader parses `description:` from frontmatter and uses it only to build the
  skill's *own* sub-prompt (`prompt = "Skill: " + name + "\nDescription: " + …`).
  The calling agent never sees it.
- The `handler` parameter of `dispatch_tasks` says only *"Registered subagent or
  skill handler; defaults to multi_step"* — no enumeration. Same for
  `spawn_agent`'s `name`.
- `internal/cli/prompt.go` contains no occurrence of "skill".

So a skill is reachable only by guessing its exact name. This repo ships eight
of them with carefully written routing hints — `bug-audit`'s description says
*"Use when the user asks for a bug audit… do not use for ordinary
implementation"* — and none of that text can reach the model that would act on
it.

Users authoring their own skills in their own projects hit the same wall, which
is the case that matters: a skill nobody can discover is a file, not a feature.

## 2. Decision

**Surface each skill's name and, when present, its description on the tool
parameter that accepts it.** Description is optional: a skill without one is
still listed by name.

Not a preload. `06` §1 already settled that mivia skills are delegated one-shots
with their own untrusted-content wrapper, so injecting skill *bodies* into
context is the wrong semantic. This plan surfaces the **routing hint only** —
enough to choose a skill, nothing more.

## 3. Scope correction — no hoist needed

An earlier reading of this said it needed `05` P3 (hoisting `skills.LoadMarkdown`
out of `attachSessionDispatcher`) because skills load after the tool registry is
built. **That is wrong for this plan.** Verified at HEAD:
`attachSessionDispatcher` (`internal/cli/chat_repl.go`) loads `skillReg` *before*
calling `NewSessionDispatcher`, and `dispatchTasksTool` is constructed with
`skillReg` already in hand (`internal/cli/dispatcher.go:156`). The registry is
available exactly where the tool schema is built.

`spawnAgentTool` is constructed **without** `skillReg` (`orchestrate.go`) and
needs it passed in — that is the only plumbing this requires.

## 4. Open decision: how to surface it

| | Option | Assessment |
|---|---|---|
| **A** | JSON-schema `enum` on `handler` / `name`, listing the registered skills plus the built-in handlers | Strongest: an invalid name becomes unrepresentable rather than merely discouraged. But it must include `multi_step`, `oneshot`, `delegate`, or it breaks every existing call — and a wrong enum is worse than none |
| **B** | Append a `name — description` list to the parameter's `description` string | Cannot break existing calls. Purely advisory; the model can still emit a bad name |
| **C** | Both — `enum` for validity, list in the description for the routing hints | The descriptions are the useful half and do not fit in an `enum` |

**Recommendation: C.** The `enum` is what stops `handler: "bugaudit"` failing at
dispatch; the description list is what lets the model choose `bug-audit` over
`verify-code-change` in the first place. Neither does the other's job.

If A is taken alone, the enum **must** be built from the live registry plus the
built-in handler names, and a test must assert `multi_step` is present — omitting
it silently breaks the default path.

## 5. Untrusted content and rule 60

Skill names and descriptions are **workspace text entering a compiled tool
surface**. Two constraints, both already precedented:

- **Rule 60.** `05` §4 hit exactly this injecting role `description` into
  `Description()` and concluded the rule-60 amendment ships with the change that
  first does it. Whichever of `05`/`16` lands first carries the amendment; say so
  in the commit rather than assuming the other did it.
- **Untrusted text.** `loader.go` already wraps skill *instructions* as untrusted
  project content. A description reaching a tool schema is prompt-injection
  surface: bound its length (mirror the existing skill byte cap), strip control
  characters and newlines so it cannot forge schema structure, and do not let it
  displace the parameter's own text.

Neither makes the feature unsafe — a description is chosen by the same person who
authors the skill and the workspace — but the bounding is not optional.

## 6. Composing with `06`

`06` restricts *which* skills a role may invoke. Discovery and restriction are
the same surface from two sides: once `06` lands, the enum and the list must show
a role only the skills it is allowed to invoke, or the model is told about
capabilities it will be refused.

Build the list from a single function that takes the registry and an optional
allowlist, so `06` supplies the allowlist later without reshaping this. Do not
inline the filtering at the two call sites.

## 7. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/skills/skills.go` | add `Description string` to `Definition` |
| 2 | `internal/skills/loader.go` | populate it; keep the existing sub-prompt behaviour unchanged |
| 3 | `internal/skills/skills.go` | one helper returning the model-facing `(name, description)` list from the registry, with an optional allowlist for `06` |
| 4 | `internal/cli/dispatch.go` | `handler` parameter gains the enum and/or the list |
| 5 | `internal/cli/orchestrate.go` + `dispatcher.go` | pass `skillReg` into `spawnAgentTool`; same treatment for `name` |
| 6 | `internal/cli/prompt.go` | one sentence that workspace skills exist and are invoked by name via `dispatch_tasks`/`spawn_agent`. Must stay generic (rule 60) — describe the mechanism, never name this repo's skills |

## 8. Verification

```bash
go build ./... && go vet ./... && go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestSkillDescriptionReachesToolSchema` — a skill with a description appears,
  with that description, in the `dispatch_tasks` parameter schema.
- `TestSkillWithoutDescriptionStillListed` — the "if available" case; name only,
  no empty separator or dangling dash.
- `TestNoSkillsLeavesToolSchemaUnchanged` — a workspace with no skills produces
  byte-identical schema to today. This is the regression that keeps the change
  additive.
- `TestBuiltInHandlersRemainSelectable` — `multi_step`, `oneshot`, `delegate`
  still accepted (§4's failure mode if an enum is added).
- `TestSkillDescriptionIsBoundedAndSingleLine` — §5: an over-long or
  newline-bearing description is truncated/flattened, not passed through.
- `TestToolSurfaceStaysGeneric` — the existing rule-60 guard
  (`internal/tools/generic_surface_test.go`) must still pass with skills loaded.

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Drop `Description` from the schema builder | `TestSkillDescriptionReachesToolSchema` |
| M2 | Emit `name — ` for a description-less skill | `TestSkillWithoutDescriptionStillListed` |
| M3 | Build the enum from skills only, omitting built-ins | `TestBuiltInHandlersRemainSelectable` |
| M4 | Pass the description through unbounded | `TestSkillDescriptionIsBoundedAndSingleLine` |

**Docs:** `docs/product/agent.md` — that `.mivia/skills/<name>/SKILL.md` is
discovered automatically, and that `description:` is what the agent uses to
choose between skills. This is the user-facing half of the feature: authors need
to know the description is a routing hint, not decoration.

## 9. Rollback criterion

If enumerating skills proves to crowd the tool schema (many skills, long
descriptions), cap the list and say what was dropped rather than silently
truncating — a partial list the model believes is complete is worse than no list.
Do not solve it by removing descriptions; they are the half that carries the
decision.
