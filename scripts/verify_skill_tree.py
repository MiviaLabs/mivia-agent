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

import sys
from pathlib import Path

from verify_common import ROOT, fail, rel_to_root


# Mirrors descriptionMaxLen in internal/skills/skill_markdown.go. loader.go
# applies it through SanitizeModelFacingText. A longer description is truncated
# mid-sentence in the model-facing skill surface, which degrades skill selection
# with no other signal that it happened.
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
# fails open. scripts/test_verify_agent_config.py proves the two sets are equal.
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
# frontmatter. Claude Code discovers skills only under .claude/skills, so it needs a file at this
# path. The file is a pointer, not a copy: a byte-identical duplicate drifts
# from the canonical skill and nothing sees it. A symlink is not an option
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
                description = line.split(":", 1)[1].strip()
                if len(description) > SKILL_DESCRIPTION_MAX:
                    fail(
                        f"{rel_to_root(skill_path)}: description is "
                        f"{len(description)} chars, max {SKILL_DESCRIPTION_MAX} "
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
            if len(joined) > SKILL_TRIGGERS_JOINED_MAX:
                fail(
                    f"{rel_to_root(skill_path)}: triggers joined block is "
                    f"{len(joined)} chars, max {SKILL_TRIGGERS_JOINED_MAX} "
                    f"(silently truncated by internal/skills/loader.go)"
                )
            for item in trigger_items:
                if len(item) > SKILL_TRIGGER_MAX:
                    fail(
                        f"{rel_to_root(skill_path)}: trigger is {len(item)} "
                        f"chars, max {SKILL_TRIGGER_MAX} "
                        f"(silently truncated by internal/skills/loader.go)"
                    )


def frontmatter_lists(body: str) -> dict[str, list[str]]:
    """Map every frontmatter key to its list value, or to an empty list.

    The tree holds three list shapes. A skill writes an indented block
    sequence. A role writes a block sequence at column zero. A role may also
    write an empty flow sequence (`tools: []`). Read all three. A key with a
    scalar value maps to an empty list, which is correct here: no scalar key
    carries a tool or a skill name.
    """
    lines = body.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if not lines or lines[0].strip() != "---":
        return {}
    out: dict[str, list[str]] = {}
    current: str | None = None
    for line in lines[1:]:
        stripped = line.strip()
        if stripped == "---":
            break
        if not stripped or stripped.startswith("#"):
            continue
        if stripped.startswith("- "):
            # A sequence item, indented or not. An indented line that is not
            # an item is a folded scalar continuation, so skip it.
            if current is not None:
                item = stripped[2:].strip().strip("\"'")
                if item:
                    out[current].append(item)
            continue
        if line[:1] in (" ", "\t"):
            continue
        if ":" not in stripped:
            current = None
            continue
        key, _, rest = stripped.partition(":")
        key = key.strip()
        rest = rest.strip()
        if not rest:
            current = key
            out.setdefault(key, [])
            continue
        current = None
        if rest.startswith("[") and rest.endswith("]"):
            out[key] = [item for item in split_flow_items(rest[1:-1]) if item]
        else:
            out.setdefault(key, [])
    return out


def check_agent_skill_tools(root: Path) -> None:
    """Hold each role's declared tools to a superset of every skill it lists.

    CheckSkillInvocation in internal/agents/skill_policy.go refuses a skill
    whose `tools:` list the role's effective tools do not cover. The two
    declarations therefore form one contract, and both sides fail silently
    until run time:

    - A skill that under-declares admits a role that cannot then run the
      skill's own procedure. The role gets the skill and no write tool.
    - A role that under-declares is refused at the first call.

    The same walk resolves every name in a role's `skills:` list to a real
    .agents/skills/<name>/SKILL.md. `.mivia/invariants.md` INV-AG-30 rejects an
    unknown skill at load, so a typo or a renamed skill disarms the role with
    no other signal.

    Take root as an argument so a test can drive this against a fixture.

    Do NOT extend this check to grep a skill BODY for tool names and demand
    that each one is declared. A body names a tool for two different reasons:
    it uses the tool, or it discusses the tool. `review` lists `write_file`,
    `search_replace`, and `run_command` under "Disallowed operations".
    `workflow-runs-analysis` names `run_command` only to forbid it.
    `secure-change` names `run_command` as the subject of a security review. A
    grep cannot tell use from discussion, so it reports those three as
    violations, and the next author weakens or deletes the gate. Compare one
    declaration against the other declaration, and nothing else.
    """
    skills_dir = root / ".agents" / "skills"
    agents_dir = root / ".agents" / "agents"
    if not agents_dir.is_dir():
        return
    skill_tools: dict[str, list[str]] = {}
    for skill_path in sorted(skills_dir.glob("*/SKILL.md")):
        front = frontmatter_lists(skill_path.read_text(encoding="utf-8"))
        skill_tools[skill_path.parent.name] = front.get("tools", [])
    for agent_path in sorted(agents_dir.glob("*.md")):
        if agent_path.name == "README.md":
            continue
        front = frontmatter_lists(agent_path.read_text(encoding="utf-8"))
        if "skills" not in front:
            continue  # no allowlist: the role restricts nothing
        # This gate compares the declared `tools:` list. It does not resolve
        # inheritance. A role that composes its tools through `inherits`,
        # `tools_add`, `tools_remove` or `tools_core` has effective tools this
        # parser cannot compute, so comparing the raw list would report a
        # violation that does not exist. Skip such a role and say so. The Go
        # test TestCommittedRosterSkillCompatibilityMatrix resolves the real
        # EffectiveTools through the loader and covers these roles.
        composed = [
            key
            for key in ("inherits", "tools_add", "tools_remove", "tools_core")
            if key in front
        ]
        if composed:
            print(
                f"verify_agent_config: {rel_to_root(agent_path)}: skipping the "
                f"skill-tool check because the role composes tools through "
                f"{composed}. TestCommittedRosterSkillCompatibilityMatrix "
                f"covers it.",
                file=sys.stderr,
            )
            continue
        have = set(front.get("tools", []))
        for skill_name in front["skills"]:
            if skill_name not in skill_tools:
                fail(
                    f"{rel_to_root(agent_path)}: skills lists {skill_name!r}, "
                    f"which has no .agents/skills/{skill_name}/SKILL.md. "
                    f"An unknown skill is rejected at load (INV-AG-30), so the "
                    f"role loses the skill with no other signal."
                )
            missing = [tool for tool in skill_tools[skill_name] if tool not in have]
            if missing:
                fail(
                    f"{rel_to_root(agent_path)}: skill {skill_name!r} declares "
                    f"tool(s) {missing} that the role does not. "
                    f"internal/agents/skill_policy.go requires the role's tools "
                    f"to cover the skill's tools, so this call fails at run "
                    f"time. Add the tool(s) to the role, or stop listing the "
                    f"skill."
                )
