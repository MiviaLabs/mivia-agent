# Plan 57 - Worktree Session Deletion

Status: Validated - implementation ready

## Goal

Delete the current user's durable sessions when a worktree is removed. Prevent
an old process from restoring data after removal. Keep a later same-name
worktree separate from the removed physical worktree.

## Definitions

`WorktreeInstance` is an immutable pair of a worktree name and a random ID.
The ID identifies one physical worktree lifetime. The lifecycle is scoped by
the main repository workspace, worktree name, and ID. Session data remains
scoped by workspace and subject.

Historical session rows with no instance ID are legacy rows. The program does
not delete or claim them by a path or worktree-name guess.

Legacy rows remain available only to an unbound legacy List or Load request.
An instance-bound request never reads a legacy row. Adoption does not assign an
ID to, hide, or delete legacy rows. Destructive legacy removal fails closed.

## Durable model

### Marker and binding

CLI owns the non-tracked marker lifecycle. The VCS package stays independent
from SQLite and marker state. The marker is an atomically written, private file
at a fixed path below the canonical worktree root. It contains a version and
the random worktree instance ID. Marker reads reject a non-canonical root,
symlink escape, missing file, bad format, and mismatched ID.

At session setup, CLI resolves the canonical managed worktree root, reads the
marker, validates it against the active catalog instance, and stores the exact
`WorktreeInstance`, canonical root, and session directory in `chat.Session`.
Main-tree and unmanaged worktrees have no binding.

The session never resolves an instance by name after setup. This prevents an
old process from adopting a same-name replacement worktree. `currentDirContext`
does not supply later save metadata for a bound session.

### Schema v7

Schema v7 adds:

- `worktree_instances(workspace_id, worktree, instance_id, canonical_path,
  state, created_at, updated_at)` with states `creating`, `active`,
  `deleting`, and `deleted`. Its primary key is `(workspace_id, worktree,
  instance_id)`. A partial unique index on `(workspace_id, worktree)` applies
  while state is not `deleted`.
- `instance_id` columns on `worktree_routes`, `chat_session_dirs`,
  `chat_sessions`, `context_sessions`, and `chat_session_admissions`. Columns
  are nullable for historical rows.
- exact indexes for `(workspace_id, subject_id, worktree, instance_id)` cleanup
  and active-instance lookup.

The active-route uniqueness is workspace plus worktree. A new instance cannot
register until an earlier same-name instance is fully `deleted`. An old cleanup
always predicates on the exact instance ID. It cannot delete a new route or
new session data. A route uniqueness constraint includes the instance ID. It
cannot replace another active instance route.

Migration never backfills an instance ID from a legacy worktree name. Existing
managed worktrees need explicit adoption. Add `worktree adopt NAME`. It proves
the canonical current root, requires an unambiguous legacy route, then creates
the marker and active instance. `worktree create` rejects an existing legacy
worktree and tells the user to adopt it. Ambiguous or missing metadata makes
destructive removal fail closed.

Schema repair checks the full v7 contract on each open: tables, nullable
columns, indexes, and constraints. It adds only missing parts and is
idempotent. The repair supports every partial state that the ordered v7 schema
operations can leave. It preserves legacy rows.

## Lifecycle

### Create and recovery

1. Preflight the catalog. Reject the name if any `creating`, `active`, or
   `deleting` instance exists. Recover the `deleting` instance before reuse.
2. Allocate an ID and persist a `creating` instance with the canonical expected
   path.
3. Create the Git worktree. CLI writes its marker after it verifies the
   canonical path.
4. In one SQLite transaction, register the route with the ID and conditionally
   set the same `creating` instance to `active`.
5. If Git creation fails, conditionally delete the same `creating` row.
6. If the process fails after Git succeeds, retry finds the stored expected
   path, proves that Git has that worktree there, then writes or validates the
   marker for the stored ID and completes registration. It never allocates a
   second ID.

### Delete and recovery

1. Resolve the worktree and read its marker. Refuse a missing or mismatched
   marker.
2. Conditionally change the exact active instance to `deleting` in a
   transaction. All new reads and writes for it now fail with
   `ErrWorktreeDeleted`.
3. Remove the Git worktree.
4. In one SQLite transaction, clean this subject's matching session data,
   remove its route, and conditionally set the exact `deleting` instance to
   `deleted`.
5. If Git removal fails, return the instance to `active` only after the same
   marker and Git worktree still exist.
6. If cleanup fails after Git removal, leave `deleting`. `worktree remove NAME`
   resolves the retained `deleting` record and completes cleanup even when the
   Git worktree no longer exists. The list and dialog show this as recovery
   required. They offer the same remove action. A retry selects only the exact
   removed physical instance. It cannot target a later same-name instance.

Git and SQLite cannot form one transaction. The durable state makes each
boundary recoverable.

## Read and write fence

Every read and write carries the immutable binding or validates the stored
binding in the same SQLite transaction. The query checks workspace, worktree,
instance ID, and `active` state. Every state transition has the same exact ID
and prior-state predicate: `active` to `deleting`, then `deleting` to
`deleted`.

The fence applies to:

- `EnsureSession`
- context `Commit` and `Advance`
- named snapshot `SaveSession`
- session admission writes
- session-ID rotation and retry paths
- named snapshot list and load
- live context load and session restore
- admission load and route lookup
- picker list and filter paths

A `creating`, `deleting`, `deleted`, missing, or mismatched instance returns
`ErrWorktreeDeleted`. The UI reports the removed worktree session and does not
change directory or restart it. A stale process cannot read or write a
new same-name instance.

`contextmgr.Preparation` and its token retain the binding. The commit request
builder copies it to every `CommitRequest`. This fences turn commits that do
not pass through chat snapshot APIs.

Scoped storage read APIs accept `WorktreeInstance`. `Store.Load`, `LoadSession`,
session list, admission load, route lookup, and picker restore pass the retained
binding to one fenced read transaction. A scoped result must match that binding.
Only an explicit unbound legacy read can return a null-ID legacy row.

## Cleanup semantics

Cleanup matches workspace, subject, worktree name, and instance ID. It matches
session metadata, not a directory path, so subdirectory sessions are covered.

One storage transaction:

- Deletes matching snapshot rows, admissions, and directory rows.
- Tombstones matching live context rows with the existing payload revoke,
  audit, context-tombstone, admission, and directory lifecycle.
- Handles a snapshot and context session with the same name.
- Deletes the exact matching route.
- Moves the exact instance to `deleted`.

The implementation uses private transaction helpers. It does not loop over
public delete methods with independent transactions.

Deletion is authorized by workspace and subject. It deletes the caller's data.
Other subjects retain their instance-bound rows. The lifecycle read/write fence
hides and protects those rows. An authorized owner cleanup uses the same exact
instance cleanup path and applies normal retention and tombstone rules. This
policy prevents one subject from deleting another subject's data.

## API changes

Add `WorktreeInstance`, `ErrWorktreeDeleted`, and a scoped
`WorktreeSessionCatalog` in `internal/contextstate`.

```go
BeginWorktreeDeletion(context.Context, Principal, WorktreeInstance) error
DeleteWorktreeSessions(context.Context, Principal, WorktreeInstance) (int, error)
RegisterWorktreeInstance(context.Context, Principal, WorktreeInstance, string) error
```

Extend session metadata, requests, and runtime binding with `InstanceID`.
Extend `contextmgr.Preparation`, its token, and `CommitRequest` with the exact
binding. Extend `contextstate.AdvanceRequest`, `chat.advanceContextHead`, and
storage `Advance` with the same binding. Keep VCS independent from SQLite. CLI
owns marker creation and lifecycle orchestration. Chat owns its retained
binding. Storage owns schema, validation, read/write authorization, and atomic
cleanup.

## Files

Modify:

- `internal/contextstate/session_catalog.go` and request/error definitions
- `internal/storage/context_schema.go` plus migration, repair, and table tests
- `internal/storage/chat_sessions.go`
- `internal/storage/context_store.go`
- `internal/storage/context_lifecycle.go`
- `internal/chat/context_catalog.go`, `context_integration.go`, and session
  runtime binding paths
- `internal/contextmgr/commit.go`, `commit_request.go`, and their tests
- `internal/cli/context_setup.go`
- `internal/cli/worktree_command.go`
- `internal/cli/worktree_dialog.go`

Add focused storage, marker, chat, CLI, and TUI integration tests. Update
owned user documentation if the recovery behavior needs operator actions.

## Test plan

1. Seed representative v6 routes, directory rows, snapshots, context sessions,
   and admissions. Migrate, reopen, and prove unbound legacy List and Load
   preserve nullable IDs. Prove an instance-bound reader cannot read them,
   adoption does not assign or delete them, and destructive legacy removal
   fails closed.
2. Test repair from every supported predecessor and from dirty v7 states before
   and after each table, column, and index operation. Reopen twice and prove
   idempotence.
3. Test marker creation, canonical subdirectory discovery, main-tree absence,
   missing marker rejection, and marker mismatch rejection.
4. Crash after Git creates the worktree and before marker write. Retry must use
   the stored expected path and stored ID. Test marker symlink and root checks.
5. Test preflight rejection for same-name `creating`, `active`, and `deleting`
   instances before Git creates a replacement directory.
6. Test current-instance cleanup for multiple snapshots, live sessions,
   admissions, routes, payload revocation, audits, and tombstones.
7. Test same-name snapshot and context sessions in one instance.
8. Test another worktree, main-tree session, and other-subject retention. Test
   that other-subject data is hidden and an authorized owner cleanup applies
   retention rules.
9. Use deterministic test hooks for a check-then-act race. Pause each stale
   Ensure, Commit, Advance, snapshot save and overwrite, admission save and
   delete, rotation, and retry after it derives the old active binding and
   before its fenced mutation. Begin deletion, release the stale mutation, and
   require `ErrWorktreeDeleted`. Assert exact rows, revisions, and payload
   counts do not change.
10. Test read fences for named list/load, live load, restore, admission load,
    route lookup, and picker filter. Verify UI shows the error and does not
    change directory or restart the removed session.
11. Test same-name recreation after old cleanup. New writes succeed. An old
   bound process still fails.
12. Test a new same-name route/session cannot be deleted by an old cleanup
   retry.
13. Test Git failure keeps session data and reactivates only the same instance.
14. Test Git-success/catalog-failure recovery through CLI and TUI recovery.
    The retry cleans only the old instance.
15. Test `worktree adopt NAME`, existing-worktree create rejection, and
    ambiguous adoption refusal.
16. Run CLI and TUI end-to-end tests from a worktree subdirectory with a real
    context-enabled session. A fresh main-tree process cannot list or load the
    removed current-instance session.
17. Run the lifecycle interleaving tests under the Go race detector.

## ADLC waves

1. Add schema v7, immutable types, marker contract, repair, and RED migration
   tests.
2. Add storage lifecycle and cleanup RED tests, then implementation.
3. Thread retained binding through chat, context manager, reads, and writes.
   Add stale-reader and stale-writer RED tests, then implementation.
4. Wire CLI and TUI create, adopt, remove, and recovery. Add end-to-end RED tests,
   then implementation.
5. Run hostile storage, recovery, lifecycle, security, and UI audits.

## Readiness scorecard

- Compile: PASS by additive internal APIs.
- Dependency direction: PASS. VCS has no storage dependency.
- Testability: PASS through deterministic marker and SQLite fixtures.
- Backward compatibility: PASS. Legacy destructive cleanup fails closed.
- Rollback: reject implementation if a long-lived chat session cannot retain
  and present its original instance binding on every mutation.
