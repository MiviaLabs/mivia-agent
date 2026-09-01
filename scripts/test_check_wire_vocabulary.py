#!/usr/bin/env python3
"""Contract tests for scripts/check_wire_vocabulary.py.

Each test builds a throwaway repository root, plants exactly one defect, and
asserts the gate rejects it. The clean case proves the gate is not simply
always failing.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
GATE = REPO_ROOT / "scripts" / "check_wire_vocabulary.py"

TYPES = [
    "mivia.chat.v1.turn.started",
    "mivia.chat.v1.turn.ended",
    "mivia.chat.v1.subagent.progress",
]


def build_root(base: Path, known: list[str], events: list[str], doc_types: list[str]) -> Path:
    root = base / "repo"
    (root / "api" / "contracts").mkdir(parents=True)
    (root / "docs" / "product").mkdir(parents=True)
    contract = {
        "knownTypes": known,
        "events": {"types": {t: {"goType": "X", "fields": {}} for t in events}},
    }
    (root / "api" / "contracts" / "chat-sessions.v1.json").write_text(
        json.dumps(contract), encoding="utf-8"
    )
    body = "# Wire\n\n" + "".join(f"- `{t}`\n" for t in doc_types)
    (root / "docs" / "product" / "chat-sync-wire.md").write_text(body, encoding="utf-8")
    return root


def write_policy(base: Path) -> Path:
    policy = {
        "contract": "api/contracts/chat-sessions.v1.json",
        "scanRoots": ["docs", "api"],
        "excludeDirs": [".git"],
        "enumeratingDocs": ["docs/product/chat-sync-wire.md"],
        "exemptFiles": [],
    }
    path = base / "policy.json"
    path.write_text(json.dumps(policy), encoding="utf-8")
    return path


def run_gate(root: Path, policy: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(GATE), "--root", str(root), "--policy", str(policy)],
        capture_output=True,
        text=True,
        stdin=subprocess.DEVNULL,
    )


def case(known: list[str], events: list[str], doc_types: list[str]) -> subprocess.CompletedProcess:
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        root = build_root(base, known, events, doc_types)
        return run_gate(root, write_policy(base))


def test_clean_tree_passes() -> None:
    result = case(TYPES, TYPES, TYPES)
    assert result.returncode == 0, f"clean tree rejected:\n{result.stderr}"


def test_doc_naming_an_unrecorded_type_is_rejected() -> None:
    invented = "mivia.chat.v1.subagent.heartbeat"
    result = case(TYPES, TYPES, TYPES + [invented])
    assert result.returncode == 1, "invented type accepted"
    assert invented in result.stderr, result.stderr


def test_doc_omitting_a_recorded_type_is_rejected() -> None:
    result = case(TYPES, TYPES, TYPES[:-1])
    assert result.returncode == 1, "omitted type accepted"
    assert "never names mivia.chat.v1.subagent.progress" in result.stderr, result.stderr


def test_contract_halves_disagreeing_is_rejected() -> None:
    result = case(TYPES, TYPES[:-1], TYPES)
    assert result.returncode == 1, "contract disagreeing with itself accepted"
    assert "events.types omits" in result.stderr, result.stderr


def test_bare_prefix_and_glob_are_not_treated_as_types() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        root = build_root(base, TYPES, TYPES, TYPES)
        doc = root / "docs" / "product" / "chat-sync-wire.md"
        doc.write_text(
            doc.read_text(encoding="utf-8")
            + "\nTypes are prefixed with mivia.chat.v1. and match mivia.chat.v1.*\n",
            encoding="utf-8",
        )
        result = run_gate(root, write_policy(base))
    assert result.returncode == 0, f"prose prefix read as a type:\n{result.stderr}"


def test_missing_enumerating_doc_is_rejected() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        root = build_root(base, TYPES, TYPES, TYPES)
        (root / "docs" / "product" / "chat-sync-wire.md").unlink()
        result = run_gate(root, write_policy(base))
    assert result.returncode == 1, "missing enumerating doc accepted"
    assert "does not exist" in result.stderr, result.stderr


def test_real_repository_is_clean() -> None:
    result = subprocess.run(
        [sys.executable, str(GATE)],
        capture_output=True,
        text=True,
        stdin=subprocess.DEVNULL,
    )
    assert result.returncode == 0, f"repository is not clean:\n{result.stderr}"


def main() -> int:
    tests = [
        value
        for name, value in sorted(globals().items())
        if name.startswith("test_") and callable(value)
    ]
    for test in tests:
        test()
        print(f"  ok {test.__name__}")
    print(f"test_check_wire_vocabulary: ok ({len(tests)} tests)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
