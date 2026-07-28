# Validation Report: Wave 5 – t7 Benchmarks

## Questions

### 1. Can MemoryLedgerRepository benchmarks reproduce the existing store_bench_test.go pattern?

**PASS.**  
`store_bench_test.go` uses `import "testing"`, `b.N` loops, `b.Run` sub-benchmarks, `b.ResetTimer()`, and `b.Fatal(err)` — all standard Go benchmarking idioms. `MemoryLedgerRepository` has zero external dependencies (`NewMemoryLedgerRepository()` returns a ready instance) and `ledger_test.go` already demonstrates constructing `RunSnapshot`/`TaskSnapshot` values. The pattern is directly reproducible.

### 2. For StorageLedgerRepository, does it need storage.OpenSQLite(":memory:") or storage.NewMemory()?

**PASS — use `storage.OpenSQLite(":memory:")`.**  
The plan explicitly says "StorageLedger + SQLite". `StorageLedgerRepository` accepts a `storage.Store` interface; both `storage.NewMemory()` and `storage.OpenSQLite(":memory:")` implement it. Using `:memory:` SQLite tests the real serialization, transaction, and projection-rebuild code paths without disk I/O overhead. With `-benchtime=1x`, performance is not a concern.

### 3. Is there any risk of the benchmark hanging or taking too long?

**PASS — very low risk.**  
The verification command includes `-benchtime=1x`, which sets `b.N = 1` — each benchmark body executes exactly once. All backends (memory, in-memory SQLite) complete in milliseconds. The 120s plan timeout is extremely generous.

## Verdict

**PASS** — t7 is well-specified, all required APIs exist, existing test patterns are proven, and the `-benchtime=1x` guardrail prevents any runtime issues.
