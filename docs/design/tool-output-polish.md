# Tool-output polish — ledger, inspect_repository, and the memory family

Status: proposal, for review. Research date: 2026-08-28.
Scope: how recorded-result tool output renders in the transcript:
`internal/ui/render/ledger_output.go`, `inspect_repository_output.go`,
`FormatMemoryOutput` (`output_formatter.go`), and the tool.end render
path in `internal/ui/component/transcript`. Owners: ui-design-phase0.

## 1. Problem

A `ledger_read` block renders as a raw dump of escaped JSON: literal
`\n` and `\u003c` sequences, the subagent's `<think>` stream, cut off
mid-sentence by the result cap. The screenshot shows the failure; the
same class is reachable from `inspect_repository` and every `memory_*`
tool, because all of them share one brittle shape.

## 2. Findings

F1. **One strict parse, silent raw-dump fallback.** Every formatter in
this family has the same skeleton: `json.Unmarshal` into a fixed struct;
on any error, return the raw output lines
(`ledger_output.go:116-118`, `inspect_repository_output.go:38-40`,
`FormatMemoryOutput`'s `len(items) == 0` branch). Verified by
reproduction: when the whole envelope arrives double-encoded - the tool
result is a JSON *string* wrapping the envelope - every formatter falls
back, and the transcript prints literal `\n` / `\u003c` escapes. That is
the screenshot.

F2. **The salvage path cannot see escaped quotes.** `parseLedgerEnvelope`'s
regexes (`"ref"\s*:\s*"..."`, `ledger_output.go:35-41`) match plain JSON
only. A double-encoded or partially re-encoded payload hides `"ref"` as
`\"ref\"`, so salvage fails and the payload gets no framing at all.

F3. **Raw model dumps render.** Even on the clean path,
`unwrapLedgerContent` happily prints a recorded subagent result whose
`output` begins `<think>…</think>`. AGENTS.md's non-negotiable forbids
raw model dumps in surfaced output; the transcript must collapse them to
a badge the same way reasoning blocks collapse.

F4. **The truncation story is at the wrong end of a collapse.**
`collapseLedgerLines` keeps a head/tail window of 40 lines and the
`⚠ result was truncated…` / `… more remains — call again with offset=N`
trailers are appended after it, at the very end of a long body. Inside a
collapsed block the reader sees a header and a hint, but the ledger
summary the formatter built (`ref`, size, kind) is discarded: the merge
path in `transcript.go` keeps the tool.start detail (`ref:4817bc72`
from the call args), so the formatted summary never reaches the header.
The user sees `ledger_read ref:4817bc72 0ms ok` over a blob.

F5. **inspect_repository.** The grouping formatter is sound (per-file
headers, line gutter, dim context). Gaps: the same F1 fallback; no
per-file cap, so one hot file can own the whole expanded body; long
paths and context lines clip inconsistently (matches clip at
`width-12`, context not at all); and the summary counts files from
`Results` even when `truncated` says the list was cut.

F6. **memory family.** `FormatMemoryOutput` renders cards but: unmarshal
errors are discarded (`_ = json.Unmarshal`), so a mismatched shape - an
error object, a `{"results": [...]}` wrapper, a truncated array - is a
raw dump; `memory_save`/`memory_delete` return plain sentences and fall
through the same branch (harmless today, but one shape away from F1);
and there is no error-envelope handling analogous to
`ledgerErrorEnvelope`.

F7. **Producer-side smell — traced 2026-08-28, no writer found.** The
canonical path is clean by construction: `StoreContent` persists raw
bytes under content-addressed refs (`internal/ledgercore/engine.go`),
`Reference` mints a digest, and `unwrapLedgerContent` already handles
nested string shapes to depth five. Two anomalies remain from the
screenshot: the model called `ledger_read` with a NON-canonical ref
(`ref:<8hex>` — no kind segment; `shortenRef` passes such shapes
through, so the model was handed a shortened or hand-minted ref by
something upstream), and the payload still resolved. A runtime capture
of one failing payload is the only honest next step: add a debug-gated
dump at `FormatLedgerOutput`'s fallback branch, reproduce, and pin the
bytes as the R7 fixture. Suspects for the capture to settle: a synopsis
or panel writer that shortens refs for model-visible text
(`cliorchestrate/synopsis.go`, `panel_aggregation.go`), and any writer
that builds a map with a string field holding marshaled JSON.

## 3. Recommendations

R1. **Shared robust decode ladder.** One helper in `render`
(`decodeToolEnvelope`) used by all three formatters: trim; if the
payload unmarshals to a JSON *string*, unwrap and retry (fixes the
screenshot class); parse the envelope; salvage with regexes that accept
escaped quotes (`\\?\"ref\\?\"`); only then fall back - and make the
fallback loud but calm: a dim first line `unparsed tool result · N B`
above the raw lines, so a blob is never mistaken for content.

R2. **Never print raw model dumps.** After unwrapping, strip
`<think>…</think>` (and a leading unclosed `<think>` on truncated
payloads) from ledger content; replace with one dim line
`· thinking N words hidden`, matching the reasoning-block grammar. The
model still receives the raw bytes - this is display-only, the same
discipline as transcript R2.

R3. **Truncation as a header badge, not a tail trailer.** Reuse the
Round-A truncation badge grammar in the tool.end header meta:
`· truncated: kept X of Y B → read_output`, and `· more · offset=N` for
paged ledger reads. Keep the trailer lines too (they travel with
`Dump()`), but the header must state the fact for a reader who never
expands.

R4. **Surface the formatted summary in the header.** On the tool.end
merge path, prefer the end block's summary detail over the carried
start-block detail when the end summary is non-empty, so the reader
gets `ref:4817bc72 · 4.2 KiB · page 2 of 5` instead of a bare arg echo.
Apply the elapsed ladder (R5 of transcript-polish) to the `0ms` meta.

R5. **inspect_repository hardening.** Route through the R1 ladder; cap
per-file matches at 5 with `… +N more in this file`; middle-truncate
long paths (`src/co…ponent.tsx`); clip context lines to width; summary
`N matches · M files` plus the truncated badge when `truncated` is set,
counting what the envelope claims, not what survived.

R6. **memory hardening.** Handle the error-envelope shape explicitly
(ledger's `✖ message` pattern); accept a `{"results": [...]}` wrapper;
keep cards at three rows (header, summary, tags); route
save/delete sentences through unchanged. All display-only.

R7. **Pin with tests.** Per formatter: valid envelope, tail-truncated
mid-JSON, double-encoded payload, error envelope - four goldens each,
plus one regression test built from the real captured bytes of the
screenshot session (the failing payload, redacted) so the
double-encode shape can never silently regress.

R8. **Chase the producer.** With R7's captured fixture, find the writer
that emits a string-of-JSON (suspects: the ledger record writer
marshaling an already-marshaled envelope, or the event-recording path
in `uiadapter` re-encoding `ToolEndBody.Result`). Fix the source; the
display ladder stays as defense.

## 4. Sequencing

R1 + R2 + R7 first: they fix the visible failure and pin it. R3 + R4
are small header changes with golden churn. R5 + R6 are per-formatter
polish. R8 needs the captured fixture and its own investigation round.
Docs to amend in the same change: `wireframes-panes.md` §4 tool-block
rows gain the badge grammar; `ux-rules.md` §10 gains the overturn rows.

Residual risk: un-wrapping one more JSON layer widens what a hostile
recorded payload can reach the renderer with - the decode ladder must
keep the fixed depth cap `unwrapLedgerContent` already applies and must
never execute recorded bytes, only style them.
