# 16 - Make workspace skills discoverable

**Status:** ✅ Implemented 2026-07-30 (`b17988f`) - §4 decided (B, with a constrained C
for `handler`). Header corrected 2026-07-30: it still read "Design-ready" after the
plan shipped and was archived.
**Date:** 2026-07-30 (audited against HEAD `e933a73` same day)
**Depends on:** nothing. **Blocks:** nothing. **Composes with:** `06` (§6).
**Blast radius:** LOW - additive; no behaviour changes for a workspace with no skills.

---

## 1. The gap

Workspace skills are loaded, registered and invocable - and the model has no way
to learn they exist.

`skills.LoadMarkdown` reads `.mivia/skills/<name>/SKILL.md` and registers each as
a `Subagent` handler, so `dispatch_tasks{handler: "bug-audit"}` works **today**.
But nothing tells the model that `bug-audit` is a name it may use.
(Invocability is not assumed: `TestMarkdownSkillReachesProductionDispatcherPath`,
`internal/cli/delegation_test.go:608`, already executes `dispatch_tasks` with a
loaded workspace skill as `handler`.)

- `Definition` (`internal/skills/skills.go`) has **no `Description` field**. The
  loader parses `description:` from frontmatter and uses it only to build the
  skill's *own* sub-prompt (`prompt = "Skill: " + name + "\nDescription: " + …`).
  The calling agent never sees it.
- The `handler` parameter of `dispatch_tasks` says only *"Registered subagent or
  skill handler; defaults to multi_step"* - no enumeration. Same for
  `spawn_agent`'s `name`.
- `internal/cli/prompt.go` contains no occurrence of "skill".

So a skill is reachable only by guessing its exact name. This repo ships eight
of them with carefully written routing hints - `bug-audit`'s description says
*"Use when the user asks for a bug audit… do not use for ordinary
implementation"* - and none of that text can reach the model that would act on
it.

Users authoring their own skills in their own projects hit the same wall, which
is the case that matters: a skill nobody can discover is a file, not a feature.

## 2. Decision

**Surface each skill's name and, when present, its description on the tool
parameter that accepts it.** Description is optional: a skill without one is
still listed by name.

Not a preload. `06` §1 already settled that mivia skills are delegated one-shots
with their own untrusted-content wrapper, so injecting skill *bodies* into
context is the wrong semantic. This plan surfaces the **routing hint only** -
enough to choose a skill, nothing more.

## 3. Scope correction - no hoist needed

An earlier reading of this said it needed `05` P3 (hoisting `skills.LoadMarkdown`
out of `attachSessionDispatcher`) because skills load after the tool registry is
built. **That is wrong for this plan.** Verified at HEAD:
`attachSessionDispatcher` (`internal/cli/chat_repl.go`) loads `skillReg` *before*
calling `NewSessionDispatcher`, and `dispatchTasksTool` is constructed with
`skillReg` already in hand (`internal/cli/dispatcher.go:156`). The registry is
available exactly where the tool schema is built.

`spawnAgentTool` is constructed **without** `skillReg` (`orchestrate.go`) and
needs it passed in - that is the only plumbing this requires.

## 4. Open decision: how to surface it

| | Option | Assessment |
|---|---|---|
| **A** | JSON-schema `enum` on `handler` / `name`, listing the registered skills plus the built-in handlers | Strongest in principle: an invalid name becomes unrepresentable. But the enum has no complete source of truth today - see below |
| **B** | Append a `name - description` list to the parameter's `description` string | Cannot break existing calls. Purely advisory; the model can still emit a bad name |
| **C** | Both - `enum` for validity, list in the description for the routing hints | The descriptions are the useful half and do not fit in an `enum` |

**Recommendation: B for both parameters. C only for `handler`, and only if the
enum is sourced correctly** (below). The description list is what lets the model
choose `bug-audit` over `verify-code-change`; the enum only stops a typo, and
buys that at a cost this plan originally under-priced.

### Why the enum is not free

**The enum has no complete source of truth.** `runtime.Dispatcher` exposes
`Has(Kind, name)` but no enumeration of registered handlers
(`internal/runtime/dispatcher.go` - there is no `List`). So an enum can only be
built from `skillReg.List()` plus a hardcoded `multi_step`/`oneshot`/`delegate`.
That silently excludes every handler registered by any other path:

- `05` registers **per-role handlers** as `Kind=Subagent` (`agent_roles.go`,
  Layer B). Those names become schema-invalid the moment `05` lands.
- `NewSessionDispatcher` is **exported** and returns a `*runtime.Dispatcher`
  with a public `Register`. An embedder registering a custom subagent after
  construction gets a schema that forbids calling it.

An enum built from an incomplete source is exactly the "wrong enum is worse than
none" case. If A or C is taken, it must be fed by a new
`Dispatcher.Handlers(Kind) []string` - not by skills plus a hardcoded triple -
and a test must assert `multi_step` is present.

**The two parameters are not symmetric.** `dispatch_tasks.handler` is optional
and defaults to `multi_step` (`dispatch.go:216-219`), so a bad enum degrades the
call. `spawn_agent`'s `name` is **required**, and its task item schema is
`"additionalProperties": false` (`orchestrate.go:125-126`), so a bad enum there
is a hard call failure. Do not put an enum on `name`.

## 5. Untrusted content and rule 60

Skill names and descriptions are **workspace text entering a compiled tool
surface**. Two constraints, both already precedented:

- **Rule 60.** `05` §4 hit exactly this injecting role `description` into
  `Description()` and concluded the rule-60 amendment ships with the change that
  first does it. Whichever of `05`/`16` lands first carries the amendment; say so
  in the commit rather than assuming the other did it.
- **Untrusted text.** `loader.go` already wraps skill *instructions* as untrusted
  project content. A description reaching a tool schema is prompt-injection
  surface: bound its length, strip control characters and newlines so it cannot
  forge schema structure, and do not let it displace the parameter's own text.
  `parseMarkdown` today trims only quotes and whitespace (`loader.go:112`);
  newlines cannot survive the line-split parser, but tabs, ANSI escapes and
  unbounded length can.

Neither makes the feature unsafe - a description is chosen by the same person who
authors the skill and the workspace - but the bounding is not optional.

### Two corrections to the precedents this section assumed

**There is no per-description cap to mirror.** The only cap is
`maxSkillBytes = 256 << 10` (`internal/skills/loader.go:14`), applied to the whole
`SKILL.md` file. Mirroring 256 KB onto a one-line description bounds nothing. The
cap must be invented - ~200 characters - not inherited.

**The existing rule-60 guard does not cover the tools this plan changes.**
`collectModelFacingToolText` (`internal/tools/generic_surface_test.go:43-49`)
builds `NewDefaultRegistry(DefaultOptions{Workspace: ws})` over a temp dir.
`dispatch_tasks` and `spawn_agent` are **session** tools, added only by
`registerSessionTool` during `NewSessionDispatcher` (`dispatcher.go:165`), so they
are absent from a default registry - as `delegation_test.go:434` implicitly
asserts by building a dispatcher before it can find them. The same blind spot
applies to `TestOpenAIToolsJSONHasNoLanguageBias` and
`TestToolOpenAIToolsConsistency`.

So **no existing test would catch workspace skill text reaching the tool
surface.** Extending the rule-60 guard to a session-built registry is part of this
change, not a pre-existing safety net to lean on.

## 6. Composing with `06`

`06` restricts *which* skills a role may invoke. Discovery and restriction are
the same surface from two sides: once `06` lands, the enum and the list must show
a role only the skills it is allowed to invoke, or the model is told about
capabilities it will be refused.

Build the list from a single function that takes the registry and an optional
allowlist, so `06` supplies the allowlist later without reshaping this. Do not
inline the filtering at the two call sites.

**The allowlist is tri-state and the obvious signature loses it.** `06` §1
defines omitted ⇒ all skills, explicit `[]` ⇒ none, and ships
`TestRoleSkillAllowlist_EmptyAllowsNone` to catch the collapse. A helper written
as `func List(r *Registry, allow []string)` with the idiomatic
`if len(allow) == 0 { return all }` silently implements `06`'s own M2 mutation.
Use `*[]string`, a named option type, or an explicit `allowAll bool` - and say so
here, because `16` is where the signature gets fixed.

Timing is not a problem: `--agent` resolves once at startup (`05` §5, H7), so a
schema built at tool-construction time cannot go stale mid-session.

## 7. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/skills/skills.go` | add `Description string` to `Definition` |
| 2 | `internal/skills/loader.go` | populate it; keep the existing sub-prompt behaviour unchanged |
| 3 | `internal/skills/skills.go` | one helper returning the model-facing `(name, description)` list from the registry, with an optional allowlist for `06` |
| 4 | `internal/cli/dispatch.go` | `handler` parameter gains the list (and the enum only under §4's sourcing constraint) |
| 5 | `internal/cli/orchestrate.go` + `dispatcher.go` | pass `skillReg` into `spawnAgentTool` (`registerOrchestrationTools` gains the parameter); `name` gains the **list only** - no enum, see §4 |
| 6 | `internal/cli/prompt.go` | one sentence that workspace skills exist and are invoked by name via `dispatch_tasks`/`spawn_agent`. Must stay generic (rule 60) - describe the mechanism, never name this repo's skills |
| 7 | `internal/tools/generic_surface_test.go` (or a new `internal/cli` guard) | extend the rule-60 check to a **session-built** registry, which is the only one containing `dispatch_tasks`/`spawn_agent` - see §5 |

Change #1 is safe: every `skills.Definition` composite literal in the tree is
keyed (`loader.go:64`, `skills_test.go:12,26,44,55,58`,
`delegation_test.go:579`), nothing reflects over the struct, and it was never
JSON-marshalled (it holds a func field). Adding a field breaks no consumer.

### Precondition on change #5

`spawn_agent` cannot carry a skill's `Permission`. `dispatchTasksTool.buildTasks`
looks the skill up and sets `Permission` on the task (`dispatch.go:222-226`);
`spawnAgentTool.Execute` sets none (`orchestrate.go:189-201`), and
`handler.Invoke` rejects on mismatch (`skills.go:85`). Harmless today because
`LoadMarkdown` never sets `Permission` - but the moment `spawn_agent` advertises
skills by name, any permission-bearing skill it lists is unreachable through it.
Either mirror the `dispatch.go:222-226` lookup in `spawnAgentTool.Execute`, or
omit permission-bearing skills from `spawn_agent`'s list. Do not advertise what
that tool cannot invoke.

## 8. Verification

```bash
go build ./... && go vet ./... && go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestSkillDescriptionReachesToolSchema` - a skill with a description appears,
  with that description, in the `dispatch_tasks` parameter schema.
- `TestSkillWithoutDescriptionStillListed` - the "if available" case; name only,
  no empty separator or dangling dash.
- `TestNoSkillsLeavesToolSchemaUnchanged` - a workspace with no skills produces
  the same schema as an empty/nil registry. **There is no existing baseline**: no
  test in the tree snapshots tool schema bytes, and the schema assertions that do
  exist (`TestOpenAIToolsSchemaValidRequiredArrays`,
  `TestToolOpenAIToolsConsistency`) are structural and do not include these two
  tools. So this test must build its own baseline - deep-equal a dispatcher built
  with a nil `skillReg` against one built with an empty registry - rather than
  compare against "today".
- `TestBuiltInHandlersRemainSelectable` - `multi_step`, `oneshot`, `delegate`
  still accepted (§4's failure mode if an enum is added).
- `TestSkillDescriptionIsBoundedAndSingleLine` - §5: an over-long or
  control-character-bearing description is truncated/flattened, not passed
  through. The bound is a new ~200-char constant; `maxSkillBytes` is a file cap
  and is not it.
- `TestSessionToolSurfaceStaysGeneric` - **new** guard over a session-built
  registry with skills loaded. The existing
  `internal/tools/generic_surface_test.go` cannot see `dispatch_tasks` or
  `spawn_agent` (§5), so it would pass vacuously and prove nothing here.

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Drop `Description` from the schema builder | `TestSkillDescriptionReachesToolSchema` |
| M2 | Emit `name - ` for a description-less skill | `TestSkillWithoutDescriptionStillListed` |
| M3 | Build the enum from skills only, omitting built-ins | `TestBuiltInHandlersRemainSelectable` |
| M4 | Pass the description through unbounded | `TestSkillDescriptionIsBoundedAndSingleLine` |
| M5 | Put a language/product-biased skill description on the tool surface | `TestSessionToolSurfaceStaysGeneric` |
| M6 | Collapse the §6 allowlist so `[]` means "all skills" | `06`'s `TestRoleSkillAllowlist_EmptyAllowsNone` |

**Docs:** `docs/product/agent.md` - that `.mivia/skills/<name>/SKILL.md` is
discovered automatically, and that `description:` is what the agent uses to
choose between skills. This is the user-facing half of the feature: authors need
to know the description is a routing hint, not decoration.

## 9. Rollback criterion

If enumerating skills proves to crowd the tool schema (many skills, long
descriptions), cap the list and say what was dropped rather than silently
truncating - a partial list the model believes is complete is worse than no list.
Do not solve it by removing descriptions; they are the half that carries the
decision.
