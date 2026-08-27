# Bug-hunt task template (fast, opportunistic)

Paste this as the `task` input for a bug-hunting `feature-delivery` or `bug-fix`
workflow run. Fill in `Scope:` at the end — everything above it is reusable
as-is.

This is the FAST variant: broad, opportunistic scanning across many places
instead of one deep methodical pass. For the slow, thorough,
hypothesis-ledger-driven investigation instead, use the bug-fix workflow's
`hunt` step (auditor agent, bug-audit skill) directly — this template is for
when you want speed over exhaustiveness.

Design notes (why it's phrased this way — keep this section for future edits,
drop only the fenced template body below when copying into a `task` input):
- No wall-clock instructions ("~N minutes"): the model has no access to real
  time inside its own reasoning, so a time budget is not something it can
  observe or enforce.
- No self-counted tool-call budget ("3 calls per lead"): research on LLM
  agent consistency found tool-sequence repetition holds up better than
  self-reported counters over long trajectories, and no evidence supports
  numeric self-tracking as a reliable stopping mechanism. A forced verdict
  per lead is a content checkpoint the model evaluates from what it already
  read, not a count it has to remember.
- Positive framing is the default (Anthropic's and OpenAI's own prompting
  guidance both state this as the general rule); negative framing ("do not")
  is kept only for the few genuine hard boundaries, where it is unambiguous
  and doesn't compete with a positive alternative.
- Explicit permission to jump around non-linearly: the default bug-audit
  skill is deliberately slow and methodical (hypothesis ledger, invariant-first,
  recurring-class probes) - correct for an adversarial deep audit, wrong when
  the goal is a couple of quick, confident finds. This template asks for
  breadth-first scanning instead.

Sources consulted when drafting this (2026-08-18):
- https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices
- https://developers.openai.com/api/docs/guides/prompt-engineering
- https://eval.16x.engineer/blog/the-pink-elephant-negative-instructions-llms-effectiveness-analysis
- https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- https://github.com/codexstar69/bug-hunter
- https://arxiv.org/abs/2509.02360 (When Agents go Astray — course-correcting SWE agents with PRMs)

---

Find and fix at most 2 reachable, confirmed bugs in this repository's Go source (non-test code). Optimize for speed over exhaustiveness: scan broadly and jump between candidate areas freely rather than working one area deeply before moving to the next — a wide, shallow-first pass surfaces obvious bugs faster than a narrow, deep one.

A bug is "reachable" when you can trace a real call path from a production entrypoint to the defect. A bug is "confirmed" when you can demonstrate it with a concrete failing input or state.

Triage each candidate to one of three verdicts before moving on: **confirmed** (you have a concrete failing input/state and a real call path), **dropped** (state the one-line reason), or **needs one more specific check** (name exactly what you still need to look at, then resolve that to confirmed or dropped — never leave a lead in an open-ended "keep looking" state). The moment a lead isn't confirmed within one or two checks, drop it and jump to a different area — do not chase it further.

Favor defects you can confirm by reading a single function over ones that need tracing state across multiple files or reasoning about runtime conditions you can't observe directly in the source — a confirmed simple bug beats an unconfirmed complex one, and simple bugs are usually faster to spot by skimming many places than by studying one place closely.

For each of the (at most 2) bugs you keep: write a RED regression test that fails against the current code, apply the minimal fix, confirm the test goes GREEN. Stop as soon as you have 2, even if you noticed other candidates along the way.

A clean-audit conclusion (no bug cleared the confirmed bar) is a fine outcome — do not fix a non-bug and do not stretch a "needs one more check" into a fix just to hit the count.

Scope: <fill in — e.g. "internal/tools, internal/storage" or "outside internal/cli, internal/workflows/controller">
