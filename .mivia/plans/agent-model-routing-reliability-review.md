# Agent model routing and reliability review

Status: review note for later discussion; not active ADLC workflow state.

## Goal

Make agent execution reliable enough to assign different models to different
agents, while preserving cancellation, budgets, provider validation, and safe
parallel research fan-out.

## Current implementation

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
- Include provider, model, and model generation in the immutable agent digest.
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
