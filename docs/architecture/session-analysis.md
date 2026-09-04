# Session-Analysis Skill: Ledger Surface

`session-analysis` is a read-only, metadata-only process-quality analysis skill
(`.agents/skills/session-analysis/`). It analyzes chat sessions recorded in the
durable chat ledger. This document records the design surface and the hostile
audit/review findings that shaped it (both challenges returned conditional-fail
verdicts; every must-fix landed).

## Data surface: the durable chat ledger, not JSON files

Early drafts read `.mivia/sessions/<name>/meta.json` (the legacy file-backed
session store). That design was wrong twice over: the file store is not the
default (`.mivia/mivia.toml:614` `store_backend = "sqlite"`; chat is
unconditionally SQLite-backed per `internal/cli/context_setup.go:26-34`), and
reading file transcripts re-opened the content-privacy surface. The skill's
surface is the SQLite ledger, opened read-only.

Ledger resolution (mirrors `internal/cli/chat_repository_binding.go:124-141`,
`internal/workspace/namespace.go:78-84`):

1. `[subagents].store_path` in `.mivia/mivia.toml` (expand `~`; join relative
   to the workspace root) — pins this workspace to its own file.
2. Else `~/.mivia/context.db` — the **global ledger, shared across every
   workspace on the machine**. Sessions are isolated inside it by workspace ID.

Principal scoping (mirrors `internal/cli/context_setup_session.go:91-107`):
`workspace_id = "workspace-" + hex(sha256(realpath(root))[:8])` (hex of the first
8 bytes = 16 hex chars, matching `context_setup_session.go`;
`hex.EncodeToString(digest[:8])`), `subject_id = "local-user"`. Every query is scoped by both. The audit rated
"wrong ledger path + no principal derivation" the top failure mode: on a
machine-shared ledger, an unscoped query reads other workspaces' session
metadata.

## Query parity: the harness's own read path

The companion `queries.py` embeds `ListSessions` (`internal/storage/chat_sessions.go:221`)
verbatim — the three-arm union (snapshots/projections, live sessions deduped by
`NOT EXISTS`, worktree routes with the active-instance guard) — plus derived
queries that keep its parity predicates:

- arm 2 window anchor is `MAX(context_checkpoints.created_at)` because
  `context_sessions` has **no `updated_at`** (only `title` was added, v10);
- `context_checkpoints` has no `instance_id` (joins must not reference it);
- copies = `session_id IS NULL OR NOT EXISTS(live row)` — a post-tombstone
  projection keeps a non-NULL `session_id` (`chat_sessions.go:170-172`);
- orphan dirs left-join BOTH `chat_sessions` (by name) and `context_sessions`
  (by session_id), because `chat_session_dirs.name` is a snapshot name *or* a
  live session_id (`chat_sessions.go:333`).

Schema gate: `user_version >= 11` required (the `session_id` column is v11).

## Privacy perimeter

- `messages` VALUE never selected; `LENGTH(messages)` only (a scalar).
- Closed never-touch list: `context_payloads.data`, `context_payload_chunks.data`,
  `context_source_events.payload_ref`, `context_checkpoints.summary_metadata` /
  `active_context`, `chat_session_admissions.names/agent/digest` values,
  `context_sessions.title`.
- Read-only open (`mode=ro` + `PRAGMA query_only`); no dot-commands; never
  `--immutable` against the live WAL ledger (`-readonly` may touch `-shm`; a
  hot `-wal` from a crashed writer can surface `SQLITE_CORRUPT` — both map to
  NOT_RUN).

## Method decisions (review-driven)

- **Stalled** = `session_type='live' AND checkpoint_count=0`; snapshots are
  never stalled (they have no checkpoint relationship).
- **Staleness labels**: `token_count`/`turn_count` are save-time estimates,
  invalidated by compaction (`internal/cli/sessions_command.go:317-318` label
  them STALE); `payload_bytes` is current, post-compaction.
- **Anchor bias**: per-arm anchor translation table + whole-store context line
  in every report.
- **Never "duration"**: `updated − created` is first-to-last-save span.
- **Outlier floors**: n<5 none; 5≤n<10 Tukey IQR only (5×-median dropped — too
  aggressive on count data with ties); n≥10 Tukey + z>2, z>3 EXTREME.
- **Measured absence**: zero-session window is a real finding with calibration
  (ledger totals vs workspace totals + derived workspace_id); NOT_RUN only for
  dependency/ledger/schema failures.
- **Validation**: primary = blind subagent re-derivation from raw JSON; fallback
  = two-output cross-check (COUNT vs SUM, window total = sum of arms); selftest
  = hermetic golden DB from the real v11 DDL. `validated:false` is acceptable
  only when the run produced no data findings.

## Fixture strategy

`queries.py --selftest` builds a golden in-memory DB from the real v11 DDL
(chat_sessions, context_sessions, context_checkpoints, chat_session_dirs,
worktree_routes, worktree_instances, chat_session_admissions), seeds
representative rows (projection, copy, arm-2 dedup, stalled, tombstoned,
unknown-model recovery artifact, active/inactive instance routes, orphan dir,
admissions, stale rows), and asserts exact outputs — including that no message
value or title ever appears in the JSON. This is hermetic: no dependency on the
machine-shared ledger, no sqlite3 CLI, no delegation. The repo's committed-skill
gate is `internal/agents/project_agents_fixture_test.go`
(`TestCommittedSkillsDeclareValidTools` pins the catalogue; the roster matrix
passes because the skill is owned by the unrestricted root agent `mivia`, which
has no skills allowlist).

## Operational notes

- Driver: `python3` (stdlib `sqlite3` + `tomllib`, Python ≥ 3.11); the `sqlite3`
  CLI is allowlisted but not on every sandbox PATH, and `mivia` is not
  allowlisted at all — the skill must never depend on either.
- Verified on this machine: ledger at `~/.mivia/context.db` (user_version 11,
  3806 context sessions / 95 snapshots for other workspaces; 0 for this
  workspace) — the skill reports measured absence with calibration, which is
  the honest outcome when a workspace has no recorded sessions.
