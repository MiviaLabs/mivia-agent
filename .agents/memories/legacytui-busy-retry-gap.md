---
id: legacytui_busy_retry_gap
title: Worktree-instance write lacks busy-retry; do not inherit the gap post-refactor
content: The worktree-instance allocation write surfaced raw SQLITE_BUSY under parallel load (suppressed via t.Skip on TestAsyncCreateMessageRetainsAllocatedInstance); any replacement for internal/legacytui must route instance writes through storage's retrySQLiteBusy wrapper.
importance: medium
tags: [legacytui, sqlite, busy-retry, refactor-debt, worktree]
---

TestAsyncCreateMessageRetainsAllocatedInstance
(internal/legacytui/worktree_picker_instance_test.go) was skipped in
commit 01f238c3 because the worktree-instance allocation write fails
with raw SQLITE_BUSY ("database is locked") under a fully parallel
`make invariants` run, while passing consistently in isolation. Other
storage writes clear this class of failure through
`retrySQLiteBusy` (internal/storage/sqlite_busy_retry.go, ~16s retry
budget); the instance-write path never got wrapped.

**When the mivia-ui refactor deletes internal/legacytui**, whatever
replaces its worktree-dialog flow must call the instance write through
the busy-retry wrapper (or an equivalent transaction-retry discipline),
or the same load-ordering failure reappears in the new surface. The
skip carries this rationale inline; this memory makes it findable after
the file is gone.

Fix shape if closing the gap earlier instead: wrap the failing
instance-write statement in `retrySQLiteBusy(ctx, ...)` inside
internal/storage, then un-skip the test and run `make invariants` twice
under `-count=1` on a loaded host to confirm.
