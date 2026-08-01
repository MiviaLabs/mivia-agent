# 25 - Make `triggers:` real, or delete it

**Status:** ✅ Implemented 2026-07-30 (§3 → **B**, §8 → 64/400). Pinned by INV-AG-17.
Shipped across `7f4ddb7` (waves 1-4), `fdd3c40` (audit fixes), and a follow-up review
commit that wired the unknown-key rejection, corrected block-sequence handling, and
cleared `check_go_structure`. See §14.
**Date:** 2026-07-30
**Depends on:** nothing. **Amends:** `05` §6 (parser ownership - see §5).
**Blast radius:** LOW (skill loading only; no privilege surface, no persisted state).

---

## Premise correction up front

This is not "a parser bug". Three things are missing, not one, and they fail in order:

| Layer | State at HEAD | Evidence |
|---|---|---|
| Parser | Hand-rolled line scanner; recognizes exactly `name` and `description` | `internal/skills/loader.go:107-119` |
| Model | `skills.Definition` has **no `Triggers` field at all** | `internal/skills/skills.go:14-27` |
| Consumer | **Nothing anywhere reads a trigger** | `grep -rni trigger --include=*.go internal/ cmd/` → two unrelated comments |

Fixing only the parser writes a list into a struct field that does not exist. Adding
the field too writes it somewhere nothing reads. **The consumer is the plan; the
parser is a prerequisite.**

Nine skills declare `triggers:` today. All nine are silently discarded.

## 1. The defect

`.mivia/skills/*/SKILL.md` frontmatter advertises a `triggers:` list that authors
have maintained across nine files. It has never done anything. A control-surface
field that looks load-bearing and is inert is worse than an absent one: it invites
authors to tune trigger phrases that cannot affect behaviour.

### 1a. Severity - LOW, and honest about it

No incorrect behaviour results today. Skill selection works: `Registry.Select`
resolves by name (`internal/skills/skills.go:99`), and the model chooses from the
name + description injected into the model-facing surface at `loader.go:64`. The
cost is wasted authoring effort and a false signal, not a malfunction.

This severity is why §3 is a real decision and not a formality.

## 2. What a consumer would actually be

Plan `16` shipped skill discoverability: name + description reach the model-facing
tool surface, sanitized (`SanitizeModelFacingText`, `loader.go:61-62`). That is a
shipped mechanism at HEAD, not a hypothesis.

Trigger phrases are exactly the signal that surface is missing. "Use when the user
says X" is what makes a model pick the right skill; a one-line description compressed
to 200 chars often cannot carry it. So the only consumer worth building extends the
mechanism `16` already established.

## 3. Options - DECIDED: **B** (2026-07-30)

### A. Delete `triggers:` from all nine skills

Cheapest honest fix. Nine deletions, no Go change. Removes the false signal.
Loses the authoring work already done, and forfeits the selection improvement in §2.

### B. Parse triggers and inject them into the model-facing surface - **CHOSEN**

Parser subset + `Definition.Triggers` + injection alongside description at
`loader.go:64`. Gives the field a real consumer, extends a shipped mechanism, and
keeps the nine authored trigger sets. Cost is §5's four waves.

### C. Defer entirely to `05` §6

`05` §6 already specifies the subset parser and says to backport `skills.parseMarkdown`
onto it. But `05` is HIGH blast radius, heads a five-plan program, and sits at position
7 in the `INDEX` triage. This is a LOW-risk, self-contained fix; parking it behind a
privilege-surface program is the wrong sequencing.

### D. Parse triggers, ship no consumer

**Rejected - do not build.** This is the speculative-generality case
`.mivia/skills/architecture-review/SKILL.md` step 2 exists to catch: a field with
zero production readers and no contracted principal. If B's consumer is not wanted,
the answer is A, not D.

**Decision: B.** Build the consumer. `A` is not taken - the selection improvement in
§2 is wanted and the nine authored trigger sets are kept. `D` remains DO NOT BUILD;
§4's wave ordering is the mechanism that prevents drifting into it.

## 4. Reachability - required by `architecture-review` step 2

| Element | Prod callers at HEAD | Callers this change adds | Verdict |
|---|---|---|---|
| `Definition.Triggers` | 0 (field absent) | 1 - the surface injection in Wave 3 | Not a finding; this change is the remedy |
| Subset parser | 0 | 1 - `loader.go`; later `internal/roles` per §5 | Not a finding |

Wave 2 must not land without Wave 3. Wave 2 alone *is* option D.

## 5. Parser ownership - amends `05` §6

`05` §6 places the subset parser in `internal/roles/markdown.go` and has skills
backport onto it, with the standing constraint: **"Do not maintain two frontmatter
parsers."** That constraint is correct and this plan keeps it - but inverts the
direction, because `internal/roles` does not exist and this plan should not wait for it.

**Build the parser in `internal/skills/frontmatter.go`.** When `05` lands,
`internal/roles` imports it rather than the reverse.

Dependency direction is sound: `05` §7 already requires roles to validate that every
`skills` entry names a real skill, so `roles` → `skills` is the direction that
already exists. `skills` → `roles` would be the inversion. Verify with
`go list -deps ./internal/roles` when `05` lands.

**`05` §6 must be updated to point at `internal/skills/frontmatter.go`** as part of
Wave 4, or the two plans will disagree.

## 6. Subset grammar

Per rule 30 - no YAML dependency. Documented strict subset, rejecting rather than guessing:

- `---` delimited frontmatter, first line only.
- `key: scalar`, optional surrounding quotes.
- `key: [a, b, c]` flow sequence.
- `key:` followed by indented `- item` block sequence.
- `#` comments and blank lines skipped.
- Anything else (nested maps, `>` / `|` block scalars, anchors, multi-doc) ⇒ **hard
  error naming the line number**.
- 256 KiB cap, mirroring `maxSkillBytes`.

Unknown keys: **hard error**, listing the recognized set. This is what makes a
future dead field impossible - the class of bug this plan exists to fix. It is a
breaking change for any workspace skill carrying an extra key; call it out in Wave 4
docs.

## 7. Implementation waves

TDD per ADLC: RED test task precedes each production task.

| Wave | File | Change |
|---|---|---|
| 1 | `internal/skills/frontmatter.go` (new) | `ParseFrontmatter([]byte) (map[string]any, error)` implementing §6. Table-driven tests: each accepted form, each rejected form asserting the line number, the 256 KiB cap, CRLF, tabs-vs-spaces |
| 1 | `internal/skills/frontmatter_test.go` (new) | RED first |
| 2 | `internal/skills/skills.go` | Add `Triggers []string` to `Definition` |
| 2 | `internal/skills/loader.go` | Replace the `:107-119` scanner with `ParseFrontmatter`; populate `Name`, `Description`, `Triggers`. **Must not change existing name/description behaviour** - pin with the current loader tests before touching it |
| 3 | `internal/skills/loader.go` | Inject triggers into the model-facing prompt beside description at `:64`. Sanitize **each** trigger via `SanitizeModelFacingText`; cap the joined block (see §8) |
| 4 | `scripts/verify_agent_config.py` | Assert every `triggers:` entry is non-empty and the joined block is within cap - mirroring the existing 200-char description assertion |
| 4 | `.mivia/plans/05-agent-model-core/00-overview.md` | Update the active agent-plan set to point at `internal/skills/frontmatter.go` (§5) |
| 4 | `docs/development/agent-workflow.md` | Document the frontmatter subset and the unknown-key rejection |

## 8. Model-facing caps - DECIDED (2026-07-30)

`description` is capped at 200 (`loader.go:62`) because it reaches a tool schema.
Triggers reach the same surface and need their own bound. **Accepted starting values: 64 per trigger, 400 for the joined block**, keeping a
skill's total model-facing text under ~700.

These are deliberately chosen as a starting point, not a measured limit. Wave 3 must
emit both as named constants beside `SKILL_DESCRIPTION_MAX`'s Go counterpart so they
are adjustable in one place. If a provider's tool-schema limit is hit in practice,
re-derive them from that limit rather than tuning by feel - the numbers are a
starting guess and the code comment must say so.

## 9. Verification

- `go test ./internal/skills/...` - parser table, loader behaviour preserved
- `go test -race ./...`
- `go build ./... && go vet ./...`
- `make verify-agent` - new triggers assertion
- `make verify`
- Manual: confirm a skill's triggers appear in the model-facing tool surface, and
  that nine existing skills load unchanged apart from gaining triggers

## 10. Invariant registration

Wave 3 needs a row: *skill frontmatter unknown keys are rejected, and declared
triggers reach the model-facing surface*. Lowest free id above the current maximum -
`INV-AG-16` is taken, so **`INV-AG-17`**, allocated at landing time and confirmed by
hand. Neither `validate_invariants.py` nor `invariant_coverage.py` parses ids, so a
duplicate passes every gate silently.

## 11. What this does NOT solve

- `Definition.Tools` is still never populated from frontmatter. Wave 2 makes it
  *possible*; wiring tool scoping is `05`'s privilege surface and stays there.
- Trigger phrases will influence selection only as well as the model reads them. This
  plan ships a mechanism, not a measured selection improvement. If selection quality
  is the actual goal, measure it before and after - otherwise B's benefit is asserted,
  not demonstrated.

## 12. Plan scorecard

| Criterion | Score |
|---|---|
| Compiles | PASS - additive field, parser is new |
| No cycles | PASS - `internal/skills` gains no import |
| No breaking API | PASS for Go; **FAIL for workspace skills carrying unknown frontmatter keys** (§6, intentional, documented) |
| Testable in isolation | PASS - parser is pure |
| Backward-compatible config | PASS - all nine in-repo skills parse unchanged |
| Every function has a test | PASS by Wave 1/2 task structure |

## 13. Rollback criterion

If Wave 3 cannot bound trigger text within the provider's tool-schema limits, lower
the §8 caps first. Only if the surface cannot carry triggers at any useful size does
option **A** become correct. Do not land Waves 1-2 alone - that is option D.

## 14. Implementation record

Landed in three commits. The first two left the plan's central mechanism inoperative
and are recorded here because the failure mode is the one this plan exists to name.

`7f4ddb7` - waves 1-4. `ParseFrontmatterKnown` was written but never called, so
unknown-key rejection (§6) shipped as dead code while `INV-AG-17` asserted it worked.
`make verify` was red: `ParseFrontmatter` and `LoadMarkdown` both exceeded the
function-length limit.

`fdd3c40` - six audit findings, all genuine but shallow: quote-aware flow splitting,
CRLF normalisation, two Python-gate parsing fixes, a named constant, and removal of an
unused `lineNum` parameter. That last one deleted the evidence of a live defect without
fixing it: `parseBlockItem` still dropped non-list indented lines silently. None of the
structural findings were addressed and `make verify` stayed red.

Follow-up review commit - wired `ParseFrontmatterKnown` into `parseMarkdown` behind
`knownSkillKeys`; made `blockItem` hard-error on nested maps and empty items per §6;
made comments and blank lines skippable inside block sequences, including between the
key and its first item; rendered the prompt from `Definition.Triggers` so the field has
a production reader; cut the joined block on a rune boundary; split `LoadMarkdown` and
`ParseFrontmatter` to clear the structure gate; mirrored `knownSkillKeys` into
`verify_agent_config.py` so the gate and the loader cannot disagree.

**Lesson for the next plan of this shape.** §4's reachability table listed the callers
each element would gain. Nothing checked that table against the code at landing, so a
helper with zero callers and an invariant row claiming otherwise both passed every gate.
A reachability table is only worth writing if something verifies it after the fact.
