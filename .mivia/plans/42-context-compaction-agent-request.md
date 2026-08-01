# 42 - Agent-requested context compaction

**Status:** DESIGN - implement only after `41` is shipped and re-audited.
**Date:** 2026-08-01
**Depends on:** `41` deterministic context compaction and the existing generic
tool-surface policy.
**Blocks:** nothing.
**Blast radius:** MEDIUM-HIGH - model-facing tools, agent-loop control flow,
prompt injection resilience, compaction cost, and observability.

## 1. Goal

Allow an agent to request early context compaction at a semantic phase boundary
while preserving deterministic host enforcement as the safety authority.

## 2. Non-negotiable authority split

- The host decides whether a context is safe and structurally valid to compact.
- The host may force compaction at the static threshold from plan `41`.
- The agent may request compaction but may not veto, postpone, or disable host
  compaction.
- The request is advisory control data, not a user-authorized side effect.
- A model-generated reason must never affect permissions, tool scope, budgets,
  persistence retention, or security policy.

## 3. Proposed control surface

Add a generic internal control tool, tentatively named `compact_context`, with a
small bounded schema:

```json
{
  "reason": "completed investigation phase",
  "preserve": ["current objective", "latest decisions"]
}
```

The schema must not accept arbitrary transcript content, file paths,
authorization claims, or retention directives. The tool should be available
only to the agent loop that owns the context, not one-shot agents with no
history to compact.

When invoked, the host should validate a complete exchange, record a bounded
reason, compact using plan `41`, and continue with the compacted context on the
next model request. A cooldown must prevent repeated requests from creating a
compaction loop.

## 4. Safety rules

- Defer requests during an unfinished tool batch.
- Require a minimum amount of growth since the previous compaction unless the
  host is already above its hard threshold.
- Cap requests per turn and per session budget.
- Treat malformed arguments as a normal bounded tool error; never echo raw args.
- User content and tool output cannot invoke the control path directly; only a
  genuine assistant tool call may do so.
- Preserve the current user objective and latest active work regardless of the
  agent's `preserve` list.
- Keep the static threshold active when the agent never requests compaction.

## 5. Prompt/status contract

Expose only bounded approximate status, for example:

```text
Approximate context usage: 61% of the effective prompt budget.
Early compaction is available when a work phase is complete.
```

Do not expose internal source paths, secret-bearing metadata, raw estimator
input, or hidden reasoning. Wording must remain project- and language-generic
and pass the model-facing generic-surface tests.

## 6. Failure and adversarial analysis

Test agent silence, compaction on every step, premature requests, user attempts
to invoke or suppress the control, fake tool calls in tool output, summary
failure/timeouts, incomplete tool pairing, concurrent session generations, and
replayed requests after resume. Every case must remain bounded, must not change
privilege, and must not silently delete durable history.

## 7. Required test matrix

- Valid early request at a safe tool boundary.
- Request deferred until tool pairing is complete.
- Cooldown and per-turn request limits.
- Static fallback when the agent never requests compaction.
- Static override when the agent attempts to defer compaction.
- Invalid schema, oversized reason, duplicate request, and unknown fields.
- Prompt-injection and tool-output spoofing fixtures.
- Concurrent send/clear/load/model-switch races.
- Subagents cannot access or trigger the parent's compaction surface.
- Event output contains bounded metadata only.
- The request does not enter the durable user transcript as a user message.

## 8. Verification gates

```text
go test ./internal/tools ./internal/agent ./internal/chat ./internal/subagents
go test -race ./internal/tools ./internal/agent ./internal/chat ./internal/subagents
go vet ./internal/tools ./internal/agent ./internal/chat ./internal/subagents
make verify
make docs-check
```

Run hostile correctness, security, and prompt-injection audits. Schema tests
alone are insufficient evidence for this model-facing control surface.

## 9. Rollback criterion

Remove the agent-directed path and retain plan `41` if the control cannot remain
advisory, a spoofed request can reach compaction authority, or cooldown and
host fallback cannot prevent repeated or late compaction.
