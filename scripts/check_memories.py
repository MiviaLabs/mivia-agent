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

# A flat flow sequence. A prefix test would accept "[[a, b]]", a one-element
# list holding a list, and "[unclosed". A blanket re-wrap once produced the
# first shape in 15 files while the gate reported ok, so check the shape.
# The elements are checked separately: this pattern alone still admits an
# empty element, a trailing comma and a nested map.
TAGS = re.compile(r"^\[\s*[^\[\]\s][^\[\]]*\]\s*(#.*)?$")

# A scalar that opens with a quote must close with the same quote and nothing
# may follow it. PyYAML is not a dependency of this repository, so the gate
# cannot parse the block; this one rule catches the shape a real parser
# rejects. A title of `"status"/"stats" ...` reads as the scalar "status"
# followed by junk, and no YAML parser can load the file.
# Both YAML escapes are legal inside the scalar and must pass: a doubled ''
# inside single quotes, and a backslash escape inside double quotes. The
# remediation text below tells an author to single-quote the value, so a
# rule that refused '' would reject the fix it asks for.
QUOTED = re.compile(r"^(\"([^\"\\]|\\.)*\"|'([^']|'')*')$")


# Characters YAML refuses at the head of a plain scalar, verified against
# yaml.safe_load in both the `Xfoo` and `X foo` forms. Deliberately NOT here:
# `&` (anchor) and `?`/`-`/`:` (legal in the plain-scalar position), `!` (tag),
# and `|`/`>`, which open a block scalar - the first draft rejected
# `content: >-`, which every parser accepts.
YAML_INDICATORS = ("@", "`", "*", "%", ",", "]", "}")

# A well-formed block scalar header: | or > with optional chomping and indent
# indicators. The value itself follows on indented lines.
BLOCK_SCALAR = re.compile(r"^[|>][+-]?[0-9]*$")


def strip_trailing_comment(value: str) -> str:
    """Drop an unquoted trailing `# ...` comment, which YAML ignores.

    Only a `#` that opens a token is a comment, and only outside quotes: a
    fragment like `a#b` is part of the scalar.
    """
    out, quote = [], ""
    for index, char in enumerate(value):
        if quote:
            out.append(char)
            if char == quote:
                quote = ""
            continue
        if char in ('"', "'"):
            quote = char
            out.append(char)
            continue
        if char == "#" and (index == 0 or value[index - 1] in " \t"):
            break
        out.append(char)
    return "".join(out)


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
        # Validate the block, then read it. The old filter skipped any line
        # without a colon and any indented line, so a stray line of prose and
        # an unquoted `title: status: stats` both passed while PyYAML raised
        # a ScannerError on the same block. The gate's own docstring says its
        # motivating defect was frontmatter no parser can load.
        fields = {}
        for line in front.split("\n"):
            if not line.strip():
                continue
            if line.lstrip().startswith("#"):
                continue  # a comment line is legal YAML
            if line[:1] in (" ", "\t"):
                if not fields:
                    fail(
                        f"{name}: frontmatter opens with an indented line, "
                        f"which no YAML parser can read: {line!r}."
                    )
                continue  # a continuation of the previous key
            if ":" not in line:
                fail(
                    f"{name}: frontmatter line {line!r} has no colon, so it is "
                    f"neither a key nor a continuation. No YAML parser can "
                    f"read this block."
                )
            key, _, value = line.partition(":")
            raw_value = value
            value = strip_trailing_comment(value).strip()
            # Test the RAW value: .strip() removes the leading and trailing
            # tabs that are exactly the positions a parser refuses, so testing
            # the stripped value made this rule dead.
            if "\t" in raw_value:
                fail(
                    f"{name}: {key.strip()} holds a raw tab, which YAML does "
                    f"not accept as whitespace: {value!r}."
                )
            if BLOCK_SCALAR.match(value):
                # A block scalar; its text is on the indented lines that
                # follow. Record the key as present so the required-key check
                # does not read it as missing.
                fields[key.strip()] = value
                continue
            if value[:1] in ("|", ">"):
                fail(
                    f"{name}: {key.strip()} opens a block scalar with a "
                    f"malformed header: {value!r}."
                )
            if value[:2] in ("- ", "? ", ": ", "& ") or value in ("-", "?", "&"):
                fail(
                    f"{name}: {key.strip()} opens with {value[:1]!r} followed "
                    f"by a space, which YAML reads as a block indicator or an "
                    f"empty anchor and then refuses: {value!r}."
                )
            # A bang opens a tag, not the indicator list above: "! foo" (a
            # bare non-specific tag) and "!!str foo" / "!<uri> foo" (built-in
            # or verbatim tags) are all legal under yaml.safe_load. A custom
            # single-bang tag like "!foo" or "!foo bar" is not - safe_load has
            # no constructor for it and raises. Removing "! " from the block-
            # indicator list above without adding this check would silently
            # readmit "! foo" as a rejection AND leave "!foo" unrejected.
            if value.startswith("!") and not (
                (value.startswith("!!") and len(value) > 2)
                or (value.startswith("!<") and ">" in value)
                or value in ("!", "! ")
                or value.startswith("! ")
            ):
                fail(
                    f"{name}: {key.strip()} opens a custom tag {value!r}. "
                    f"yaml.safe_load has no constructor for it and refuses "
                    f"to load this frontmatter."
                )
            if value[:1] in YAML_INDICATORS:
                fail(
                    f"{name}: {key.strip()} opens with {value[:1]!r}, which "
                    f"YAML reserves as an indicator. No parser can read this "
                    f"frontmatter. Wrap the whole value in single quotes: "
                    f"{value!r}."
                )
            if value[:1] in ("[", "{"):
                closer = "]" if value[0] == "[" else "}"
                if not value.endswith(closer):
                    fail(
                        f"{name}: {key.strip()} opens a flow collection that "
                        f"never closes: {value!r}."
                    )
            if value[:1] not in ('"', "'", "[", "{", "") and (
                ": " in value or value.endswith(":")
            ):
                fail(
                    f"{name}: {key.strip()} is an unquoted scalar holding a "
                    f"colon, which YAML reads as a nested mapping and then "
                    f"refuses: {value!r}. Wrap the whole value in single "
                    f"quotes."
                )
            fields[key.strip()] = value
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
        # fields["tags"] is already comment-stripped: the field split above
        # stores strip_trailing_comment(value), so a second strip here was
        # dead code (confirmed by neutering it - no test moved).
        inner = fields["tags"]
        inner = inner[inner.index("[") + 1 : inner.rindex("]")]
        for tag in inner.split(","):
            tag = tag.strip()
            if not tag:
                fail(
                    f"{name}: tags holds an empty element. A stray or trailing "
                    f"comma is a parse error in YAML: {fields['tags']!r}."
                )
            if ":" in tag or "{" in tag or "}" in tag:
                fail(
                    f"{name}: tag {tag!r} is not a plain keyword. A list of "
                    f"maps defeats the tag-set comparison housekeeping uses "
                    f"to find near-duplicates."
                )
        for key, value in fields.items():
            if value[:1] in ('"', "'") and not QUOTED.match(value):
                fail(
                    f"{name}: {key} opens with a quote but is not one quoted "
                    f"scalar, so no YAML parser can read this frontmatter. "
                    f"Wrap the whole value in single quotes: {value!r}."
                )


def main() -> None:
    check_memories(MEMORIES_DIR)
    print("check_memories: ok")


if __name__ == "__main__":
    main()
