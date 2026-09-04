#!/usr/bin/env python3
"""Contract tests for scripts/check_memories.py.

Every check runs against a fixture directory. A test must never write a probe
memory into .agents/memories: AGENTS.md tells every agent to read that tree at
the start of a task, so a stray probe becomes an instruction.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "check_memories.py"

GOOD = """\
---
id: probe_memory
title: Probe memory
content: One sentence of fact.
importance: high
tags: [probe]
---

Body.
"""


def load_gate():
    if str(GATE.parent) not in sys.path:
        sys.path.insert(0, str(GATE.parent))
    spec = importlib.util.spec_from_file_location("check_memories", GATE)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_memories.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def run_on(files: dict[str, str]) -> str | None:
    """Run the gate over a fixture. Return the failure text, or None."""
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        directory = Path(tmp) / "memories"
        directory.mkdir()
        for name, body in files.items():
            (directory / name).write_text(body, encoding="utf-8")
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_memories(directory)
        except SystemExit:
            return captured.getvalue().strip()
    return None


def expect_rejection(files: dict[str, str], expected: str) -> None:
    rejection = run_on(files)
    if rejection is None:
        raise AssertionError(f"gate accepted a store that should fail: {expected}")
    if expected not in rejection:
        raise AssertionError(f"expected {expected!r}, got:\n{rejection}")


def test_accepts_a_valid_memory() -> None:
    if (rejection := run_on({"probe-memory.md": GOOD})) is not None:
        raise AssertionError(rejection)


def test_id_must_derive_from_the_filename() -> None:
    """The rule the README used to state was unsatisfiable.

    It asked for an id equal to the filename, while every memory in the store
    used a snake_case id against a kebab-case filename. A housekeeping run
    would have rewritten all 20 ids and broken every supersedes reference.
    """
    expect_rejection(
        {"probe-memory.md": GOOD.replace("id: probe_memory", "id: probe-memory")},
        "the filename derives",
    )


def test_rejects_a_memory_with_no_frontmatter() -> None:
    expect_rejection(
        {"probe-memory.md": "# Just a heading\n\nBody.\n"},
        "missing a closed YAML frontmatter block",
    )


def test_rejects_a_missing_required_key() -> None:
    expect_rejection(
        {"probe-memory.md": GOOD.replace("importance: high\n", "")},
        "missing a non-empty 'importance'",
    )


def test_rejects_an_unknown_importance() -> None:
    expect_rejection(
        {"probe-memory.md": GOOD.replace("importance: high", "importance: urgent")},
        "not one of",
    )


def test_rejects_a_non_slug_filename() -> None:
    expect_rejection({"Probe_Memory.md": GOOD}, "kebab-case slug")


def test_rejects_bare_csv_tags() -> None:
    expect_rejection(
        {"probe-memory.md": GOOD.replace("tags: [probe]", "tags: a, b")},
        "flat non-empty list",
    )


def test_rejects_a_nested_tags_list() -> None:
    """[[a, b]] is one element holding a list, not a list of keywords.

    A blanket re-wrap produced exactly this in 15 memories while the gate,
    which then tested only the first character, reported ok.
    """
    expect_rejection(
        {"probe-memory.md": GOOD.replace("tags: [probe]", "tags: [[a, b]]")},
        "flat non-empty list",
    )


def test_rejects_empty_tags() -> None:
    expect_rejection(
        {"probe-memory.md": GOOD.replace("tags: [probe]", "tags: []")},
        "flat non-empty list",
    )


def test_rejects_an_unclosed_tags_list() -> None:
    """The flow-collection rule now names the cause more precisely.

    It fires before the shape rule, so assert on the message that is actually
    produced; asserting the older text would pass only by accident.
    """
    expect_rejection(
        {"probe-memory.md": GOOD.replace("tags: [probe]", "tags: [unclosed")},
        "never closes",
    )


def test_rejects_an_empty_tag_element() -> None:
    """A stray or trailing comma is a parse error in real YAML."""
    for shape in ("[a, , b]", "[a,]", "[,]"):
        expect_rejection(
            {"probe-memory.md": GOOD.replace("tags: [probe]", f"tags: {shape}")},
            "empty element",
        )


def test_rejects_a_tag_that_is_not_a_plain_keyword() -> None:
    """A list of maps defeats the tag-set comparison housekeeping uses."""
    expect_rejection(
        {"probe-memory.md": GOOD.replace("tags: [probe]", "tags: [{a: b}]")},
        "not a plain keyword",
    )


def test_rejects_a_scalar_that_no_yaml_parser_can_read() -> None:
    """A value opening with a quote must be one complete quoted scalar.

    A live memory carried `title: "status"/"stats" request format ...`, which
    reads as the scalar "status" followed by junk. It sat green through every
    run because the gate never checked that the block parses.
    """
    bad = 'title: "status"/"stats" request format'
    expect_rejection(
        {"probe-memory.md": GOOD.replace("title: Probe memory", bad)},
        "not one quoted scalar",
    )


def test_accepts_a_properly_quoted_scalar() -> None:
    good = "title: '\"status\"/\"stats\" request format'"
    if (r := run_on({"probe-memory.md": GOOD.replace("title: Probe memory", good)})) is not None:
        raise AssertionError(r)


def test_accepts_the_yaml_escapes_inside_a_quoted_scalar() -> None:
    """A doubled '' and a backslash escape are legal YAML and must pass.

    The gate's own remediation text tells the author to single-quote the
    value. An author whose title holds an apostrophe writes the doubled '',
    which every parser reads, so refusing it would reject the prescribed fix.
    """
    for good in ("title: 'It''s fine.'", 'title: "say \\"hi\\" now"'):
        body = GOOD.replace("title: Probe memory", good)
        if (r := run_on({"probe-memory.md": body})) is not None:
            raise AssertionError(f"{good!r} rejected: {r}")


def test_rejects_frontmatter_no_parser_can_read() -> None:
    """Two shapes PyYAML refuses that the lenient field split skipped.

    The old split dropped any line without a colon and any indented line, so
    an unquoted `title: status: stats` (a nested mapping YAML then refuses)
    and a stray line of prose both reported ok. Verified against yaml.safe_load
    for every shape here.
    """
    expect_rejection(
        {"probe-memory.md": GOOD.replace("title: Probe memory",
                                         "title: status: stats disagree")},
        "unquoted scalar holding a",
    )
    expect_rejection(
        {"probe-memory.md": GOOD.replace("title: Probe memory", "title: broken:")},
        "unquoted scalar holding a",
    )
    expect_rejection(
        {"probe-memory.md": GOOD.replace("importance: high\n",
                                         "importance: high\nthis line has no colon\n")},
        "has no colon",
    )


def test_accepts_shapes_a_real_parser_accepts() -> None:
    """The gate must not reject legal YAML: a trailing comment on the tags
    list, a folded continuation line, and a quoted scalar holding a colon."""
    for label, body in (
        ("tags comment", GOOD.replace("tags: [probe]", "tags: [probe, memory]  # in sync")),
        ("folded value", GOOD.replace("content: One sentence of fact.",
                                      "content: >-\n  folded text here")),
        ("quoted colon", GOOD.replace("title: Probe memory", "title: 'status: stats'")),
    ):
        if (r := run_on({"probe-memory.md": body})) is not None:
            raise AssertionError(f"{label} rejected: {r}")


def test_rejects_frontmatter_opening_with_an_indented_line() -> None:
    """An indented first key is a parse error and had no test."""
    expect_rejection(
        {"probe-memory.md": GOOD.replace("id: probe_memory", "  id: probe_memory")},
        "opens with an indented line",
    )


def test_rejects_a_custom_tag_scalar() -> None:
    """A single-bang custom tag has no constructor under yaml.safe_load.

    "! " (bare bang) and "!!str"/"!<uri>" (built-in and verbatim tags) are
    legal and must not be confused with this.
    """
    expect_rejection(
        {"probe-memory.md": GOOD.replace("title: Probe memory", "title: !foo bar")},
        "opens a custom tag",
    )
    expect_rejection(
        {"probe-memory.md": GOOD.replace("title: Probe memory", "title: !foo")},
        "opens a custom tag",
    )


def test_accepts_the_legal_tag_forms() -> None:
    for label, value in (
        ("bare bang", "! foo"),
        ("built-in tag", "!!str foo"),
        ("verbatim tag", "!<tag:yaml.org,2002:str> foo"),
    ):
        body = GOOD.replace("title: Probe memory", f"title: {value}")
        if (r := run_on({"probe-memory.md": body})) is not None:
            raise AssertionError(f"{label} ({value!r}) rejected: {r}")


def test_rejects_tag_shapes_the_first_tag_check_missed() -> None:
    """Six shapes the first differential sweep did not cover, all refused by
    yaml.safe_load: an unrecognized double-bang construct, a malformed
    verbatim tag with no scheme, an empty verbatim tag, a bare double-bang
    collection tag, and a chained tag indicator."""
    for value in ("!!python/object:foo", "!<a:b> x", "!<>", "!!map",
                  "! !nested", "!!binary abc"):
        body = GOOD.replace("title: Probe memory", f"title: {value}")
        if (r := run_on({"probe-memory.md": body})) is None:
            raise AssertionError(f"{value!r} was accepted; yaml.safe_load refuses it")


def test_accepts_more_legal_tag_forms() -> None:
    for label, value in (
        ("null tag alone", "!!null"),
        ("int tag", "!!int 5"),
        ("standard verbatim tag", "!<tag:yaml.org,2002:str> foo"),
    ):
        body = GOOD.replace("title: Probe memory", f"title: {value}")
        if (r := run_on({"probe-memory.md": body})) is not None:
            raise AssertionError(f"{label} ({value!r}) rejected: {r}")


def main() -> None:
    test_accepts_a_valid_memory()
    test_id_must_derive_from_the_filename()
    test_rejects_a_memory_with_no_frontmatter()
    test_rejects_a_missing_required_key()
    test_rejects_an_unknown_importance()
    test_rejects_a_non_slug_filename()
    test_rejects_bare_csv_tags()
    test_rejects_a_nested_tags_list()
    test_rejects_empty_tags()
    test_rejects_an_unclosed_tags_list()
    test_rejects_an_empty_tag_element()
    test_rejects_a_tag_that_is_not_a_plain_keyword()
    test_rejects_a_scalar_that_no_yaml_parser_can_read()
    test_accepts_a_properly_quoted_scalar()
    test_accepts_the_yaml_escapes_inside_a_quoted_scalar()
    test_rejects_frontmatter_no_parser_can_read()
    test_accepts_shapes_a_real_parser_accepts()
    test_rejects_frontmatter_opening_with_an_indented_line()
    test_rejects_a_custom_tag_scalar()
    test_accepts_the_legal_tag_forms()
    test_rejects_tag_shapes_the_first_tag_check_missed()
    test_accepts_more_legal_tag_forms()
    print("test_check_memories: ok")


if __name__ == "__main__":
    main()
