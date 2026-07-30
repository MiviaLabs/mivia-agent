---
name: architecture-review
description: Structural review of a mivia design or plan; package boundaries, dependency direction, abstraction level, pattern fitness, speculative generality. Rejects over-engineering before code exists.
triggers:
  - architecture review
  - design review
  - review this plan
  - is this over-engineered
  - package boundaries
  - abstraction check
---

# Architecture Review

Advisory structural review. Runs at ADLC Step 0 alongside the hostile challenge
panel, not ahead of it. Its findings are input to the orchestrator's disposition
step; it does not by itself reject a plan.

This skill does not ask "is it correct?" (`bug-audit`), "is it safe?"
(`secure-change`), or "does it pass?" (`verify-change`). It asks whether the
structure introduced by *this change* is the smallest one that satisfies the
requirement — and, when it is not, whether the answer is to delete it or to
sequence it.

## Scope

Steps 2, 3 and 4 apply **only to structure introduced or modified by the change
under review**. Shipped code that matches a flagged shape is out of scope; raise it
separately. Reviewing HEAD as though it were being proposed produces
findings against a dozen deliberate decisions and buries the real ones.

## Read First

- `AGENTS.md`
- `.mivia/rules/05-adlc-agentic-development-lifecycle.md` — Step 0 is this skill's home
- `.mivia/rules/30-go-standards.md`, `50-concurrency-subagents.md`, `60-tools-project-language-generic.md`
- `.mivia/invariants.md` — every existing INV row for the packages in scope
- `docs/architecture/overview.md` and `docs/OWNERS.yaml`
- The plan or diff scope named by the user

## Method

1. **Map it.** Package boundaries in scope, dependency direction, and every existing
   caller. Name the layer each new type belongs to.

2. **Reachability, measured at HEAD.** For each new abstraction, guard, or config
   knob, report three things separately:
   - Production (non-test) callers present at `git HEAD` **before** this change,
     as file:line, reproducible by a stated grep.
   - Callers this change would add.
   - The concrete principal, actor, or tenant it defends against **today**.

   A count of zero in the first line is not by itself a verdict. Classify it:

   | Case | Verdict |
   |---|---|
   | Nothing will ever reach it; no principal is contracted | **Delete** — speculative generality |
   | A later wave of this change makes it reachable | **Sequencing** — name the wave that must land with or before it. Never recommend deleting it, and never recommend landing the reachable half alone |
   | This change itself adds the caller | Not a finding. The change is the remedy |

   Collapsing "defends nothing" into "delete it" is the central failure mode of this
   review. A guard that must land *before* the thing that makes it reachable looks
   identical to dead weight at Step 0. Distinguish them explicitly, in writing.

3. **Necessity ladder.** For each structural element introduced by this change, ask
   in order: delete it / inline it / a concrete type / an interface. Stop at the
   first rung that satisfies the requirement. Standing above that rung requires a
   written reason.

4. **Pattern fitness.** Compare the named pattern against the Go-idiomatic simpler
   form. Interface with one implementation, factory for one type, bus with one
   subscriber, registry with one entry, wrapper that only forwards — each needs a
   written reason. These shapes are *candidates*, not verdicts; price the move
   before reporting one (see Rules).

5. **Direction and cycles.** Verify `internal/` layering; no import that inverts
   ownership. Note which gate proves what:
   - `go build ./...` — proves no import cycle (Go rejects cycles at compile time).
   - `go list -deps ./internal/<pkg>` and the package's own import block — the only
     way to check *direction*. No script does this.
   - `python3 scripts/check_go_structure.py --strict --all` — file and function
     structure limits only. It does **not** inspect imports, direction, or cycles;
     do not cite it as evidence for this step.

6. **Blast radius.** Which invariant families does this touch (`INV-AG-*`,
   `INV-SEC-*`, `INV-TUI-*`)? Every accepted structural decision must name the
   invariant it preserves, or the new invariant it needs. Ids are allocated at
   landing time, lowest free per prefix. `make verify` rejects a duplicate id, but
   nothing checks that a row's stated property is one the code actually has, or that
   its tests are selected by `make invariants` — verify both by hand.

7. **Testability at the seam.** Can each boundary be tested without the layers above
   it? An abstraction whose only justification is enabling a mock is a finding — say
   what it would take to test the concrete type directly.

8. **Doc ownership.** A structural change updates the owned `docs/architecture/*`
   path for the topic. Never a parallel doc. See `.mivia/rules/40-docs-ownership.md`.

## Under-engineering (symmetric check)

YAGNI is not the only failure mode. Flag the reverse: a foundation omitted now that
cannot be retrofitted without a rewrite.

Decidable test — it is a **foundation** (flag its absence) if adding it later would
require **either**:

- **(a)** editing callers that are not themselves being changed; or
- **(b)** reinterpreting data, files, or wire formats already written by a shipped
  version that carries no field identifying which version wrote it.

It is a **feature** (do not flag) only if adding it later is a local change at one
site **and** no already-persisted state must be reinterpreted.

By that test: `context.Context` plumbing and cancellation propagation are foundations
under (a). A schema version marker is a foundation under (b) — it is one line in one
function, so test (a) alone scores it a feature and is wrong. An extra config knob, a
second provider, or a cache is a feature.

**Wave-ordering caveat.** When a change is split into waves and one wave alone
is reachable-unsafe, the later wave is a foundation for the earlier one regardless of
this test. Never recommend landing the wave that scores lower on the necessity ladder
in isolation.

## Rules

- Advisory and report-only. Does not write production code, commit, or push. Its
  findings are dispositioned by the orchestrator alongside the challenge panel's.
- Severity never gates approval; open structural gaps block `PASS`.
- **Price the move.** A finding must name (a) the simpler form, (b) the exact
  production sites that change, and (c) what the current form buys that the simpler
  form does not — dependency direction, an import-cycle constraint, a compile-time
  boundary, or a seam pinned by an invariant row. If (c) is non-empty, it is not a
  finding regardless of implementation count.
- **Justification must predate the change.** "It's cleaner", "more testable", and
  "we'll need it later" are not justifications. A justification cites exactly one of:
  - a production (non-test) caller present at `git HEAD` before this change, as
    file:line and reproducible by a stated grep; or
  - an invariant row already present in `.mivia/invariants.md` at HEAD, by id; or
  - a requirement stated in an owned document under `docs/` (see `docs/OWNERS.yaml`)
    at HEAD, by path and section.

  The design under review is not its own justification — the author wrote it in the
  same edit. A caller this change adds is not a justification. An invariant row this
  change allocates is not a justification. Intent to do something later is not a
  contract.

  Cite durable sources only: code at HEAD, `.mivia/invariants.md`, and owned `docs/`
  paths. Do not cite workflow documents — they are transient and this skill must
  outlive them.
- This skill cannot run mutations or probes. When a structural question turns on
  measured behaviour — a cross-process regression, a mutation surviving the suite —
  say so and hand it to the challenge panel or `bug-audit`. Do not guess.
- Distinct from the `engineering:architecture` plugin skill, which authors ADRs. This
  one reviews against mivia's own rules, invariants, and gates.

## Required Report

Always emit the compact `mivia-report/v1` from `.mivia/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — boundaries sound; every abstraction introduced has a live production
  caller at HEAD, a recorded prior requirement, or a stated sequencing constraint;
  invariant mapping complete; no cycle; no inverted import.
- `BLOCK` — an abstraction that nothing will reach and nothing has contracted, an
  inverted dependency, an import cycle, an unmapped invariant, or a missing
  foundation by the test above. A sequencing finding is **not** a `BLOCK` — report
  it as a finding with the required wave named.
- `PARTIAL` — useful findings, but plan detail, source access, or measured evidence
  this skill cannot gather remains outstanding.
- `NOT_RUN` — no design or diff in scope.
