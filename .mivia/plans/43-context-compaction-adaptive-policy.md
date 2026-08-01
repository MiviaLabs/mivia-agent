# 43 - Adaptive context-compaction policy

**Status:** DESIGN - research and evaluation plan; do not implement before
plans `41` and `42` have production evidence.
**Date:** 2026-08-01
**Depends on:** `41`, optionally `42`, measured compaction telemetry, and
representative long-running agent evaluations.
**Blocks:** nothing.
**Blast radius:** MEDIUM - policy/configuration, quality, cost, latency, and
evaluation infrastructure.

## 1. Goal

Use measured context composition and task state to improve when and how much
Mivia compacts, without weakening the deterministic hard-budget invariant.

## 2. Why this is deferred

A fixed 80% trigger is explainable and testable. Adaptive policy is justified
only if measurements show that fixed compaction causes avoidable cost, latency,
or quality loss. Do not introduce a learned or model-directed threshold merely
because it is more sophisticated.

## 3. Candidate signals

The policy may consider bounded host-observed signals: prompt usage fraction,
recent growth rate, tool-result density and size, completed exchanges since the
last checkpoint, phase-boundary requests from plan `42`, prior compaction
reduction/latency, provider token usage, and remaining step/time budget.

It must not use raw secrets, hidden reasoning, arbitrary user instructions,
unbounded transcript text, or model-generated policy claims as direct threshold
inputs.

## 4. Safe policy envelope

```text
minimum_trigger >= 0.65 × B
default_trigger  = 0.80 × B
emergency trigger = before hard overflow
target_after     = 0.40–0.60 × B
```

Adaptive behavior may move the trigger earlier or choose a different retained
tail, but may never move the hard limit later or omit structural invariants
from plan `41`. Begin with a rules-based policy and observational/shadow mode.

## 5. Metrics and evaluation

Record bounded numeric metadata only: compaction count and trigger type,
before/after estimates, summarization latency/failures, provider usage where
available, tool-step counts, task completion/retry/error outcomes, and
user-visible latency/cancellation rates. Never record raw prompts, summaries,
model dumps, secrets, or PII.

Benchmark short conversations, long tool-heavy coding, research with large
results, repeated file edits, clear and unclear phase boundaries, small and
large budgets, and multiple providers/tokenizers.

Evaluate task success, factual continuity, tool correctness, provider rejection
rate, total token cost, compaction cost, and p95 latency. Lower token count
alone is not success.

## 6. Configuration proposal

Only expose configuration after evidence supports it:

```toml
[chat.context_compaction]
mode = "static" # static | adaptive | disabled
trigger_ratio = 0.80
target_ratio = 0.50
```

The shipped default remains static. `disabled` cannot disable hard overflow
protection or local rejection; it only disables proactive summary compaction.
Ratios must be validated against the safe envelope and effective budget. Do not
add provider-specific settings to the generic policy.

## 7. Required test and audit matrix

- Ratios below, at, and above the safe envelope.
- Model switch and `/budget` recomputation.
- Static, adaptive, and disabled modes with identical history.
- Adaptive policy never exceeds the hard budget.
- Deterministic output for the same metrics snapshot.
- Shadow mode does not alter sent messages.
- Metric redaction and boundedness.
- Replay produces equivalent policy inputs without raw content.
- Race tests around concurrent turns and policy snapshots.
- Quality regression suite shows no material continuity regression.

## 8. Verification gates

```text
go test ./internal/config ./internal/provider ./internal/agent ./internal/chat
go test -race ./internal/config ./internal/provider ./internal/agent ./internal/chat
go vet ./internal/config ./internal/provider ./internal/agent ./internal/chat
make verify
make docs-check
```

Before implementation, complete ADLC Step 0 using actual telemetry. Before
changing the default, run the full bug-audit and benchmark suite and document
the decision in this plan.

## 9. Rollback criterion

Keep the fixed 80% policy if adaptive behavior cannot demonstrate measurable
quality, cost, or latency improvement; if metrics are not privacy-safe; or if
decisions are not reproducible and bounded by the effective budget.
