#!/usr/bin/env python3
"""Planted-failure tests for the agent-to-skill binding contract."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "verify_agent_config.py"


def load_gate():
    spec = importlib.util.spec_from_file_location("verify_agent_config", GATE)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load verify_agent_config.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def fixture(role_frontmatter: str, skill_name: str = "probe-skill") -> Path:
    root = Path(tempfile.mkdtemp())
    (root / ".agents" / "agents").mkdir(parents=True)
    (root / ".agents" / "skills" / skill_name).mkdir(parents=True)
    (root / ".agents" / "agents" / "probe.md").write_text(
        role_frontmatter, encoding="utf-8"
    )
    (root / ".agents" / "skills" / skill_name / "SKILL.md").write_text(
        "---\nname: probe-skill\ndescription: Probe.\ntools:\n  - write_file\n---\n\nBody.\n",
        encoding="utf-8",
    )
    return root


def expect_failure(root: Path, text: str) -> None:
    gate = load_gate()
    captured = io.StringIO()
    try:
        with contextlib.redirect_stderr(captured):
            gate.check_agent_skill_contract(root)
    except SystemExit:
        if text not in captured.getvalue():
            raise AssertionError(captured.getvalue())
        return
    raise AssertionError("agent-skill contract accepted a planted failure")


def test_rejects_skill_tool_not_granted() -> None:
    root = fixture(
        "---\nname: probe\ndescription: Probe.\ntools:\n  - read_file\nskills:\n  - probe-skill\n---\n"
    )
    expect_failure(root, "agent.tools must be a superset of skill.tools")


def test_rejects_unknown_skill_name() -> None:
    root = fixture(
        "---\nname: probe\ndescription: Probe.\ntools:\n  - write_file\nskills:\n  - ghost-skill\n---\n"
    )
    expect_failure(root, "declares unknown skill(s)")


def main() -> None:
    test_rejects_skill_tool_not_granted()
    test_rejects_unknown_skill_name()
    print("test_agent_skill_contract: ok (2 tests)")


if __name__ == "__main__":
    main()
