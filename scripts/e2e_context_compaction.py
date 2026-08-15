#!/usr/bin/env python3
"""End-to-end check of context compaction through the REAL mivia binary.

Every assertion here reads a surface a user or a host app actually observes:
the NDJSON wire, and the durable SQLite checkpoint. Nothing imports Go
packages or reaches into internals, so a regression that only unit tests would
catch by construction cannot make this pass.

Two backends:

  --provider stub   a local OpenAI-compatible server this script runs itself.
                    Hermetic, no credentials, safe anywhere. This is the
                    default.
  --provider real   a real provider over the network, configured from the
                    environment (DEEPSEEK_API_KEY, OPENROUTER_API_KEY,
                    ZAI_API_KEY, or OLLAMA_API_KEY). Costs money and needs a
                    key.

NEVER run this from `make verify`, CI, or any automated path, and never run
the `real` backend without the user explicitly asking in that session: it
spends their credits. It is a manual check, like the e2e workflows in
AGENTS.md.

    scripts/e2e_context_compaction.py                    # hermetic
    scripts/e2e_context_compaction.py --provider real    # real API calls

Exit code is 0 only when every scenario passes.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
BINARY = REPO / "mivia"

# The first sentence of summarySystemPrompt in internal/contextmgr. The stub
# answers a summarize request only when it sees this, so a scenario cannot
# pass by mistaking an ordinary turn for a summary call.
SUMMARY_MARKER = "You summarize an earlier part of a conversation."
# provider.Message.Name on the rendered summary (agent.SummaryMessageName).
SUMMARY_NAME = "context-summary"

# Real providers, in the order they are tried. Each entry is
# (provider name, env var, model, base_url).
REAL_PROVIDERS = [
    ("deepseek", "DEEPSEEK_API_KEY", "deepseek-v4-flash", "https://api.deepseek.com/v1"),
    ("openrouter", "OPENROUTER_API_KEY", "openai/gpt-4o-mini", "https://openrouter.ai/api/v1"),
    ("zai", "ZAI_API_KEY", "glm-5.2", "https://api.z.ai/api/paas/v4"),
    ("ollama", "OLLAMA_API_KEY", "gpt-oss:120b", "https://ollama.com/v1"),
]


# --------------------------------------------------------------------------
# Stub provider
# --------------------------------------------------------------------------


class _StubHandler(BaseHTTPRequestHandler):
    def log_message(self, *_args):  # noqa: D102 - silence the default logger
        pass

    def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler API
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        messages = body.get("messages", [])
        is_summary = bool(messages) and messages[0].get("role") == "system" and (
            SUMMARY_MARKER in (messages[0].get("content") or "")
        )
        carries = any(
            "[host-injected context summary" in (m.get("content") or "") for m in messages
        )
        self.server.calls.append({"summary": is_summary, "carries_summary": carries})

        if is_summary:
            content = _summary_echo_reply(messages[-1].get("content") or "")
        else:
            content = "ok"
        payload = json.dumps(
            {
                "id": "1",
                "object": "chat.completion",
                "model": body.get("model", "stub"),
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": content},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def _summary_echo_reply(prompt: str) -> str:
    """Echo the version and source_range the summarize prompt mandates.

    The host validates the reply against the exact request that produced it, so
    a canned constant would be rejected and every scenario would degrade to
    structural-only - passing for the wrong reason.
    """
    version = "1"
    source_range = "{}"
    match = re.search(r"^version: (.+)$", prompt, re.M)
    if match:
        version = match.group(1).strip()
    match = re.search(r"^source_range: (.+)$", prompt, re.M)
    if match:
        source_range = match.group(1).strip()
    return json.dumps(
        {
            "version": int(version),
            "objective": "the user objective",
            "state": "work continued",
            "decisions": [],
            "evidence": [],
            "changed_surfaces": [],
            "open_work": [],
            "risks": [],
            "source_range": json.loads(source_range),
        }
    )


class StubServer:
    def __init__(self):
        self.httpd = HTTPServer(("127.0.0.1", 0), _StubHandler)
        self.httpd.calls = []
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_exc):
        self.httpd.shutdown()

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}/v1"

    def summary_calls(self) -> int:
        return sum(1 for c in self.httpd.calls if c["summary"])

    def calls_carrying_summary(self) -> int:
        return sum(1 for c in self.httpd.calls if c["carries_summary"])

    def reset(self):
        self.httpd.calls.clear()


# --------------------------------------------------------------------------
# Workspace + binary driving
# --------------------------------------------------------------------------


CONFIG = """model = "{model}"

[provider]
name = "{provider}"

[providers.{provider}]
models = [ {{ name = "{model}", context_window_tokens = {window}, max_output_tokens = {output} }} ]
default_model = "{model}"
api_key_env = "{key_env}"
base_url = "{base_url}"

[context]
store_backend = "sqlite"
{summary_section}
{privacy_section}
"""

SUMMARY_ON = '\n[context.summary]\nenabled = true\n'
PRIVACY_ON = '\n[privacy]\nredaction_patterns = ["never-match-this-e2e-pattern"]\n'


@dataclass
class Backend:
    provider: str
    model: str
    base_url: str
    key_env: str
    key_value: str
    stub: StubServer | None = None


@dataclass
class Workspace:
    root: Path
    home: Path

    @property
    def context_db(self) -> Path:
        return self.home / ".mivia" / "context.db"


@dataclass
class Result:
    name: str
    passed: bool
    detail: str = ""


@dataclass
class Report:
    results: list[Result] = field(default_factory=list)

    def check(self, name: str, ok: bool, detail: str = "", evidence: str = "") -> bool:
        """detail explains a FAILURE; evidence annotates either outcome."""
        self.results.append(Result(name, ok, detail))
        mark = "PASS" if ok else "FAIL"
        line = f"  [{mark}] {name}"
        if evidence:
            line += f" ({evidence})"
        if not ok and detail:
            line += f"\n         {detail}"
        print(line, flush=True)
        return ok

    @property
    def ok(self) -> bool:
        return all(r.passed for r in self.results)


def make_workspace(
    base: Path,
    name: str,
    backend: Backend,
    *,
    summary: bool,
    privacy: bool,
    window: int | None = None,
    output: int | None = None,
) -> Workspace:
    root = base / name
    (root / ".mivia").mkdir(parents=True)
    home = root / "home"
    home.mkdir()
    # Larger windows for a real provider: its tokenizer differs from the
    # host's estimate, and a window sized for the stub can refuse the very
    # first turn instead of compacting later ones.
    if window is None or output is None:
        window, output = (1600, 200) if backend.stub else (4000, 500)
    (root / ".mivia" / "mivia.toml").write_text(
        CONFIG.format(
            provider=backend.provider,
            model=backend.model,
            window=window,
            output=output,
            key_env=backend.key_env,
            base_url=backend.base_url,
            summary_section=SUMMARY_ON if summary else "",
            privacy_section=PRIVACY_ON if privacy else "",
        )
    )
    subprocess.run(["git", "init", "-q"], cwd=root, check=False)
    return Workspace(root=root, home=home)


def run_chat(ws: Workspace, backend: Backend, lines: list[str], *, tools: bool = False) -> list[dict]:
    """Drive one `mivia chat --json` session and return its NDJSON events."""
    env = dict(os.environ)
    env["HOME"] = str(ws.home)
    env[backend.key_env] = backend.key_value
    args = [str(BINARY), "chat", "--json"]
    if not tools:
        args.append("--no-tools")
    proc = subprocess.run(
        args,
        cwd=ws.root,
        env=env,
        input="\n".join(lines) + "\n",
        capture_output=True,
        text=True,
        timeout=600,
    )
    (ws.root / "stderr.log").write_text(proc.stderr)
    events = []
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return events


def checkpoint_bodies(ws: Workspace) -> list[str]:
    """Every durable checkpoint's active context, oldest first."""
    if not ws.context_db.exists():
        return []
    db = sqlite3.connect(ws.context_db)
    try:
        rows = db.execute(
            "SELECT active_context FROM context_checkpoints ORDER BY rowid"
        ).fetchall()
    finally:
        db.close()
    bodies = []
    for (blob,) in rows:
        if isinstance(blob, (bytes, bytearray)):
            bodies.append(blob.decode(errors="replace"))
        else:
            bodies.append(str(blob))
    return bodies


def exported_source(ws: Workspace) -> str:
    """Every source-event payload body, decoded.

    Source projection must never carry the summary: it is host-generated and
    has no source event of its own (INV-AG-32/39). Payload bodies are stored
    separately from the events, so both are scanned.
    """
    if not ws.context_db.exists():
        return ""
    db = sqlite3.connect(ws.context_db)
    try:
        parts = []
        for (blob,) in db.execute("SELECT data FROM content").fetchall():
            if isinstance(blob, (bytes, bytearray)):
                parts.append(blob.decode(errors="replace"))
            elif blob is not None:
                parts.append(str(blob))
        return "\n".join(parts)
    finally:
        db.close()


def big(marker: str, reps: int = 60) -> str:
    return f"{marker} " + "lorem ipsum dolor sit " * reps


# --------------------------------------------------------------------------
# Scenarios
# --------------------------------------------------------------------------


def scenario_automatic(base: Path, backend: Backend, report: Report) -> None:
    """A threshold compaction must announce itself, summarize, and persist."""
    print("\n[1] automatic compaction: event + summary + durability")
    if backend.stub:
        backend.stub.reset()
    ws = make_workspace(base, "auto", backend, summary=True, privacy=True)
    turns = [big(f"msg{i}-MARKER") for i in range(1, 8)]
    events = run_chat(ws, backend, turns)

    compactions = [e for e in events if e.get("type") == "compaction"]
    report.check(
        "a threshold compaction reaches the json wire",
        bool(compactions),
        detail="no compaction event reached the wire",
        evidence=f"{len(compactions)} compaction event(s)",
    )
    if compactions:
        rec = compactions[0].get("compaction") or {}
        report.check(
            "the compaction record reports a real reduction",
            rec.get("before_tokens", 0) > rec.get("after_tokens", 0),
            detail="the record reports no reduction",
            evidence=f"{rec.get('before_tokens')} -> {rec.get('after_tokens')}",
        )

    bodies = checkpoint_bodies(ws)
    report.check("durable checkpoints were written", bool(bodies), detail="no checkpoint was written", evidence=f"{len(bodies)} checkpoint(s)")
    if bodies:
        report.check(
            "the latest checkpoint carries the compaction summary",
            SUMMARY_NAME in bodies[-1],
            detail="no context-summary in the durable active context",
        )
        # Durability across the boundary: the summary must be in a checkpoint
        # written AFTER the one that first compacted, not only in that one.
        first = next((i for i, b in enumerate(bodies) if SUMMARY_NAME in b), None)
        report.check(
            "the summary survives later turns, not just the compacting one",
            first is not None and first < len(bodies) - 1 and SUMMARY_NAME in bodies[-1],
            detail="the summary did not outlive the compacting turn",
            evidence=f"first summary checkpoint {first} of {len(bodies)}",
        )
        report.check(
            "old turns really were dropped (compaction did work)",
            "msg1-MARKER" not in bodies[-1],
            detail="msg1 is still present; nothing was compacted away",
        )

    report.check(
        "the summary stays out of source projection",
        SUMMARY_NAME not in exported_source(ws),
        detail="summary leaked into the durable source events",
    )
    if backend.stub:
        report.check(
            "the summarizer was really called over the wire",
            backend.stub.summary_calls() > 0,
            detail="the summarizer was never called",
            evidence=f"{backend.stub.summary_calls()} summarize request(s)",
        )
        report.check(
            "later requests carry the injected summary",
            backend.stub.calls_carrying_summary() > 0,
            detail="no request carried the injected summary",
            evidence=f"{backend.stub.calls_carrying_summary()} request(s)",
        )


def scenario_manual(base: Path, backend: Backend, report: Report) -> None:
    """/compact must summarize too, not return an instant structural cut."""
    print("\n[2] manual /compact: summary is produced and persisted")
    if backend.stub:
        backend.stub.reset()
    ws = make_workspace(base, "manual", backend, summary=True, privacy=True)
    turns = [big(f"m{i}-MARKER") for i in range(1, 5)] + ["/compact"]
    events = run_chat(ws, backend, turns)

    report.check(
        "/compact emits a typed compaction event",
        any(e.get("type") == "compaction" for e in events),
        detail="no compaction event on the wire",
    )
    report.check(
        "/compact does not report structural-only on a configured workspace",
        not any(
            "structural only" in (e.get("message") or "") for e in events
        ),
        detail="the run reported structural-only despite a configured summary",
    )
    bodies = checkpoint_bodies(ws)
    report.check(
        "a manual compact persists its summary",
        bool(bodies) and SUMMARY_NAME in bodies[-1],
        detail="no context-summary in the durable active context",
    )
    if backend.stub:
        report.check(
            "/compact really called the summarizer",
            backend.stub.summary_calls() > 0,
            detail="the summarizer was never called",
            evidence=f"{backend.stub.summary_calls()} summarize request(s)",
        )


def scenario_agent_loop(base: Path, backend: Backend, report: Report) -> None:
    """The tool-enabled path has its own injector and its own commit seam."""
    print("\n[3] agent-loop (tools enabled) path")
    if backend.stub:
        backend.stub.reset()
    # The published tool schemas are themselves ~8.5k tokens of prompt, so this
    # path needs a window that fits the tool surface AND enough conversation on
    # top to cross the threshold. Sized too small, every turn fails with
    # "prompt budget exceeded" before a compaction can ever happen - the run
    # would report a product bug that is really a harness bug.
    ws = make_workspace(
        base, "agent", backend, summary=True, privacy=True, window=24000, output=1000
    )
    turns = [big(f"t{i}-MARKER", reps=600) for i in range(1, 15)]
    events = run_chat(ws, backend, turns, tools=True)

    budget_errors = [
        e for e in events if "prompt budget exceeded" in (e.get("message") or "")
    ]
    report.check(
        "the tools scenario is sized so turns can actually run",
        not budget_errors,
        detail=f"every turn hit the prompt budget: {budget_errors[:1]}",
    )
    # The planner's trigger is 80% of the prompt budget as IT prices the
    # request (messages plus tool schemas), so the scenario must push past
    # that, not merely past the status line's percentage.
    report.check(
        "the tools scenario really reaches a compaction",
        any(e.get("type") == "compaction" for e in events),
        detail="no compaction event on the tools path; the conversation never crossed the planner trigger",
    )
    bodies = checkpoint_bodies(ws)
    report.check(
        "the agent path persists its summary",
        bool(bodies) and SUMMARY_NAME in bodies[-1],
        detail="no context-summary in the durable active context",
    )


def scenario_gate_off(base: Path, backend: Backend, report: Report) -> None:
    """An unconfigured workspace must SAY it is structural-only."""
    print("\n[4] summary gate off: honest, diagnosable structural-only compaction")
    for name, summary, privacy, expect in [
        ("no-summary-flag", False, True, "context.summary"),
        ("no-privacy", True, False, "privacy"),
    ]:
        if backend.stub:
            backend.stub.reset()
        ws = make_workspace(base, f"off-{name}", backend, summary=summary, privacy=privacy)
        turns = [big(f"g{i}-MARKER") for i in range(1, 5)] + ["/compact"]
        events = run_chat(ws, backend, turns)

        notices = [
            e.get("message") or ""
            for e in events
            if "structural only" in (e.get("message") or "")
        ]
        report.check(
            f"{name}: /compact reports structural-only",
            bool(notices),
            detail="the run compacted silently with no explanation",
        )
        report.check(
            f"{name}: the notice names the missing condition",
            any(expect in n for n in notices),
            detail=f"notices did not mention {expect!r}: {notices}",
        )
        bodies = checkpoint_bodies(ws)
        report.check(
            f"{name}: no summary is persisted",
            not bodies or SUMMARY_NAME not in bodies[-1],
            detail="a summary was persisted with the gate off",
        )
        if backend.stub:
            report.check(
                f"{name}: no summarize request is sent",
                backend.stub.summary_calls() == 0,
                detail=f"{backend.stub.summary_calls()} unexpected summarize request(s)",
            )


# --------------------------------------------------------------------------


def resolve_real_backend() -> Backend:
    for provider, env, model, url in REAL_PROVIDERS:
        key = os.environ.get(env, "").strip()
        if key:
            print(f"real provider: {provider} ({model})")
            return Backend(provider=provider, model=model, base_url=url, key_env=env, key_value=key)
    names = ", ".join(env for _, env, _, _ in REAL_PROVIDERS)
    raise SystemExit(
        f"--provider real needs one of these set in the environment: {names}"
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provider", choices=("stub", "real"), default="stub")
    parser.add_argument("--keep", action="store_true", help="keep the temp workspaces")
    args = parser.parse_args()

    if not BINARY.exists():
        print(f"building {BINARY} ...", flush=True)
        subprocess.run(["make", "build"], cwd=REPO, check=True)

    base = Path(tempfile.mkdtemp(prefix="mivia-e2e-compaction-"))
    report = Report()
    stub = None
    try:
        if args.provider == "real":
            backend = resolve_real_backend()
        else:
            stub = StubServer().__enter__()
            backend = Backend(
                provider="ollama",  # any registered name; base_url points at the stub
                model="stub-model",
                base_url=stub.base_url,
                key_env="OLLAMA_API_KEY",
                key_value="stub-key",
                stub=stub,
            )
            print(f"stub provider on {stub.base_url}")

        print(f"workspaces: {base}")
        scenario_automatic(base, backend, report)
        scenario_manual(base, backend, report)
        scenario_agent_loop(base, backend, report)
        scenario_gate_off(base, backend, report)
    finally:
        if stub is not None:
            stub.__exit__()
        if not args.keep:
            shutil.rmtree(base, ignore_errors=True)
        else:
            print(f"\nkept: {base}")

    failed = [r for r in report.results if not r.passed]
    print(
        f"\n{len(report.results) - len(failed)}/{len(report.results)} checks passed"
        f" ({args.provider} provider)"
    )
    for r in failed:
        print(f"  FAILED: {r.name} - {r.detail}")
    return 0 if report.ok else 1


if __name__ == "__main__":
    sys.exit(main())
