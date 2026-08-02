# 48 - Uncapped-by-default reliability: make the system safe without caps

**Status:** IN PROGRESS — partial (validated against HEAD 2026-08-02)
**Date:** 2026-08-02 (rewritten same day after codebase validation)
**Depends on:** nothing.
**Blocks / related:** plan `49` (compaction elision) remains the primary
context-cost control once large results are reliable; plan `51` may further
shape dispatcher results.
**Blast radius:** MEDIUM-HIGH — remaining work is mostly dispatcher ceiling
semantics, `run_command` bounded capture, durable-payload strategy, and
operator-facing config/docs.

---

## 0. How to read this plan

| Section | Purpose |
|---------|---------|
| §1 Decision | Product rule that still holds |
| §2 Status matrix | **Source of truth for done vs left** |
| §3 Done (shipped) | Do not re-implement |
| §4 Remaining work | What still needs code / docs / tests |
| §5 Design locks | Resolved divergences and open decisions |
| §6 Implementation order | Execute only residual items |
| §7 Testing residual | Tests still missing vs §6 of original plan |
| §8 Failure analysis | Risks after residual lands |

**Do not implement items marked DONE.** **Do not re-open locked design
decisions without an explicit re-lock.** Open decisions are called out in §5.2.

---

## 1. Decision (unchanged)

**Defaults stay uncapped.** Capped defaults make agents unreliable: a
truncated grep or build log silently hides the answer and the agent acts on
partial data. `0` remains "unlimited" and remains the shipped default for
`max_read_bytes`, `max_output_bytes`, `max_tool_result_bytes`,
`max_list_dir_entries`, `max_write_kb`.

The problem is inverted from an early draft of this plan: the rest of the
system must not *assume* bounded results and misbehave when they are not.
Fix those layers so the uncapped default is actually reliable, and keep every
operator-facing limit an explicit `mivia.toml` knob.

---

## 2. Status matrix (HEAD 2026-08-02)

| ID | Work item | Status | Evidence / location |
|----|-----------|--------|---------------------|
| A | Uncapped tool defaults (`0` = unlimited) | **DONE** | `ToolsConfig` / `DefaultToolsConfig`; `unlimited_defaults_test.go` |
| B | Durable volume caps operator-owned, default uncapped | **DONE** (alternate shape) | `[context]` + `contextstate.Limits` / INV-AG-35; not compile-time 64 KiB reject |
| C | Per-tool dispatcher ceiling *derivation* (honest budgets fit) | **DONE** | `output_ceiling.go`, `DeriveOutputCeiling` |
| D | `search_replace` / edit: size guard, result budget, mode preserve | **DONE** | `write.go`, `edit_test.go` (`TestSearchReplacePreservesFileMode`, etc.) |
| E | Oversize refusals state size + windowing (`offset`/`limit`) | **DONE** | `read.go`, edit guard messages, registry/window tests |
| F | Dispatcher: truncate-with-notice instead of destroy | **TODO** | Still `fail(overCeilingError)` in `dispatcher.go` |
| G | Destroy only at `ceiling×4` runaway, and only in bounded mode | **TODO** | No ×4 path; destroy at 1× ceiling always |
| H | `dualCapture` head 1/3 + tail 2/3 under `max_output_bytes` | **TODO** | Still head-only in `capped_buffer.go` |
| I | Payload chunking + transparent reassembly | **TODO / DEFERRED** | Single BLOB per ref; schema v2. Large works only via uncapped default. See §5.2 |
| J | `memory_backstop_mb` config knob | **TODO** (or document non-config) | Hardcoded `256 << 20` |
| K | Warn when large `max_tool_result_bytes` exceeds useful provider request | **TODO** | Only hard-error for `0 < n < 1024` today |
| L | Startup log of effective limits (incl. “all unlimited”) | **TODO** | No summary line |
| M | Docs / example TOML aligned with reality + residual design | **PARTIAL** | Tools knobs documented; still describe destroy-on-ceiling; incomplete context surface |
| N | Plan § testing matrix (see §7) | **PARTIAL** | Edit mode + some large-turn tests; ceiling/dualCapture/chunk E2E incomplete |

### Residual summary (what is left)

**Must fix (P0 — reliability under uncapped / bounded operator mode):**

1. **F + G** — Ceiling policy: truncate honest oversize; destroy only at ×4 runaway when bounds are explicit.
2. **H** — `dualCapture` head+tail so bounded `run_command` keeps failure tails.

**Decide then implement or drop (P1):**

3. **I** — Payload chunking vs accept single-BLOB + optional reject (open decision §5.2).

**Operator surface (P2):**

4. **J, K, L, M** — Config polish, warn, startup log, docs/examples.

**Verification (with each item above):**

5. **N** — Tests listed in §7.

---

## 3. Done (shipped) — do not re-implement

### 3.1 Uncapped tool defaults

Shipped defaults under `[tools]`:

```toml
[tools]
max_read_bytes        = 0
max_tool_result_bytes = 0
max_output_bytes      = 0
max_list_dir_entries  = 0
max_write_kb          = 0
```

`0` means unlimited. Positive `max_tool_result_bytes` below 1024 is rejected at
config load.

### 3.2 Durable limits: config-backed, default uncapped

**Original problem:** compile-time `MaxSourceEventBytes` (and friends) rejected
large payloads so a tool result the loop accepted could not be persisted.

**Shipped fix (different shape than first draft):**

- Runtime `contextstate.Limits` with `0` = unlimited (`limits.go`).
- Operator knobs under **`[context]`** (byte units), not plan’s original
  `[context.limits]` kb/mb names.
- Applied at chat startup via `applyContextLimits`.
- INV-AG-35: durable bounds must not destroy finished turns under defaults.

Actual keys today:

```toml
[context]
max_source_event_bytes = 0
max_checkpoint_bytes = 0
max_commit_events = 0
max_commit_event_bytes = 0
max_session_state_bytes = 0
max_export_bytes = 0
# plus summary_metadata_bytes, checkpoint_metadata_bytes
```

When a nonzero bound is set, oversize still **rejects** (not chunked). Under
defaults, large payloads store as a single SQLite BLOB.

### 3.3 Ceiling derivation (not the same as truncate policy)

Per-tool ceilings are derived so config-compliant tool budgets are less likely
to hit the dispatcher backstop. This reduces accidental destroy for honest
reads; it does **not** implement truncate-instead-of-destroy (still **TODO F**).

### 3.4 Edit tools + windowing refusals

- File-size guard for edit tools: tied to `MaxReadBytes`, or 256 MiB when
  uncapped.
- Declared result budgets for search_replace / multi_edit.
- Mode-preserving rewrites (`rewriteRegularFileContents`).
- Full-file / oversize paths instruct `offset`/`limit` (or edit-window guidance).

---

## 4. Remaining work (implement these)

### 4.1 P0 — Dispatcher ceiling: truncate, never destroy honest output

**Problem today:** after invoke, if `len(out) > ceiling`, the dispatcher
returns `overCeilingError` and the agent pays for the tool run with nothing
useful — unreliability reintroduced one layer above the tools.

**Target behavior:**

| Condition | Behavior |
|-----------|----------|
| Uncapped effective ceiling path / pure uncapped config | Pass through (memory/OOM backstop is the hard stop) |
| Bounded: `ceiling < len(out) ≤ ceiling×4` | **Tail-truncate** at rune boundary + notice `... (truncated: kept X of Y bytes)` (reuse `trimPartialRune` / notice reserve accounting from remainder package where possible) |
| Bounded: `len(out) > ceiling×4` | **Destroy** with clear error (runaway backstop only) |

**Touch:**

- `internal/runtime/dispatcher.go` (post-invoke check ~over-ceiling branch)
- `internal/runtime/output_ceiling.go` (comments + helpers; today documents hard-fail)
- Tests that currently require destroy at 1× ceiling:
  - `per_tool_output_ceiling_test.go`
  - `tool_output_budget_regression_test.go`
  - related result-cap / registry gates

**Done when:** honest oversize results are truncated with notice; destroy only
at ×4 under explicit bounds; uncapped path never uses destroy-at-ceiling.

### 4.2 P0 — `dualCapture` head + tail (bounded `run_command`)

**Problem today:** under operator `max_output_bytes`, capture keeps the **head**
only. Compilers print errors last, so the bound drops the useful tail.
(`exit=` header is already composed outside the buffer and is fine.)

**Target behavior:**

- Shared budget: ~**1/3 head + 2/3 tail** with an elision marker between.
- Preserve exit status framing (already outside capture).
- Update head-only unit tests (`coverage_helpers_test.go`, etc.).

**Touch:**

- `internal/tools/capped_buffer.go` (`dualCapture` / stream sides)
- `internal/tools/run.go` (if notice text needs alignment)
- New fixture: failing build / error-at-end under a tight `max_output_bytes`

**Done when:** a bounded capture of a failing build keeps the error tail and
shows an elision marker; head-only tests replaced.

### 4.3 P1 — Durable large payloads: chunking (open) or lock single-BLOB

See **§5.2**. Until decided:

| If we choose… | Remaining work |
|---------------|----------------|
| **Chunking** | Schema migration (v3+) with chunk sequence; write split / read reassemble; `max_source_event_bytes` (or new chunk knob) becomes **chunk size**, not whole-payload reject; SHA of full payload on reassembly; migration crash test (atomic version/dirty) |
| **Single-BLOB (current)** | Document as locked; optional operator reject bounds stay as-is; multi-GB risk is operator/context problem; drop chunk tests from residual; may still add soft warnings for huge events |

**Do not start chunking code until §5.2 is locked.**

### 4.4 P2 — Config / docs / observability residual

| Item | Action |
|------|--------|
| `memory_backstop_mb` | Add under `[tools]` **or** document “hardcoded 256 MiB, not configurable” and remove from desired surface |
| Provider-size warn | On nonzero `max_tool_result_bytes` larger than a useful single-request carry size: **warn, never clamp** |
| Startup log | One line summarizing effective tool + context limits; if all unlimited, say so once |
| Docs / examples | Align `.mivia/mivia.toml`, `.mivia/mivia.toml.example`, `docs/product/config.md` with real keys and residual ceiling/dualCapture behavior (stop documenting destroy-on-ceiling as the final product intent once F lands) |
| Plan vs code naming | Prefer documenting real `[context]` *byte* keys; do **not** invent a second `[context.limits]` section unless product wants a rename migration |

### 4.5 Explicitly out of residual scope (already closed)

- Uncapped defaults for tool caps
- Making durable caps config-backed with default 0
- Edit mode preservation / edit size guard / windowing refusal copy
- Ceiling *derivation* alone (kept; policy change is F/G)

---

## 5. Design locks

### 5.1 Locked (do not reverse without re-lock)

1. **Defaults stay uncapped** for tool result / read / output / list / write caps.
2. **`0` means unlimited** on those knobs and on `[context]` volume bounds.
3. **Durable limits live under `[context]` as byte fields** (shipped shape). No
   requirement to rename to `[context.limits]` / `*_kb` / `*_mb` unless a
   separate product change demands it.
4. **Edit tools preserve file mode** and refuse oversize with window guidance.
5. **Context cost of large results is owned by compaction/elision (plan 49 /
   harness economics), not by silent truncation defaults.**

### 5.2 Open decision — must lock before P1 chunking

**Question:** Should large durable source payloads be **chunked** in storage,
or is **single-BLOB + uncapped default (+ optional reject when operator sets a
bound)** the permanent design?

| Option | Pros | Cons |
|--------|------|------|
| **A. Chunking** (original plan §3.2) | Stable under multi-100MB events; reject bound becomes chunk size | Schema migration risk; more storage code; not needed for current defaults |
| **B. Single-BLOB** (current HEAD behavior) | Already shipped; simple | Huge events stress SQLite/memory; no graceful persist path when operator sets a small reject bound |

**Recommendation pending implementer re-lock:** default to **B for v1 closeout of
this plan** unless a measured multi-MB durable failure forces A. If B is
locked, mark item **I** as **WONTFIX / deferred** and archive chunking as a
follow-up plan.

Until locked, treat **I** as blocked.

---

## 6. Implementation order (residual only)

Execute in this order after ADLC Step 0 on the residual slice:

| Step | Item | Notes |
|------|------|-------|
| 1 | **F + G** Ceiling truncate + ×4 destroy | Highest reliability impact under uncapped/large tools |
| 2 | **H** dualCapture head+tail | Bounded-mode correctness; independent of ceiling |
| 3 | **§5.2 lock** | Chunking yes/no |
| 4 | **I** only if chunking chosen | Migration + reassembly + tests |
| 5 | **J, K, L, M** | Config/docs/startup polish |
| 6 | **N** residual tests | Run with each step; full matrix before archive |

Archive this plan only when:

- P0 (**F, G, H**) are done and tested, and
- **I** is either implemented or explicitly **WONTFIX** with §5.2 locked, and
- P2 is done or explicitly deferred with rationale in the archive note.

---

## 7. Testing residual

| Test | Status | Required for |
|------|--------|--------------|
| `search_replace` executable preserves `+x` | **DONE** | — |
| Edit/read oversize messages mention windowing | **DONE** (behavior) | — |
| Uncapped large-ish turn still commits (tens–hundreds of KiB) | **PARTIAL** | Raise to multi-MB if claiming full uncapped E2E |
| Bounded matrix: cap−1 / cap / cap+1 / runaway → pass / **truncate-notice** / **destroy@×4** | **TODO** (today asserts destroy@1×) | **F, G** |
| `run_command` failing-build: error **tail** survives under `max_output_bytes` | **TODO** | **H** |
| Migration crash (version/dirty atomic) for **chunk** schema | **N/A until I** | **I** only |
| Chunk reassembly byte-identical + content-ref SHA | **N/A until I** | **I** only |
| Config warn on huge `max_tool_result_bytes` | **TODO** | **K** |
| Startup limits log (optional assert via log hook if cheap) | **TODO** | **L** |

---

## 8. Failure analysis (residual-aware)

- **Unbounded results still cost context:** owned by plan `49` / harness
  economics — reliability first, cost second. Do not “fix” cost by
  reintroducing silent default caps.
- **Destroy-at-ceiling left in place:** agent pays for tool work and gets an
  empty failure — primary residual reliability bug (**F/G**).
- **Head-only dualCapture:** bounded operators lose the failure tail — residual
  (**H**).
- **Chunking bugs (if chosen):** content-ref SHA on reassembly must fail closed;
  never silent truncate.
- **Single-BLOB lock (if chosen):** document multi-GB risk; operator may set
  reject bounds knowingly.
- **Operator sets tiny bounds:** their choice; truncation notices with totals
  keep it observable (after **F/H**).

---

## 9. Original breakage map (historical → status)

| Original symptom | Status |
|------------------|--------|
| Dispatcher destroys over-ceiling results | **Still open (F/G)** |
| Compile-time durable 64 KiB reject | **Fixed** via uncapped config-backed limits (not chunking) |
| `dualCapture` head-cut loses failures | **Still open (H)** |
| `search_replace` no size guard / mode clobber | **Fixed** |
| Memory backstop bare error / no window guidance | **Fixed** for read/edit paths; backstop not yet a TOML knob (**J**) |

---

## 10. Closeout checklist

- [ ] **F** Truncate-with-notice at dispatcher ceiling
- [ ] **G** Destroy only at `ceiling×4` in bounded mode
- [ ] **H** dualCapture head 1/3 + tail 2/3 + failing-build test
- [ ] **§5.2** Chunking locked: implement **or** WONTFIX
- [ ] **I** Chunking (if chosen) + migration crash + reassembly tests
- [ ] **J** `memory_backstop_mb` or documented non-config
- [ ] **K** Warn on huge `max_tool_result_bytes`
- [ ] **L** Startup effective-limits log
- [ ] **M** Docs/examples match HEAD + residual design
- [ ] **N** Residual tests green
- [ ] Archive plan with final status + decision record for §5.2
