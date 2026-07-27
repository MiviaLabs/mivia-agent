# Subagent orchestration and live ledger

Status: Validated draft — independent challenge reconciled; implementation pending
Current phase: Phase 0 — contract and architecture validation
Last verified: 2026-07-28
Next action: Human review of the async contract and live-vs-durable boundary, then implement Phase 1 only.

## Objective

Refactor Mivia’s subagent execution into an extensible orchestration boundary with one clear model-facing spawn tool, unique human-readable agent names, explicit parent/child identity, dependency-aware DAG execution, and a live queryable ledger that the root orchestrator can use to understand current application state.

The first delivery is local/in-process and bounded. It must preserve the existing no-process-farm rule, model/tool genericity, context cancellation, shared resource caps, and protected-action ownership by the root orchestrator.

## Evidence ledger

### Confirmed current behavior

- `internal/cli/delegate.go`: `delegate` creates a new `subagents.Pool` for one task, uses a fixed task ID (`d1`), and assigns the static owner `mivia`; it has no user-visible stable agent name or run identity. Transient runtime IDs already exist in `runtime.Request`/`runtime.Metadata`, but they are not a durable or queryable agent identity contract.
- `internal/cli/dispatch.go`: `dispatch_tasks` accepts a batch of prompts and `depends_on`, defaults to `multi_step`, and creates task IDs from caller input. It returns results only after the batch completes; no handle exists for later status, cancellation, resume, or inspection.
- `internal/subagents/subagents.go`: `Pool` validates fan-out/depth/budget, schedules dependency-ready batches, runs bounded goroutines, and detects cycles, but all run state is local to one `Run` call and results are not persisted or queryable while running.
- `internal/subagents/multi_step.go`: multi-step agents run an embedded `agent.Loop` with delegation tools removed from the copied registry. Heartbeats are emitted to an optional callback, but the state is not a durable ledger and nested delegation is intentionally blocked.
- `internal/runtime/dispatcher.go`: `Dispatcher` has transient `active`, `completed`, waiters, fingerprints, budgets, and resource maps. `Close` clears retained state; metadata is redacted and emitted through a sink, but there is no run/task query API or durable orchestration ledger.
- `internal/cli/dispatcher.go`: the session wires handlers named `delegate`, `oneshot`, and `multi_step`, then registers `delegate` and `dispatch_tasks` as model-visible tools. The root event callback can surface nested events to the TUI.
- `internal/config/types.go` and `mivia.toml.example`: subagent policy has worker, depth, fan-out, timeout, budget, partial-results, prompt, and nested-step settings, but no ledger store, naming policy, run retention, or orchestration mode. `mivia.toml` is not tracked in this checkout.
- `internal/storage/store.go` and `internal/storage/queue.go`: a storage seam already exists with memory and SQLite/WAL implementations, duplicate event protection, reopen/backup-oriented tests, and a bounded queued writer. It is not wired to runtime lifecycle events and is not yet a task/run ledger or projection.
- `internal/agent/emit.go`, `internal/events`, and `internal/cli/subagent_progress.go`: transient event publication exists for the agent loop/TUI, but it is not a durable, queryable orchestration state source.
- `docs/architecture/concurrency.md` and `.ai/rules/50-concurrency-subagents.md`: the intended model is bounded in-process tasks with shared pools, cancellation trees, heartbeats, deterministic joins, and no process farm.
- `docs/architecture/embedded-persistence.md`: the repository already recommends an append-oriented SQLite-backed event model for sessions, runs, events, artifacts, and rebuildable projections, while explicitly stating JSONL is not a concurrent lifecycle source of truth.
- Existing subagent tests cover dependency order, cycle rejection, invocation-key collisions, partial failure, timeout, cancellation, real-tool integration, and event forwarding. They do not prove unique human-readable identity, live inspection, restart/resume, or durable DAG recovery.

### Confirmed external patterns

- Temporal uses durable workflow history/event sourcing, deterministic workflow logic, idempotent or non-retryable activities, task queues, and query/signal boundaries. Sources: [Temporal documentation](https://docs.temporal.io/) and [Temporal architecture](https://github.com/temporalio/temporal/blob/main/docs/architecture/README.md).
- AWS Step Functions models workflows as state machines with explicit Parallel/Map states, retries, catch/fallback transitions, and child execution history for distributed maps. Sources: [state machines](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-statemachines.html), [error handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html), and [Distributed Map](https://docs.aws.amazon.com/step-functions/latest/dg/state-map-distributed.html).
- LangGraph separates thread-scoped checkpoints from long-term stores, exposes resumable interrupts through a persistent checkpointer, and warns that replayed nodes require idempotent side effects. Sources: [persistence](https://docs.langchain.com/oss/python/langgraph/persistence), [interrupts](https://docs.langchain.com/oss/python/langgraph/interrupts), and [durability/idempotency](https://docs.langchain.com/oss/python/langgraph/durable-execution).
- OpenTelemetry is an observability/correlation reference only: propagate context across task boundaries and avoid high-cardinality identifiers in metric labels. Sources: [context propagation](https://opentelemetry.io/docs/concepts/context-propagation/) and [metrics](https://opentelemetry.io/docs/concepts/signals/metrics/).

### Unverified / must not be assumed

- Native Mivia MCP project/workflow/agent-run tools were not mounted in this session; their availability and exact contract remain unverified.
- No benchmark proves the current or proposed concurrency limits, provider throughput, SQLite write capacity, or crash-recovery latency. Existing storage benchmarks/validation tests are useful evidence but are not proof of orchestration durability or capacity.
- The current repository does not prove that an interrupted multi-step LLM/tool call can resume from a checkpoint; that requires an implementation and failure-injection tests.
- External workflow engines are design references only. This plan does not authorize adding Temporal, LangGraph, or another runtime dependency.

## Design direction

Introduce a single extensible `spawn_agent` model-facing boundary, while keeping a compatibility adapter for existing `delegate` and `dispatch_tasks` during migration. The spawn request should carry a run-scoped task identity, optional human-readable name/role, parent ID, prompt/input reference, handler/profile, dependencies, policy overrides, idempotency key, and an explicit wait mode. The system, not the model, allocates the final unique display name and immutable ID; caller-supplied names are sanitized and collision-resolved. The minimum identity tuple is `RunID`, `TaskID`, `AttemptID`, `ParentTaskID`, and presentation name.

Use a run coordinator over a validated immutable DAG definition and a ledger boundary. Distinguish two guarantees: an ephemeral live ledger supports in-process inspect/cancel/join; a durable ledger supports restart/recovery only after persistence and replay contracts pass. The durable ledger records run/task lifecycle transitions, parentage, dependencies, attempts, cancellation, bounded heartbeat/lease metadata, error category, redacted evidence references, and output/artifact references. A current-state projection supports fast live reads; the event stream remains the audit/recovery source. The root orchestrator gets read-only status/list/query operations and owns cancellation, approval, and protected writes.

Keep execution separate from identity, scheduling, persistence, and presentation:

```text
spawn_agent(wait=none|task|run) / inspect_agents / join_run / cancel_run
        -> orchestration API
        -> validated DAG + coordinator
        -> bounded worker pool / handler registry
        -> append-only ledger + current projection
        -> TUI, future API, recovery/resume
```

Do not expose raw prompts, provider payloads, hidden reasoning, secrets, or unbounded tool output through the ledger. Persist hashes, redacted summaries, typed status, safe refs, and bounded diagnostics only.

## Scope

- Define stable run, agent, task, dependency, attempt, status, event, and artifact-reference contracts.
- Add a unique human-readable naming allocator with deterministic collision handling and a machine ID separate from display name.
- Extract orchestration from tool adapters so one coordinator powers single spawn, batch fan-out, and DAG execution.
- Add live ledger writes and read APIs with bounded status snapshots and heartbeat/lease semantics.
- Add cancellation, timeout, retry classification, partial-result, blocked-dependency, and cycle/error transitions.
- Add a compatibility layer for `delegate` and `dispatch_tasks`, with deprecation telemetry or documentation and no silent behavior change.
- Define a first local persistence backend behind an interface, using the existing SQLite recommendation only after benchmark and privacy/recovery gates pass.
- Add contract, integration, race, failure-injection, redaction, and restart/resume tests.

## Non-goals

- No process-per-agent or unbounded nested agent trees.
- No external workflow engine or distributed service in the first slice.
- No automatic code mutation, commit, push, PR, or protected action by child agents.
- No raw prompt/transcript/hidden-reasoning persistence.
- No claim of crash recovery, multi-process durability, or scale capacity until tests and benchmarks prove it.
- No removal of compatibility tools until callers, prompts, tests, and migration telemetry are updated.

## Proposed phases

### Phase 0 — contracts and evidence gate

Read first: `AGENTS.md`, `.ai/INDEX.md`, `.ai/rules/10-security-privacy.md`, `.ai/rules/20-agent-quality.md`, `.ai/rules/50-concurrency-subagents.md`, `.ai/skills/concurrency-review/SKILL.md`, `internal/cli/{delegate,dispatch,dispatcher}.go`, `internal/subagents/*.go`, `internal/runtime/dispatcher.go`, and `docs/architecture/embedded-persistence.md`.

Deliver: a reviewed contract table and state machine before code. The primary contract must support an asynchronous run handle (`RunID`/task IDs returned immediately), read-only `inspect`/`join`, and state-changing `cancel`; synchronous wait is an explicit option over the same run. Decide whether Phase 1 is explicitly ephemeral live inspection and Phase 3 is durable execution; do not call an in-memory ledger crash-recoverable. Define status vocabulary, terminal vs retryable errors, retry/backoff, cancellation precedence, heartbeat sequence, DAG immutability/version hash, output/artifact propagation across dependency edges, naming rules, ownership/authorization, and compatibility behavior.

Acceptance: no unresolved ambiguity about the first queryable state, identity semantics, async/sync behavior, DAG edge semantics, or restart boundary; all claims tied to source/tests or explicitly marked open.

### Phase 1 — identity, ledger, and coordinator seams

Likely files: new package under `internal/orchestration` or `internal/subagents`, `internal/runtime` metadata/events, config types/defaults, and focused tests. Exact package placement is pending Phase 0 ownership review.

Implement immutable run/task records, a naming allocator, ledger interface, in-memory implementation for deterministic tests, coordinator interface, and event-to-projection reducer. Make all writes bounded/redacted and all reads snapshot-based. Keep the existing pool behavior available behind the coordinator. The Phase 1 vertical slice must expose async spawn, inspect while running, join after completion, and cancel with late-completion guards; it must not claim restart recovery. Separate `cancel_requested` from terminal `canceled`, and use monotonic event/heartbeat sequence values.

Acceptance: unique stable names under concurrent allocation; parent/child lineage; deterministic DAG validation and output-reference rules; live status transitions; cancellation and heartbeat tests; race suite passes for the package; no raw sensitive content in persisted/event output. Add a test proving an expired/stale in-process attempt cannot publish a terminal result after cancellation or replacement.

### Phase 2 — unified spawn and compatibility adapters

Likely files: `internal/cli/delegate.go`, `internal/cli/dispatch.go`, tool schemas/tests, dispatcher wiring, and prompt/tool documentation.

Implement `spawn_agent` over the coordinator. Support one task and a validated immutable DAG through one extensible request shape. Return a server-issued run handle by default; support explicit synchronous wait for compatibility. Add read-only `inspect_agents`/`join_run` and state-changing `cancel_run` capabilities for the root orchestrator. Route legacy tools through the same coordinator and preserve their result shape during migration. Do not treat a caller-provided parent ID as authorization; bind parentage to the active root run context.

Acceptance: root agent can spawn, observe, cancel, and join named children; legacy calls produce the same behavior plus ledger identity; children cannot recursively create unbounded delegation; tool descriptions remain project/language-generic.

### Phase 3 — durable persistence and recovery

Likely files: storage implementation, schema/migrations, config, persistence tests, and existing embedded-persistence documentation.

Extend/reuse `internal/storage.Store` and `internal/storage.QueuedWriter` rather than introducing a second persistence seam. Align Memory and SQLite ordering/duplicate semantics before using Memory as a contract oracle. Add the selected backend only after workload, crash, disk-full, duplicate append, ordering, retention, redaction, and backup/restore tests. Persist append-only lifecycle events and rebuildable projections. Add durable attempt/idempotency records, compare-and-set terminal transitions, and leases only when multi-process/orphan recovery is in scope. Resume only from durable checkpoints/step boundaries that are proven safe; hashes/redacted summaries alone cannot reconstruct an interrupted LLM/tool call. Otherwise fail closed as `interrupted_unrecoverable`.

Acceptance: kill/restart recovery tests show exactly which tasks are resumed, skipped from durable results, retried, or terminally blocked. No duplicate side effects under retry; no false `completed` status.

### Phase 4 — observability and scale gates

Add TUI/live views, bounded metrics/traces, operator diagnostics, and benchmark fixtures. Use OpenTelemetry only for correlation/observability, not as the ledger or recovery source; propagate context explicitly and avoid high-cardinality run/task IDs as metric labels. Expose counts and timings without PII or prompt leakage. Measure provider calls, queue wait, active children, retries, ledger write latency, and recovery time under configured caps.

Acceptance: evidence-backed capacity table, race and stress results, and documented limits. Any unavailable live/MCP/distributed proof remains `NOT_RUN`.

## Verification plan

Run the narrowest checks first, then broader gates:

1. `go test ./internal/subagents ./internal/runtime ./internal/cli -count=1`
2. Contract tests for name uniqueness, DAG validation, status transitions, dependency blocking, idempotency, cancellation, timeout, retry classification, redaction, and compatibility output.
3. Integration tests with scripted providers for live spawn/inspect/cancel/join and root/child lineage.
4. Failure-injection tests for process interruption, late completion, duplicate event append, stale lease, restart, and partial DAG completion.
5. `go test -race ./internal/...` or the narrowest concurrency package equivalent.
6. `make verify`, `make test`, `make race`, `make build`, and `git diff --check` when implementation changes land.
7. Benchmark representative fan-out and ledger workloads; report measured throughput, p95 latency, memory, WAL/database growth, and recovery time. Do not infer capacity from configuration values.

## Security, privacy, and operational constraints

- Treat prompts, tool inputs/outputs, provider metadata, workspace paths, and agent findings as potentially sensitive. Store only redacted bounded summaries and hashes where possible.
- Enforce authorization at the coordinator/ledger boundary for inspect, cancel, resume, and protected actions; do not rely on prompt instructions.
- Use immutable IDs for joins and authorization; display names are presentation-only.
- Make retries explicit by error class and require idempotency keys for any durable side effect.
- Use context cancellation, bounded worker/resource semaphores, heartbeats, leases, and deterministic joins.
- Define retention and deletion behavior before enabling persistent history. Human security/privacy review is required for any ledger that stores workspace or user data.
- Migration must be additive and rollbackable; legacy tools remain available until the new contract is proven.

## Open questions / human review

- Is the desired first release asynchronous (returns a run handle) or synchronous with optional inspection during execution? The implementation can support both, but the primary model-visible contract should be chosen before Phase 1.
- Should human-readable names be model-suggested labels, generated role names, or both? Recommendation: generated names are authoritative; model labels are optional aliases.
- Which storage backend and data root are approved for local deployments? SQLite is a conditional recommendation, not yet a measured decision.
- Does the first release promise only in-process live inspection, or durable restart/recovery? Recommendation: promise live inspection first; gate durable recovery separately.
- What operator/API surface should expose the ledger beyond the root orchestrator and TUI?
- Product/security review is required for retention, workspace-path exposure, cancellation authority, restart/resume semantics, and any future multi-process backend.

## Independent validation reconciled

- Confirmed: current storage and event seams exist but are not integrated into a queryable orchestration ledger; the plan was corrected to reuse them.
- Confirmed: current `delegate`/`dispatch_tasks` calls are synchronous and return no live run handle; async spawn/inspect/join/cancel is now a Phase 0 contract gate.
- Confirmed: current dependency edges gate scheduling but do not define predecessor output propagation; typed output/artifact-reference semantics are now required before DAG implementation.
- Confirmed: retry/idempotency and stale-attempt fencing were underspecified; durable effect keys/CAS terminal transitions and explicit failure-injection tests are now required.
- Confirmed: leases are deferred from the strictly in-process Phase 1; they are required only if durable multi-process/orphan recovery is actually approved.
- NOT_RUN: native Mivia MCP contracts, provider throughput, live spawn/inspect/cancel/join, restart/recovery, multi-process durability, and OpenTelemetry export/cardinality behavior.
- Validation command status: current checkout `wsl.exe -d Ubuntu -- bash -lc 'cd /home/mac/mivialabs/mivia-agent && go test ./internal/subagents ./internal/runtime ./internal/cli -count=1'` passed. This is not a clean base-branch baseline because the worktree has concurrent unrelated edits; rerun after scope isolation before implementation. Do not treat subagent test status as proof of the base repository.

## Decision gate

Do not begin implementation until independent validation has challenged: (1) whether current code already provides a hidden live ledger, (2) whether a single `spawn_agent` tool can safely represent both one-shot and DAG work, (3) whether durable persistence is required for the requested “live ledger,” (4) whether compatibility can preserve current model behavior, and (5) whether the proposed recovery semantics are testable without claiming unsupported durability.
