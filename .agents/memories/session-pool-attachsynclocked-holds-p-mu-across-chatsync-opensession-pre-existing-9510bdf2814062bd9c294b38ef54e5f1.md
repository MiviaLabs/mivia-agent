---
id: session_pool_attachsynclocked_holds_p_mu_across_chatsync_opensession_pre_existing_9510bdf2814062bd9c294b38ef54e5f1
title: 'session_pool attachSyncLocked holds p.mu across chatsync.OpenSession (pre-existing)'
content: 'In internal/tui/adapter, attachSyncLocked (session_pool_sync.go) runs chatsync.OpenSession — a blocking network round-trip — and watcher.StopSync(2s) while the CALLER holds p.mu (NewSessionPool, CreateFresh, publishEntryLocked, and ReattachSyncAfterLogin''s per-session short lock). ReattachSyncAfterLogin''s own doc comment claims p.mu is not held across OpenSession, which is only true of its snapsho'
importance: medium
x-scope: project
x-verdict: neutral
tags: [session-pool, concurrency, lock-scope, chatsync, follow-up]
updated: 2026-09-16
---

# session_pool attachSyncLocked holds p.mu across chatsync.OpenSession (pre-existing)

## Summary
In internal/tui/adapter, attachSyncLocked (session_pool_sync.go) runs chatsync.OpenSession — a blocking network round-trip — and watcher.StopSync(2s) while the CALLER holds p.mu (NewSessionPool, CreateFresh, publishEntryLocked, and ReattachSyncAfterLogin's per-session short lock). ReattachSyncAfterLogin's own doc comment claims p.mu is not held across OpenSession, which is only true of its snapsho

## What worked
Recorded as an explicit follow-up instead; lens review kept the move-only series pure.

## What did not work
Patching it inside the move-only refactor commits would have mixed behavior change into a verbatim-move series and broken the review contract.

## Why
Flagged medium by the concurrency lens during the 2024 session-pool file-split review (commits 1e20e40a..69582d41). It is pre-existing behavior, deliberately NOT fixed inside the move-only refactor series; a future fix must drop p.mu across OpenSession, record the SyncSession back under a short lock, and re-check p.released after the I/O (the latch is what stops a released pool from resurrecting a sync session). Fixing it touches the documented latch invariant spanning session_pool_lifecycle.go, session_pool_sync.go, session_pool_worktree.go and workflow_notices.go.

## References
- none
