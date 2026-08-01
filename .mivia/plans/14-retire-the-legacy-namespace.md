# 14 - Retire `.ai` from the tree entirely

**Status:** Design-ready; one open decision (§4).
**Date:** 2026-07-30
**Depends on:** `04` (implemented). **Blocks:** nothing.
**Blast radius:** LOW - test and doc surface only; no production code names `.ai`.

---

## 1. Why this exists

`04` moved the product namespace to `.mivia/` and forbade compiling `.ai` into
the binary. That held: no production file names it. But eight files still do,
and each was written for a transition that is over.

Re-derived at HEAD 2026-07-30 (`git ls-files | xargs grep -lE '(^|[^A-Za-z0-9])\.ai(/|")'`):

| File | What names `.ai` | Kind |
|---|---|---|
| `internal/workspace/namespace_test.go:44` | the guard regex itself | guard |
| `internal/cli/prompt_test.go` | `TestWorkspaceIgnoresLegacyAIDir` fixture | mutation proof (`04` M1) |
| `internal/tools/secret_path_test.go` | `TestAgentCanEditLegacyAIDir` fixture | mutation proof (`04` M3) |
| `cmd/mivia/main_test.go:31` | `.ai` in a "no startup writes" assertion | regression guard |
| `docs/development/agent-self-prompt.md:21-35` | the hand-migration instructions | transitional doc |
| `.mivia/plans/03`, `04`, `09` | historical references | record |

The standing rule is "no legacy code or features". These are not features - most
are guards *against* legacy returning - so deleting them wholesale would remove
protection rather than debt. The point of this plan is to separate the two.

## 2. The distinction that matters

Three of these tests assert a property that has nothing to do with `.ai`:

- `TestWorkspaceIgnoresLegacyAIDir` really asserts **"a directory other than
  `.mivia/` is not consulted."**
- `TestAgentCanEditLegacyAIDir` really asserts **"a directory other than
  `.mivia/` is ordinary, writable content."**
- `main_test.go` really asserts **"startup writes nothing into the workspace."**

Each is stated in terms of `.ai` only because `.ai` is what it used to be. Named
in terms of the general property, they get *stronger* - `main_test.go` in
particular currently checks two specific paths, and would be better asserting
the directory is unchanged at all.

`TestNoHardcodedLegacyNamespace` is different in kind: it must name `.ai`,
because naming it is the whole mechanism. It cannot be rewritten generically.

## 3. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/cli/prompt_test.go` | rename to `TestWorkspaceIgnoresNonNamespaceDirs`; fixture uses a neutral directory (`.someothertool/`) instead of `.ai/`. Same assertion, no legacy framing |
| 2 | `internal/tools/secret_path_test.go` | rename to `TestAgentCanEditOrdinaryDotDirs`; same treatment |
| 3 | `cmd/mivia/main_test.go` | assert the workspace directory is **empty after startup**, rather than checking `AGENTS.md` and `.ai` by name. Strictly stronger and mentions no legacy path |
| 4 | `docs/development/agent-self-prompt.md` | delete the "Moved from `.ai/` - migrate by hand" block (§4 decides when). Keep the sentence explaining why the namespace is tool-scoped, with the legacy name removed |
| 5 | `.mivia/plans/03`, `04`, `09` | **leave alone.** These are the record of decisions taken; rewriting history to remove a name makes the reasoning unfollowable, and `04` in particular has to say what it moved away from to be comprehensible |
| 6 | `internal/workspace/namespace_test.go` | **leave alone**, subject to §4 |

Note items 1–3 also close a real gap: after them, nothing in the test suite
depends on the legacy name, so `04`'s two mutation proofs keep working while
being about the actual invariant instead of a historical accident.

## 4. Open decision: how long does the guard live?

`TestNoHardcodedLegacyNamespace` is the last thing that will name `.ai`. It
earned its keep - it caught all twelve original hardcodes and would catch a
re-introduction - but its value decays as the migration recedes.

| | Option | Assessment |
|---|---|---|
| **A** | Keep it indefinitely | Costs a few ms per run; keeps the rule mechanically enforced forever. The rule ("one namespace, tool-scoped") outlives the specific string |
| **B** | Generalise it: fail on ANY hardcoded dot-directory outside `internal/workspace` | Enforces the real rule rather than one instance of it, and stops naming `.ai`. More false positives to allowlist (`.git`, `.env`, `.claude`) |
| **C** | Delete it once the docs note goes (item 4) | Fully retires the name; loses the enforcement that made §1's claim true |

**Recommendation: B**, with A as the fallback if the allowlist proves noisy.
B is the only option that both removes the legacy name and keeps the rule
enforced - and the rule it would enforce ("no call site names a namespace
directory; go through `internal/workspace`") is the one `04` §3 actually wanted.
C is not recommended: it trades a working guard for tidiness, and §1's whole
premise is that the guard is why no production file names `.ai` today.

**Decide before implementing item 4**, since deleting the migration note and
keeping the guard is coherent, but deleting both at once is not - that would
retire the name and its enforcement in the same commit, with nothing left to
notice a regression.

## 5. Verification

```bash
go build ./... && go vet ./...
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

- After items 1–3, `git ls-files | xargs grep -lE '(^|[^A-Za-z0-9])\.ai(/|")'`
  must return only `internal/workspace/namespace_test.go` and the three plan
  files. That command is the acceptance check for this plan.
- The renamed tests must still fail under `04`'s original mutations: adding a
  fallback to a non-`.mivia` directory must fail item 1; adding that directory
  to the secret-path patterns must fail item 2.
- Item 3 must fail if startup writes anything into the workspace - verify by
  reintroducing a write, not by inspection.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Consult a second directory when `.mivia/` lacks the file | `TestWorkspaceIgnoresNonNamespaceDirs` |
| M2 | Add that directory to the secret-path patterns | `TestAgentCanEditOrdinaryDotDirs` |
| M3 | Write any file into the workspace at startup | `cmd/mivia` startup test |

## 6. Rollback criterion

If removing the migration note (item 4) turns out to strand users who never
migrated, restore the note - do **not** restore a code fallback. `04` §4 forbids
one permanently, and a documented manual `mv` was the accepted cost of that
decision.
