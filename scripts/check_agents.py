#!/usr/bin/env python3
"""Validate the subagent role files under .agents/agents/.

Each role file (Markdown with YAML frontmatter) must:
  - have `name`, `description`, and `tools` frontmatter keys
  - have `name` equal to the filename without `.md`
  - declare at least one tool
  - include a "Disallowed operations" section in the body

Exits non-zero with a list of failures if any rule is broken. Used by
`make agents-check`; run before committing any new or edited role file.

This script is stdlib-only and mirrors the shape of the other `check_*`
gate scripts in scripts/. It does not parse the frontmatter with PyYAML
to keep the dependency surface minimal; the frontmatter is small enough
that a focused parser is clearer than a generic one.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

REQUIRED_KEYS = ("name", "description", "tools")
ALLOWED_ROLES = frozenset({"planner", "plan-reviewer", "builder", "reviewer"})


def _parse_frontmatter(text: str) -> tuple[dict[str, str], str]:
    """Return (frontmatter_keys, body) for a Markdown file.

    Frontmatter is the first `---\\n...\\n---\\n` block. Values for `tools`
    are accepted as a YAML-style list; other values are taken verbatim.
    """
    if not text.startswith("---\n"):
        return {}, text
    end = text.find("\n---\n", 4)
    if end == -1:
        return {}, text
    block = text[4:end]
    body = text[end + len("\n---\n"):]
    keys: dict[str, str] = {}
    current_key: str | None = None
    current_lines: list[str] = []
    for raw in block.split("\n"):
        if raw.startswith("  - ") and current_key is not None:
            current_lines.append(raw.strip()[2:])
            continue
        if current_key is not None:
            keys[current_key] = "\n".join(current_lines).strip()
            current_key = None
            current_lines = []
        if ":" in raw and not raw.startswith(" "):
            key, _, value = raw.partition(":")
            key = key.strip()
            value = value.strip()
            if value == "":
                current_key = key
                current_lines = []
            else:
                keys[key] = value.strip()
    if current_key is not None:
        keys[current_key] = "\n".join(current_lines).strip()
    return keys, body


def _tools_count(value: str) -> int:
    """Count the number of tools declared under `tools:`.

    Accepts either an inline list `[a, b]` or a block list (`- a\\n- b`).
    Returns 0 if the value is empty.
    """
    value = value.strip()
    if not value:
        return 0
    if value.startswith("[") and value.endswith("]"):
        inner = value[1:-1].strip()
        if not inner:
            return 0
        return len([item for item in inner.split(",") if item.strip()])
    # Block list lives one per line; the parser above joins them with \n.
    return len([line for line in value.split("\n") if line.strip()])


def _check_role(path: Path) -> list[str]:
    failures: list[str] = []
    role_name = path.stem
    if role_name not in ALLOWED_ROLES:
        failures.append(f"{path}: role {role_name!r} is not in the standard set {sorted(ALLOWED_ROLES)}")
    text = path.read_text(encoding="utf-8")
    keys, body = _parse_frontmatter(text)
    for key in REQUIRED_KEYS:
        if key not in keys:
            failures.append(f"{path}: missing required frontmatter key {key!r}")
    name_value = keys.get("name", "").strip()
    if name_value and name_value != role_name:
        failures.append(f"{path}: frontmatter name {name_value!r} does not match filename {role_name!r}")
    tools_value = keys.get("tools", "")
    if "tools" in keys and _tools_count(tools_value) == 0:
        failures.append(f"{path}: tools list is empty")
    if "Disallowed operations" not in body:
        failures.append(f"{path}: body must include a 'Disallowed operations' section")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--agents-dir",
        type=Path,
        default=Path(__file__).resolve().parent.parent / ".agents" / "agents",
        help="Path to the agents directory (default: <repo>/.agents/agents).",
    )
    args = parser.parse_args()
    if not args.agents_dir.is_dir():
        print(f"agents dir not found: {args.agents_dir}", file=sys.stderr)
        return 1
    all_failures: list[str] = []
    checked = 0
    for path in sorted(args.agents_dir.glob("*.md")):
        # The README is not a role file; skip it.
        if path.name == "README.md":
            continue
        checked += 1
        all_failures.extend(_check_role(path))
    if all_failures:
        print(f"agents-check: {len(all_failures)} failure(s) across {checked} role file(s):", file=sys.stderr)
        for line in all_failures:
            print(f"  - {line}", file=sys.stderr)
        return 1
    print(f"agents-check: ok ({checked} role file(s))")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())