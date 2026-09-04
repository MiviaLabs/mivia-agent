---
id: legacytui_busy_retry_gap
title: Worktree-instance write lacks busy-retry; do not inherit the gap post-refactor
content: The historical legacytui worktree-instance allocation write surfaced raw SQLITE_BUSY under parallel load; internal/legacytui has been deleted, and any new worktree-dialog or session instance-write flows must route instance writes through storage's retrySQLiteBusy wrapper.
importance: medium
tags: [[legacytui, sqlite, busy-retry, refactor-debt, worktree]]
---

Historical note: TestAsyncCreateMessageRetainsAllocatedInstance
(internal/legacytui/worktree_picker_instance_test.go) was skipped in
commit 01f238c3 because the worktree-instance allocation write failed
with raw SQLITE_BUSY ("database is locked") under parallel load. Other
storage writes clear this class of failure through `retrySQLiteBusy`
(internal/storage/sqlite_busy_retry.go, ~16s retry budget).

`internal/legacytui` was deleted during the terminal UI refactor.
Whatever replacement implements worktree-dialog or session instance-write flows
must call instance writes through the busy-retry wrapper (or an equivalent
transaction-retry discipline) to prevent load-ordering failures.
