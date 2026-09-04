#!/usr/bin/env python3
"""Contract tests for scripts/verify_agent_config.py.

The skill-frontmatter gate mirrors knownSkillKeys in
internal/skills/skill_markdown.go. A narrower mirror rejects a skill that the
loader accepts, which is a false failure in `make verify` and in the pre-push
hook. These tests keep the two sets equal and exercise the gate on a fixture.

The gate runs against a fixture directory, never against .agents/skills. That
tree is the one the mivia binary loads. `make verify` also runs verify-agent
and agent-hook-test as separate targets, so a probe skill planted in the real
tree can fail a sibling target in the same run.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import re
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "verify_agent_config.py"
GO_SOURCE = ROOT / "internal" / "skills" / "skill_markdown.go"

SKILL_TEMPLATE = """\
---
name: {name}
description: Probe skill for the frontmatter key contract test.
{extra_key}: '{{"type":"object"}}'
---

Probe body.
"""


def load_gate():
    spec = importlib.util.spec_from_file_location("verify_agent_config", GATE)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load verify_agent_config.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def go_known_skill_keys() -> set[str]:
    """The key literals in the knownSkillKeys map in the Go source.

    This parse belongs in the test, not in the gate. A miss here fails the
    test, which is safe. A miss in the gate would let an unknown key through.
    """
    src = GO_SOURCE.read_text(encoding="utf-8")
    start = src.index("var knownSkillKeys = map[string]bool{")
    end = src.index("\n}", start)
    keys = set(re.findall(r'"([^"]+)":\s*true', src[start:end]))
    if not keys:
        raise AssertionError("no keys parsed from knownSkillKeys")
    return keys


def run_gate_on_fixture(extra_key: str) -> str | None:
    """Run check_skill_dir on a fixture skill. Return the failure text or None.

    fail() raises SystemExit after it prints to stderr, so a caught SystemExit
    means the gate rejected the skill. A rejection is the expected result in one
    of these tests, so stderr is captured to keep the run output clean.
    """
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        skills_dir = Path(tmp) / "skills"
        name = "probe-skill"
        (skills_dir / name).mkdir(parents=True)
        (skills_dir / name / "SKILL.md").write_text(
            SKILL_TEMPLATE.format(name=name, extra_key=extra_key),
            encoding="utf-8",
        )
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_skill_dir(skills_dir)
        except SystemExit:
            return captured.getvalue().strip()
    return None


def test_known_keys_match_go_source() -> None:
    mod = load_gate()
    go_keys = go_known_skill_keys()
    if mod.SKILL_KNOWN_KEYS != go_keys:
        missing = sorted(go_keys - mod.SKILL_KNOWN_KEYS)
        extra = sorted(mod.SKILL_KNOWN_KEYS - go_keys)
        raise AssertionError(
            "SKILL_KNOWN_KEYS drifted from knownSkillKeys in "
            f"{GO_SOURCE.relative_to(ROOT)}: missing={missing} extra={extra}"
        )


def test_gate_accepts_schema_keys() -> None:
    for key in ("input_schema", "output_schema"):
        rejection = run_gate_on_fixture(key)
        if rejection is not None:
            raise AssertionError(rejection)


def test_gate_rejects_unknown_key() -> None:
    rejection = run_gate_on_fixture("bogus_key")
    if rejection is None:
        raise AssertionError("gate accepted an unknown frontmatter key")
    if "unknown frontmatter key 'bogus_key'" not in rejection:
        raise AssertionError(
            "expected an unknown-key rejection, got:\n" + rejection
        )


def main() -> None:
    test_known_keys_match_go_source()
    test_gate_accepts_schema_keys()
    test_gate_rejects_unknown_key()
    print("test_verify_agent_config: ok")


if __name__ == "__main__":
    main()
