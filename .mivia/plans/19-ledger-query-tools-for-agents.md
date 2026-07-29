# 19 — Ledger query tools for sub-agent execution introspection

**Status:** Proposal / Problem statement.
**Date:** 2026-07-30
**Depends on:** `12` (resume restores task config), `13` (run execution fencing).
**Blocks:** nothing.
**Blast radius:** MEDIUM — adds new tools to the sub-agent registry; existing
agent workflows unchanged.

## 1. The problem

When mivia dispatches a sub-agent via `dispatch_tasks`, `spawn_agent`, or
`delegate`, the sub-agent's output is returned in the conversation context
as an opaque result. The higher-level agent (me) cannot inspect what the
sub-agent actually did — what tool calls it made, what errors it hit, what
content it produced — after the sub-agent's turn ends.

The only surface I can access is:

- The sub-agent's structured `output` field (a `json.RawMessage`)
- A `ref:output:<hash>` or `ref:error:<hash>` string stored in the task's
  `OutputRef` / `ErrorRef` ledger fields

**The `ref:output:` strings are not resolvable by any agent tool.** There is
no `read_file`, `grep`, or `fetch_url` that can read `ref:output:abc123`.
The content-addressable store (plan `13` §6) writes the bytes to a `content`
table in SQLite, but no tool exposes it.

I currently have two options:

1. **Blind trust:** Assume the sub-agent did what the output says. This is
   what I do today — I cannot verify.
2. **Manual re-discovery:** Re-read the source files, trace the code paths,
   and reconstruct what must have happened. This is what I did during the
   bug audit — 50+ `read_file` calls to prove a ref was a dead pointer.

Both are unacceptable for correctness-critical work. I need runtime
introspection into what dispatched agents produced.

## 2. Proposal: two new agent tools

### Tool A: `ledger_read(ref string) -> (data []byte, error)`

Resolves a content-addressed reference to its stored bytes.

**Usage:**
```
ledger_read("ref:output:abc123")
→
{"result": "ok", "steps": 3, "elapsed": "1.2s", "status": "completed"}
```

**Implementation:**
- The tool resolves the ref against the session's `content` table in SQLite
  (or the in-memory store for ephemeral sessions).
- Returns `ErrContentNotFound` for unknown refs — which itself is valuable
  debug information (proves the ref is a dead pointer).
- Must handle `ref:output:` and `ref:error:` prefixes consistently (both
  are stored in the same `content` table by `StoreContent`).
- Read-only, bounded max size (e.g. 1 MB).

**Security:**
- Only resolves `ref:`-prefixed keys — not arbitrary SQLite queries.
- Scoped to the current session's store.

### Tool B: `ledger_query(sql string, limit int) -> (rows []map[string]any, error)`

Executes a read-only SELECT against the current session's SQLite ledger tables.

**Accessible tables:**
| Table | Columns | Purpose |
|---|---|---|
| `events` | `run_id, sequence, kind, payload` | Every lifecycle event (task_created, task_completed, task_failed, lifecycle_event) |
| `content` | `ref, data` | Stored output/error blobs |
| `run_claims` | `run_id, holder, acquired_at` | Active run execution claims |

**Forbidden:**
- `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `PRAGMA` — any mutation.
- Access to non-ledger tables (`sessions`, `users`, etc. — not that these
  exist, but the guard should be generic).

**Usage:**
```
ledger_query("SELECT kind, payload FROM events WHERE run_id = 'run-X' ORDER BY sequence", 10)
→
[
  {kind: "run_created", payload: {run_id: "run-X", status: "created"}},
  {kind: "task_created", payload: {task_id: "t1", handler_name: "bug-audit"}},
  {kind: "task_running", payload: {task_id: "t1"}},
  {kind: "task_completed", payload: {task_id: "t1", output: "..."}},
]
```

**Implementation:**
- Runs inside a read-only transaction (`BEGIN RO` in SQLite) so it cannot
  block writers.
- Row limit to prevent memory exhaustion (default 100, max 1000).
- Returns payload as decoded JSON when possible, raw bytes otherwise.

**Security:**
- SQL validation: reject anything that doesn't start with `SELECT`.
- Run through a separate read-only connection or `PRAGMA query_only=ON`.
- If the backend is in-memory (not SQLite), returns an error.

## 3. What these tools enable

| Bug / gap found in the audit | How the tool would have caught it |
|---|---|
| `ref:output:` dead pointers (content never stored) | `ledger_read("ref:output:abc123")` → `ErrContentNotFound` → instantly proves the ref is a dead pointer, no source code tracing needed |
| Content stored wrong or empty | `ledger_read` returns empty bytes or wrong format |
| Sub-agent produced unexpected output | `ledger_query("SELECT payload FROM events WHERE run_id='X' AND kind='task_completed'")` → inspect the raw output |
| Claim not released on error | `ledger_query("SELECT holder FROM run_claims WHERE run_id='X'")` → shows claim still held after error return |
| Task status stuck | `ledger_query("SELECT kind, sequence FROM events WHERE run_id='X'")` → see the last event, find where the chain broke |
| Duplicate execution | `ledger_query("SELECT kind, payload FROM events WHERE run_id='X' AND kind LIKE 'task_%'")` → detect repeated status transitions |

## 4. Risk / cost

- **SQL injection potential:** The tool accepts freeform SQL. Even with the
  SELECT-only guard, the query can be expensive (`SELECT * FROM events` on a
  table with millions of rows). Mitigation: hard row limit, query timeout (5s),
  no aggregate functions that scan without bounds.
- **Data exposure:** The ledger contains task inputs and outputs, which may
  include sensitive data. The tool is already scoped to the agent's own
  session — no cross-session access — but the agent could dump its own data.
  Acceptable given the agent already has `read_file` and `run_command`.
- **Tight coupling:** The tool depends on the SQLite schema (table names,
  column names). Schema changes require tool updates. Mitigation: keep the
  tool description pointing at the canonical schema (`storage_schema.go`).

## 5. Success criterion

A bug auditor investigating "why did my dispatched sub-agent return an error?"
can run:

```
ledger_query("SELECT kind, payload FROM events WHERE run_id = 'run-XXX' ORDER BY sequence", 20)
ledger_read("ref:error:def789")    -- if the last event references an error
```

And understand the failure in under 10 seconds, without reading any source code.
