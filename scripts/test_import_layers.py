#!/usr/bin/env python3
"""Contract tests for scripts/check_import_layers.py."""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_import_layers.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("check_import_layers", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_import_layers.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def base_policy(mod, *, edge_cap: int, deny=None, allow=None) -> dict:
    return {
        "description": "test fixture policy",
        "edgeCap": edge_cap,
        "deny": set(deny or []),
        "allow": {k: set(v) for k, v in (allow or {}).items()},
    }


def write_policy_json(path: Path, payload) -> None:
    path.write_text(json.dumps(payload), encoding="utf-8")


def test_real_tree_passes() -> None:
    proc = subprocess.run(
        [sys.executable, str(CHECKER)],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    assert proc.returncode == 0, proc.stdout + proc.stderr


def test_edge_outside_allow_and_not_denied_fails() -> None:
    mod = load_mod()
    policy = base_policy(mod, edge_cap=10, allow={"a": ["c"]})
    edges = {("a", "b")}
    problems = mod.check(edges, policy)
    assert any("not declared" in p for p in problems), problems


def test_deny_listed_edge_fails_even_if_also_in_allow() -> None:
    mod = load_mod()
    policy = base_policy(
        mod,
        edge_cap=10,
        deny=[("a", "b")],
        allow={"a": ["b"]},
    )
    edges = {("a", "b")}
    problems = mod.check(edges, policy)
    assert any("denied edge" in p for p in problems), problems
    # Only the deny violation fires; the same edge is not also reported
    # as "not declared" just because it happens to be deny-listed too.
    assert not any("not declared" in p for p in problems), problems


def test_edge_count_over_cap_fails_even_with_every_edge_allowed() -> None:
    mod = load_mod()
    policy = base_policy(
        mod,
        edge_cap=1,
        allow={"a": ["b"], "c": ["d"]},
    )
    edges = {("a", "b"), ("c", "d")}
    problems = mod.check(edges, policy)
    assert any("exceeds edgeCap" in p for p in problems), problems
    # Every individual edge is allowed; the cap is the only violation.
    assert len(problems) == 1, problems


def test_conforming_tree_passes() -> None:
    mod = load_mod()
    policy = base_policy(
        mod,
        edge_cap=5,
        deny=[("x", "y")],
        allow={"a": ["b"], "c": ["d"]},
    )
    edges = {("a", "b"), ("c", "d")}
    problems = mod.check(edges, policy)
    assert problems == [], problems


def test_stale_allow_row_fails() -> None:
    mod = load_mod()
    policy = base_policy(
        mod,
        edge_cap=5,
        allow={"a": ["b", "c"]},
    )
    edges = {("a", "b")}
    problems = mod.check(edges, policy)
    assert any("stale allow entry" in p for p in problems), problems


def test_malformed_policy_missing_edge_cap_fails_closed() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "import-layers.json"
        write_policy_json(p, {"description": "x", "deny": [], "allow": {}})
        try:
            mod.load_policy(p)
            raise AssertionError("expected PolicyError")
        except mod.PolicyError as exc:
            assert "edgeCap" in str(exc), exc


def test_malformed_policy_non_list_deny_fails_closed() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "import-layers.json"
        write_policy_json(
            p,
            {"description": "x", "edgeCap": 10, "deny": {"from": "a", "to": "b"}, "allow": {}},
        )
        try:
            mod.load_policy(p)
            raise AssertionError("expected PolicyError")
        except mod.PolicyError as exc:
            assert "'deny' must be a list" in str(exc), exc


def test_malformed_policy_missing_allow_fails_closed() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "import-layers.json"
        write_policy_json(p, {"description": "x", "edgeCap": 10, "deny": []})
        try:
            mod.load_policy(p)
            raise AssertionError("expected PolicyError")
        except mod.PolicyError as exc:
            assert "allow" in str(exc), exc


def test_malformed_policy_invalid_json_fails_closed() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "import-layers.json"
        p.write_text("{not valid json", encoding="utf-8")
        try:
            mod.load_policy(p)
            raise AssertionError("expected PolicyError")
        except mod.PolicyError as exc:
            assert "invalid JSON" in str(exc), exc


def test_malformed_policy_deny_entry_missing_reason_fails_closed() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        p = Path(td) / "import-layers.json"
        write_policy_json(
            p,
            {
                "description": "x",
                "edgeCap": 10,
                "deny": [{"from": "a", "to": "b"}],
                "allow": {},
            },
        )
        try:
            mod.load_policy(p)
            raise AssertionError("expected PolicyError")
        except mod.PolicyError as exc:
            assert "reason" in str(exc), exc


def test_committed_policy_loads_and_validates() -> None:
    mod = load_mod()
    policy = mod.load_policy()
    assert isinstance(policy["edgeCap"], int)
    assert ("internal/storage", "internal/contextmgr") in policy["deny"]


def main() -> None:
    test_edge_outside_allow_and_not_denied_fails()
    test_deny_listed_edge_fails_even_if_also_in_allow()
    test_edge_count_over_cap_fails_even_with_every_edge_allowed()
    test_conforming_tree_passes()
    test_stale_allow_row_fails()
    test_malformed_policy_missing_edge_cap_fails_closed()
    test_malformed_policy_non_list_deny_fails_closed()
    test_malformed_policy_missing_allow_fails_closed()
    test_malformed_policy_invalid_json_fails_closed()
    test_malformed_policy_deny_entry_missing_reason_fails_closed()
    test_committed_policy_loads_and_validates()
    test_real_tree_passes()
    print("test_import_layers: ok")


if __name__ == "__main__":
    main()
