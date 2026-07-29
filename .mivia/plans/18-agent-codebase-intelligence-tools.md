# 18 — Agent codebase intelligence tools

**Status:** Proposal / Problem statement.
**Date:** 2026-07-30
**Depends on:** nothing. **Blocks:** nothing.
**Blast radius:** LOW — additive; no behaviour changes for existing agent workflows.

## 1. The problem

A coding agent (mivia) auditing or implementing a change must first understand
the codebase's structure. Today this requires 50-100+ primitive tool calls per
audit session:

| Task | Current approach | Typical calls |
|---|---|---|
| Find all implementations of an interface | `grep` for method signatures + `glob` for files + `read_file` each candidate | 5-10 |
| Trace an error path through layers | `grep` for the sentinel name + `read_file` at each definition + `grep` for `errors.Is` callers | 5-8 |
| Find all callers of a function | `grep` for function name, filter declarations from calls manually | 3-5 |
| Understand what changed in a diff | `git diff --stat` + `read_file` each changed file + `grep` for new symbols | 10-20 |
| Find missing interface implementations | Build the code and read every compiler error + fix each one | 5-10 |

The cost is not the number of calls — it is the **context-switching**: each
`read_file` requires loading a new file into context, discarding what was there,
and orienting. After 50+ files, that context churn causes missed connections
("I saw that function in store.go 30 calls ago; where was it?").

## 2. Proposal: structured intelligence tools

Replace the ad-hoc grep+read chain with purpose-built tools that return
structured answers, not raw file content.

### Tool A: `search_references` (or extend existing `grep`)

Given a Go symbol name (function, type, interface, sentinel), return:

```
search_references("storage.Store")
→
implementations:
  - storage.Memory (store.go:67)
  - storage.SQLite (store.go:186)
  - ledger.countingStore (storage_catchup_test.go:12)
  - storage.flushSQLite (store_agent_integration_test.go:171)
  - storage.gatedStore (queue_test.go:10)
callers:
  - ledger.NewStorageLedgerRepository (storage.go:55)
  - ledger.StorageLedgerRepository (storage.go:15)   # embeds
```

**How:** The orchestrator runs `go doc` + AST parsing of the target package, or
uses gopls analysis cache. No new deployment — this is metadata the language
server already produces.

### Tool B: `trace_value`

Given a Go sentinel/constant name, trace its definition and error-is/cmp paths:

```
trace_value("ErrClaimHeld")
→
defined:     storage/store.go:20
returned by: storage.Memory.ClaimRun (store.go:181)
             storage.SQLite.ClaimRun (store.go:416)
translated:  storage_claims.go:27  (storage.ErrClaimHeld → ledger.ErrClaimHeld)
             recovery.go:95        (ledger.ErrClaimHeld → ErrRunHeldByAnotherExecutor)
checked by:  spawn.go:63           (errors.Is(err, ledger.ErrClaimHeld))
             recovery.go:93        (errors.Is(err, ledger.ErrClaimHeld))
```

**How:** Grep for the symbol name + `errors.Is` patterns + `==` comparisons,
then group by file. This is grep with structured output — one call instead of
five.

### Tool C: `diff_analysis`

Given a git ref range (e.g. `HEAD~1..HEAD`), return structured change summary:

```
diff_analysis("HEAD~1..HEAD")
→
files_changed:
  internal/storage/store.go:
    new_types: [Claim]
    new_methods: [Memory.ClaimRun, Memory.ReleaseClaim, Memory.ClearClaim,
                  SQLite.ClaimRun, SQLite.ReleaseClaim, SQLite.ClearClaim]
    interface_additions: [Store.ClaimRun, Store.ReleaseClaim, Store.ClearClaim]
  internal/coordinator/recovery.go:
    new_functions: [resumeValidateAndMark]
    modified_functions: [ResumeInterruptedRun]  # +defer+claimed guard
```

**How:** Diff parsing + AST scanning for function/type/method boundaries.
Same data `git log -p` produces, but structured as a checklist.

### Tool D: `find_untested_code`

Given an interface, find every method and cross-reference against test files:

```
find_untested_code("store.Store")
→
store.Store:
  Append:       tested (store_test.go:20)
  Events:       tested (store_test.go:25)
  ClaimRun:     tested (storage_fence_test.go:15, ledger_test.go:690)
  ReleaseClaim: tested (ledger_test.go:702)
  PutContent:   UNTESTED — no test calls PutContent directly
```

**How:** Grep function names in `_test.go` files matching the package. Simple
but saves the manual "did anyone test this?" search.

## 3. Risk / cost

- **False negatives:** AST parsing in Go is reliable within a single module,
  but cross-module references may be missed. The tools should return "not
  found" rather than a partial set.
- **Performance:** Each tool must complete in <1s. If it requires `go build`,
  it's too slow — use gopls or textual analysis instead.
- **Scope creep:** These tools replace the *orchestrator's* code-reading
  workflow, not the agent's tool-use workflow. They should be implemented as
  orchestrator-side helpers, not as subagent tools.

## 4. Success criterion

A bug audit session that today requires 50+ tool calls should require 10-15.
The time to answer "who implements Store?" goes from ~2 minutes to ~5 seconds.
