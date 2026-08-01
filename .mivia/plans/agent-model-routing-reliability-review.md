# Agent model routing and reliability review

Status: phases 1-3 implemented; phase 4 partially implemented; phase 5 partial.
See "Implementation record" at the end of this file for what shipped, the
decisions taken, and what is deliberately still open.

## Goal

Make agent execution reliable enough to assign different models to different
agents, while preserving cancellation, budgets, provider validation, and safe
parallel research fan-out.

## Current implementation (as reviewed, before this plan was executed)

The bullets below describe the state that motivated the plan. Several are no
longer true; see "Implementation record".


- Agent definitions are TOML files under `.mivia/agents/` and are resolved into
  immutable agent snapshots with a digest.
- A routed task can select an agent and an agent-defined model. The model is
  currently validated against the active provider's model catalog.
- Routed work gets a fresh scoped multi-step handler, but the session still
  owns the provider completer. Model routing is therefore provider-local; an
  agent cannot independently select a different provider yet.
- Parallel agents are shared-pool tasks/goroutines, not process-per-agent
  workers. This is the right baseline for bounded fan-out and cancellation.
- `max_turns = 0` is the existing unlimited-turn sentinel. All seven workspace
  agent definitions now use it. Unlimited turns do not remove provider token,
  context, cancellation, timeout, or concurrency limits.

## Reliability assessment

### Existing strengths

- Immutable resolution prevents a running task from changing agent policy
  halfway through execution.
- Unknown or unavailable model selections fail closed rather than silently
  falling back.
- Scoped handlers, cancellation, fan-out limits, and race-tested shared pools
  provide useful isolation for parallel research.
- Parser and runtime tests already cover the `max_turns = 0` semantics.

### Main gaps to address

1. Provider and model identity should be one explicit binding. A model name
   alone is ambiguous when multiple providers expose the same name.
2. Each routed agent should receive a request-scoped completer bound to its
   resolved provider/model, rather than inheriting the session completer.
3. Agent policy should separate turn count from resource ceilings: per-agent
   token budget, context budget, wall-clock timeout, retry budget, and fan-out
   quota should remain bounded even when turns are unlimited.
4. Parallel research should return typed results with cancellation causes,
   partial-result policy, provenance, and deterministic aggregation. One slow
   or failed researcher must not strand the whole run.
5. Logs and events should expose resolved agent, provider, model, generation,
   attempt, and termination reason without recording prompts, model dumps, or
   sensitive payloads.

## Proposed implementation sequence

### Phase 1: explicit model binding

- Define a provider-qualified model reference in the agent configuration.
- Resolve and validate the complete binding at agent-load or dispatch time.
- Include provider and model in the immutable agent digest. Model generation
  stays on the routed work identity: it is a session-local counter, so
  digesting it would make the digest non-deterministic across sessions and
  would trip in-flight routing on any mid-run `/model` switch.
- Preserve fail-closed behavior for missing credentials, unsupported models,
  and stale catalog entries.

### Phase 2: provider-aware execution

- Add a factory that creates a request-scoped completer from the resolved
  binding.
- Keep session-level authentication and cancellation ownership, but prevent a
  routed agent from accidentally using the session's model.
- Ensure resume restores the resolved work configuration without granting new
  authority or bypassing current policy.

### Phase 3: bounded unlimited-turn execution

- Keep `max_turns = 0` as unlimited iterations.
- Add independent enforcement and reporting for token, context, timeout,
  retry, concurrency, and fan-out budgets.
- Make every budget exhaustion path cancellable, typed, and observable.

### Phase 4: reliable parallel research

- Define a bounded research task contract with a parent cancellation context.
- Run independent research agents concurrently within configured worker and
  fan-out limits.
- Aggregate successful results deterministically; preserve partial results
  when policy allows; return actionable errors for total failure.
- Use internet-capable agents only for questions requiring current or external
  evidence; local repository investigation remains the default.

### Phase 5: verification and operations

- Add parser, resolution, provider-selection, cancellation, timeout, budget,
  resume, and parallel-fan-out tests.
- Add race tests for shared pools and cancellation propagation.
- Add contract tests proving every routed request uses its resolved provider
  and model.
- Add observability assertions for identity and termination metadata, with
  redaction checks.
- Re-run project-native verification and the hostile bug-audit loop for each
  implementation slice.

## Decisions to make before implementation

- Configuration syntax: separate `provider` and `model` fields versus one
  provider-qualified model reference.
- Whether an agent may inherit the session model when no model is specified.
- Whether unavailable configured models fail immediately or can use an
  explicitly declared fallback.
- Default per-agent token, timeout, retry, and fan-out ceilings when turns are
  unlimited.
- Whether partial research results are returned by default or only when the
  caller opts in.

## Acceptance criteria

- Two agents in one run can use different provider/model bindings without
  cross-contamination.
- Invalid or unavailable bindings fail closed with actionable diagnostics.
- Unlimited turns do not create unbounded provider spend, context growth, or
  orphaned work.
- Parallel research remains cancellable, bounded, deterministic to aggregate,
  and honest about partial failure.
- Resume, audit events, logs, and tests preserve the selected agent/model
  identity without exposing sensitive content.

## Implementation record

Implemented on master over four commits, each verified with `make verify` and
`go test -race ./...`.

### Decisions taken (from "Decisions to make before implementation")

- **Configuration syntax**: separate `provider` and `model` keys, not one
  qualified `provider/model` string. OpenRouter model names already contain a
  slash (`openai/gpt-4o-mini`), so a qualified reference would be ambiguous.
- **Session inheritance**: an agent that names neither key follows the session
  binding, exactly as before. This keeps every existing definition unchanged.
- **Unavailable bindings**: fail closed immediately. There is no declared
  fallback, and an empty model catalog is not treated as authorization.
- **Default ceilings**: none are imposed by default. `timeout_seconds` and
  `max_tokens` are opt-in per agent and always resolve to the tighter of the
  agent's value and the operator's session cap.
- **Partial research results**: returned by default. The fan-out already
  reports every task independently; the caller opts out by ignoring them.

### Shipped

- Phase 1: provider-qualified binding, inherited as one unit, validated after
  inheritance, digest-stable for definitions that do not use it.
- Phase 2: request-scoped completer per routed agent, per-model context budget,
  fail-closed catalog validation, factory wired at all production sites.
- Phase 3: `timeout_seconds` and `max_tokens` ceilings independent of
  `max_turns`, with a typed, observable exhaustion cause.
- Phase 4: typed termination reasons and agent provenance on every aggregated
  fan-out result; deterministic ordering and partial-result preservation pinned
  by tests (both were already true by construction and are now regression-proof).

### Accepted risk: workspace definitions may select a provider

A workspace provider gate was implemented and then **removed at the operator's
explicit direction**, so that this repository's own roster in `.mivia/agents/`
could split across providers.

The residual risk is recorded here deliberately. Unlike a model name, a
provider name is not session-local, so a checked-out repository can ship an
agent definition that routes the operator's prompts, tool results, and file
contents to a different vendor's endpoint, authenticated with the operator's
own credentials. Running `mivia` inside an untrusted repository now carries
that exposure.

What still contains it:

- The provider must be configured in the operator's own config and must hold a
  credential there. `provider.NewForProvider` fails closed on an unconfigured
  provider and on a missing key, so a workspace file can only select among
  endpoints the operator has already set up.
- The provider must name a built-in descriptor; arbitrary endpoints cannot be
  introduced from an agent file.
- The (provider, model) pair must be selectable in the operator's catalog.

If this is revisited, the shape to restore is a trusted opt-in in the `[agents]`
section of the user config (`~/.mivia/mivia.toml`), which workspace config
already cannot influence - not a blanket source check.

### Deliberately still open

- **Per-agent retry and fan-out quotas** (Phase 3). These remain on the global
  `[subagents]` knobs (`max_workers`, `max_fanout`, retry policy). Making them
  per-agent requires threading agent policy into the coordinator's retry state
  and the dispatch tools' fan-out accounting.
- **Per-model output-token ceilings** (Phase 3). `config.ModelSpec` carries only
  `ContextWindowTokens`, so a routed model with a lower output cap than the
  session's will still be discovered at request time.
- **Resume does not pin a session-inherited binding** (Phase 2). An agent with
  an explicit provider/model resumes onto the same pair because the pair is part
  of its digest. An agent that follows the session resumes onto whatever the
  session's binding is at resume time. Pinning that would mean persisting the
  resolved provider/model as work metadata on `ledger.TaskSnapshot`.
- **Internet-capable agent routing** (Phase 4). The plan's rule that
  internet-capable agents are used only for questions requiring external
  evidence remains a prompt-level convention in the agent definitions, not an
  enforced routing constraint.
