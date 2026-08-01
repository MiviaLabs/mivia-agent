# 33.7 - `Stop` event wiring

**Status:** DESIGN - ready.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §4, §2
**Depends on:** `03`, `04`, `05`.
**Blast radius:** LOW - pure observation, cannot block.

---

## 1. Scope

Fire `Stop` hooks where `KindTurnEnd` is published for the root loop:
`internal/cli/tui_events.go:78`. stdout becomes a continuation prompt the model may
act on; the hook cannot block and cannot undo the turn.

This is the smallest slice in the plan, and it is separate for one reason: it is the
only v1 event whose seam is **outside** `internal/runtime`. Bundling it into `06`
would mix a dispatcher change with a CLI change under one review.

## 2. Root loop only

`KindTurnEnd` is published for turns generally. `Stop` fires for the **root** loop's
turn end, not for a subagent's. A `Stop` hook that fired per subagent turn would run N
times per user-visible turn and its "the assistant is done" semantics would be false
every time but the last.

If distinguishing root from nested at that publish site is not currently possible, that
is a finding to record rather than paper over - say so in the slice's implementation
notes and gate on it, exactly as `SessionStart` was cut for lacking a seam
(`00-overview.md` §4).

## 3. Not `SessionStart`

`SessionStart` was cut from v1 (`00-overview.md` §2, §4): `KindSessionStart` is
declared (`internal/events/event.go:22`) and enumerated in `allKnownKinds`
(`internal/events/metrics.go:48`) but **never published** anywhere in `internal/`.
Shipping it means creating the publish point and deciding what "session start" means
for resume, model-switch (which rebuilds the dispatcher generation, INV-AG-28), and
one-shots. That is its own plan.

Cutting it also removed v1's only prompt-injection surface - `SessionStart` stdout
would concatenate into system context, whereas `Stop` output is model-visible turn
content, bounded and attributed. Do not quietly reintroduce it here because the wiring
looks similar.

## 4. Verification

`go test ./internal/cli/...`:

- a `Stop` hook fires once per root turn end
- it does **not** fire per subagent turn end
- stdout appears as an attributed continuation prompt, within the slice `03` bound
- a `Stop` hook cannot block: a non-zero exit or a `decision:"block"` warns and the
  turn still ends
- a timed-out `Stop` hook does not delay the next turn beyond its 5s default

## 5. Done when

A `Stop` hook can log turn cost without being able to affect whether the turn ended.
