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

import datetime
import functools
import re
from pathlib import Path

from verify_common import ROOT, rel_to_root
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="check_memories")


MEMORIES_DIR = ROOT / ".agents" / "memories"


# The six keys .agents/memories/README.md marks mandatory.
REQUIRED_KEYS = ("id", "title", "content", "importance", "tags", "updated")

IMPORTANCE_VALUES = ("high", "medium", "low")

SLUG = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")

# A flat flow sequence. A prefix test would accept "[[a, b]]", a one-element
# list holding a list, and "[unclosed". A blanket re-wrap once produced the
# first shape in 15 files while the gate reported ok, so check the shape.
# The elements are checked separately: this pattern alone still admits an
# empty element, a trailing comma and a nested map.
TAGS = re.compile(r"^\[\s*[^\[\]\s][^\[\]]*\]\s*(#.*)?$")

# The `updated` stamp: an ISO calendar date, the only format the README
# declares. The shape alone is not enough - "2026-02-30" matches - so the
# value is also parsed and refused when it is not a real calendar date.
# Recency is deliberately NOT checked here: "in the future" has no answer
# without a wall-clock read, and a clock read fails a valid store on any
# machine with a skewed clock. Judging recency - including a stamp ahead
# of the session date - is the housekeeping audit's job (Step 2), where
# the comparison has a trustworthy reference.
UPDATED = re.compile(r"^\d{4}-\d{2}-\d{2}$")

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
BLOCK_SCALAR = re.compile(r"^[|>](?:[+-][1-9]?|[1-9][+-]?)?$")


def has_unquoted_tab(value: str) -> bool:
    quote = ""
    escaped = False
    for char in value:
        if quote == '"' and escaped:
            escaped = False
            continue
        if quote == '"' and char == "\\":
            escaped = True
            continue
        if char in ('"', "'"):
            if not quote:
                quote = char
            elif quote == char:
                quote = ""
        elif char == "\t" and not quote:
            return True
    return False


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


# yaml.safe_load's SafeConstructor resolves these tags from a plain scalar
# with no format validation beyond "there is content" - str/int/float/bool
# accept any non-empty text (the actual int()/float()/bool() coercion is a
# construction-time concern this gate does not need to replicate). binary
# and timestamp both validate their content's FORMAT (base64, ISO 8601) and
# are deliberately excluded: "!!binary abc" opens a tag this list would
# accept but the base64 decoder still refuses. map/seq/set/omap/merge are
# collection tags, never valid on a single-line frontmatter scalar. int,
# float and bool are ALSO excluded, on purpose: their SafeConstructor still
# validates the scalar text ("!!int abc" raises the same as "!!binary abc"
# does), which this gate does not replicate. str is the only tag whose
# constructor accepts arbitrary text.
YAML_SAFE_SCALAR_TAGS = ("str",)

# The only tag: URIs a real yaml.safe_load can resolve. A bare "tag:" prefix
# check (an earlier draft of this function) waved through any scheme-shaped
# text, including "tag:example.com,2000:foo", which yaml.safe_load has no
# constructor for and still refuses.
YAML_SAFE_VERBATIM_TAGS = ("tag:yaml.org,2002:str",)


def check_tag_scalar(name: str, key: str, value: str) -> None:
    """A value opening with "!" must be a tag yaml.safe_load can construct.

    Conservative on purpose: only forms yaml.safe_load is KNOWN to accept for
    arbitrary text pass. "! foo" (bare non-specific tag), "!!str foo"/"!!str"
    (the only built-in whose constructor imposes no format), and the one
    verbatim spelling of it are legal. A custom single-bang tag ("!foo"), an
    unrecognized or format-validating "!!name" tag ("!!python/object:x",
    "!!map", "!!int abc"), a non-str verbatim tag, a malformed one ("!<>",
    "!<a:b>"), or a second tag indicator right after a bare bang
    ("! !nested") all raise under yaml.safe_load and must fail here too.
    """
    if value.startswith("! "):
        if value[2:3] == "!":
            fail(f"{name}: {key} chains a second tag indicator after a bare "
                 f"bang, which YAML cannot parse: {value!r}.")
        return  # bare non-specific tag: legal
    if value == "!":
        return
    if value.startswith("!!"):
        rest = value[2:]
        tag_name, _, _ = rest.partition(" ")
        # "!!str" and "!!null" both resolve with no trailing content: str's
        # constructor accepts the empty string, and null's resolver treats
        # an empty tail (no separator, or a separator with nothing after) as
        # the null value either way.
        if tag_name in ("str", "null"):
            return
        fail(f"{name}: {key} opens tag {value!r}, which yaml.safe_load either "
             f"has no constructor for, or validates in a way this gate does "
             f"not replicate (only str and null are unconditionally safe "
             f"here).")
    if value.startswith("!<"):
        if ">" in value:
            inner, _, tail = value[2:].partition(">")
            if inner in YAML_SAFE_VERBATIM_TAGS and tail[:1] in (" ", ""):
                return
        fail(f"{name}: {key} opens a malformed or unresolvable verbatim tag "
             f"{value!r}.")
    fail(f"{name}: {key} opens a custom tag {value!r}. yaml.safe_load has "
         f"no constructor for it and refuses to load this frontmatter.")


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
            if has_unquoted_tab(raw_value):
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
            if value.startswith("!"):
                check_tag_scalar(name, key.strip(), value)
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
        # `title` accepts a quoted scalar because some titles need quoting,
        # and the same tolerance applies here: a quoted ISO date carries the
        # same value as the plain spelling. Strip one outer MATCHED quote
        # pair before validating; anything else (an unclosed quote) fails
        # the shape check below with the raw value in the message.
        stamp = fields["updated"]
        if (
            len(stamp) >= 2
            and stamp[0] == stamp[-1]
            and stamp[0] in ('"', "'")
        ):
            stamp = stamp[1:-1]
        if not UPDATED.match(stamp):
            fail(
                f"{name}: updated must be an ISO date of the shape YYYY-MM-DD, "
                f"got {stamp!r}."
            )
        try:
            datetime.date.fromisoformat(stamp)
        except ValueError:
            fail(
                f"{name}: updated is {stamp!r}, which is not a real calendar "
                f"date."
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
