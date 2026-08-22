# CLI Split Handoff — Slice 6: Router Cleanup + make verify Green

Repo: `/home/mac/projects/mivialabs/mivia-agent`, branch `dev`.
Start after `a3c4a430` (extract internal/clichat) is the tip of `dev`.
Confirm: `git log --oneline dev | head -1` shows `a3c4a430`.

---

## What this slice does

The five domain-package extractions are done. This is the final
cleanup commit that makes `make verify` fully green and locks in
the measured import edge count.

Two independent problems to fix, both required before the slice
is done:

---

## Problem 1 — diff-coverage gate fails on pre-existing lines

`make diff-coverage` reports 3 uncovered lines in
`internal/workflows/definition/failures.go` (lines 20, 21, 85).
This file was last changed at `491c7789` (workflows-merge commit,
before the cli split). The diff-coverage gate compares against
`main`, so those lines appear in scope.

**Fix:** add test cases to
`internal/workflows/definition/failures_test.go` (or create it)
covering:

- `contextErrorFromRun` with `context.DeadlineExceeded` input → returns
  `context.DeadlineExceeded` (line 20)
- `contextErrorFromRun` with `context.Canceled` input → returns
  `context.Canceled` (line 21)
- The dedup path in `boundFailureLines` (or equivalent): feed duplicate
  failure lines and confirm only one copy appears in the output (line 85)

Read the function bodies at those lines first to confirm exact signatures.

**Verify fix:**
```bash
cd /home/mac/projects/mivialabs/mivia-agent
go test ./internal/workflows/definition/... -count=1 -v -run TestContext
make diff-coverage
```
`make diff-coverage` must exit 0 before proceeding.

---

## Problem 2 — clichat_aliases.go is 794 lines (structure soft-warn)

`internal/cli/clichat_aliases.go` is 794 lines. The structure gate
emits a soft warning at 500 lines; it does not hard-fail on file LOC.
However it is good practice to split it now rather than carry the debt.

This file is a type-alias shim: it re-exports symbols that moved to
`internal/clichat` so callers in `internal/cli` and
`internal/legacytui` compile without per-file import updates.

**Option A — split the alias file (preferred)**

Split into thematic groups, e.g.:
- `internal/cli/clichat_aliases_repl.go` — REPL and chat types
- `internal/cli/clichat_aliases_ui.go` — rendering, dialog, bubble types
- `internal/cli/clichat_aliases_stack.go` — stack types
- `internal/cli/clichat_aliases_session.go` — session/catalog types

Each split file keeps `package cli` and imports only
`internal/clichat`. No logic changes — pure alias redistribution.

**Option B — eliminate the shim entirely**

For each alias in the file, find every caller still in `internal/cli`
and update it to `clichat.TypeName` directly, then delete the alias.
This is cleaner but requires touching more files. Only viable if the
alias count is small enough.

Pick whichever option keeps all gates green. Verify after:
```bash
python3 scripts/check_go_structure.py --strict --all
go build ./...
```

---

## Problem 3 — finalize import-layers.json

The policy description still references "interim cap" language.
Update it to reflect the final state:

- Change `"edgeCap": 400` description text to say the final measured
  count is 398 edges (or whatever `python3 scripts/check_import_layers.py`
  reports), cap set to 400 with 2-edge headroom.
- Remove the phrase "Known debt, not a target" — this is now the final
  measured tree, not an estimate.
- Keep the deny list unchanged.

**Do not change any allow-list entries or the edgeCap number itself.**

---

## Final gate run

Run the full suite (not just verify-fast) before committing:

```bash
cd /home/mac/projects/mivialabs/mivia-agent
gofmt -l ./internal/cli/ ./internal/clichat/ ./internal/workflows/definition/
go build ./...
go vet ./...
git diff HEAD -- internal/cli/characterization_test.go   # must be empty
go test ./internal/cli/ -run TestCharacterization -count=1 -v
go test ./internal/workflows/definition/... -count=1
go test ./... -count=1
python3 scripts/check_go_structure.py --strict --all
python3 scripts/check_import_layers.py
make diff-coverage
make verify
```

`make verify` must exit 0. That is the acceptance criterion for this slice.

---

## Commit

One commit covering all three fixes:

```
refactor(cli): slice-6 router cleanup and make verify green

Fix diff-coverage failure: add tests for contextErrorFromRun and
boundFailureLine dedup path in internal/workflows/definition.

Split internal/cli/clichat_aliases.go (794 lines) into thematic
groups under the 500-line soft cap.

Finalize import-layers.json description: measured 398 edges, cap 400.
```

Pre-commit hook runs verify-fast. `make verify` must also pass.
Never use `--no-verify`.

---

## Hard invariants

1. `internal/cli/characterization_test.go` must not change.
2. `make verify` exits 0 — this is the acceptance criterion.
3. `go build ./...` and `go vet ./...` clean.
4. No gofmt issues in touched files.
5. No TODO/FIXME/HACK/XXX in comments.
6. `python3 scripts/check_go_structure.py --strict --all` → 0 hard failures.
7. `python3 scripts/check_import_layers.py` → ok, ≤ 400 edges.
