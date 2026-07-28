# Active-Run Recovery Plan

Status: Implemented
Current phase: Durable run ownership and recovery
Last verified: 2026-07-28
Next action: Review and merge the implementation commit after full CI verification

## Goal

Allow a recreated session/coordinator to inspect and control orchestration runs
whose ledger state survived the original dispatcher, without pretending that a
new process owns an active executor it cannot control.

## Source evidence

- `internal/coordinator/coordinator.go`: `Coordinator.Spawn` stores idempotency
  handles only in process memory; `Join` and `Cancel` require `RunHandle`.
- `internal/ledger/repository.go`: the repository is the persistence boundary,
  but the public CLI previously constructed a private memory repository per
  dispatcher.
- `internal/cli/orchestrate.go`: lifecycle handles are scoped by dispatcher;
  the default repository must be shared and an external repository must be
  injectable.
- `internal/cli/dispatcher.go`: `NewSessionDispatcher` is the construction
  seam for session-scoped orchestration dependencies.

## Scope

- Share the built-in in-memory repository across recreated default sessions.
- Add `NewSessionDispatcherWithLedger` for durable/custom repositories.
- Associate lifecycle handles with their repository, allowing same-repository
  session recreation while preserving cross-repository isolation.
- Bound coordinator idempotency-handle retention and clean up failed run
  creation.
- Make cancellation terminalize queued descendants.
- Do not claim active execution can survive a process crash; durable executor
  ownership/restart is a future phase.

## Expected files

- `internal/cli/dispatcher.go`
- `internal/cli/orchestrate.go`
- `internal/cli/orchestrate_lifecycle.go`
- `internal/cli/delegate.go`
- `internal/cli/dispatch.go`
- `internal/coordinator/coordinator.go`
- `internal/coordinator/handle_lifecycle.go`
- `internal/coordinator/validation.go`
- focused CLI/coordinator tests

## Acceptance criteria

- Recreated dispatchers using the same repository can operate on stored run
  handles; a different repository cannot access them.
- Completed idempotency handles are evicted after bounded retention.
- Queued descendants become canceled after run cancellation.
- Failed cleanup errors are returned, not discarded.
- Public orchestration responses expose references/status, not raw model output
  or error text.
- Existing focused tests, race tests, vet, structure checks, and diff checks
  pass.

## Verification

```text
go test ./internal/ledger ./internal/coordinator ./internal/subagents ./internal/cli -count=1
go test -race ./internal/ledger ./internal/coordinator ./internal/subagents ./internal/cli -count=1
go vet ./internal/ledger ./internal/coordinator ./internal/subagents ./internal/cli
make structure-check
make verify
```

## Risks and human review

- Process crash recovery for an actively running pool remains out of scope;
  production durable execution requires ownership leases, heartbeats, and a
  restart/reconciliation protocol.
- Review repository lifetime, retention, and cancellation semantics before
  enabling a non-memory backend in production.
