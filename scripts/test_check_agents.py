#!/usr/bin/env python3
"""Contract tests for the subagent roster gate."""

from __future__ import annotations

import importlib.util
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "check_agents.py"


def load_gate():
    spec = importlib.util.spec_from_file_location("check_agents", GATE)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def run(body: str, filename: str = "builder.md") -> list[str]:
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / filename
        path.write_text(body, encoding="utf-8")
        return mod._check_role(path)


GOOD = """---
name: builder
description: Build changes.
tools:
  - read_file
---
## Disallowed operations

Do not commit.
"""


def test_roster_invariants_are_enforced() -> None:
    cases = [
        (GOOD.replace("name: builder", "name: wrong"), "does not match"),
        (GOOD.replace("description: Build changes.\n", "bogus: value\n"), "unknown"),
        (GOOD.replace("  - read_file", ""), "empty"),
        (GOOD.replace("## Disallowed operations", "## Other"), "Disallowed operations"),
    ]
    for body, expected in cases:
        failures = run(body)
        assert any(expected in failure for failure in failures), failures


def test_good_role_passes() -> None:
    assert run(GOOD) == []


def test_role_name_uses_identifier_pattern() -> None:
    custom = GOOD.replace("name: builder", "name: custom-role_2").replace(
        "## Disallowed operations", "## Guidelines"
    )
    assert run(custom, "custom-role_2.md") == []

    malformed = custom.replace("name: custom-role_2", "name: Custom.role")
    failures = run(malformed, "Custom.role.md")
    assert any("role 'Custom.role' is not a valid lowercase identifier" in failure for failure in failures), failures


if __name__ == "__main__":
    test_roster_invariants_are_enforced()
    test_good_role_passes()
    test_role_name_uses_identifier_pattern()
    print("test_check_agents: ok")
