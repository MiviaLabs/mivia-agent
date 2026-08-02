# 51.05 - Tool-schema gating: stop paying for tools that cannot be used

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** nothing. Interacts with `04` (schema mass is one of its
calibration classes).
**Blast radius:** MEDIUM - changes what the model can call on a given turn.
Getting this wrong makes the agent unable to do its job, which is strictly
worse than the tokens it saves.

## 1. Goal

Send the model only the tool schemas it is permitted and likely to use on
this turn, instead of the entire registry on every request.

## 2. Verified baseline

- `EstimateRequestCost` marshals every `ToolSpec` and charges
  `schemaFrameTokens + estimateTokens(json)` for each, on every request
  (`internal/provider/context.go`).
- The registry exposes `List()` and every tool declares a `Capability` with
  a `Class` (`ExecutionRead` and siblings) and a `ResourceKey`
  (`internal/tools/tools.go`, `internal/tools/search.go:44`,
  `internal/tools/read.go:22`).
- Agent files already carry a `skills` allowlist enforced at dispatch and
  spawn (archived plan `06`), so per-agent capability restriction is an
  established pattern in this codebase - but it gates *authorization*, not
  what is sent to the model.
- Nothing anywhere subsets `[]ToolSpec` per turn.

## 3. The defect

Schema mass is paid on every single request of a session and is invariant to
what the turn is about. It is also the most cache-friendly part of the
prompt (it sits in the stable prefix), which cuts both ways: removing a
schema mid-session **breaks the prefix** and costs a cache reset. That
tension is what makes this plan MEDIUM and not LOW.

## 4. Design

### 4.1 Gate on authorization first, relevance second

Two mechanisms, and they are not equally safe:

**(a) Authorization gating - safe, deterministic.** If the resolved agent
cannot invoke a capability (agent-file allowlist, dispatcher policy,
read-only mode), its schema must not be sent at all. Sending a schema the
dispatcher will refuse is pure waste and actively harmful: it invites the
model to plan around a tool that will deny it. This gating is a pure
function of session-fixed authority, so it is computed **once at session
start** and never changes mid-session. No prefix churn.

**(b) Relevance gating - unsafe by default, opt-in only.** Narrowing the
tool set by inferred task phase saves more but can make the agent unable to
proceed, and it mutates the prefix mid-session. Any relevance mechanism
must be off by default and must be recoverable (§4.3).

This plan **commits to (a)** and specifies (b) only far enough to be
evaluated in Step 0.

### 4.2 Where the subset is computed

A `ToolSpecs(authority)` seam on the registry, returning the specs for
capabilities the given authority may actually invoke. The agent loop already
holds the resolved authority when it builds `toolSpecs` for `prepareStep`
(`internal/agent/context.go`), so the subset is computed exactly where the
full list is built today. The planner is unchanged - it prices whatever list
it is handed, which is the point.

### 4.3 Recovery for any future relevance gating

If (b) is ever built, a gated-out tool must remain reachable: the model
receives a single stable `list_capabilities`-style entry describing what is
available but not loaded, and asking for one re-admits it for the remainder
of the session (monotonic - never removed again). Monotonic re-admission
bounds prefix invalidation to at most one reset per tool per session.
Without this, relevance gating is a correctness hazard and should not ship.

## 5. Invariants

- **INV-CE-05-A.** A schema is sent if and only if the session's authority
  could successfully invoke it. No schema is sent for a capability the
  dispatcher would deny.
- **INV-CE-05-B.** The sent schema set is fixed for the lifetime of a
  session under authorization-only gating. Prefix stability (INV-CE-B) is
  preserved.
- **INV-CE-05-C.** If relevance gating is ever enabled, re-admission is
  monotonic: a tool admitted mid-session is never withdrawn.
- **INV-CE-05-D.** The subset is derived from the same authority data the
  dispatcher enforces with. Two independent copies of "what may this agent
  do" is the failure mode this invariant exists to forbid.

## 6. Open decisions for Step 0

1. Is there any authority state that can change mid-session (a hook, a
   resume, a `/agent` switch) that would break INV-CE-05-B? A `/agent`
   switch plausibly does. Decide whether a switch resets the session prefix
   anyway - if it does, the invariant holds trivially.
2. Should relevance gating be specified at all in this program, or split to
   its own plan once (a) has measured evidence?
3. Does an empty tool set (an agent authorized for nothing) need a distinct
   error, or is it a legitimate configuration?

## 7. Delivery slices

1. `ToolSpecs(authority)` on the registry, wired into the loop's spec
   construction. Authorization-only.
2. Telemetry: schema token mass sent per session, before and after.
3. (Conditional on evidence, and on Step 0 answering §6.2) relevance
   gating with monotonic re-admission.

## 8. Required tests

- An agent whose allowlist excludes a tool never receives its schema, and
  the dispatcher still denies it if called - both halves, so the two
  mechanisms cannot drift.
- A fully authorized agent receives exactly today's spec list.
- The spec list is byte-identical across every turn of a session.
- Cost: a gated session's `EstimateRequestCost` is lower by exactly the
  removed schemas' charge, framing included.

## 9. Out of scope

- Compressing or abbreviating individual schemas.
- Provider-side tool caching.
- Changing `Capability` semantics or the dispatcher's enforcement.
