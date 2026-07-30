# Agent Quality

## Tests

- Write tests before or alongside production code. Tests are not deferred.
- Every exported function needs at least one success-path test and one relevant error-path test unless the task explicitly narrows the contract.
- Use `t.TempDir()` for filesystem writes; real temp Git repos for Git behavior.
- Helpers call `t.Helper()` and fail through `testing.TB`; they must not return opaque booleans that hide failure cause.
- Table-driven tests for multi-case behavior; failure messages include got/want context.
- Do not mock the unit under test when the risk is real filesystem, Git, or CLI wiring.

## Mutation Proofs

Required for every:

- reject / deny / guard
- stale-check / path-policy check
- idempotency claim
- secret-scrubbing rule
- hook-bypass block
- concurrency cap / process-farm prohibition

Procedure: apply the described code mutation → confirm the named test fails → revert → record result in the completion report. Inspection-only “mutation proof” is invalid.

## Regression Tests

Every commit of type `fix` **must** include a reproduction test that exercises the
broken invariant before the fix and passes after. Every commit of type `feat` **must**
run the invariant tests for the affected area(s) listed in `.mivia/invariants.md`.

- If the broken invariant is already listed in `.mivia/invariants.md`, reference it by ID
  in the commit body (e.g. `Regression: INV-TUI-2 (TestTuiTickMsgAlwaysRequeuesPoll)`).
- If no existing invariant covers the regression, create a new entry in the manifest
  and add the test.
- Trivial fixes (typo, comment, formatting) may write `Regression: none (<reason>)`.

## Invariant Verification

Before committing changes that touch a listed invariant area (TUI, agent loop,
security/privacy), run `make invariants` to execute the relevant test suite(s).

- Use `make invariants ARGS="-run 'TestTUI|TestBridge'"` to scope by area.
- Full validation: `make validate-invariants` (cross-references all manifest test
  names against the codebase; fails on stale entries).
- The invariant manifest lives at `.mivia/invariants.md`. Keep test entries as exact
  `func Test` names, not wildcards.
- **Liveness invariants** must have at least one stress test that exercises the
  invariant under scheduling pressure or concurrent dispatch. Pure unit tests on
  a single call sequence are insufficient. See `internal/cli/liveness_stress_test.go`.

## Reviews

- Before merge-ready claims, run adversarial review of changed behavior and tests (skill: `bug-audit` / `secure-change` / `verify-code-change` as applicable).
- Mentally remove each implementation guard and verify at least one test would fail.
- Residual risk must name the missing test, fixture, or external behavior still unproven.

## Critical Drift Guard

When adding or changing a durable standard, forbidden pattern, hook policy, security invariant, or repeated agent failure mode:

1. Prefer a static check under `semgrep/` when the rule can be checked statically.
2. Every Semgrep rule change updates tests with one bad fixture (fails) and one good fixture (clean).
3. Run the relevant Semgrep/hook test target before commit once those targets exist.
4. Do not use Semgrep suppression comments to bypass repo policy; fix code, fix rule, or document a reviewed exception outside the scanned path.

## Coverage

- Coverage percentage is secondary. **Contract coverage** is required.
- Cover success paths, error paths, malformed inputs, idempotency, secret hygiene, and no-network constraints where applicable.
- Hooks/adapters: assert real payload shapes, and output shapes under a configured redaction policy — asserting scrubbing with no policy installed passes trivially (rule 10).
- Fake-only closure is not acceptable for shipped commands: every user-facing `mivia` command and approved adapter needs at least one real subprocess or built-binary integration path.
- Keep fakes for unit isolation and edge cases; do not treat them as proof of real CLI wiring.
- If a real integration path is gated on local tool availability, the gate is explicit, the missing prerequisite is reported, and CI still covers built-binary paths that do not need third-party CLIs.

## Verification Commands (baseline)

Once the module builds:

```bash
go test ./...
go vet ./...
go build -o bin/mivia ./cmd/mivia
```

Add package-scoped tests for the packages you touch. Prefer the narrowest meaningful suite first.
