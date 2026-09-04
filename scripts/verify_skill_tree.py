#!/usr/bin/env python3
"""Skill-tree contract checks for scripts/verify_agent_config.py.

Split out of that gate to keep both files under the repository's file-size
norm. These functions carry the whole skill surface: the frontmatter contract
that mirrors internal/skills, the refusal of a second skill tree, and the
Claude Code alias stubs. verify_agent_config.py imports and calls them.

Each check takes its root or directory as an argument so a test can drive it
against a fixture. A test must never write a probe skill into .agents/skills.
"""

from __future__ import annotations

from pathlib import Path

import functools

from verify_common import ROOT, rel_to_root
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="verify_skill_tree")


# Mirrors descriptionMaxLen in internal/skills/skill_markdown.go. loader.go
# applies it through SanitizeModelFacingText.
#
# The caps count BYTES, not characters. internal/skills/skills.go compares Go
# len() on a string, which is a byte count, so measure with .encode("utf-8").
# One em dash in a 200-character description otherwise passes this gate and is
# then truncated mid-sentence in the model-facing surface, which degrades skill
# selection with no other signal.
SKILL_DESCRIPTION_MAX = 200

# Mirrors triggerMaxLen and triggersJoinedMax in
# internal/skills/skill_markdown.go. loader.go applies both.
SKILL_TRIGGER_MAX = 64       # per trigger
SKILL_TRIGGERS_JOINED_MAX = 400  # joined block

# Mirrors knownSkillKeys in internal/skills/skill_markdown.go. Keep the two sets
# equal. Change them together in one commit. Both directions of drift are bugs:
# - a key here that the loader rejects passes `make verify` and then fails at
#   runtime;
# - a key the loader accepts but this set omits makes the gate stricter than the
#   contract, so a valid skill fails `make verify` for no real reason.
# This set is an explicit literal on purpose. Do not derive it from the Go source
# at runtime. A parse of the Go map must fail closed, and a simple regex parse
# fails open. scripts/test_verify_skill_tree.py proves the two sets are equal.
SKILL_KNOWN_KEYS = {
    "name", "description", "triggers", "user-invocable", "argument-hint",
    "short-description", "tools",
    # JSON-string schemas. The frontmatter subset parser holds them as
    # strings, not as nested maps.
    "input_schema", "output_schema",
}


def frontmatter_keys(body: str) -> list[str]:
    """Top-level keys in a SKILL.md frontmatter block.

    Mirrors the subset grammar in internal/skills/frontmatter.go: indented
    lines belong to a block sequence, comments and blanks are skipped.
    """
    lines = body.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if not lines or lines[0].strip() != "---":
        return []
    keys = []
    for line in lines[1:]:
        stripped = line.strip()
        if stripped == "---":
            break
        if not stripped or stripped.startswith("#") or line[:1] in (" ", "\t"):
            continue
        if ":" in stripped:
            keys.append(stripped.split(":", 1)[0].strip())
    return keys


def frontmatter_violations(body: str) -> list[str]:
    """Frontmatter shapes that internal/skills/frontmatter.go rejects at load.

    frontmatter_keys is deliberately lax: it collects key names and skips
    anything else. The Go parser is stricter, so a file can pass this gate and
    then fail the loader. parseFrontLines refuses three shapes this checks for:
    an indented line outside a block sequence (nested maps are not supported,
    frontmatter.go:168), a repeated key (frontmatter.go:198), and a
    non-indented line with no colon.
    """
    lines = body.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if not lines or lines[0].strip() != "---":
        return []
    problems: list[str] = []
    seen: set[str] = set()
    in_block = False
    for offset, line in enumerate(lines[1:]):
        line_num = offset + 2
        stripped = line.strip()
        if stripped == "---":
            break
        if not stripped or stripped.startswith("#"):
            continue
        if line[:1] in (" ", "\t"):
            if not in_block:
                problems.append(
                    f"line {line_num}: unexpected indented line "
                    f"(nested maps are not supported)"
                )
            continue
        if ":" not in stripped:
            problems.append(f"line {line_num}: expected key: value (no colon found)")
            in_block = False
            continue
        key, _, rest = stripped.partition(":")
        key = key.strip()
        if not key:
            problems.append(f"line {line_num}: empty key")
            continue
        if key in seen:
            problems.append(f"line {line_num}: duplicate frontmatter key {key!r}")
        seen.add(key)
        # A key with no inline value opens a block sequence.
        in_block = rest.strip() == ""
    return problems


def split_flow_items(inner: str) -> list[str]:
    """Split a flow sequence inner string with quote awareness.

    Matches the Go splitFlowSequence behaviour: commas inside single or
    double quotes are preserved.
    """
    inner = inner.strip()
    if not inner:
        return []
    items = []
    current: list[str] = []
    in_single = False
    in_double = False
    for ch in inner:
        if ch == '"' and not in_single:
            in_double = not in_double
            current.append(ch)
        elif ch == "'" and not in_double:
            in_single = not in_single
            current.append(ch)
        elif ch == "," and not in_single and not in_double:
            item = "".join(current).strip().strip("\"'")
            if item:
                items.append(item)
            current = []
        else:
            current.append(ch)
    item = "".join(current).strip().strip("\"'")
    if item or items:
        items.append(item)
    return items


def check_no_dead_skill_tree(root: Path) -> None:
    """Refuse a second workspace skill tree under .mivia/skills.

    .mivia/skills was a copy that nothing loaded. It drifted three skills
    behind before anyone noticed, because a gate that walks each tree on its
    own never compares them. Take root as an argument so a test can exercise
    this against a fixture.
    """
    if (root / ".mivia" / "skills").exists():
        fail(
            ".mivia/skills must not exist: workspace skills live only in "
            ".agents/skills, the path internal/workspace.SkillsDir reads. "
            "A second copy drifts because nothing loads it."
        )


# The alias body every .claude/skills/<name>/SKILL.md must carry after its
# frontmatter. Claude Code discovers skills only under .claude/skills, so it
# needs a file at this path. The file is a pointer, not a copy: a
# byte-identical duplicate drifts from the canonical skill and nothing sees
# it. A symlink is not an option
# either, because Git sets core.symlinks=false on Windows by default and the
# clone turns the link into a plain text file.
CLAUDE_ALIAS_BODY = (
    "\nThis skill is defined in `.agents/skills/{name}/SKILL.md`.\n\n"
    "Read that file now and follow it exactly. It is the only definition. This file\n"
    "is an alias so Claude Code can find the skill. Bundled resources live beside\n"
    "the canonical file.\n"
)


def frontmatter_block(body: str) -> str | None:
    """The `---` fenced block at the head of a SKILL.md, with its newline.

    Return None when the file has no closed frontmatter block.
    """
    lines = body.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if not lines or lines[0] != "---":
        return None
    for index in range(1, len(lines)):
        if lines[index] == "---":
            return "\n".join(lines[: index + 1]) + "\n"
    return None


def check_claude_skill_aliases(root: Path) -> None:
    """Hold .claude/skills to alias stubs of the canonical .agents/skills tree.

    Claude Code selects a skill by the `name` and `description` in the
    frontmatter, so the stub must repeat that block byte for byte. Everything
    after the block must be the alias text. Take root as an argument so a test
    can exercise this against a fixture.
    """
    canonical_dir = root / ".agents" / "skills"
    alias_dir = root / ".claude" / "skills"
    canonical = {
        path.parent.name: path
        for path in sorted(canonical_dir.glob("*/SKILL.md"))
    }
    if not alias_dir.is_dir():
        fail(
            ".claude/skills is missing: Claude Code discovers skills only "
            "under .claude/skills, so "
            "each skill needs an alias stub at .claude/skills/<name>/SKILL.md"
        )
    for name, source in canonical.items():
        stub_path = alias_dir / name / "SKILL.md"
        if not stub_path.is_file():
            fail(
                f".claude/skills/{name}/SKILL.md is missing; every skill in "
                f".agents/skills needs an alias stub, because Claude Code "
                f"discovers skills only under .claude/skills"
            )
        want_front = frontmatter_block(source.read_text(encoding="utf-8"))
        if want_front is None:
            fail(f"{rel_to_root(source)}: missing YAML frontmatter")
        stub = stub_path.read_text(encoding="utf-8")
        # frontmatter_block normalises line endings, so normalise here too.
        # A raw CRLF prefix would under-count and slice inside the frontmatter,
        # which reports a body error for what is really a line-ending problem.
        stub = stub.replace("\r\n", "\n").replace("\r", "\n")
        got_front = frontmatter_block(stub)
        if got_front is None:
            fail(f"{rel_to_root(stub_path)}: missing YAML frontmatter")
        if got_front != want_front:
            fail(
                f"{rel_to_root(stub_path)}: frontmatter is not identical to "
                f"{rel_to_root(source)}. Claude Code selects a skill by `name` "
                f"and `description`, so drift here changes which skill it picks."
            )
        want_body = CLAUDE_ALIAS_BODY.format(name=name)
        got_body = stub[len(got_front):]
        if got_body != want_body:
            fail(
                f"{rel_to_root(stub_path)}: body must be the alias text that "
                f"names .agents/skills/{name}/SKILL.md, and nothing else. The "
                f"stub points at the canonical skill; it never copies it."
            )
    for child in sorted(alias_dir.iterdir()):
        if not child.is_dir():
            fail(
                f".claude/skills/{child.name} is a file. This tree holds one "
                f"directory per skill and nothing else."
            )
        if child.name not in canonical:
            fail(
                f".claude/skills/{child.name} has no canonical skill at "
                f".agents/skills/{child.name}. Delete the orphan directory."
            )
        for entry in sorted(child.rglob("*")):
            if entry.is_dir() or entry.name == "SKILL.md":
                continue
            fail(
                f"{rel_to_root(entry)}: .claude/skills/<name> holds the alias "
                f"stub alone. Bundled resources live beside the canonical "
                f"skill in .agents/skills/{child.name}/."
            )


def check_skill_dir(skills_dir: Path) -> None:
    """Apply the skill frontmatter rules to every SKILL.md in one directory.

    This is a separate function so the tests can run it against a fixture
    directory. A test must never write a probe skill into .agents/skills.
    That tree is the one the mivia binary loads. `make verify` also runs
    verify-agent and agent-hook-test as separate targets, so a planted skill
    can fail a sibling target in the same run.
    """
    for skill_path in sorted(skills_dir.glob("*/SKILL.md")):
        body = skill_path.read_text(encoding="utf-8")
        name = skill_path.parent.name
        if not body.lstrip().startswith("---"):
            fail(f"{rel_to_root(skill_path)}: missing YAML frontmatter")
        if f"name: {name}" not in body and f'name: "{name}"' not in body:
            fail(f"{rel_to_root(skill_path)}: frontmatter name must be {name}")
        # Unknown keys are rejected at load by internal/skills/skill_markdown.go,
        # which checks the frontmatter against knownSkillKeys. Catch them here so
        # `make verify` fails before the loader does.
        for problem in frontmatter_violations(body):
            fail(
                f"{rel_to_root(skill_path)}: {problem} "
                f"(rejected by internal/skills/frontmatter.go)"
            )
        for key in frontmatter_keys(body):
            if key not in SKILL_KNOWN_KEYS:
                fail(
                    f"{rel_to_root(skill_path)}: unknown frontmatter key "
                    f"{key!r}; recognised: {sorted(SKILL_KNOWN_KEYS)} "
                    f"(rejected by internal/skills/skill_markdown.go)"
                )
        # Check description length.
        for line in body.splitlines():
            if line.startswith("description:"):
                # internal/skills/frontmatter.go unquote() strips a
                # balanced quote pair before the cap applies, so measure
                # the same text the loader measures.
                description = line.split(":", 1)[1].strip().strip("\"'")
                if len(description.encode("utf-8")) > SKILL_DESCRIPTION_MAX:
                    fail(
                        f"{rel_to_root(skill_path)}: description is "
                        f"{len(description.encode('utf-8'))} bytes, max "
                        f"{SKILL_DESCRIPTION_MAX} "
                        f"(silently truncated by internal/skills/loader.go)"
                    )
                break
        # Check trigger entries are non-empty and joined block within cap.
        in_triggers = False
        trigger_items = []
        for line in body.splitlines():
            stripped = line.strip()
            if stripped == "triggers:" or stripped.startswith("triggers: ["):
                if stripped == "triggers:":
                    in_triggers = True
                elif stripped.startswith("triggers: ["):
                    # Flow sequence: extract items. Handle trailing content after ].
                    inner = stripped[len("triggers: ["):]
                    # Find the closing bracket, handling trailing whitespace/comments.
                    bracket_idx = inner.find("]")
                    if bracket_idx >= 0:
                        inner = inner[:bracket_idx]
                    # Also strip any trailing comment before the bracket
                    # (already handled by find("]") above).
                    inner = inner.strip()
                    for part in split_flow_items(inner):
                        item = part.strip().strip("\"'")
                        if item:
                            trigger_items.append(item)
                continue
            if in_triggers:
                # Comments and blank lines are skipped in the Go parser
                # but stay in the block - handle them the same way.
                if stripped == "" or stripped.startswith("#"):
                    continue
                if stripped.startswith("- "):
                    item = stripped[2:].strip()
                    if item:
                        trigger_items.append(item)
                elif line.startswith("  ") or line.startswith("\t"):
                    # Still in block sequence (indented continuation).
                    continue
                else:
                    in_triggers = False
        if trigger_items:
            joined = "\n".join(trigger_items)
            if len(joined.encode("utf-8")) > SKILL_TRIGGERS_JOINED_MAX:
                fail(
                    f"{rel_to_root(skill_path)}: triggers joined block is "
                    f"{len(joined.encode('utf-8'))} bytes, max "
                    f"{SKILL_TRIGGERS_JOINED_MAX} "
                    f"(silently truncated by internal/skills/loader.go)"
                )
            for item in trigger_items:
                if len(item.encode("utf-8")) > SKILL_TRIGGER_MAX:
                    fail(
                        f"{rel_to_root(skill_path)}: trigger is "
                        f"{len(item.encode('utf-8'))} bytes, max {SKILL_TRIGGER_MAX} "
                        f"(silently truncated by internal/skills/loader.go)"
                    )
