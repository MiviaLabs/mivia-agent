# Phase 05 - Invariant, audit, and closeout (superseded; do not implement)

The proposed invariant depends on invalid provider-wide sampling assumptions.
Allocate no invariant identifier until a replacement design is validated.

Files:

- Modify `.mivia/invariants.md` at landing time, allocating the lowest free
  `INV-AG-*` identifier rather than assuming `INV-AG-32` remains available.
- Add or update the exact test names referenced by the invariant.

Record the invariant covering active suppression, unset byte-compatibility, and
nil-dialect omission. Run the hostile bug-audit loop, including a check that
direct chat and agent-loop requests behave identically for the same binding.

Verification:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `make invariants`
- `make verify`

Manual residual check: send one configured reasoning request and one unset
request to representative provider endpoints, without recording credentials,
prompts, or provider payloads in repository artifacts.
