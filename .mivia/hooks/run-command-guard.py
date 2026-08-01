#!/usr/bin/env python3
"""PreToolUse gate: refuse a run_command argv this repository does not permit.

Two policies, two questions, one gate:

  agent-hook-bypass.json    is this call skipping verification?
  destructive-commands.json is this call about to lose work?

They stay separate files because they need different corrective messages. A
single blocked list would report a hard reset as a hook bypass, which is neither
what happened nor what to fix.

This gate holds NO patterns of its own. Duplicating them here is exactly the
"parallel policy doc" AGENTS.md forbids - the failure mode is not that the copy
is wrong today, it is that someone tightens a policy and this file silently
keeps enforcing last year's. The same JSON drives the Git hooks and
scripts/agent_hook_guard.py, so a rule is written once and lands everywhere it
applies.

Protocol: exit 0 allows, exit 2 blocks with stderr as the reason. Any other exit
is "no decision" and resolves through on_timeout, which is `block` for this hook
- a guard that crashes must not become a guard that is off.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

POLICY_DIR = Path(__file__).resolve().parent.parent / "policy"

# (filename, keys applied to the joined argv). Bypass patterns come first so a
# call that is both a bypass and destructive is reported as the bypass - that is
# the rule with the sharper corrective action.
POLICIES = (
    ("agent-hook-bypass.json", ("blockedCommandPatterns", "blockedFlagPatterns", "blockedEnvPatterns")),
    ("destructive-commands.json", ("destructiveCommandPatterns",)),
)


def block(reason: str) -> None:
    print(reason, file=sys.stderr)
    sys.exit(2)


def load(name: str) -> dict:
    path = POLICY_DIR / name
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        # Fail closed, and say what to fix. A policy this gate cannot read means
        # it cannot answer, and "cannot answer" is not "allow".
        block(f"policy unreadable at {path}: {err}")
        raise  # unreachable; keeps the type checker and the reader honest


def matches(patterns: list, command: str):
    for pattern in patterns:
        try:
            found = re.search(pattern, command)
        except re.error:
            # A malformed pattern is that policy's bug, not this call's.
            # Skipping it is right: blocking every command over one typo would
            # take the repository offline.
            continue
        if found:
            return found.group(0)
    return None


def main() -> None:
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

    for name, keys in POLICIES:
        policy = load(name)
        for key in keys:
            if hit := matches(policy.get(key, []), command):
                block(
                    f"blocked by {name} ({key}): {hit!r}. "
                    + str(policy.get("correctiveMessage", "this command is not permitted here"))
                )
        # Blocked bare flags are matched as exact argv ELEMENTS, not substrings.
        # "-n" appears inside plenty of innocent arguments, and the pattern list
        # already carries the `git commit ... -n` case with the surrounding
        # context that makes it meaningful.
        exact = {str(flag) for flag in policy.get("blockedFlags", []) if str(flag) != "-n"}
        for part in argv:
            if str(part) in exact:
                block(
                    f"blocked by {name} (blockedFlags): {part!r}. "
                    + str(policy.get("correctiveMessage", "this command is not permitted here"))
                )

    sys.exit(0)


if __name__ == "__main__":
    main()
