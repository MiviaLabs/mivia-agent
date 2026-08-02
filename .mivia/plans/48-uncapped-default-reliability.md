# 48 - Uncapped-by-default reliability: make the system safe without caps

**Status:** DONE — residual F–N shipped (2026-08-02)
**Date:** 2026-08-02 (rewritten after codebase validation; decisions locked same day;
residual closeout same day)
**Depends on:** nothing.
**Blocks / related:** plan `49` (compaction elision) remains the primary
context-cost control once large results are reliable; plan `51` may further
shape dispatcher results.
**Blast radius:** MEDIUM-HIGH — remaining work is dispatcher ceiling
semantics, `run_command` bounded capture, durable payload **chunking**, and
operator-facing config/docs.

---

## 0. How to read this plan

| Section | Purpose |
|---------|---------|
| §1 Decision | Product rule that still holds |
| §2 Status matrix | **Source of truth for done vs left** |
| §3 Done (shipped) | Do not re-implement |
| §4 Remaining work | What still needs code / docs / tests |
| §5 Design locks | Resolved decisions (including chunking + backstop) |
| §6 Implementation order | Execute only residual items |
| §7 Testing residual | Tests still missing |
| §8 Failure analysis | Risks after residual lands |

**Do not implement items marked DONE.** **Do not re-open locked design
decisions without an explicit re-lock.**

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
| B | Durable volume caps operator-owned, default uncapped | **DONE** | `[context]` + chunking (I); SourceEventBytes = chunk size |
| C | Per-tool dispatcher ceiling *derivation* (honest budgets fit) | **DONE** | `output_ceiling.go`, `DeriveOutputCeiling` |
| D | `search_replace` / edit: size guard, result budget, mode preserve | **DONE** | `write.go`, `edit_test.go` (`TestSearchReplacePreservesFileMode`, etc.) |
| E | Oversize refusals state size + windowing (`offset`/`limit`) | **DONE** | `read.go`, edit guard messages, registry/window tests |
| F | Dispatcher: truncate-with-notice instead of destroy | **DONE** | `applyOutputCeiling` in `output_ceiling.go`; dispatcher post-invoke |
| G | Destroy only at `ceiling×4` runaway, and only in bounded mode | **DONE** | `outputExceedsRunaway`; matrix test `TestOutputCeilingMatrixPassTruncateDestroy` |
| H | `dualCapture` head 1/3 + tail 2/3 under `max_output_bytes` | **DONE** | Fixed ring in `capped_buffer.go`; failing-build tests |
| I | Payload chunking + transparent reassembly | **DONE** | Schema v3 `context_payload_chunks`; reassembly + SHA fail-closed |
| J | `memory_backstop_mb` config knob (default **256**) | **DONE** | `[tools] memory_backstop_mb`; wired via `MemoryBackstopBytes` |
| K | Warn when large `max_tool_result_bytes` exceeds useful provider request | **DONE** | `ToolResultBytesWarnings`; never clamps |
| L | Startup log of effective limits (incl. “all unlimited”) | **DONE** | `logEffectiveLimitsOnce` on chat start |
| M | Docs / example TOML aligned with reality + residual design | **DONE** | `config.md`, `mivia.toml`, `mivia.toml.example` |
| N | Plan § testing matrix (see §7) | **DONE** | Residual package tests green |

### Residual summary

All residual items **F–N** are done. Archive when ready.

---

## 3. Done (shipped) — do not re-implement

### 3.1 Uncapped tool defaults

Shipped defaults under `[tools]` today (backstop not yet a knob — see **J**):

```toml
[tools]
max_read_bytes        = 0
max_tool_result_bytes = 0
max_output_bytes      = 0
max_list_dir_entries  = 0
max_write_kb          = 0
# memory_backstop_mb  = 256   # TODO J — required; default 256
```

`0` means unlimited on the volume caps above. Positive `max_tool_result_bytes`
below 1024 is rejected at config load.

### 3.2 Durable limits: config-backed, default uncapped (pre-chunking)

**Original problem:** compile-time `MaxSourceEventBytes` (and friends) rejected
large payloads so a tool result the loop accepted could not be persisted.

**Shipped interim fix (not the end state):**

- Runtime `contextstate.Limits` with `0` = unlimited (`limits.go`).
- Operator knobs under **`[context]`** (byte units).
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
defaults, large payloads store as a single SQLite BLOB. **P1 replaces reject
with chunking** for source-event payloads; see §4.3.

### 3.3 Ceiling derivation (not the same as truncate policy)

Per-tool ceilings are derived so config-compliant tool budgets are less likely
to hit the dispatcher backstop. This reduces accidental destroy for honest
reads; it does **not** implement truncate-instead-of-destroy (still **TODO F**).

### 3.4 Edit tools + windowing refusals

- File-size guard for edit tools: tied to `MaxReadBytes`, or the effective
  memory backstop (today hardcoded 256 MiB; after **J**, `memory_backstop_mb`).
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

### 4.3 P1 — Durable large payloads: **chunking (required)**

**Design locked (§5.2): implement payload chunking.** Single-BLOB + uncapped
default is an interim state only, not the permanent design.

**Target behavior:**

- A source-event payload larger than the chunk size is stored as an **ordered
  chunk sequence** under one content ref.
- `ReadPayload` / `ReadRange` reassemble transparently; reassembly is
  **byte-identical** to the original; content ref remains SHA-256 of the
  **full** payload.
- Whole-payload accept-any-size on validation paths; **per-chunk** invariants
  replace whole-payload reject for source events.
- Schema migration (v3+): `chunk_index` / `chunk_count` (or child table).
  Version bump and dirty-clear must be **atomic** (learn from v2 crash window).
- Config: keep existing `[context]` byte fields. Map
  `max_source_event_bytes` (or an explicit chunk-size alias documented in
  examples) to **chunk size** once chunking lands — default should preserve a
  sensible granularity (e.g. 64 KiB when set as chunk size; `0` may mean
  “use built-in default chunk size” — settle at implementation with tests).
  Session/export integrity bounds remain optional operator ceilings (`0` =
  unlimited under current defaults unless product re-introduces non-zero
  integrity defaults in the same change).

**Touch (expected):**

- `internal/storage/context_schema.go` (migration)
- `internal/storage/context_source.go` (write split / read reassemble)
- `internal/contextstate/sanitize.go`, `contracts.go` / `limits.go` (per-chunk
  vs whole-payload semantics)
- Migration crash + round-trip tests

**Done when:** multi-MB (and larger) source payloads commit and reassemble
byte-identical; oversize-as-reject for whole source payloads is gone under
normal operation; migration is crash-safe.

### 4.4 P2 — Config / docs / observability residual

| Item | Action | Requirement |
|------|--------|-------------|
| **J** `memory_backstop_mb` | Add under `[tools]`, **default `256`**. Wire through registry/edit/read paths that today hardcode `256 << 20`. Document as OOM guard, not a context cap. `0` semantics: either forbidden or “use default 256” — prefer **reject `0` or treat as default** so OOM guard cannot be accidentally disabled without an explicit high value. | **Required** — not optional, not “document hardcoded” |
| **K** Provider-size warn | On nonzero `max_tool_result_bytes` larger than a useful single-request carry size: **warn, never clamp** | Required |
| **L** Startup log | One line summarizing effective tool + context limits + memory backstop; if volume caps are all unlimited, say so once (still print backstop) | Required |
| **M** Docs / examples | Align `.mivia/mivia.toml`, `.mivia/mivia.toml.example`, `docs/product/config.md` with: real keys, residual ceiling/dualCapture after they land, **chunking semantics**, **`memory_backstop_mb = 256`** | Required |
| Naming | Prefer documenting real `[context]` *byte* keys; do **not** invent a second `[context.limits]` section unless product wants a rename migration | Locked |

**Target tools surface after J:**

```toml
[tools]
max_read_bytes        = 0   # unlimited (default)
max_tool_result_bytes = 0
max_output_bytes      = 0
max_list_dir_entries  = 0
max_write_kb          = 0
memory_backstop_mb    = 256 # OOM guard; configurable; shipped default 256
```

### 4.5 Explicitly out of residual scope (already closed)

- Uncapped defaults for tool volume caps
- Making durable volume caps config-backed with default 0 (interim; chunking builds on this)
- Edit mode preservation / edit size guard / windowing refusal copy
- Ceiling *derivation* alone (kept; policy change is F/G)
- Single-BLOB as permanent design (**rejected** — chunking locked)

---

## 5. Design locks

### 5.1 Locked (do not reverse without re-lock)

1. **Defaults stay uncapped** for tool result / read / output / list / write caps.
2. **`0` means unlimited** on those volume knobs and on `[context]` volume
   bounds (chunk-size mapping for source events settles at implementation —
   must not reintroduce silent whole-payload destroy under defaults).
3. **Durable limits live under `[context]` as byte fields** (shipped shape). No
   requirement to rename to `[context.limits]` / `*_kb` / `*_mb` unless a
   separate product change demands it.
4. **Edit tools preserve file mode** and refuse oversize with window guidance.
5. **Context cost of large results is owned by compaction/elision (plan 49 /
   harness economics), not by silent truncation defaults.**
6. **Payload chunking is required (P1).** Large source-event payloads persist
   via ordered chunks under one content ref with transparent reassembly. See
   §5.2.
7. **`memory_backstop_mb` is required and configurable (P2).** Shipped default
   **256**. Hardcoded 256 MiB is interim only.

### 5.2 Decision record — payload storage (LOCKED 2026-08-02)

**Chosen: A — Payload chunking.**

| Option | Outcome |
|--------|---------|
| **A. Chunking** | **LOCKED** — implement as P1 |
| **B. Single-BLOB permanent** | **Rejected** — interim HEAD behavior only |

**Rationale:** uncapped defaults make multi-MB (and larger) tool results
legitimate; a single SQLite BLOB does not scale for durable commit/reassembly
and leaves operator-set reject bounds as a sharp edge. Chunking keeps content
refs as full-payload SHA-256, fails closed on reassembly mismatch, and turns
source-event size config into **chunk granularity** instead of silent
whole-payload destroy.

**Item I is not blocked** — implement after P0 (or in parallel if staffing
allows; storage work is independent of F/G/H).

---

## 6. Implementation order (residual only)

Execute in this order after ADLC Step 0 on the residual slice:

| Step | Item | Notes |
|------|------|-------|
| 1 | **F + G** Ceiling truncate + ×4 destroy | Highest reliability impact under uncapped/large tools |
| 2 | **H** dualCapture head+tail | Bounded-mode correctness; independent of ceiling |
| 3 | **I** Payload chunking | **Required** — migration + reassembly + tests |
| 4 | **J** `memory_backstop_mb` (default 256) | Wire all hardcoded 256 MiB backstops to config |
| 5 | **K, L, M** | Warn + startup log + docs/examples |
| 6 | **N** residual tests | Run with each step; full matrix before archive |

Archive this plan only when:

- P0 (**F, G, H**) are done and tested, and
- P1 (**I** chunking) is done and tested, and
- P2 (**J** at minimum, plus K/L/M) is done, and
- residual **N** tests are green.

---

## 7. Testing residual

| Test | Status | Required for |
|------|--------|--------------|
| `search_replace` executable preserves `+x` | **DONE** | — |
| Edit/read oversize messages mention windowing | **DONE** (behavior) | — |
| Uncapped large-ish turn still commits (tens–hundreds of KiB) | **PARTIAL** | Raise to multi-MB through loop → dispatcher → durable reassembly |
| Bounded matrix: cap−1 / cap / cap+1 / runaway → pass / **truncate-notice** / **destroy@×4** | **DONE** | **F, G** |
| `run_command` failing-build: error **tail** survives under `max_output_bytes` | **DONE** | **H** |
| Migration crash (version/dirty atomic) for **chunk** schema | **DONE** | **I** |
| Chunk reassembly byte-identical + content-ref SHA fail-closed | **DONE** | **I** |
| `memory_backstop_mb` default 256; override honored by read/edit guards | **DONE** | **J** |
| Config warn on huge `max_tool_result_bytes` | **DONE** | **K** |
| Startup limits log (optional assert via log hook if cheap) | **DONE** | **L** |

---

## 8. Failure analysis (residual-aware)

- **Unbounded results still cost context:** owned by plan `49` / harness
  economics — reliability first, cost second. Do not “fix” cost by
  reintroducing silent default caps.
- **Destroy-at-ceiling left in place:** agent pays for tool work and gets an
  empty failure — primary residual reliability bug (**F/G**).
- **Head-only dualCapture:** bounded operators lose the failure tail — residual
  (**H**).
- **Chunking bugs:** content-ref SHA on reassembly must fail closed; never
  silent truncate or partial commit without atomic dirty/version handling.
- **Operator sets tiny bounds:** their choice; truncation notices with totals
  keep it observable (after **F/H**).
- **Disabling the memory backstop:** config must not make it easy to set
  “unlimited RAM” by accident (`0` → default or reject; see **J**).

---

## 9. Original breakage map (historical → status)

| Original symptom | Status |
|------------------|--------|
| Dispatcher destroys over-ceiling results | **Still open (F/G)** |
| Compile-time durable 64 KiB reject | **Interim fixed** via uncapped config-backed limits; **chunking still required (I)** |
| `dualCapture` head-cut loses failures | **Still open (H)** |
| `search_replace` no size guard / mode clobber | **Fixed** |
| Memory backstop bare error / no window guidance | **Messages fixed**; backstop must become TOML **`memory_backstop_mb` default 256 (J)** |

---

## 10. Closeout checklist

- [x] **F** Truncate-with-notice at dispatcher ceiling
- [x] **G** Destroy only at `ceiling×4` in bounded mode
- [x] **H** dualCapture head 1/3 + tail 2/3 + failing-build test
- [x] **I** Payload chunking + migration crash + reassembly tests (**required**)
- [x] **J** `memory_backstop_mb` configurable, default **256**, wired through read/edit paths
- [x] **K** Warn on huge `max_tool_result_bytes`
- [x] **L** Startup effective-limits log (includes backstop)
- [x] **M** Docs/examples match design (chunking + backstop + residual ceiling)
- [x] **N** Residual tests green
- [x] Archive plan with final status (chunking + backstop decision records in §5)
