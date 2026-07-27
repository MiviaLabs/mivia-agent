# Tool and Skill Execution Before Subagents

## Decision

Improve single-agent tool and skill execution before subagents. Subagents multiply concurrency, state, failure, and observability risks.

## Sequence

tools -> skills -> execution contracts -> subagents

## Phase 1: Tools

- Validate tool names and JSON arguments.
- Add timeouts and cancellation.
- Replace unlimited goroutines with bounded concurrency.
- Classify read-only, mutating, and external-effect tools.
- Run independent reads concurrently; serialize conflicts.
- Preserve deterministic result ordering.
- Enforce result, time, and batch budgets.
- Keep logs free of secrets, prompts, and sensitive data.

Tests: validation, cancellation, caps, conflict handling, ordering, errors, truncation, and race tests.

## Phase 2: Skills

- Define discovery, selection, input, output, and failure contracts.
- Prevent recursive self-invocation and skill/tool loops.
- Enforce scope, permissions, timeouts, and budgets.
- Make nested calls cancellation-aware.
- Record bounded, redacted metadata.
- Reject conflicting skills.

Tests: selection, invalid inputs, nesting, cancellation, cycles, permissions, timeouts, retries, and redaction.

## Phase 3: Subagents

Start only after Phases 1 and 2 pass.

- Add a bounded worker pool.
- Isolate context, budget, cancellation, and ownership per subagent.
- Schedule independent tasks concurrently and dependent tasks in order.
- Prevent duplicate work and conflicting mutations.
- Aggregate typed results with provenance and status.
- Add depth, cost, timeout, and concurrency limits.

Tests: worker bounds, parent cancellation, failures, dependencies, duplicates, conflicts, recursion, deterministic aggregation, stress, and race behavior.

## Acceptance gates

Run focused tests first, then go test ./... -count=1, go test -race ./... -count=1, make verify, make build, and make secret-scan.

No subagents while tool or skill execution has unbounded concurrency, undefined cancellation, unsafe conflicting writes, or unverified recursion.

## Research updates

Current source confirms the executor creates one goroutine per model tool call and has no active-call limit or per-call deadline. Registry dispatch also lacks capability metadata, conflict keys, and schema validation. Existing tests cover parallel execution and cancellation, but not bounded concurrency, conflicting workspace operations, per-call deadlines, or retry classification.

Upstream guidance confirms that Go errgroup provides shared cancellation plus SetLimit and TryGo for bounded active goroutines: https://pkg.go.dev/golang.org/x/sync/errgroup

Go cancellation guidance requires context propagation and cancellation-aware operations: https://go.dev/doc/database/cancel-operations

Bubble Tea examples support background work through commands and messages, preserving UI state ownership in the update path: https://github.com/charmbracelet/bubbletea/tree/main/examples

Bubbles already supplies reusable viewport, list, table, textarea, textinput, spinner, paginator, and filepicker components: https://github.com/charmbracelet/bubbles

Bubbles v2 is available, but this repository is pinned to v1. Keep migration separate until behavior is stable: https://github.com/charmbracelet/bubbles/releases

## Revised implementation order

Phase 0: establish contracts and collect p50/p95 latency, queue wait, active goroutines, cancellation latency, memory growth, and TUI update latency for batches of 1, 2, 4, 8, and 16 calls.

Phase 1A: add tool capability metadata, argument validation, bounded execution, per-call deadlines, conflict scheduling, deterministic results, and structured redacted events. Keep the existing loop seam and add focused plus race tests.

Phase 1B: benchmark errgroup.SetLimit against a small worker pool. Select based on measured latency, cancellation, memory, and failure behavior; do not add a dependency by assumption.

Phase 2: define skill identity, schemas, permissions, budgets, invocation stack, cycle detection, duplicate suppression, cancellation, and redacted metadata. Skills must declare tool capabilities and mutation scope.

Phase 3: define shared invocation IDs, parent and turn correlation, budgets, event lifecycle, provenance, and test-only performance counters. UI workers must communicate through Bubble Tea messages or commands and never mutate TUI state directly.

Phase 4: add bounded subagents with isolated contexts, dependency-aware scheduling, conflict handling, idempotency, partial-result policy, deterministic aggregation, depth/fan-out/time/cost limits, stress tests, and race tests.

## Revised stop conditions

Stop if capability or resource metadata requires guessing, cancellation leaves goroutines behind, skill cycles are possible, permission is inferred from names, or mutation conflict behavior is undefined.

Do not enable parallel external side effects or parallel writes by default. Human review is required before enabling those behaviors or subagent fan-out.

## Centralization requirement

All executable model-directed work must cross one reusable runtime boundary:

```text
AgentRuntime
  -> InvocationDispatcher
       -> tool adapter
       -> skill adapter
       -> subagent adapter
  -> ExecutionPolicy
  -> EventSink
  -> Result/History adapter
```

The current `Loop -> Registry.Execute` path is transitional. Before skills or
subagents are added, extract a typed dispatcher that owns invocation identity,
parent/turn correlation, validation, permissions, budgets, timeout, conflict
scheduling, cancellation, result limits, and lifecycle events. TUI, persistence,
metrics, and audit consumers must receive typed events through the event sink;
they must not call tools directly or depend on formatted callback strings.

## Validation updates

- Worker-pool execution must be proven free of deadlock under cancellation,
  empty batches, queue saturation, and conflicting resources.
- Capability class and resource keys must be enforced, not advisory.
- Built-in tools must expose typed capabilities; name-based fallback is only a
  compatibility path for unannotated third-party tools.
- Resource keys must be workspace-normalized and include read/write conflicts.
- The end-to-end timeout must include queue and conflict-wait time.
- Registry validation must agree with declared JSON schemas, including required
  fields and rejection of unknown properties.
- Independent external operations must not be globally serialized unless their
  capability declares a shared resource.
- Lifecycle events must expose bounded, redacted input and output previews to
  existing UI consumers while retaining an explicit status and never emitting
  raw secrets, prompts, or unbounded tool payloads.

## Revalidation gate

Before implementation proceeds past the current executor slice, run focused
tests for worker shutdown, cancellation while queued, conflict ordering, schema
validation, and event redaction. Then run the full repository and race gates.
Any hang, leak, race, or policy bypass is a stop condition.
