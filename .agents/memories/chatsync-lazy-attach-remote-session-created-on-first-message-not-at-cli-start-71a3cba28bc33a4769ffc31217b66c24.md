# chatsync lazy attach: remote session created on first message, not at CLI start

scope: project
verdict: good
tags: chatsync, lazy-attach, session, tdd, regression-test
created: 2026-09-04

## Summary
Chat sync now defers the remote attach (create/re-attach, heartbeat, input poller) to the session's first event; OpenSession only arms local state. Implemented in internal/chatsync/session_attach.go (ensureAttached); both hosts (attachCLISync, SessionPool.attachSyncLocked) inherited the fix with no code changes.

## What worked
["Moving the attach into ensureAttached (worker runs it once before the first projection) needed zero host-code changes; both surfaces got the fix from one place", "Tests pinning eager behavior re-anchor by sending a synthetic first message (attachByFirstEvent / sendFirstMessage helpers) and asserting the create lands", "Strict structure gate promotes touched-function LOC warnings to hard failures - TestStatusFileSurvivesProcessExit hit 83 lines (soft 80) and blocked the commit", "Bare guard fakes (client_auth) need an /events ack route once a real append flows, or the short-ack readback turns the test into an unrelated upload failure"]

## What did not work
["Do not try to keep eager semantics for leftover outbox backlogs - under lazy attach the backlog rides the first message (production shape: the user comes back and sends one)", "Session deleted before the first event now falls to a plain fresh create at attach (no fork marker) - same as the eager model's GetSession-404 path, but tests had to attach first to exercise recovery", "poller object must be constructed at open (Inputs() non-nil for the TUI pump) while poller.Start waits for attach - construct-at-attach breaks pumpRemoteInputs and races"]

## Why
User requirement: with sync enabled, starting the CLI must not create anything in the mivia.app API until a message is sent. The fix lives in one place (chatsync) so both surfaces stay in contract. Key traps hit while re-anchoring tests: fake servers without an /events route turn the first real append into a 404-recovery (second session); ErrTranscriptConflict (not a gap-400) is the mechanism behind the foreign-transcript test; vacuous passes appeared wherever a test asserted on requests that lazy attach legitimately never makes.

## References
- none
