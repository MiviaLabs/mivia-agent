#!/usr/bin/env python3
"""PreToolUse gate: refuse a run_command argv that bypasses Git hooks.

This is mivia's own lifecycle hook, running against mivia's own repository. It
enforces the same rule `scripts/agent_hook_guard.py` enforces for Claude Code,
one layer down: whatever harness is driving, `run_command` does not get to skip
verification.

It reads the patterns from `.mivia/policy/agent-hook-bypass.json` and holds none
of its own. Duplicating them here is exactly the "parallel policy doc" AGENTS.md
forbids - the failure mode is not that the copy is wrong today, it is that
someone tightens the policy and this file silently keeps enforcing last year's.

Protocol: exit 0 allows, exit 2 blocks with stderr as the reason. Any other exit
is treated as "no decision" and resolves through on_timeout, which is `block`
for this hook - a guard that crashes must not become a guard that is off.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

POLICY = Path(__file__).resolve().parent.parent / "policy" / "agent-hook-bypass.json"

# Pattern groups applied to the joined argv. Env-var assignments cannot reach a
# child through run_command's argv - there is no shell - but a script or wrapper
# named in that argv can carry them, so they are matched too.
PATTERN_KEYS = ("blockedCommandPatterns", "blockedFlagPatterns", "blockedEnvPatterns")


def block(reason: str) -> "None":
    print(reason, file=sys.stderr)
    sys.exit(2)


def main() -> None:
    try:
        policy = json.loads(POLICY.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        # Fail closed, and say what to fix. A missing policy file means this
        # gate cannot answer, and "cannot answer" is not "allow".
        block(f"hook-bypass policy unreadable at {POLICY}: {err}")

    try:
        payload = json.loads(sys.stdin.read() or "{}")
    except ValueError as err:
        block(f"hook payload did not parse: {err}")

    argv = (payload.get("input") or {}).get("argv") or []
    if not isinstance(argv, list):
        block("run_command input carried a non-list argv")
    command = " ".join(str(part) for part in argv)
    if not command.strip():
        sys.exit(0)

    for key in PATTERN_KEYS:
        for pattern in policy.get(key, []):
            try:
                matched = re.search(pattern, command)
            except re.error:
                # A malformed pattern is the policy's bug, not this call's.
                # Skipping it is right: blocking every command over a typo in
                # one entry would take the repository offline.
                continue
            if matched:
                block(
                    f"blocked by {POLICY.name} ({key}): {matched.group(0)!r}. "
                    + str(policy.get("correctiveMessage", "do not bypass verification"))
                )

    # Blocked bare flags are checked as exact argv elements rather than as
    # substrings. "-n" appears inside plenty of innocent arguments, and the
    # pattern list already carries the `git commit ... -n` case with the context
    # that makes it meaningful.
    exact = {str(flag) for flag in policy.get("blockedFlags", []) if str(flag) != "-n"}
    for part in argv:
        if str(part) in exact:
            block(
                f"blocked by {POLICY.name} (blockedFlags): {part!r}. "
                + str(policy.get("correctiveMessage", "do not bypass verification"))
            )

    sys.exit(0)


if __name__ == "__main__":
    main()
