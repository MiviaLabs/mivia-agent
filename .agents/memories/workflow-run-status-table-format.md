---
id: workflow_run_status_table_format
title: '"status"/"stats" request format for dispatched mivia workflow runs'
content: Table with columns Run ID, scope, status+step, last heartbeat, resolution (no_diff or PR link), scoped to the currently-tracked batch only.
importance: medium
tags: [workflow, cli, reporting]
updated: 2026-09-04
---

When asked for "status" or "stats" (bare, no other qualifiers) on dispatched
`mivia workflow run` batches (`bug-fix` / `feature-delivery`), render a table
with these columns, in this order:

1. Run ID
2. Scope - short label for the package/area the run's task/scope input
   targets
3. Status - running (+ current step), succeeded, failed, canceled
4. Last Heartbeat - age (e.g. "15s ago") or "-" if none recorded yet
5. Resolution - `no_diff` for a clean-audit conclusion (no bug found, no PR
   opened), a clickable `#NNN` PR link when delivery opened one, or "-"
   while still running

Scope the table to the currently-tracked batch only, not every run in the
ledger. When a run is canceled and replaced, drop the canceled run from the
table entirely instead of keeping it as a row.

Literal markdown format (copy this shape):

```markdown
| # | Run | Scope | Status | Last Heartbeat | Resolution |
|---|---|---|---|---|---|
| 1 | `wfr-XXXXXXXXXXXXXXXX` | controller | running (implement) | 15s ago | — |
| 2 | `wfr-XXXXXXXXXXXXXXXX` | tools | succeeded | — | no_diff |
| 3 | `wfr-XXXXXXXXXXXXXXXX` | cli TUI | succeeded | — | [#211](https://github.com/MiviaLabs/mivia-agent/pull/211) |
```

Run ID is an inline code span. Resolution is a bare `no_diff`, a markdown
link `[#NNN](<pr-url>)` (clickable, not a bare number), or an em dash `—`
while still running/no outcome yet.
