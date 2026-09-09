#!/usr/bin/env python3
"""session-analysis companion: read-only ledger analyzer + golden selftest.

Part of the `session-analysis` skill (.agents/skills/session-analysis/SKILL.md).
This program is the skill's data surface: every query mirrors the harness's own
catalog read path (internal/storage/chat_sessions.go ListSessions) against the
durable chat ledger (SQLite). It is STRICTLY READ-ONLY against the ledger.

Privacy perimeter (never violated by any code path here):
  - The `messages` VALUE of chat_sessions is never selected, materialized, or
    printed. Only scalar LENGTH(messages) (a record-header value) is used as a
    payload-size proxy.
  - Never-touch columns (content or capability-revealing): context_payloads.data,
    context_payload_chunks.data, context_source_events.payload_ref,
    context_checkpoints.summary_metadata / active_context,
    chat_session_admissions.agent / digest / names (only key/coverage counts),
    context_sessions.title (content-derived label).
  - No writes: ledger is opened with SQLite URI mode=ro + PRAGMA query_only.
  - The mivia binary is never invoked (it is not on the executor allowlist).

Usage (executor):
  python3 queries.py --root <workspace-root> [--window 24h|7d|all|ISO-start] [--json]
  python3 queries.py --selftest          # hermetic golden-DB fixture (exit 0/1)

--ledger <path> overrides ledger resolution (fixture/selftest use it).
Requires Python >= 3.11 (tomllib) with the stdlib sqlite3 module.
"""

import argparse
import hashlib
import io
import json
import os
import re
import sqlite3
import statistics
import sys
import tempfile
from contextlib import redirect_stdout
from datetime import datetime, timedelta, timezone

SKILL = "session-analysis"
SUBJECT_ID = "local-user"  # context_setup_session.go:107 (NewPrincipal subject)

# ---------------------------------------------------------------------------
# Schema knowledge (mirrors internal/storage/context_schema_v*.go at v11).
# Used only by --selftest to build a golden DB with the REAL v11 shape.
# ---------------------------------------------------------------------------

DDL = [
    """CREATE TABLE chat_sessions(
        workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
        model TEXT NOT NULL, provider TEXT NOT NULL, messages BLOB NOT NULL,
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
        turn_count INTEGER NOT NULL, token_count INTEGER NOT NULL,
        message_count INTEGER NOT NULL,
        instance_id TEXT, session_id TEXT,
        PRIMARY KEY(workspace_id, subject_id, name))""",
    """CREATE TABLE context_sessions(
        workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, session_id TEXT NOT NULL,
        capability_digest TEXT NOT NULL, session_revision INTEGER NOT NULL,
        durable_revision INTEGER NOT NULL, source_sequence INTEGER NOT NULL,
        provider TEXT NOT NULL, model TEXT NOT NULL, binding_generation INTEGER NOT NULL,
        active_checkpoint_id TEXT, tombstoned INTEGER NOT NULL DEFAULT 0,
        instance_id TEXT, title TEXT,
        PRIMARY KEY(workspace_id, session_id), UNIQUE(session_id),
        UNIQUE(workspace_id, session_id, subject_id),
        CHECK(session_revision >= 0 AND durable_revision >= 0 AND source_sequence >= 0),
        CHECK(tombstoned IN (0,1)))""",
    """CREATE TABLE context_checkpoints(
        checkpoint_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, session_id TEXT NOT NULL,
        subject_id TEXT NOT NULL, source_start INTEGER NOT NULL, source_end INTEGER NOT NULL,
        algorithm TEXT NOT NULL, schema_version INTEGER NOT NULL, summary_model TEXT NOT NULL,
        operation_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
        session_revision INTEGER NOT NULL, durable_revision INTEGER NOT NULL,
        binding_generation INTEGER NOT NULL, turn_id INTEGER NOT NULL,
        summary_metadata BLOB NOT NULL, active_context BLOB NOT NULL,
        content_fingerprint TEXT NOT NULL, complete INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
        UNIQUE(session_id, operation_id), UNIQUE(session_id, idempotency_key),
        CHECK(source_start <= source_end), CHECK(complete IN (0,1)))""",
    """CREATE TABLE chat_session_dirs(
        workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
        dir TEXT NOT NULL DEFAULT '', worktree TEXT NOT NULL DEFAULT '',
        instance_id TEXT,
        PRIMARY KEY(workspace_id, subject_id, name))""",
    """CREATE TABLE worktree_routes(
        workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, worktree TEXT NOT NULL,
        dir TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
        instance_id TEXT,
        PRIMARY KEY(workspace_id, subject_id, worktree))""",
    """CREATE TABLE worktree_instances(
        workspace_id TEXT NOT NULL, worktree TEXT NOT NULL, instance_id TEXT NOT NULL,
        canonical_path TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('creating','active','retired','abandoned')),
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
        PRIMARY KEY(workspace_id, worktree, instance_id))""",
    """CREATE TABLE chat_session_admissions(
        workspace_id TEXT NOT NULL, subject_id TEXT NOT NULL, name TEXT NOT NULL,
        agent TEXT NOT NULL, digest TEXT NOT NULL, names TEXT NOT NULL,
        updated_at TEXT NOT NULL, instance_id TEXT,
        PRIMARY KEY(workspace_id, subject_id, name))""",
]

# ---------------------------------------------------------------------------
# Ledger resolution + principal (mirrors the harness).
# ---------------------------------------------------------------------------


def resolve_ledger(root: str, override: str | None) -> str:
    """[subagents].store_path (expand ~; join relative to root) else ~/.mivia/context.db.

    Mirrors chat_repository_binding.go:124-141 and workspace/namespace.go:78-84:
    the chat ledger defaults to the GLOBAL ~/.mivia/context.db shared across
    workspaces; a pinned store_path overrides it. Never hardcode .mivia/context.db.
    """
    if override:
        return os.path.expanduser(override)
    toml = os.path.join(root, ".mivia", "mivia.toml")
    if os.path.isfile(toml):
        try:
            import tomllib  # Python >= 3.11

            with open(toml, "rb") as f:
                cfg = tomllib.load(f)
            sub = cfg.get("subagents") or {}
            sp = sub.get("store_path")
            if isinstance(sp, str) and sp.strip():
                sp = os.path.expanduser(sp.strip())
                if not os.path.isabs(sp):
                    sp = os.path.join(root, sp)
                return sp
        except Exception as exc:  # noqa: BLE001 - resolution failure must not crash
            raise RuntimeError(f"mivia.toml parse failed: {exc}") from exc
    return os.path.join(os.path.expanduser("~"), ".mivia", "context.db")


def derive_workspace_id(root: str) -> str:
    """workspace-<hex(sha256(canonical root)[:8])> where [:8] is the first 8
    BYTES (16 hex chars) — mirror of context_setup_session.go:91-101.

    canonical root = realpath(abs(root)); the harness does Abs then EvalSymlinks.
    Python's hexdigest()[:8] (8 hex chars = 4 bytes) is WRONG and mis-scopes;
    digest()[:8].hex() matches Go's hex.EncodeToString(digest[:8]).
    """
    canonical = os.path.realpath(os.path.abspath(root))
    raw = hashlib.sha256(canonical.encode("utf-8")).digest()
    return "workspace-" + raw[:8].hex()


# ---------------------------------------------------------------------------
# Timestamp handling (tolerant of RFC3339 and SQLite CURRENT_TIMESTAMP forms).
# ---------------------------------------------------------------------------

def parse_ts(s: str | None) -> datetime | None:
    if not s:
        return None
    t = s.strip()
    try:
        t = t.replace("Z", "+00:00")
        dt = datetime.fromisoformat(t)
    except ValueError:
        try:
            dt = datetime.strptime(t, "%Y-%m-%d %H:%M:%S")
        except ValueError:
            return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def parse_window(arg: str, now: datetime) -> tuple[datetime | None, str]:
    a = arg.strip()
    if a == "all":
        return None, a
    if a.endswith("h") and a[:-1].isdigit():
        return now - timedelta(hours=int(a[:-1])), a
    if a.endswith("d") and a[:-1].isdigit():
        return now - timedelta(days=int(a[:-1])), a
    dt = parse_ts(a)
    if dt:
        return dt, a
    raise ValueError(f"unrecognized window {a!r} (use 24h, 7d, all, or an ISO start)")


# ---------------------------------------------------------------------------
# Read-only connection.
# ---------------------------------------------------------------------------

class NotRun(Exception):
    """Maps to a NOT_RUN report: dependency/ledger/schema unavailable."""


def open_ro(path: str) -> sqlite3.Connection:
    if not os.path.isfile(path):
        raise NotRun(f"ledger not found at {path} (not a file)")
    try:
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    except sqlite3.Error as exc:
        raise NotRun(f"cannot open ledger read-only: {exc}") from exc
    con.row_factory = sqlite3.Row
    try:
        con.execute("PRAGMA query_only=ON")
        con.execute("PRAGMA busy_timeout=5000")
    except sqlite3.Error as exc:  # pragma: no cover - defensive
        con.close()
        raise NotRun(f"ledger probe failed: {exc}") from exc
    return con


# ---------------------------------------------------------------------------
# Queries. Every one is scoped by workspace_id + subject_id. None selects
# message content, payloads, titles, or admission values.
# ---------------------------------------------------------------------------

# Verbatim mirror of ListSessions (internal/storage/chat_sessions.go:221),
# parameterized by workspace/subject; window filtering is applied in Python so
# the harness-parity SQL stays byte-identical to the source.
LIST_SESSIONS_SQL = """SELECT c.name,COALESCE(s.title,''),c.model,c.provider,COALESCE(s.session_id,''),c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,'' FROM chat_sessions c LEFT JOIN context_sessions s ON s.workspace_id=c.workspace_id AND s.subject_id=c.subject_id AND s.session_id=c.session_id AND s.tombstoned=0 AND s.instance_id IS NULL LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id IS NULL UNION ALL SELECT t.session_id,t.title,t.model,t.provider,t.session_id,t.created,t.updated,t.source_sequence,0,t.source_sequence,COALESCE(d.dir,''),COALESCE(d.worktree,''),0,'' FROM (SELECT cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence,COALESCE(MIN(cc.created_at),CURRENT_TIMESTAMP) AS created,COALESCE(MAX(cc.created_at),CURRENT_TIMESTAMP) AS updated FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.session_id=cs.session_id AND cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.complete=1 WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.tombstoned=0 AND cs.source_sequence>0 AND cs.instance_id IS NULL AND NOT EXISTS (SELECT 1 FROM chat_sessions c WHERE c.workspace_id=cs.workspace_id AND c.subject_id=cs.subject_id AND c.name=cs.session_id) GROUP BY cs.workspace_id,cs.subject_id,cs.session_id,cs.title,cs.model,cs.provider,cs.source_sequence) t LEFT JOIN chat_session_dirs d ON d.workspace_id=t.workspace_id AND d.subject_id=t.subject_id AND d.name=t.session_id UNION ALL SELECT 'worktree:' || r.worktree,'','','', '',r.created_at,r.updated_at,0,0,0,r.dir,r.worktree,1,COALESCE(r.instance_id,'') FROM worktree_routes r WHERE r.workspace_id=? AND r.subject_id=? AND (r.instance_id IS NULL OR EXISTS (SELECT 1 FROM worktree_instances wi WHERE wi.workspace_id=r.workspace_id AND wi.worktree=r.worktree AND wi.instance_id=r.instance_id AND wi.state='active'))"""

# Arm 1 detail: metrics incl. payload-length proxy. Never selects messages.
ARM1_DETAIL_SQL = """SELECT c.name,COALESCE(c.session_id,''),c.model,c.provider,c.created_at,c.updated_at,c.turn_count,c.token_count,c.message_count,LENGTH(c.messages),COALESCE(d.dir,''),COALESCE(d.worktree,'') FROM chat_sessions c LEFT JOIN chat_session_dirs d ON d.workspace_id=c.workspace_id AND d.subject_id=c.subject_id AND d.name=c.name WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id IS NULL"""

LIVE_CHECKPOINTS_SQL = """SELECT cs.session_id,COUNT(cc.checkpoint_id) AS cps,COALESCE(MAX(cc.created_at),'') AS last_cp FROM context_sessions cs LEFT JOIN context_checkpoints cc ON cc.session_id=cs.session_id AND cc.workspace_id=cs.workspace_id AND cc.subject_id=cs.subject_id AND cc.complete=1 WHERE cs.workspace_id=? AND cs.subject_id=? AND cs.tombstoned=0 AND cs.source_sequence>0 AND cs.instance_id IS NULL GROUP BY cs.session_id"""

ORPHAN_DIRS_SQL = """SELECT d.name,d.dir,d.worktree FROM chat_session_dirs d LEFT JOIN chat_sessions cs ON cs.workspace_id=d.workspace_id AND cs.subject_id=d.subject_id AND cs.name=d.name LEFT JOIN context_sessions c ON c.workspace_id=d.workspace_id AND c.subject_id=d.subject_id AND c.session_id=d.name WHERE d.workspace_id=? AND d.subject_id=? AND d.instance_id IS NULL AND cs.name IS NULL AND c.session_id IS NULL"""

COPIES_SQL = """SELECT c.name,COALESCE(c.session_id,''),c.model,c.provider,c.created_at,c.updated_at,c.turn_count,c.message_count,LENGTH(c.messages) FROM chat_sessions c WHERE c.workspace_id=? AND c.subject_id=? AND c.instance_id IS NULL AND (c.session_id IS NULL OR NOT EXISTS (SELECT 1 FROM context_sessions s WHERE s.workspace_id=c.workspace_id AND s.subject_id=c.subject_id AND s.session_id=c.session_id AND s.tombstoned=0 AND s.instance_id IS NULL))"""

ADMISSIONS_KEYS_SQL = """SELECT name FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND instance_id IS NULL"""

MODEL_PROVIDER_SQL = """SELECT model,provider,COUNT(*) AS n FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id IS NULL GROUP BY model,provider"""

LIVE_MODEL_PROVIDER_SQL = """SELECT model,provider,COUNT(*) AS n FROM context_sessions WHERE workspace_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS NULL GROUP BY model,provider"""


# ---------------------------------------------------------------------------
# Analysis.
# ---------------------------------------------------------------------------

def tukey_outliers(values: list[float], method: str) -> list[float]:
    if len(values) < 5:
        return []
    qs = statistics.quantiles(sorted(values), n=4, method="inclusive")
    q1, q3 = qs[0], qs[2]
    iqr = q3 - q1
    lo, hi = q1 - 1.5 * iqr, q3 + 1.5 * iqr
    return [v for v in values if v < lo or v > hi]


def distribution(metric: str, values: list[float]) -> dict:
    n = len(values)
    out: dict = {"metric": metric, "n": n, "method": "none"}
    if n == 0:
        return out
    out.update({"min": min(values), "max": max(values), "median": statistics.median(values)})
    if n >= 2:
        out["mean"] = round(statistics.fmean(values), 2)
    if n < 5:
        out["method"] = "none (sample below outlier floor)"
        out["note"] = "no distribution outlier flags below n=5; raw values only"
        return out
    if n < 10:
        out["method"] = "tukey IQR 1.5x (robust only)"
        out["outliers"] = tukey_outliers(values, "iqr")
        return out
    out["method"] = "tukey IQR 1.5x + z>2 (z>3 = extreme)"
    iqr_outs = tukey_outliers(values, "iqr")
    mean = statistics.fmean(values)
    sd = statistics.pstdev(values) or 1.0
    z_outs = [v for v in values if abs(v - mean) / sd > 2.0]
    out["outliers"] = sorted(set(iqr_outs + z_outs))
    out["extreme"] = sorted({v for v in values if abs(v - mean) / sd > 3.0})
    return out


def analyze(root: str, ledger: str, window_arg: str, now: datetime, wid: str | None = None) -> dict:
    wid = wid or derive_workspace_id(root)
    con = open_ro(ledger)
    try:
        uv = con.execute("PRAGMA user_version").fetchone()[0]
        if uv < 11:
            raise NotRun(
                f"ledger schema user_version={uv} < 11; session_id-based catalog "
                "queries require v11 (run the harness once to migrate)"
            )

        def q(sql: str, *params) -> list[sqlite3.Row]:
            if len(params) == 1 and isinstance(params[0], (tuple, list)):
                params = params[0]
            return con.execute(sql, params).fetchall()

        # Calibration: ledger is machine-shared; other workspaces' rows exist.
        cs_total = q("SELECT count(*) AS n FROM context_sessions")[0]["n"]
        cs_mine = q("SELECT count(*) AS n FROM context_sessions WHERE workspace_id=?", wid)[0]["n"]
        chat_total = q("SELECT count(*) AS n FROM chat_sessions")[0]["n"]
        chat_mine = q("SELECT count(*) AS n FROM chat_sessions WHERE workspace_id=?", wid)[0]["n"]

        # Store accounting (SA-2): a context_sessions row is created at session
        # setup with source_sequence=0 (context_store.go:76) and only becomes
        # catalog-visible (ListSessions arm 2) once a source event publishes.
        # Surface the full breakdown so never-published rows are explicit.
        # Counts only; filters never touch content columns.
        sa_total = q("SELECT count(*) AS n FROM context_sessions WHERE workspace_id=? AND subject_id=?", wid, SUBJECT_ID)[0]["n"]
        sa_tomb = q("SELECT count(*) AS n FROM context_sessions WHERE workspace_id=? AND subject_id=? AND tombstoned=1", wid, SUBJECT_ID)[0]["n"]
        sa_alive = q("SELECT count(*) AS n FROM context_sessions WHERE workspace_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS NULL", wid, SUBJECT_ID)[0]["n"]
        sa_never = q("SELECT count(*) AS n FROM context_sessions WHERE workspace_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS NULL AND source_sequence=0", wid, SUBJECT_ID)[0]["n"]
        sa_pub = q("SELECT count(*) AS n FROM context_sessions WHERE workspace_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS NULL AND source_sequence>0", wid, SUBJECT_ID)[0]["n"]

        # Catalog rows (harness-parity arms). Row layout mirrors ListSessions
        # scan order: name,title,model,provider,session_id,created,updated,
        # turn,token,message,dir,worktree,flag(0=catalog,1=route),instance.
        # Arm 1 (snapshots/projections) and arm 2 (live) share flag 0; arm 2
        # rows carry name==session_id and are excluded from arm 1. The SQL's
        # NOT EXISTS guard already removed live rows covered by a same-named
        # chat_sessions projection, so arm 2 here is the SQL's arm 2 verbatim.
        rows = q(LIST_SESSIONS_SQL, wid, SUBJECT_ID, wid, SUBJECT_ID, wid, SUBJECT_ID)
        catalog = [r for r in rows if r[12] == 0 and not r[0].startswith("worktree:")]
        arm3 = [r for r in rows if r[12] == 1]
        live_ids = {r["session_id"] for r in q(LIVE_CHECKPOINTS_SQL, wid, SUBJECT_ID)}
        snap_ids = {r["name"] for r in q("SELECT name FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id IS NULL", wid, SUBJECT_ID)}
        arm2 = [r for r in catalog if r[0] == r[4] and r[4] in live_ids and r[0] not in snap_ids]
        arm2_keys = {(r[0], r[4], r[6]) for r in arm2}
        arm1 = [r for r in catalog if (r[0], r[4], r[6]) not in arm2_keys]

        window_start, window_label = parse_window(window_arg, now)

        def in_window(dt_s: str, start: datetime | None) -> bool:
            if start is None:
                return True
            dt = parse_ts(dt_s)
            return dt is not None and dt >= start

        w_arm1 = [r for r in arm1 if in_window(r[6], window_start)]
        w_arm2 = [r for r in arm2 if in_window(r[6], window_start)]
        w_arm3 = [r for r in arm3 if in_window(r[6], window_start)]

        # Arm 1 detail with payload-length proxy (LENGTH only, never the value).
        details = {r["name"]: dict(r) for r in q(ARM1_DETAIL_SQL, wid, SUBJECT_ID)}
        # checkpoints per live session
        cps = {r["session_id"]: {"cps": r["cps"], "last_cp": r["last_cp"]} for r in q(LIVE_CHECKPOINTS_SQL, wid, SUBJECT_ID)}
        orphans = [dict(r) for r in q(ORPHAN_DIRS_SQL, wid, SUBJECT_ID)]
        copies = [dict(r) for r in q(COPIES_SQL, wid, SUBJECT_ID)]
        adm_keys = {r["name"] for r in q(ADMISSIONS_KEYS_SQL, wid, SUBJECT_ID)}
        mp = [dict(r) for r in q(MODEL_PROVIDER_SQL, wid, SUBJECT_ID)]
        lmp = [dict(r) for r in q(LIVE_MODEL_PROVIDER_SQL, wid, SUBJECT_ID)]

        # --- signal assembly ------------------------------------------------
        def arm1_sig(r: sqlite3.Row) -> dict:
            d = details.get(r[0], {})
            return {
                "id": r[0], "model": r[2], "provider": r[3],
                "session_id": r[4], "created": r[5], "updated": r[6],
                "turn_count": r[7], "token_count": r[8], "message_count": r[9],
                "payload_bytes": d.get("LENGTH(c.messages)"),
                "has_live_projection": bool(r[4]),
                "never_checkpointed": False,
            }

        def arm2_sig(r: sqlite3.Row) -> dict:
            cp = cps.get(r[0], {"cps": 0, "last_cp": ""})
            return {
                "id": r[0], "model": r[2], "provider": r[3], "session_id": r[4],
                "created": r[5], "updated": r[6],
                "turn_count": r[7], "token_count": r[8], "message_count": r[9],
                "payload_bytes": None, "has_live_projection": True,
                "checkpoint_count": cp["cps"], "last_checkpoint": cp["last_cp"],
                "never_checkpointed": cp["cps"] == 0,
            }

        def arm3_sig(r: sqlite3.Row) -> dict:
            return {"id": r[0], "created": r[5], "updated": r[6], "dir": r[10], "worktree": r[11]}

        w1 = [arm1_sig(r) for r in w_arm1]
        w2 = [arm2_sig(r) for r in w_arm2]
        w3 = [arm3_sig(r) for r in w_arm3]

        # distributions over arm-1 window rows (counts only)
        dists = {}
        if w1:
            for metric, key in (
                ("turn_count", "turn_count"),
                ("message_count", "message_count"),
                ("token_count", "token_count"),
                ("payload_bytes", "payload_bytes"),
            ):
                vals = [float(x[key]) for x in w1 if x.get(key) is not None]
                dists[metric] = distribution(metric, vals)

        # anomalies — structural classes are whole-store (window-independent).
        # The window determines the detailed listing, not what is anomalous.
        anomalies: list[dict] = []
        all_arm2 = [arm2_sig(r) for r in arm2]
        all_arm1 = [arm1_sig(r) for r in arm1]
        for x in all_arm2:
            if x["never_checkpointed"]:
                anomalies.append({"kind": "live_no_checkpoints", "count": 1, "ids": [x["id"]], "note": "live session with 0 completed checkpoints (stalled or never turned)"})
        for x in all_arm1:
            pb = x.get("payload_bytes")
            if pb is not None and pb > 1_000_000 and (x["turn_count"] or 0) < 10:
                anomalies.append({"kind": "disproportionate_payload", "count": 1, "ids": [x["id"]], "note": f"payload {pb} bytes with {x['turn_count']} turns"})
            if pb == 0 and (x["turn_count"] or 0) > 0:
                anomalies.append({"kind": "payload_zero_with_turns", "count": 1, "ids": [x["id"]], "note": "empty payload despite turns (compaction destroyed messages?)"})
            if (x["turn_count"] or 0) == 0 and (x["message_count"] or 0) > 0:
                anomalies.append({"kind": "assistant_only", "count": 1, "ids": [x["id"]], "note": "messages but no user turns"})
            if (x["turn_count"] or 0) == 0 and (x["message_count"] or 0) == 0:
                anomalies.append({"kind": "empty", "count": 1, "ids": [x["id"]], "note": "no turns and no messages"})
        if copies:
            anomalies.append({"kind": "snapshots_without_live_projection", "count": len(copies), "ids": [c["name"] for c in copies], "note": "chat_sessions rows with no live context row (plain copies or post-tombstone projections)"})
        if orphans:
            anomalies.append({"kind": "orphan_dir_rows", "count": len(orphans), "ids": [o["name"] for o in orphans], "note": "chat_session_dirs rows matching neither a snapshot nor a live session"})
        unknown = [x for x in w1 + w2 if (x.get("model") == "unknown" or x.get("provider") == "unknown")]
        if unknown:
            anomalies.append({"kind": "unknown_model_recovered", "count": len(unknown), "ids": [x["id"] for x in unknown], "note": "model/provider 'unknown' marks recovered-file sessions, never a real model"})

        # stale (whole-store, per-arm anchor)
        def stale_count(items, idx):
            return sum(1 for r in items if (parse_ts(r[idx]) or now) < now - timedelta(days=7))

        stale = {
            "snapshots": stale_count(arm1, 6),
            "live": stale_count(arm2, 6),
            "routes": stale_count(arm3, 6),
        }

        # admissions coverage over window snapshot+live sessions
        window_sessions = {x["id"] for x in w1} | {x["id"] for x in w2}
        with_adm = window_sessions & adm_keys

        out = {
            "skill": SKILL,
            "schema_version": "v1",
            "generated_at": now.isoformat(),
            "ledger_path": ledger,
            "user_version": uv,
            "workspace_id": wid,
            "derivation": {
                "algorithm": "workspace- + hex(sha256(canonical root)[:8]) where [:8] is the first 8 BYTES (16 hex chars)",
                "harness_site": "internal/cli/context_setup_session.go:100",
                "hex_chars": 16,
                "note": "corrected: the previous version derived 8 hex chars (4 bytes) and mis-scoped every run",
            },
            "subject_id": SUBJECT_ID,
            "window": {"requested": window_label, "start_utc": window_start.isoformat() if window_start else None, "end_utc": now.isoformat()},
            "scope": (
                "Metadata-only analysis of the durable chat ledger for this workspace. "
                "Message content, payloads, titles, and admission values are never read. "
                "The ledger is machine-shared; only rows for workspace_id=%s subject_id=%s are included." % (wid, SUBJECT_ID)
            ),
            "calibration": {
                "total_context_sessions": cs_total, "this_workspace_context_sessions": cs_mine,
                "total_chat_sessions": chat_total, "this_workspace_chat_sessions": chat_mine,
                "other_workspace_rows_present": (cs_total - cs_mine) > 0 or (chat_total - chat_mine) > 0,
            },
            "signals": {
                "window_counts": {"snapshots": len(w1), "live": len(w2), "worktree_routes": len(w3), "total": len(w1) + len(w2) + len(w3)},
                "whole_store": {"snapshots": len(arm1), "live": len(arm2), "worktree_routes": len(arm3)},
                "snapshot_distributions": dists,
                "live_checkpoints": {
                    "live_in_window": len(w2),
                    "with_completed_checkpoints": sum(1 for x in w2 if not x["never_checkpointed"]),
                    "never_checkpointed": sum(1 for x in w2 if x["never_checkpointed"]),
                },
                "stalled_live_in_window": sum(1 for x in w2 if x["never_checkpointed"]),
                "copies": len(copies),
                "orphan_dirs": len(orphans),
                "admissions_coverage": {
                    "window_sessions": len(window_sessions),
                    "with_admission_record": len(with_adm),
                    "recorded_names_total": len(adm_keys),
                    "note": "chat_session_admissions rows exist only for sessions whose last save had a non-empty admitted tool set; an empty set deletes the row (internal/storage/session_admissions.go:38-41, 42-43; persistAdmission at snapshot save, internal/chat/context_catalog.go:107 / persistence.go:172). Zero coverage is the normal state for sessions that never admitted tools.",
                },
                "model_provider_snapshots": mp,
                "model_provider_live": lmp,
                "stale_7d": stale,
                "store_accounting": {
                    "total": sa_total,
                    "tombstoned": sa_tomb,
                    "alive": sa_alive,
                    "never_published_source_sequence_0": sa_never,
                    "published_source_sequence_gt0": sa_pub,
                },
            },
            "anomalies": anomalies,
            "rows": {
                "arm1_window": w1,
                "arm2_window": w2,
                "arm3_window": w3,
                "orphan_dirs": orphans,
                "copies": copies,
            },
            "validation": {"status": "unvalidated", "notes": ["filled by the executor's validation protocol; selftest only proves query correctness on the golden DB"]},
            "not_run": None,
        }
        return out
    finally:
        con.close()


# ---------------------------------------------------------------------------
# Golden selftest: hermetic DB built from the REAL v11 DDL with seeded rows.
# Run: python3 queries.py --selftest  (exit 0 = PASS, 1 = FAIL)
# ---------------------------------------------------------------------------

NOW = datetime(2026, 6, 1, 12, 0, 0, tzinfo=timezone.utc)
W = "workspace-test"
S = SUBJECT_ID


def seed(con: sqlite3.Connection, wid: str = W) -> None:
    # Fixture-scope workspace id. The nested helpers below close over this
    # local, so the same golden DDL seeds any workspace id (default: module W).
    W = wid  # noqa: F841
    cur = con.cursor()
    for ddl in DDL:
        cur.execute(ddl)
    iso = lambda d: (NOW - timedelta(days=d)).isoformat().replace("+00:00", "Z")  # noqa: E731
    big = b"x" * (1_100_000)

    def snap(name, session_id, model, provider, days_old, turns, msgs, tokens, payload, instance=None):
        cur.execute(
            "INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count,instance_id,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
            (W, S, name, model, provider, payload, iso(days_old), iso(days_old), turns, tokens, msgs, instance, session_id),
        )

    def live(sid, model, provider, seq, tombstoned, days_old, cps=0, instance=None, title=""):
        cur.execute(
            "INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,active_checkpoint_id,tombstoned,instance_id,title) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
            (W, S, sid, "d", 0, 0, seq, provider, model, 0, None, tombstoned, instance, title),
        )
        for i in range(cps):
            cur.execute(
                "INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                (f"cp-{sid}-{i}", W, sid, S, 0, i + 1, "structural", 1, "", f"op-{sid}-{i}", f"idem-{sid}-{i}", 0, 0, 0, i + 1, b"{}", b"{}", "fp", 1, iso(days_old - i)),
            )

    def route(worktree, days_old, instance=None):
        cur.execute(
            "INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,?)",
            (W, S, worktree, f"/wt/{worktree}", iso(days_old), iso(days_old), instance),
        )

    def dirrow(name):
        cur.execute("INSERT INTO chat_session_dirs(workspace_id,subject_id,name,dir,worktree) VALUES(?,?,?,?,?)", (W, S, name, f"/dir/{name}", "wt"))

    def adm(name):
        cur.execute("INSERT INTO chat_session_admissions(workspace_id,subject_id,name,agent,digest,names,updated_at) VALUES(?,?,?,?,?,?,?)", (W, S, name, "agent", "digest", '["read_file"]', iso(1)))

    # --- arm 1: snapshots. A live projection carries name == session_id
    # (v11 backfill semantics: name is the catalog key, session_id names the
    # live row). copy1 and old-snap are plain snapshots (session_id NULL).
    snap("L1", "L1", "m1", "p1", 0, 4, 4, 400, b"[]")          # in window, live projection exists (L1)
    snap("copy1", None, "m1", "p1", 0, 2, 2, 200, big)          # in window, copy, disproportionate payload (2 turns, >1MB)
    snap("old-snap", None, "m1", "p1", 30, 0, 0, 0, b"")        # outside window, stale, empty
    # --- arm 2: live sessions
    live("L1", "m1", "p1", 5, 0, 0, cps=2)                      # covered by arm1 via projection (NOT EXISTS dedup)
    live("L2", "m2", "p2", 3, 0, 0, cps=0)                      # in window, never checkpointed -> stalled
    live("old-live", "m1", "p1", 2, 0, 30, cps=1)               # outside window, stale
    live("gone", "m9", "p9", 1, 1, 0, cps=0)                    # tombstoned -> excluded everywhere
    live("recovered", "unknown", "unknown", 4, 0, 0, cps=0)     # in window, unknown model artifact, stalled too
    live("seq0-session", "m1", "p1", 0, 0, 0, cps=0)           # never published (seq=0): excluded from catalog, counted in store_accounting
    # --- arm 3: routes
    route("r1", 0)                                               # in window, plain
    route("r2", 0, instance="inst-a")                            # in window, active instance
    cur.execute("INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", (W, "r2", "inst-a", "/wt/r2", "active", iso(1), iso(1)))
    route("r3", 0, instance="inst-b")                            # excluded: non-active instance
    cur.execute("INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", (W, "r3", "inst-b", "/wt/r3", "creating", iso(1), iso(1)))
    route("r4", 30)                                              # outside window, stale
    # --- dirs: one orphan (no match anywhere), two legit (snapshot name, live session_id)
    dirrow("ghost-dir")
    dirrow("L1")
    dirrow("L2")
    # --- admissions: records for L1 and L2
    adm("L1")
    adm("L2")
    con.execute("PRAGMA user_version = 11")  # harness migration stamps this at v11
    con.commit()


def selftest() -> int:
    failures: list[str] = []
    tmp = tempfile.NamedTemporaryFile(suffix=".db", delete=False)
    tmp.close()
    try:
        con = sqlite3.connect(tmp.name)
        seed(con)
        con.close()
        res = analyze(root=".", ledger=tmp.name, window_arg="24h", now=NOW, wid=W)

        def expect(label, got, want):
            if got != want:
                failures.append(f"{label}: got {got!r}, want {want!r}")

        s = res["signals"]
        expect("window_counts", s["window_counts"], {"snapshots": 2, "live": 2, "worktree_routes": 2, "total": 6})
        expect("whole_store.snapshots", s["whole_store"]["snapshots"], 3)
        expect("whole_store.live", s["whole_store"]["live"], 3)   # L2, old-live, recovered (L1 deduped, gone tombstoned)
        expect("whole_store.routes", s["whole_store"]["worktree_routes"], 3)  # r1, r2, r4 (r3 inactive excluded)
        expect("stalled_live_in_window", s["stalled_live_in_window"], 2)  # L2 + recovered
        expect("copies", s["copies"], 2)                          # copy1 + old-snap
        expect("orphan_dirs", s["orphan_dirs"], 1)                # ghost-dir only
        expect("admissions_coverage.window_sessions", s["admissions_coverage"]["window_sessions"], 4)
        expect("admissions_coverage.with_admission_record", s["admissions_coverage"]["with_admission_record"], 2)
        expect("stale_7d.snapshots", s["stale_7d"]["snapshots"], 1)
        expect("stale_7d.live", s["stale_7d"]["live"], 1)
        expect("stale_7d.routes", s["stale_7d"]["routes"], 1)
        expect("anomaly kinds", sorted({a["kind"] for a in res["anomalies"]}), sorted([
            "disproportionate_payload",   # copy1: >1MB with 2 turns
            "empty",                       # old-snap: 0 turns 0 messages
            "live_no_checkpoints",         # L2 + recovered
            "orphan_dir_rows",             # ghost-dir
            "snapshots_without_live_projection",  # copy1 + old-snap
            "unknown_model_recovered",     # recovered
        ]))
        # distribution guardrail: window arm1 n=2 -> no outlier flags
        d = s["snapshot_distributions"].get("turn_count", {})
        expect("turn_count dist method (n=2)", d.get("method"), "none (sample below outlier floor)")
        # privacy: payload_bytes is a number; no message value anywhere in output
        blob = json.dumps(res)
        expect("no messages value leaked", "x" * 64 in blob, False)
        expect("no title leaked", "secret-title" in blob, False)

        # Block A: workspace-id derivation parity (SA-1 regression guard).
        # The harness (internal/cli/context_setup_session.go:100) hex-encodes
        # the first 8 BYTES of sha256(canonical root) = 16 hex chars. The
        # buggy 8-hex-char form must never return.
        with tempfile.TemporaryDirectory() as probe_dir:
            probe_root = os.path.join(probe_dir, "ws")
            derived = derive_workspace_id(probe_root)
            expected_wid = "workspace-" + hashlib.sha256(
                os.path.realpath(os.path.abspath(probe_root)).encode("utf-8")
            ).digest()[:8].hex()
            expect("derivation prefix", derived.startswith("workspace-"), True)
            expect(
                "derivation is 16 hex chars (8 bytes)",
                re.fullmatch(r"workspace-[0-9a-f]{16}", derived) is not None,
                True,
            )
            expect("derivation matches Go byte semantics", derived, expected_wid)

        # Block B: CLI end-to-end fixture (hermetic). Proves the full CLI path
        # (argparse -> root -> derive_workspace_id -> analyze) scopes correctly
        # and never touches the real global ledger (~/.mivia/context.db).
        with tempfile.TemporaryDirectory() as cli_dir:
            cli_root = os.path.realpath(os.path.join(cli_dir, "cli-ws"))
            cli_wid = "workspace-" + hashlib.sha256(cli_root.encode("utf-8")).digest()[:8].hex()
            cli_db = tempfile.NamedTemporaryFile(suffix=".db", delete=False)
            cli_db.close()
            try:
                ccon = sqlite3.connect(cli_db.name)
                seed(ccon, wid=cli_wid)
                ccon.close()
                buf = io.StringIO()
                with redirect_stdout(buf):
                    rc = main(argv=["--root", cli_root, "--ledger", cli_db.name, "--window", "all"])
                cli_out = json.loads(buf.getvalue())
                expect("cli fixture exit code", rc, 0)
                expect("cli fixture workspace_id", cli_out.get("workspace_id"), cli_wid)
                expect(
                    "cli fixture window counts",
                    cli_out.get("signals", {}).get("window_counts"),
                    {"snapshots": 3, "live": 3, "worktree_routes": 3, "total": 9},
                )
                expect("cli fixture ledger path", cli_out.get("ledger_path"), cli_db.name)
            finally:
                os.unlink(cli_db.name)

        # Block C: store accounting + admissions condition + derivation
        # disclosure (SA-2 / SA-3 / AR-4). RED until analyze() emits them.
        sa = s["store_accounting"]
        expect("store_accounting.total", sa["total"], 6)
        expect("store_accounting.tombstoned", sa["tombstoned"], 1)
        expect("store_accounting.alive", sa["alive"], 5)
        expect("store_accounting.never_published", sa["never_published_source_sequence_0"], 1)
        expect("store_accounting.published", sa["published_source_sequence_gt0"], 4)
        expect(
            "admissions note mentions non-empty set",
            "non-empty" in s["admissions_coverage"].get("note", ""),
            True,
        )
        expect("derivation field present", "derivation" in res, True)
        expect("derivation hex_chars", res.get("derivation", {}).get("hex_chars"), 16)
        expect("no admission value leaked", '"read_file"' in blob, False)
    finally:
        os.unlink(tmp.name)
    if failures:
        print("SELFTEST FAIL")
        for f in failures:
            print("  -", f)
        return 1
    print("SELFTEST PASS")
    return 0


# ---------------------------------------------------------------------------

def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=SKILL + " read-only ledger analyzer")
    ap.add_argument("--root", default=os.getcwd(), help="workspace root (default: cwd)")
    ap.add_argument("--ledger", default=None, help="override ledger path (fixtures)")
    ap.add_argument("--window", default="24h", help="24h|7d|all|ISO start (default 24h)")
    ap.add_argument("--json", action="store_true", help="print JSON (default output format; flag kept for the documented CLI surface)")
    ap.add_argument("--selftest", action="store_true", help="run hermetic golden-DB selftest")
    args = ap.parse_args(argv)

    if args.selftest:
        return selftest()

    root = os.path.realpath(args.root)
    now = datetime.now(timezone.utc)
    try:
        ledger = resolve_ledger(root, args.ledger)
        res = analyze(root, ledger, args.window, now)
    except NotRun as exc:
        res = {
            "skill": SKILL, "schema_version": "v1", "generated_at": now.isoformat(),
            "not_run": {"reason": str(exc)},
            "validation": {"status": "none", "notes": ["NOT_RUN: no data findings produced"]},
        }
    except Exception as exc:  # noqa: BLE001
        res = {
            "skill": SKILL, "schema_version": "v1", "generated_at": now.isoformat(),
            "not_run": {"reason": f"analysis failed: {exc}"},
            "validation": {"status": "none", "notes": ["NOT_RUN: no data findings produced"]},
        }
    print(json.dumps(res, indent=2, default=str))
    return 0


if __name__ == "__main__":
    sys.exit(main())
