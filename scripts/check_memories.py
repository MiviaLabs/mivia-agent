#!/usr/bin/env python3
"""Hold .agents/memories/*.md to the schema its README declares.

AGENTS.md tells every agent to read this directory at the start of a task and
to treat each memory as an active constraint. No compiled code reads it, so
this gate is the only control over its shape.

Two skills act on that shape. `capture` writes a new memory and validates it.
`housekeeping` classifies the store and proposes a `fix-schema` change. Both
drive off the README, so a rule the store cannot satisfy turns a routine audit
into a mass rewrite.

That happened. The README asked for an id equal to the filename. The store used
a snake_case id against a kebab-case filename. All 20 memories therefore read as
schema failures.
"""

from __future__ import annotations

import functools
import re
from pathlib import Path

from verify_common import ROOT, rel_to_root
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="check_memories")


MEMORIES_DIR = ROOT / ".agents" / "memories"


# The five keys .agents/memories/README.md marks mandatory.
REQUIRED_KEYS = ("id", "title", "content", "importance", "tags")

IMPORTANCE_VALUES = ("high", "medium", "low")

SLUG = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")

# A flat, non-empty flow sequence. A prefix test would accept "[[a, b]]",
# which is a one-element list holding a list, and "[unclosed". A blanket
# re-wrap once produced exactly the first shape in 15 files while the gate
# reported ok, so the shape is checked, not the first character.
TAGS = re.compile(r"^\[\s*[^\[\]\s][^\[\]]*\]$")


def expected_id(stem: str) -> str:
    """The id the README derives from a filename: hyphens become underscores."""
    return stem.replace("-", "_")


def frontmatter(text: str) -> str | None:
    """The body of the leading `---` block, or None when there is no closed one."""
    lines = text.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if not lines or lines[0] != "---":
        return None
    for index in range(1, len(lines)):
        if lines[index] == "---":
            return "\n".join(lines[1:index])
    return None


def check_memories(directory: Path) -> None:
    """Check every memory file in one directory. Takes the directory so a test
    can exercise this against a fixture."""
    if not directory.is_dir():
        return
    for path in sorted(directory.glob("*.md")):
        if path.name == "README.md":
            continue
        name = rel_to_root(path)
        if not SLUG.match(path.stem):
            fail(
                f"{name}: filename must be a kebab-case slug of [a-z0-9-], "
                f"with no leading, trailing or doubled hyphen."
            )
        front = frontmatter(path.read_text(encoding="utf-8"))
        if front is None:
            fail(
                f"{name}: missing a closed YAML frontmatter block. Every memory "
                f"carries {', '.join(REQUIRED_KEYS)}."
            )
        fields = {}
        for line in front.split("\n"):
            if line[:1] in (" ", "\t") or ":" not in line:
                continue
            key, _, value = line.partition(":")
            fields[key.strip()] = value.strip()
        for key in REQUIRED_KEYS:
            if not fields.get(key):
                fail(f"{name}: frontmatter is missing a non-empty {key!r}.")
        want = expected_id(path.stem)
        if fields["id"] != want:
            fail(
                f"{name}: id is {fields['id']!r} but the filename derives "
                f"{want!r}. The README derives the id from the filename: drop "
                f"the .md, then replace every hyphen with an underscore."
            )
        # No duplicate-id check: the id is derived from the filename and a
        # directory cannot hold two files with one name, so a duplicate id
        # always fails the derivation check above first. A check that cannot
        # fire reads as coverage and provides none.
        if fields["importance"] not in IMPORTANCE_VALUES:
            fail(
                f"{name}: importance is {fields['importance']!r}, not one of "
                f"{list(IMPORTANCE_VALUES)}."
            )
        if not TAGS.match(fields["tags"]):
            fail(
                f"{name}: tags must be a flat non-empty list, for example "
                f"[a, b]; got {fields['tags']!r}."
            )


def main() -> None:
    check_memories(MEMORIES_DIR)
    print("check_memories: ok")


if __name__ == "__main__":
    main()
