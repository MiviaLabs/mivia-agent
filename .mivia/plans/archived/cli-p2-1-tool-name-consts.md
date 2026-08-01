# P2.1 - Centralize tool/handler-name string literals

**Status:** DONE - implemented and archived on master. REFACTOR, TDD-preserving, behavior-preserving.
**Date:** 2026-07-31
**Depends on:** nothing. (Listed in the review's suggested execution order as Wave 4,
*after* P1.5 dead-code, P1.1 theme, and P1.3/P1.4 orchestration helpers land. Those
waves shrink the touched surface but are not hard prerequisites: this plan is
self-contained and touches a distinct set of files.)
**Blocks:** P2.3 (constructor collapse) loosely - touching `dispatcher.go` for the
`delegate`/`oneshot` register calls benefits from the consts existing first. Nothing
else.
**Blast radius:** LOW - pure identifier-for-literal substitution plus one new
declarative file. No type changes, no API changes, no behavioral change. The risk is
mistyped-equivalence (a literal that *looks* like one of these names but is not), so
verification leans on compile + grep residuals + mutation tests.

---

## Problem

The set `{multi_step, delegate, oneshot, dispatch_tasks, spawn_agent, join_run,
inspect_agents, cancel_run}` - the names of the built-in handlers and the
agent-control tools - is re-declared as raw string literals across roughly ten files
in `internal/cli`, with no shared constants. Verified at HEAD:

| File:line | Form |
|---|---|
| `action.go:19` | `agentControlTools` map literal - the full 8-name set |
| `orchestrate.go:167` | `enumValues := []string{"multi_step","delegate","oneshot"}` + inject |
| `dispatch.go:143` | `enumValues := []string{"multi_step","delegate","oneshot"}` + inject (**byte-for-byte duplicate of orchestrate.go**) |
| `model_binding.go:69` | `reservedSkillNames()` - the `{delegate,oneshot,multi_step}` subset |
| `dispatcher.go:168` | `d.Register(runtime.Subagent, "delegate", …)` / `"oneshot"` |
| `tool_verbs.go:37` | `switch` cases: `"delegate"`, `"dispatch_tasks"` |
| `toolui.go:164` | `switch` cases: `"delegate", "dispatch_tasks"` |
| `toolui_agent.go:12` | `switch name { case "delegate": … case "dispatch_tasks": }` |

The two enum-injection blocks (`orchestrate.go:167`, `dispatch.go:143`) are identical
*except* for the property they navigate to (`"name"` vs `"handler"`) and the source map
they walk. Both build the list, both append registered skill names, both drill into the
same deeply-nested `result["properties"]["tasks"]["items"]["properties"][…]["enum"]`
path. One typo in either - a dropped `"multi_step"`, a reordered element - silently
changes which handler names the model may emit, with no test catching the drift today.

### Scope boundary - what is NOT a Go identifier here

`prompt.go` is cited in the review but the eight matches there are **prompt-template
prose**, not Go references: they are natural-language instructions such as
`Dispatch 2-4 hostile reviews via dispatch_tasks (handler:"multi_step")` embedded in
the strings handed to sub-agents. Substituting a Go constant into a prose template
would be wrong (the value is documentation, not a dispatch key) and would couple prompt
text to identifiers. **`prompt.go` is explicitly out of scope** (see Non-goals).

Likewise the capitalized `ChatBlock.ToolName` struct field (`chatblock.go:103`) and the
`toolNameStyle` lipgloss variable (`toolui.go:213`) are unrelated identifiers that
happen to share a substring - they are not touched.

## Goals and non-goals

### Goals

- Declare the 8 built-in handler/tool names once as unexported `const` strings in a new
  `tool_names.go`.
- Expose a `builtinHandlerNames` slice capturing the ordered `{multi_step, delegate,
  oneshot}` enum list, so the two duplicated `[]string{...}` literals become one
  declaration.
- Provide an `injectHandlerEnum(result, propName)` helper that performs the
  build-list → append-skills → drill-and-set drill currently duplicated in
  `orchestrate.go` and `dispatch.go`.
- Replace the raw literals at every verified call site with the constants / the helper.
- Preserve behavior exactly: same strings reach the same consumers, the JSON schema
  enum is byte-identical, the `agentControlTools` membership is unchanged.

### Non-goals

- Do **not** touch `prompt.go` - its literals are prompt-template prose, not dispatch
  keys (see Problem).
- Do not introduce a registry/enum *type* (e.g. a typed `HandlerName` enum or a
  `stringer`). The names cross the tool-schema JSON boundary as plain strings; a type
  would force conversions at every marshal site for no behavior gain. Stay `string`.
- Do not rename any tool or handler. The wire names are a public, model-facing
  contract.
- Do not change the ordering semantics of the enum list beyond what already exists.
- Do not fold in the P2.6 status-constant work or the P2.3 constructor collapse -
  separate plans.

## Approach

New file `internal/cli/tool_names.go` (package `cli`), declarative + one helper:

```go
// tool_names.go - the wire names of built-in handlers and agent-control tools.
// These strings cross the model/tool-schema JSON boundary as plain values; they are
// intentionally untyped string consts, not a typed enum, to avoid marshal conversions.

// Built-in handler names registered with runtime.Subagent.
const (
	handlerMultiStep    = "multi_step"
	handlerDelegate     = "delegate"
	handlerOneshot      = "oneshot"
)

// Agent-control tool names (the surfaces that launch/control another agent).
// `agentControlTools` (action.go) is the membership source of truth for this set.
const (
	toolDispatchTasks = "dispatch_tasks"
	toolSpawnAgent    = "spawn_agent"
	toolJoinRun       = "join_run"
	toolInspectAgents = "inspect_agents"
	toolCancelRun     = "cancel_run"
)

// builtinHandlerNames is the ordered enum advertised in the dispatch_tasks /
// orchestrate schemas before registered skill names are appended. Order is part of
// the model-facing contract - do not reorder.
var builtinHandlerNames = []string{
	handlerMultiStep, handlerDelegate, handlerOneshot,
}
```

Plus the extracted helper, which collapses the two duplicated blocks. Today both sites
build the list, append `skillReg.ListModelFacing(nil)` names, then drill into
`result["properties"]["tasks"]["items"]["properties"][<prop>]["enum"]`. The only
varying input is the property name:

```go
// injectHandlerEnum writes the built-in-handler + registered-skill name enum into the
// <prop> property of the tasks[].items schema map. It is the single implementation of
// the logic previously duplicated in orchestrate.go (prop "name") and dispatch.go
// (prop "handler").
func injectHandlerEnum(result map[string]any, prop string, skillReg SkillRegistryLike) {
	enumValues := make([]string, len(builtinHandlerNames))
	copy(enumValues, builtinHandlerNames)
	if skillReg != nil {
		for _, info := range skillReg.ListModelFacing(nil) {
			enumValues = append(enumValues, info.Name)
		}
	}
	props := result["properties"].(map[string]any)
	tasks := props["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	target := itemProps[prop].(map[string]any)
	target["enum"] = enumValues
}
```

`SkillRegistryLike` (or whichever narrow interface the two call sites already see) is
read off the actual `t.skillReg` type during implementation; if no interface exists,
the helper takes the concrete `*skills.Registry` the sites already use. No new
abstraction is invented - the helper signature mirrors the existing call shape.

### Call-site rewrites (identifier-for-literal only)

- `action.go:19` - `agentControlTools` map keys → the 8 consts.
- `orchestrate.go:167` - replace the inline block with `injectHandlerEnum(result, "name", t.skillReg)`.
- `dispatch.go:143` - replace the inline block with `injectHandlerEnum(result, "handler", t.skillReg)`.
- `model_binding.go:69` - `reservedSkillNames()` keys → `handlerDelegate`/`handlerOneshot`/`handlerMultiStep`.
- `dispatcher.go:168` - the two `d.Register(..., "delegate"|"oneshot", …)` calls → consts.
- `tool_verbs.go:37` - switch cases → `toolDispatchTasks` (and `handlerDelegate` if the `delegate` case is in the same switch).
- `toolui.go:164` - switch cases → consts.
- `toolui_agent.go:12` - switch cases → consts.

`agentControlTools` (action.go) becomes the named source of truth for the agent-control
set; if a reviewer prefers, the eight membership checks scattered across `tool_verbs`/
`toolui`/`toolui_agent` can later be routed through it, but that membership-collapse is
**not** in this plan's goals (it risks behavior change at the glyph/verb layer). Here
we only swap literals for consts at those switches.

## Implementation waves

Every production task is preceded by a compiling RED test that fails an assertion
before the implementation is added (ADLC Step 4). 1 file per task; a task creates OR
modifies one file, never both.

| Wave | Task | File | Type | Proof |
|---|---|---|---|---|
| 0 | Challenge | (context) | review | Architecture + correctness reviewers disposition every finding; scorecard (compile, no cycles, no breaking API, testable in isolation, backward-compatible, every fn has a test) all PASS. Confirm `SkillRegistryLike`/concrete type and that no call site relies on the enum slice being a *fresh* allocation (copy in helper guards aliasing). |
| 1 | t1a | `tool_names_test.go` (new) | RED test | Asserts the 8 consts equal their literal strings; `builtinHandlerNames` equals `[]string{"multi_step","delegate","oneshot"}`; `injectHandlerEnum` writes the enum into a constructed schema map for prop `"name"` and `"handler"`, with and without a fake skill registry. Fails (symbols undefined). |
| 1 | t1b | `tool_names.go` (new) | GREEN | Declares consts, slice, helper. `t1a` passes. |
| 2 | t2a | `orchestrate_test.go` (or new `orchestrate_enum_test.go`) | RED test | Asserts the produced schema's `tasks[].items.properties.name.enum[0:3]` equals `builtinHandlerNames` and includes a registered skill name. Captures current byte output as the golden before refactor. |
| 2 | t2b | `orchestrate.go` | GREEN | Inline block → `injectHandlerEnum(result, "name", t.skillReg)`. `t2a` passes. |
| 3 | t3a | `dispatch_test.go` (or `dispatch_enum_test.go`) | RED test | Same as t2a but for `handler` prop. |
| 3 | t3b | `dispatch.go` | GREEN | Inline block → `injectHandlerEnum(result, "handler", t.skillReg)`. |
| 4 | t4a–t4g | `action.go`, `model_binding.go`, `dispatcher.go`, `tool_verbs.go`, `toolui.go`, `toolui_agent.go` | GREEN (mechanical) | Literal→const swaps. These are identifier-for-literal and compile-checkable; each lands with a `go build` gate. No new test needed per site - the existing switch/map tests (e.g. `action_test.go:46-49` already asserts `"delegate"` membership) pin behavior, and a grep residual check (below) proves completeness. |
| 5 | review | (context) | review | Reviewer reads all changed files + `tool_names.go`, confirms no residual literals and no behavior change. |

Wave 4's mechanical swaps are grouped because they carry zero new logic and are each
1–3 lines; they can dispatch as parallel `dispatch_tasks` within the wave. Wave 0's
copy-aliasing check is the one correctness subtlety: today each site builds a fresh
`[]string{...}`, and a careless shared slice could let a later `append` mutate the
shared `builtinHandlerNames`. The helper's `copy(...)` makes that impossible - this is
pinned by the RED test that appends to the returned/observed slice and asserts
`builtinHandlerNames` is unchanged.

## Verification

Minimum gates (ADLC Step 6):

```text
go build ./...
go vet ./...
go test ./internal/cli -count=1
go test -race ./internal/cli -count=1
```

Behavior-preservation proof:

- **Schema byte-equivalence.** A test snapshots the JSON of the dispatch_tasks and
  orchestrate tool schemas before and after the refactor and asserts byte-identical
  output (the existing schema tests already pin the enum; extend to assert exact bytes
  if they currently only check membership).
- **No residual literals.** After the refactor, this grep must return only
  `prompt.go` (prose, out of scope) and `tool_names.go` (the declarations):
  ```text
  grep -rn '"multi_step"\|"delegate"\|"oneshot"\|"dispatch_tasks"\|"spawn_agent"\|"join_run"\|"inspect_agents"\|"cancel_run"' internal/cli
  ```
  Every other hit is a missed site or a bug.
- **Mutation tests.** Each const's value is the wire contract; the RED tests assert
  exact-string equality, so changing a const value (e.g. a typo `"mutli_step"`) fails
  a test before it reaches the model.

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Typo a const value (`"mutli_step"`) | `tool_names_test.go` const-equality assertions |
| M2 | Reorder `builtinHandlerNames` | `tool_names_test.go` order assertion + t2a/t3a |
| M3 | Drop the `copy()` and append to the shared slice | the aliasing-safety test |
| M4 | Inject enum into wrong property (`"name"`↔`"handler"`) | t2a / t3a |
| M5 | Remove a name from `agentControlTools` | existing `action_test.go` membership assertions |

## Rollback

This change is behavior-preserving and additive (one new file) plus mechanical
literal→const swaps. Rollback is `git revert` of the commit(s); no data, config, or
storage migration is involved and there is nothing to disable at runtime.

The rollback criterion that would kill the plan (not just unwind it): if the schema
byte-equivalence test reveals that the two duplicated blocks were *not* in fact
identical in some load-bearing way the review missed (e.g. one navigates a different
schema shape under a feature flag), the `injectHandlerEnum` collapse is wrong and the
plan returns to Step 0 - keep the per-site inline blocks but still extract the consts.
A literal-substitution that breaks a test is a quick fix (<5 lines); a collapsed helper
that breaks a test is a plan flaw → Step 0.
