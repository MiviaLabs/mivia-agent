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

## Reviews

- Before merge-ready claims, run adversarial review of changed behavior and tests (skill: `adversarial` / `secure-change` / `verify-change` as applicable).
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
- Hooks/adapters: assert real payload shapes and scrubbed output shapes.
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
