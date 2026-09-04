#!/usr/bin/env python3
"""Contract tests for scripts/verify_skill_tree.py.

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
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "verify_skill_tree.py"
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
    if str(GATE.parent) not in sys.path:
        sys.path.insert(0, str(GATE.parent))
    spec = importlib.util.spec_from_file_location("verify_skill_tree", GATE)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load verify_skill_tree.py")
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
    of these tests, so this helper captures stderr and keeps the output clean.
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


def test_gate_rejects_a_long_trigger() -> None:
    """A trigger over the cap is silently truncated by the loader."""
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        skills_dir = Path(tmp) / "skills"
        (skills_dir / "probe-skill").mkdir(parents=True)
        (skills_dir / "probe-skill" / "SKILL.md").write_text(
            "---\nname: probe-skill\ndescription: Probe.\ntriggers:\n  - "
            + ("x" * (mod.SKILL_TRIGGER_MAX + 1))
            + "\n---\n\nBody.\n",
            encoding="utf-8",
        )
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_skill_dir(skills_dir)
        except SystemExit:
            if "max" not in captured.getvalue():
                raise AssertionError("expected a trigger-length rejection")
            return
    raise AssertionError("gate accepted a trigger over the cap")


def test_gate_rejects_shapes_the_go_parser_refuses() -> None:
    """Duplicate keys and stray indentation must not pass a laxer mirror."""
    mod = load_gate()
    cases = {
        "duplicate frontmatter key": "---\nname: p\ndescription: A.\nname: q\n---\n\nB.\n",
        "nested maps are not supported": "---\nname: p\n  stray: 1\ndescription: A.\n---\n\nB.\n",
    }
    for expected, body in cases.items():
        problems = mod.frontmatter_violations(body)
        if not any(expected in p for p in problems):
            raise AssertionError(
                f"expected {expected!r} among violations, got {problems}"
            )


def test_dead_skill_tree_is_refused() -> None:
    """.mivia/skills must never come back; a second copy silently drifts."""
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        mod.check_no_dead_skill_tree(root)  # absent: must not raise
        (root / ".mivia" / "skills").mkdir(parents=True)
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_no_dead_skill_tree(root)
        except SystemExit:
            if ".mivia/skills must not exist" not in captured.getvalue():
                raise AssertionError("unexpected message: " + captured.getvalue())
            return
    raise AssertionError("gate accepted a .mivia/skills directory")


def build_alias_fixture(root: Path, name: str = "probe-skill") -> Path:
    """Write one canonical skill and its matching .claude alias stub.

    Return the fixture root. The caller mutates the tree to prove the gate
    rejects each defect. A probe never goes into the real .agents/skills or
    .claude/skills tree.
    """
    mod = load_gate()
    canonical = root / ".agents" / "skills" / name
    canonical.mkdir(parents=True)
    front = f"---\nname: {name}\ndescription: Probe skill for the alias gate.\n---\n"
    canonical.joinpath("SKILL.md").write_text(
        front + "\nCanonical body.\n", encoding="utf-8"
    )
    alias = root / ".claude" / "skills" / name
    alias.mkdir(parents=True)
    alias.joinpath("SKILL.md").write_text(
        front + mod.CLAUDE_ALIAS_BODY.format(name=name), encoding="utf-8"
    )
    return root


def run_alias_gate(root: Path) -> str | None:
    """Run check_claude_skill_aliases. Return the failure text or None."""
    mod = load_gate()
    captured = io.StringIO()
    try:
        with contextlib.redirect_stderr(captured):
            mod.check_claude_skill_aliases(root)
    except SystemExit:
        return captured.getvalue().strip()
    return None


def expect_alias_rejection(mutate, expected: str) -> None:
    """Mutate a clean fixture and require the gate to reject it."""
    with tempfile.TemporaryDirectory() as tmp:
        root = build_alias_fixture(Path(tmp))
        mutate(root)
        rejection = run_alias_gate(root)
        if rejection is None:
            raise AssertionError(f"gate accepted a tree that should fail: {expected}")
        if expected not in rejection:
            raise AssertionError(
                f"expected {expected!r} in the rejection, got:\n{rejection}"
            )


def test_alias_gate_accepts_a_clean_stub() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        root = build_alias_fixture(Path(tmp))
        rejection = run_alias_gate(root)
        if rejection is not None:
            raise AssertionError(rejection)


def test_alias_gate_rejects_a_missing_stub() -> None:
    def mutate(root: Path) -> None:
        (root / ".claude" / "skills" / "probe-skill" / "SKILL.md").unlink()

    expect_alias_rejection(mutate, "is missing")


def test_alias_gate_rejects_drifted_frontmatter() -> None:
    """Claude Code picks a skill by name and description, so drift misroutes."""

    def mutate(root: Path) -> None:
        stub = root / ".claude" / "skills" / "probe-skill" / "SKILL.md"
        stub.write_text(
            stub.read_text(encoding="utf-8").replace(
                "Probe skill for the alias gate.", "A stale description."
            ),
            encoding="utf-8",
        )

    expect_alias_rejection(mutate, "frontmatter is not identical")


def test_alias_gate_rejects_a_corrupted_body() -> None:
    def mutate(root: Path) -> None:
        stub = root / ".claude" / "skills" / "probe-skill" / "SKILL.md"
        stub.write_text(
            stub.read_text(encoding="utf-8") + "Copied skill prose.\n",
            encoding="utf-8",
        )

    expect_alias_rejection(mutate, "body must be the alias text")


def test_alias_gate_rejects_a_body_naming_the_wrong_skill() -> None:
    """A copied stub that still names another skill sends the reader astray."""

    def mutate(root: Path) -> None:
        stub = root / ".claude" / "skills" / "probe-skill" / "SKILL.md"
        stub.write_text(
            stub.read_text(encoding="utf-8").replace(
                ".agents/skills/probe-skill/SKILL.md",
                ".agents/skills/other-skill/SKILL.md",
            ),
            encoding="utf-8",
        )

    expect_alias_rejection(mutate, "body must be the alias text")


def test_alias_gate_rejects_an_orphan_directory() -> None:
    def mutate(root: Path) -> None:
        orphan = root / ".claude" / "skills" / "ghost-skill"
        orphan.mkdir()
        orphan.joinpath("SKILL.md").write_text("---\nname: ghost-skill\n---\n", encoding="utf-8")

    expect_alias_rejection(mutate, "has no canonical skill")


def test_alias_gate_rejects_a_stray_resource_file() -> None:
    def mutate(root: Path) -> None:
        (root / ".claude" / "skills" / "probe-skill" / "report-template.md").write_text(
            "Duplicate resource.\n", encoding="utf-8"
        )

    expect_alias_rejection(mutate, "holds the alias stub alone")


def test_alias_gate_rejects_a_missing_claude_tree() -> None:
    def mutate(root: Path) -> None:
        (root / ".claude" / "skills" / "probe-skill" / "SKILL.md").unlink()
        (root / ".claude" / "skills" / "probe-skill").rmdir()
        (root / ".claude" / "skills").rmdir()

    expect_alias_rejection(mutate, ".claude/skills is missing")


def build_role_fixture(
    root: Path,
    skill_tools: str = "tools:\n  - read_file\n  - write_file\n",
    role_tools: str = "tools:\n- read_file\n- write_file\n",
    role_skills: str = "skills:\n- probe-skill\n",
) -> Path:
    """Write one canonical skill and one role that lists it.

    The clean fixture covers the skill's tools exactly. Each caller mutates one
    side to prove the gate rejects the mismatch. A probe never goes into the
    real .agents tree.
    """
    skill = root / ".agents" / "skills" / "probe-skill"
    skill.mkdir(parents=True)
    skill.joinpath("SKILL.md").write_text(
        "---\nname: probe-skill\ndescription: Probe skill for the role gate.\n"
        + skill_tools
        + "---\n\nCanonical body.\n",
        encoding="utf-8",
    )
    agents = root / ".agents" / "agents"
    agents.mkdir(parents=True)
    agents.joinpath("probe-role.md").write_text(
        "---\nname: probe-role\ndescription: Probe role for the role gate.\n"
        + role_tools
        + role_skills
        + "---\n\nRole prompt.\n",
        encoding="utf-8",
    )
    return root


def run_role_gate(root: Path) -> str | None:
    """Run check_agent_skill_tools. Return the failure text or None."""
    mod = load_gate()
    captured = io.StringIO()
    try:
        with contextlib.redirect_stderr(captured):
            mod.check_agent_skill_tools(root)
    except SystemExit:
        return captured.getvalue().strip()
    return None


def test_role_gate_accepts_a_covering_role() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        root = build_role_fixture(Path(tmp))
        rejection = run_role_gate(root)
        if rejection is not None:
            raise AssertionError(rejection)


def test_role_gate_rejects_a_role_missing_a_skill_tool() -> None:
    """skill_policy.go refuses the call, so the role must declare the tool."""
    with tempfile.TemporaryDirectory() as tmp:
        root = build_role_fixture(Path(tmp), role_tools="tools:\n- read_file\n")
        rejection = run_role_gate(root)
        if rejection is None:
            raise AssertionError("gate accepted a role that under-declares tools")
        if "'write_file'" not in rejection or "does not" not in rejection:
            raise AssertionError(
                "expected a missing-tool rejection, got:\n" + rejection
            )


def test_role_gate_rejects_an_empty_tool_list_against_a_skill() -> None:
    """`tools: []` is a flow sequence; the parser must read it as empty."""
    with tempfile.TemporaryDirectory() as tmp:
        root = build_role_fixture(Path(tmp), role_tools="tools: []\n")
        rejection = run_role_gate(root)
        if rejection is None:
            raise AssertionError("gate accepted a role with no tools at all")
        if "'read_file'" not in rejection:
            raise AssertionError(
                "expected the first missing tool named, got:\n" + rejection
            )


def test_role_gate_rejects_an_unknown_skill() -> None:
    """INV-AG-30 drops an unknown skill at load, so a typo disarms the role."""
    with tempfile.TemporaryDirectory() as tmp:
        root = build_role_fixture(Path(tmp), role_skills="skills:\n- probe-skil\n")
        rejection = run_role_gate(root)
        if rejection is None:
            raise AssertionError("gate accepted a role naming a skill that does not exist")
        if "has no .agents/skills/probe-skil/SKILL.md" not in rejection:
            raise AssertionError(
                "expected an unknown-skill rejection, got:\n" + rejection
            )


def test_role_gate_ignores_a_role_with_no_skills_list() -> None:
    """No allowlist means the role restricts nothing; there is nothing to check."""
    with tempfile.TemporaryDirectory() as tmp:
        root = build_role_fixture(Path(tmp), role_tools="tools: []\n", role_skills="")
        rejection = run_role_gate(root)
        if rejection is not None:
            raise AssertionError(rejection)


def test_role_gate_reads_the_committed_tree() -> None:
    """The gate must pass on the real tree it ships with."""
    mod = load_gate()
    captured = io.StringIO()
    try:
        with contextlib.redirect_stderr(captured):
            mod.check_agent_skill_tools(ROOT)
    except SystemExit:
        raise AssertionError(captured.getvalue().strip())


def main() -> None:
    test_known_keys_match_go_source()
    test_gate_accepts_schema_keys()
    test_gate_rejects_unknown_key()
    test_gate_rejects_a_long_trigger()
    test_gate_rejects_shapes_the_go_parser_refuses()
    test_dead_skill_tree_is_refused()
    test_alias_gate_accepts_a_clean_stub()
    test_alias_gate_rejects_a_missing_stub()
    test_alias_gate_rejects_drifted_frontmatter()
    test_alias_gate_rejects_a_corrupted_body()
    test_alias_gate_rejects_a_body_naming_the_wrong_skill()
    test_alias_gate_rejects_an_orphan_directory()
    test_alias_gate_rejects_a_stray_resource_file()
    test_alias_gate_rejects_a_missing_claude_tree()
    test_role_gate_accepts_a_covering_role()
    test_role_gate_rejects_a_role_missing_a_skill_tool()
    test_role_gate_rejects_an_empty_tool_list_against_a_skill()
    test_role_gate_rejects_an_unknown_skill()
    test_role_gate_ignores_a_role_with_no_skills_list()
    test_role_gate_reads_the_committed_tree()
    print("test_verify_skill_tree: ok")


if __name__ == "__main__":
    main()
