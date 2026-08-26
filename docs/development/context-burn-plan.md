# Context-burn improvement plan (mivia-agent)

Status: FINAL v3. Survived one internal correctness review and one external
industry-benchmark review. Both reviewers were adversarial; both changed the plan.

Evidence base: six parallel code/internet investigations, forensic analysis of
`.mivia/context.db`, and first-hand verification of every load-bearing claim.

## Symptom

The same exploration prompt: Claude Desktop finished at 18% context. mivia's TUI
context climbed far faster, the run took more than twice as long, and it did not
finish.

## Root cause

Three multipliers. None is a defect in the compaction machinery.

### (a) The operative window is 200k, not 1M

`.mivia/mivia.toml:7` sets `max_prompt_tokens = 200000`. This clamps the
window-derived capacity in `internal/config/prompt_budget.go:71-89`
(`EffectivePromptTokens`). The gauge then divides by that budget, not the raw
window. 80% of 200k is 160k; the database shows compaction firing at
before=160,176 and before=160,230. Exact match.

`DefaultPromptCapTokens = 200_000` (`internal/config/defaults.go:20`) is a
documented *recommendation*, not a compiled default - an unset knob keeps the
window-derived budget. This repo sets it explicitly, so this is a CONFIG problem
here, not a shipped-default problem.

Combined effect: a 5x under-declaration of the window, then divided by 80% of
that. The bar is compressed roughly 6.25x against what Claude Code would show.

### (b) Tool results are unbounded and re-sent every step until the trigger

`MaxReadBytes: 0` / `MaxOutputBytes: 0` (`internal/config/defaults.go:151-156`),
`MaxToolResultBytes` 0 = uncapped (`internal/config/tools_config.go:303-304`),
`effectiveResultCap` (`internal/agent/loop_limits.go:86-92`) stores the body
whole. `Plan` returns messages unmodified while `before < trigger`
(`internal/contextmgr/planner.go:158-160`).

Database ground truth: one grep result 163,432 bytes; one glob 105,212 bytes;
grep + glob + read_file + run_command account for ~59% of all stored payload
bytes; largest single-call input jump +55,420 tokens.

### (c) No token-economy guidance; unlimited steps

`internal/clichat/prompt.go:20-48` says "be concise" about reporting only.
`max_steps = 0`. See Open Questions - the wall-clock attribution is not evidenced.

### Ruled out, with evidence

Per-step message duplication (fixed, test-pinned at both seams), gauge summing
across steps (latest-snapshot everywhere), broken prompt caching (database:
cache_read 1,289,522 vs cache_write 103,145 in the latest session), subagent
transcripts folding into root history, hook output injection, double-stored
results.

## Corrections the reviews forced

1. **The original cap proposal would have made reads slower, not faster.**
   `internal/tools/read.go:84-87`: on the full-file path a non-zero `maxBytes`
   returns a hard ERROR, not a truncation. A 64 KiB default would turn every
   whole-file read of a larger file into a failed call plus a mandatory retry.
   Verified first-hand.

2. **It would also have silently discarded the recovery handle.**
   `readClassBudgets` (`internal/tools/default_registry.go:311-327`) pre-clamps
   read-class tools so the loop-level `capToolResult` ->
   `remainder.CapWithSpool` -> `read_output` ref path
   (`internal/agent/loop_limits.go:98-99`) never fires. Tool-internal notices
   carry no handle. Ref-recoverable truncation would become lossy truncation.

3. **64 KiB is too generous, and bytes is the wrong unit.** Peer median for
   shell/tool output is 10 KB (Codex: `TruncationPolicy::Bytes(10_000)`, or 256
   lines / 10 KiB head+tail) to 30 KB (Claude Code `BASH_MAX_OUTPUT_LENGTH`
   default 30,000 chars, hard ceiling 150,000; OpenHands `max_message_chars`
   30,000). Only the MCP lane approaches 64 KiB, and it is denominated in tokens
   (Claude Code `MAX_MCP_OUTPUT_TOKENS` = 25,000). Bytes-per-token varies 2-4x
   between minified JS and prose; the resource being protected is tokens.

4. **"2000-line read window" is out of date.** Current Claude Code Read is
   token-denominated: an implicit whole-file read that exceeds the token limit
   returns the first page plus a `PARTIAL view` notice, while a read with an
   explicit `offset`/`limit` that still overflows returns an ERROR. Gemini CLI
   still uses 2,000 lines and has an open complaint thread about it. A line cap
   alone bounds nothing: 2,000 lines can be 40 KB or 4 MB.

5. **The stale-tool-result mechanism already exists.**
   `elideToolResultsWithSpool` (`internal/contextmgr/planner_elision.go:281-309`)
   replaces oversized prior tool-result bodies with a notice, explicitly never
   changing Role/ToolCallID/Name/ToolCalls (:271-273), skips the mandatory set,
   and spools the body behind a `read_output` ref via `installRetainedElisionRefs`
   (:320-345). Verified first-hand. It is only gated behind the 80% trigger.

6. **The gauge question was framed wrong.** Claude Code shows percent of the RAW
   window, computed from input tokens only
   (`input + cache_creation + cache_read`, excluding output). But its default
   compaction trigger is ~the window itself, so numerator, denominator and
   trigger coincide. mivia's gauge reads high because of (a), not because the
   semantic differs. Fix the denominator value and the semantic question mostly
   evaporates.

7. **Native context editing is not portable.** OpenRouter *rejects* the
   `context-management-2025-06-27` beta with an error rather than ignoring it.
   OpenAI's Responses API shipped its own incompatible shape (`compact_threshold`)
   on 2026-02-11. Gemini has no equivalent. Host-side Trim must be the baseline.

8. **Two P5 items were wrong.** The 12 session/orchestration tools are registered
   AFTER the tier split is computed
   (`internal/clichat/session_tool_catalog.go:33-58`), documented in
   `.mivia/mivia.toml.example:410-414` as a hard bound on the knob. And
   `internal/cliorchestrate/synopsis.go:132-133` inlines an oversized subagent
   report only when the spool produced no ref - that is INV-AG-10, so the fix is
   truncate-with-notice, not "close the fallback".

9. **New coupling: `BatchResultBudgetBytes` is DERIVED from the prompt budget**
   (`internal/chat/binding.go:83`, `BatchResultBudgetDerived = -1`). Raising
   `max_prompt_tokens` silently raises the per-batch tool-result allowance.
   Items P1 and P2 are not independent.

## Plan

### P0 - Read deduplication (BLOCKED at the tool layer; needs a loop-layer design)

Claude Code: if the model re-reads the same file at the same range and the mtime
has not changed, the tool returns a stub instead of the content. The external
review rated this zero-risk. In THIS codebase it is not, and the attempt to
implement it found why:

- The existing observation store (`internal/tools/file_observation.go`) is
  deliberately process-wide and shared across sessions, because that is what
  makes a foreign write detectable for the stale-write guard. Keying dedup off
  it would stub a read for a session that never saw the content.
- Per-tool-instance state fails the same way. `ScopedRegistry`
  (`internal/subagents/multi_step.go:458-462`) filters the registry but reuses
  the parent's tool INSTANCES, so a subagent - which has its own fresh history -
  would receive a stub for content it never saw.
- No session or conversation identity is threaded into the tool layer, so the
  tool cannot scope the decision correctly even if it wanted to.

Dedup therefore belongs at the agent-loop layer, which knows the conversation.
That is its own design pass, not a tool tweak. Deferred deliberately rather than
shipped half-safe.

### P1a - Configure this repo (ship first, zero blast radius)

In `.mivia/mivia.toml` only, no compiled-default change:
- `max_output_bytes` ~30 KB for `run_command` (Claude Code parity)
- `max_read_bytes` sized to a token budget, not a line count
- grep/glob match caps - Claude Code uses 100 files (Glob/Grep) and a
  `head_limit` of 250 (Grep). Your 163 KB grep result dies outright at those
  numbers, which makes this the single highest-leverage value in the plan.
- `max_list_dir_entries`
- reconsider `batch_result_budget_bytes = -1`

Reversible, per-project, no test-contract fight.

### P1b - Change the shipped defaults (separate decision, blocked on prereqs)

  i.   `read.go:84-87` must page-with-notice on an implicit read instead of
       erroring. Keep the hard error only for an explicit `offset`/`limit` that
       still overflows - that is current Claude Code behaviour and it is correct:
       implicit read -> paginate; explicit range that overflows -> fail loudly.
  ii.  Route tool-internal truncation into the existing spool so a handle is
       always minted, or leave `MaxToolResultBytes` at 0 and default only the
       per-tool knobs so the loop cap still mints refs.
  iii. Rewrite `internal/tools/unlimited_defaults_test.go` (6 tests). The
       contract is deliberate. The rewritten test should pin *the marker
       contract* - that truncation always carries a machine-detectable flag -
       not merely that a cap exists.
  iv.  Update `internal/clichat/limits_summary.go:20` and
       `internal/cliorchestrate/doctor.go:79-82`, whose advisories become wrong.

Unaffected, verified: edit tools derive from `MaxReadBytes`, not the result cap
(`default_registry.go:209-230`); code-nav `budgetedJSON` degrades to valid JSON
with `truncated: true`; web tools are deliberately unclamped.

**Truncation marker contract.** The field's own critique is that markers are
"informational text the model is free to ignore - there is no structural field
that says truncated: true". So: emit a structural flag AND a marker naming what
was cut, how much of how much in the unit that was capped, and the literal next
call. Direction: head + continuation offset for reads; head+tail for command
output (stderr tails carry the error); spill-to-handle above the cap.

### P2 - Fix the window story

- Raise/remove `max_prompt_tokens` for 1M-window models in this repo's config.
- Co-decide `batch_result_budget_bytes` - it is derived. See correction 9.
- **Keep an independent, lower proactive-trim target.** Raising the declared
  window fixes the gauge lie; it does not make a 900k-token context a good idea
  (context rot). Claude Code does exactly this for Sonnet 5: 1M window,
  auto-compact at ~967k, user-lowerable via `/autocompact`.
- Preserve the gauge/trigger calibration coupling
  (`context_control.go:64` `calibration.Apply`) or the documented "gauge past
  100% while the planner never compacts" bug returns.
- Cost cliff: compaction at 80% of 1M hands the summarizer ~800k tokens in one
  call. An absolute compaction ceiling is needed.
- Move the gauge to **percent of raw window, input tokens only**
  (`input + cache_creation + cache_read`, excluding output) for cross-tool
  comparability, and render the compaction trigger as a tick mark on the bar.
- Add an operator override to declare the window for gateway/unknown model IDs
  (Claude Code's `CLAUDE_CODE_MAX_CONTEXT_TOKENS`) - directly relevant to the
  OpenAI-compat and OpenRouter dialects, where the window cannot be inferred.
- `doctor.go:79` will start reporting the session "unbounded" - update it.

### P3 - Token-economy guidance

Safe against rule 60: `internal/clichat/prompt_generic_test.go` bans only
Go/product regexes and requires needles this wording keeps.

- Must edit BOTH `internal/clichat/prompt.go` AND `.mivia/mivia.toml:11` - the
  toml `system_prompt` is a forked near-duplicate that supersedes the compiled
  one here and has already drifted.
- Must not name a deferred tool: `scripts/verify_agent_config.py`
  `check_core_tier_covers_prompted_tools` requires every tool named in prose to
  be in `[tools] core`.
- Include **delegate-verbose-operations-to-subagents**. mivia already isolates
  subagent transcripts correctly, so the missing piece is purely the instruction
  telling the root agent to use that isolation for verbose work.
- State the caps in the prompt. Costs ~20 tokens; Codex does it.

### P4 (rescoped) - Run the EXISTING elision pass below the trigger

Not a new mechanism. Add a low-water trigger calling the existing
`elideToolResultsWithSpool` path before the 80% mark. Hard requirements:

- **Hysteretic, not a sliding window.** A per-step "older than N turns" window
  mutates a different old message every step and invalidates the cache prefix on
  EVERY request. That would invert the measured ~12:1 cache_read:cache_write
  ratio and could cost more than it saves. Fire at a low-water mark, elide to a
  target, stay quiet. This is what Anthropic's `clear_at_least` parameter exists
  for - "prevents worthless cache invalidation" - and the plan needs an
  equivalent minimum-yield guard.
- **Stub in place, never drop.** SDK `applyTrim` validates messages individually
  only (`mivia-ai-sdk/agentloop/run.go:464-468`) and would not catch an orphaned
  `tool_use`. The existing pass already stubs correctly.
- **Preserve the call record.** Native clearing keeps the `tool_use` block and
  its inputs so the model retains a record that it made the call and with what
  arguments (`clear_tool_inputs` defaults false). mivia's notice should do the
  same rather than saying only "elided".
- **Exclude non-reproducible results.** Clearing is safe because the agent can
  re-run the tool. File reads and searches clear freely; one-shot command output
  and network fetches must not. Anthropic exposes `exclude_tools` for exactly
  this; P4 needs an equivalent.
- **Extend the evidence path.** `captureOmittedEvidence` and `EmitCompaction`
  are gated on `preparation.Compacted` (`internal/agent/context.go:18-31`,
  `internal/agent/sdk_prepare.go:64-80`). A sub-trigger elision that does not set
  it loses content with no evidence row - silent context loss.
- Idempotency is already handled (notice below `elisionContentMinBytes`, plus the
  strictly-cheaper guard at `planner_elision.go:299-301`).
- **Pair with somewhere to put conclusions.** Clearing tool results is only safe
  if findings survive. mivia's memory frame is capped at 6 KiB and is not
  agent-writable mid-session, so on compaction hard-won findings die. Anthropic's
  39% improvement figure is memory tool + context editing *together*.
- The "84% reduction" figure is vendor-claimed, from a 100-turn **web-search**
  evaluation, not a coding benchmark. Directional only; not a target.

### P5 (corrected)

- Flip `show_prompt_cache_notices` to true while tuning (`.mivia/mivia.toml:10`).
  Free.
- Tier-defer of session tools: NOT a small lever. Needs the tier split moved
  after dispatcher registration plus a prompt rewrite. Park it. When it is done,
  use a window-relative policy (Claude Code loads schemas upfront only when they
  fit in 10% of the window) rather than a flat tier list.
- `synopsis.go:132-133`: truncate-with-notice. Promoted in priority - a
  handle system with a silent lossy fallback is precisely the silent-truncation
  bug.
- Other unlimited defaults: `MaxWriteKB: 0`, `MaxListDirEntries: 0`
  (`defaults.go:154,156`), `SubagentConfig.MaxAuditRounds/DefaultTimeout/
  NestedSteps: 0` (`defaults.go:131-135`).

### P6 - Structural gaps worth separate design passes

- **A reference tier for large reads.** Everything is inline-or-nothing today.
  Claude Code returns a `Referenced file` path instead of content for files over
  5,000 tokens after compaction. This is a bigger structural gap than any single
  cap value.
- **Hook-side preprocessing.** A `PreToolUse` hook that filters test output to
  failures reduces "tens of thousands of tokens to hundreds". mivia has the hook
  system and does not use it as a context lever.
- **Retrieval over grep+read.** Symbol navigation replaces a grep plus several
  candidate reads. This attacks the 59%-of-payload workload directly.

## Status

SHIPPED: prerequisite (i) - the oversized implicit read now paginates instead of
refusing (`internal/tools/read.go`) - plus P1a and P2 as repo config, and the
cache-notice flip. Measured effect of the new config: read_file pages at 65,408
bytes, grep/glob/list_dir bound at 65,536, edit tools untouched at the 256 MiB
backstop.

DEFERRED: P0 (blocked - see above), P3, P4, P1b, P6.

## Recommended sequence

1. DONE - prerequisite (i), P1a, P2, cache notices.
2. P3. Small, safe, both prompt copies.
3. P4 rescoped. Real engineering, on existing tested machinery.
4. P0 at the loop layer. Needs a conversation-scoped design.
5. P1b. Only with prerequisites ii-iv and a recorded decision.
6. P6. Separate design passes.

## Open questions and residual risk

- The 2x wall-clock is attributed to oversized-call count, but nothing measured
  per-request latency against payload size. `max_steps = 0` and a 12h
  orchestration timeout (`defaults.go:13`) make "a subagent ran long" an equally
  live explanation. There is also a second-order effect: prompt cache entries
  live 5 minutes by default, so a slower run mechanically suffers more cache
  misses - part of the gap is a *consequence* of the burn, not only a cause.
  Measure before claiming.
- Flipping the uncapped default is a behaviour change for anyone relying on
  completeness. Keep `0` as the documented opt-out.
- If P4 ever maps to native compaction: sum `usage.iterations`, not top-level
  `usage`, or the gauge under-reports; and keep `cache_control` on the system
  prompt so compaction does not invalidate it.
