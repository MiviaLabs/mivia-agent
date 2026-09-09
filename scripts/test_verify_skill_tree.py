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


def test_gate_rejects_an_absent_skill_tree() -> None:
    """An absent .agents/skills must fail, not glob nothing and pass.

    check_skill_dir globs, check_claude_skill_aliases builds an empty map from
    the same tree, so with the directory gone every check is a no-op and the
    entry point printed ok on a checkout that holds no skills at all. The
    guard lives in check_skill_dir so both entry points inherit it.
    """
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_skill_dir(Path(tmp) / "skills")
        except SystemExit:
            if "is missing" not in captured.getvalue():
                raise AssertionError(captured.getvalue())
            return
    raise AssertionError("gate accepted a tree with no .agents/skills")


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
        # Drive the same shape through the gate. Testing the helper alone left
        # the call site unpinned: replacing it with an empty loop kept the
        # suite green, so the wiring could be deleted without notice.
        with tempfile.TemporaryDirectory() as tmp:
            skills_dir = Path(tmp) / "skills"
            (skills_dir / "probe-skill").mkdir(parents=True)
            (skills_dir / "probe-skill" / "SKILL.md").write_text(
                body.replace("name: p", "name: probe-skill"), encoding="utf-8")
            captured = io.StringIO()
            try:
                with contextlib.redirect_stderr(captured):
                    mod.check_skill_dir(skills_dir)
            except SystemExit:
                if "rejected by internal/skills/frontmatter.go" not in captured.getvalue():
                    raise AssertionError(captured.getvalue())
                continue
            raise AssertionError(f"check_skill_dir accepted {expected!r}")


def test_gate_rejects_a_long_joined_trigger_block() -> None:
    """The joined cap is a separate rule from the per-trigger cap.

    Each trigger here is well under SKILL_TRIGGER_MAX, so only the joined rule
    can reject the file. Without this, replacing that rule with `if False:`
    left the suite green.
    """
    mod = load_gate()
    triggers = [f"probe trigger number {n:02d} for the joined cap" for n in range(12)]
    joined = "\n".join(triggers)
    if len(joined.encode("utf-8")) <= mod.SKILL_TRIGGERS_JOINED_MAX:
        raise AssertionError("fixture does not exceed the joined cap")
    if max(len(t.encode("utf-8")) for t in triggers) > mod.SKILL_TRIGGER_MAX:
        raise AssertionError("fixture trips the per-trigger cap instead")
    body = (
        "---\nname: probe-skill\n"
        "description: Probe skill for the joined trigger cap.\ntriggers:\n"
        + "".join(f"  - {t}\n" for t in triggers)
        + "---\n\nProbe body.\n"
    )
    with tempfile.TemporaryDirectory() as tmp:
        skills_dir = Path(tmp) / "skills"
        (skills_dir / "probe-skill").mkdir(parents=True)
        (skills_dir / "probe-skill" / "SKILL.md").write_text(body, encoding="utf-8")
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_skill_dir(skills_dir)
        except SystemExit:
            if "joined block" not in captured.getvalue():
                raise AssertionError(captured.getvalue())
            return
    raise AssertionError("gate accepted a joined trigger block over the cap")


def skill_fixture(tmp: str, body: str, name: str = "probe-skill") -> Path:
    """Write one canonical skill and return its directory."""
    skills_dir = Path(tmp) / "skills"
    (skills_dir / name).mkdir(parents=True)
    (skills_dir / name / "SKILL.md").write_text(body, encoding="utf-8")
    return skills_dir


def run_check_skill_dir(body: str, name: str = "probe-skill") -> str | None:
    """Drive check_skill_dir over one fixture skill. Return the failure text."""
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        skills_dir = skill_fixture(tmp, body, name)
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_skill_dir(skills_dir)
        except SystemExit:
            return captured.getvalue().strip()
    return None


BASIC_SKILL = """\
---
name: probe-skill
description: Probe skill for the check_skill_dir rules.
---

Probe body.
"""


def test_gate_rejects_a_name_that_is_not_its_directory() -> None:
    """The rewritten name check had no test at all.

    Every other fixture writes `name: probe-skill` into `probe-skill/`, so the
    mismatch branch never executed and the whole rule could be deleted with
    the suite green.
    """
    body = BASIC_SKILL.replace("name: probe-skill", "name: probe-other")
    rejection = run_check_skill_dir(body)
    if rejection is None or "but the directory is" not in rejection:
        raise AssertionError(f"expected a name-mismatch rejection, got: {rejection}")


def test_gate_rejects_an_over_length_short_description() -> None:
    """The loader truncates this silently, so only the gate can catch it."""
    mod = load_gate()
    value = "x" * (mod.SKILL_SHORT_DESCRIPTION_MAX + 1)
    body = BASIC_SKILL.replace(
        "description: Probe skill for the check_skill_dir rules.",
        f"description: Probe.\nshort-description: {value}",
    )
    rejection = run_check_skill_dir(body)
    if rejection is None or "short-description is" not in rejection:
        raise AssertionError(f"expected a short-description cap rejection, got: {rejection}")


def test_gate_rejects_an_over_length_argument_hint() -> None:
    mod = load_gate()
    value = "x" * (mod.SKILL_ARGS_HINT_MAX + 1)
    body = BASIC_SKILL.replace(
        "description: Probe skill for the check_skill_dir rules.",
        f"description: Probe.\nargument-hint: {value}",
    )
    rejection = run_check_skill_dir(body)
    if rejection is None or "argument-hint is" not in rejection:
        raise AssertionError(f"expected an argument-hint cap rejection, got: {rejection}")


def test_gate_rejects_an_unbalanced_quote_the_loader_refuses() -> None:
    """internal/skills/frontmatter.go errors on this and drops the skill."""
    body = BASIC_SKILL.replace(
        "description: Probe skill for the check_skill_dir rules.",
        'description: Probe.\nshort-description: "abc',
    )
    rejection = run_check_skill_dir(body)
    if rejection is None or "never closes" not in rejection:
        raise AssertionError(f"expected an unbalanced-quote rejection, got: {rejection}")


def test_check_skill_dir_rejects_missing_frontmatter() -> None:
    """check_skill_dir's own copy of this rule was unpinned.

    The alias gate has an equivalent rule with identical message text, so a
    test that did not assert the .agents/skills path could be satisfied by the
    wrong site.
    """
    rejection = run_check_skill_dir("# No frontmatter here\n\nBody.\n")
    if rejection is None:
        raise AssertionError("gate accepted a skill with no frontmatter")
    # Assert the rule's own text, not just "some rejection happened" - the
    # unparenthesised `or ... and ...` here used to collapse to that, and
    # a run through .agents/skills always contains "skills/probe-skill"
    # regardless of which rule fired.
    if "missing YAML frontmatter" not in rejection:
        raise AssertionError(f"expected the missing-frontmatter rule, got: {rejection}")
    if "declares no name" in rejection:
        raise AssertionError(f"the no-name rule fired instead: {rejection}")


def test_check_skill_dir_rejects_frontmatter_with_no_name() -> None:
    body = BASIC_SKILL.replace("name: probe-skill\n", "")
    rejection = run_check_skill_dir(body)
    if rejection is None or "declares no name" not in rejection:
        raise AssertionError(f"expected a no-name rejection, got: {rejection}")


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


def test_gate_measures_description_in_bytes() -> None:
    """The loader compares Go len() on a string, which counts bytes.

    A 200-character description holding one multi-byte rune is 202 bytes. It
    passed a character count and was then truncated mid-sentence at load, which
    is the exact silent truncation this cap exists to stop.
    """
    mod = load_gate()
    body = "x" * 199 + "\u2014"  # 200 characters, 202 bytes
    with tempfile.TemporaryDirectory() as tmp:
        skills_dir = Path(tmp) / "skills"
        (skills_dir / "probe-skill").mkdir(parents=True)
        (skills_dir / "probe-skill" / "SKILL.md").write_text(
            f"---\nname: probe-skill\ndescription: {body}\n---\n\nBody.\n",
            encoding="utf-8",
        )
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_skill_dir(skills_dir)
        except SystemExit:
            if "bytes" not in captured.getvalue():
                raise AssertionError(
                    "expected a byte-count rejection, got:\n" + captured.getvalue()
                )
            return
    raise AssertionError(
        "gate accepted a description of 200 characters and 202 bytes"
    )


def test_alias_gate_rejects_a_canonical_file_without_frontmatter() -> None:
    def mutate(root: Path) -> None:
        target = root / ".agents" / "skills" / "probe-skill" / "SKILL.md"
        target.write_text("No frontmatter here.\n", encoding="utf-8")

    expect_alias_rejection(
        mutate, ".agents/skills/probe-skill/SKILL.md: missing YAML frontmatter"
    )


def test_alias_gate_rejects_a_stub_without_frontmatter() -> None:
    def mutate(root: Path) -> None:
        target = root / ".claude" / "skills" / "probe-skill" / "SKILL.md"
        target.write_text("No frontmatter here.\n", encoding="utf-8")

    expect_alias_rejection(
        mutate, ".claude/skills/probe-skill/SKILL.md: missing YAML frontmatter"
    )


def test_alias_gate_rejects_a_plain_file_in_the_tree_root() -> None:
    """.claude/skills holds one directory per skill and nothing else."""
    def mutate(root: Path) -> None:
        (root / ".claude" / "skills" / "README.md").write_text("x\n", encoding="utf-8")

    expect_alias_rejection(mutate, "is a file")


def main() -> None:
    test_known_keys_match_go_source()
    test_gate_rejects_an_absent_skill_tree()
    test_gate_accepts_schema_keys()
    test_gate_rejects_unknown_key()
    test_gate_rejects_a_long_trigger()
    test_gate_rejects_shapes_the_go_parser_refuses()
    test_gate_rejects_a_long_joined_trigger_block()
    test_gate_rejects_a_name_that_is_not_its_directory()
    test_gate_rejects_an_over_length_short_description()
    test_gate_rejects_an_over_length_argument_hint()
    test_gate_rejects_an_unbalanced_quote_the_loader_refuses()
    test_check_skill_dir_rejects_missing_frontmatter()
    test_check_skill_dir_rejects_frontmatter_with_no_name()
    test_dead_skill_tree_is_refused()
    test_alias_gate_accepts_a_clean_stub()
    test_alias_gate_rejects_a_missing_stub()
    test_alias_gate_rejects_drifted_frontmatter()
    test_alias_gate_rejects_a_corrupted_body()
    test_alias_gate_rejects_a_body_naming_the_wrong_skill()
    test_alias_gate_rejects_an_orphan_directory()
    test_alias_gate_rejects_a_stray_resource_file()
    test_alias_gate_rejects_a_missing_claude_tree()
    test_gate_measures_description_in_bytes()
    test_alias_gate_rejects_a_canonical_file_without_frontmatter()
    test_alias_gate_rejects_a_stub_without_frontmatter()
    test_alias_gate_rejects_a_plain_file_in_the_tree_root()
    print("test_verify_skill_tree: ok")


if __name__ == "__main__":
    main()
