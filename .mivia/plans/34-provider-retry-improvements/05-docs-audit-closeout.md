# Phase 05 - Documentation, audit, and closeout

Files:

- Modify `docs/architecture/overview.md` (owned by the architecture topic in
  `docs/OWNERS.yaml`).
- No invariant is required unless implementation discovers a new durable
  cross-package contract; if one is needed, allocate its ID at landing time.

Documentation:

- Record the five-attempt transport budget, bounded `Retry-After` behavior,
  z.ai permanent/transient exception, and pre-commit versus committed-stream
  boundary.
- Keep retry settings out of product configuration documentation.

Audit:

- Run the ADLC hostile bug-audit loop against changed files, specifically retry
  storms, request replay, wrapped cancellation, response-body lifecycle,
  provider classification, stream fallback, and privacy.
- Reject findings with targeted tests; fix confirmed findings and repeat until
  zero confirmed bugs remain.

Verification:

```text
go test -count=1 ./internal/provider
go test -race -count=1 ./internal/provider
go vet ./internal/provider
make validate-invariants
make invariants
make verify
go build ./...
```

Do not claim a repository gate passed unless it was executed, and do not bypass
hooks. Return to ADLC Step 0 if any gate requires retry logic outside the
shared provider transport or a new configuration/API boundary.
