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

# (filename, keys applied to the joined option vector). Bypass patterns come
# first so a call that is both a bypass and destructive is reported as the
# bypass - that is the rule with the sharper corrective action.
POLICIES = (
    ("agent-hook-bypass.json", ("blockedCommandPatterns", "blockedFlagPatterns", "blockedEnvPatterns")),
    ("destructive-commands.json", ("destructiveCommandPatterns",)),
    ("resource-exhaustion.json", ("destructiveCommandPatterns",)),
)

# For `git commit`, these consume the NEXT argv element as a message/file value
# (and `--` ends option parsing). The VALUES are data, never bypass flags, so
# they are stripped from the option vector before any pattern is applied.
GIT_COMMIT_VALUE_OPTIONS = {
    "-m",
    "-F",
    "-C",
    "--message",
    "--file",
    "--reuse-message",
    "--reedit-message",
}

# Short-option characters that consume a value for `git commit`. Uppercase -C
# (reuse-message) takes a value; lowercase -c (git config) does not.
GIT_COMMIT_SHORT_VALUE_CHARS = "mFC"

# Shell control operators that start a NEW command. Every command in a
# compound string must be vetted, so scanning restarts at these.
SHELL_SEPARATORS = {"&&", ";", "||"}


def _takes_message_value(tok: str) -> bool:
    """True when the exact option `tok` consumes the NEXT argv element as a
    message/file value (`--` ends option parsing for git commit)."""
    return tok in GIT_COMMIT_VALUE_OPTIONS or tok == "--"


def _split_shell_segments(argv: list) -> list:
    """Split argv into per-command segments at shell separators.

    A compound command that chains a second git commit after an environment
    override is TWO commands; without a split, the first -m value swallows
    every following token, hiding the second command's bypass flags.
    Splitting restores scanning per command.
    """
    segments = []
    current = []
    for tok in argv:
        if str(tok) in SHELL_SEPARATORS:
            if current:
                segments.append(current)
                current = []
        else:
            current.append(tok)
    if current:
        segments.append(current)
    return segments


def _scan_segment(segment: list) -> list:
    """Reduce one command's argv to tokens that can carry bypass flags.

    For `git commit`, -m/-F/-C/--message/--file/--reuse-message/--reedit-
    message consume EXACTLY ONE following argv element as their VALUE, and
    scanning resumes afterwards. The value is always data, even when it looks
    like a flag: `git commit -m -n` means the message is "-n" and hooks still
    run. A real option AFTER the value (`-m x -n`) is still scanned.

    Short-option bundles are parsed char-by-char: 'n' is a no-arg flag and is
    blocked in any bundle (-an, -na, -nF, -anF); 'm'/'F'/'C' consume the rest
    of the token or the next token as their value, so -Fn is -F with value
    'n' and never a flag.
    """
    vec = []
    i = 0
    n = len(segment)
    while i < n:
        tok = str(segment[i])
        if tok == "--":
            # Post-`--` elements are positional data, but dash-prefixed
            # elements are still scanned as options (fail closed).
            vec.append(tok)
            i += 1
            while i < n and not str(segment[i]).startswith("-"):
                i += 1
            continue
        if len(tok) > 2 and tok.startswith("--"):
            vec.append(tok)
            i += 2 if _takes_message_value(tok) else 1
            continue
        if len(tok) > 1 and tok[0] == "-" and tok[1] != "-":
            # Short-option bundle, parsed char-by-char.
            body = tok[1:]
            j = 0
            consumed_next = False
            while j < len(body):
                char = body[j]
                if char == "n":
                    vec.append("-n")
                    j += 1
                elif char in GIT_COMMIT_SHORT_VALUE_CHARS:
                    vec.append("-" + char)
                    if j + 1 < len(body):
                        # The rest of the token is the value: -Fv, -mx.
                        j = len(body)
                    else:
                        # The next argv element is the value: consume it.
                        consumed_next = True
                        j = len(body)
                else:
                    vec.append("-" + char)
                    j += 1
            i += 2 if consumed_next else 1
            continue
        vec.append(tok)
        i += 1
    return vec


def option_vector(argv: list) -> list:
    """Tokens that can carry bypass flags; option VALUES are excluded.

    Compound shell strings are vetted per command: the argv is split at shell
    separators (&&, ;, ||) and each segment is scanned on its own, so a
    second `git commit` after a separator can never hide behind the first
    command's -m value. Only a segment that actually invokes `git ... commit`
    gets the commit-argument char-bundling treatment, and only from the
    `commit` token onward - every other segment, and everything before
    `commit` in a git-commit segment, passes through as plain argv elements.
    Applying commit's short-option grammar to an unrelated command (a second
    `go test -fuzz X` after `&&`) shredded flags that grammar was never meant
    to see.
    """
    vec = []
    for segment in _split_shell_segments(argv):
        commit_at = _git_commit_index(segment)
        if commit_at is None:
            vec.extend(str(tok) for tok in segment)
            continue
        vec.extend(str(tok) for tok in segment[: commit_at + 1])
        vec.extend(_scan_segment(segment[commit_at + 1 :]))
    return vec


# git global options that consume the NEXT argv element as their value, in
# BOTH their short and long forms (confirmed against git.c handle_options):
# -C/--git-dir/--work-tree/--namespace/--super-prefix each take a directory
# or path, -c/--config-env take a key[=value]. Missing any of these here was
# a real, live bypass: `git --git-dir /tmp/x commit -n` was misread as a
# boolean flag followed by an unrelated value token, so _git_commit_index
# never found "commit" and the whole segment skipped both the structural -n
# check and the regex backstop (which also requires literal "git"+"commit"
# adjacency). --exec-path is deliberately excluded: given with no "=", git
# treats it as boolean (prints the path and exits), never reaching a
# subcommand, so it cannot hide one.
GIT_GLOBAL_VALUE_OPTIONS = {
    "-C", "--git-dir", "--work-tree", "--namespace", "--super-prefix",
    "-c", "--config-env",
}


def _git_commit_index(segment: list) -> int | None:
    """Index of the `commit` SUBCOMMAND token, if this segment invokes it.

    None when the segment does not invoke git commit. Scans past global
    options (and their values, for -C/-c) to find the actual subcommand
    position - the first token after `git` that is not itself an option.
    A bare scan for the first literal "commit" token anywhere misclassifies
    `git branch commit` (a branch NAMED commit) as a commit invocation, and
    under-classifies `git -c commit commit -n` (a decoy "commit" used as a
    -c VALUE, with the real subcommand one token later).
    """
    parts = [str(part) for part in segment]
    if "git" not in parts:
        return None
    i = parts.index("git") + 1
    n = len(parts)
    while i < n:
        tok = parts[i]
        if tok in GIT_GLOBAL_VALUE_OPTIONS:
            i += 2  # skip the option and its value
            continue
        if tok.startswith("-"):
            i += 1  # a boolean global flag; its value, if any, is attached
            continue
        return i if tok == "commit" else None
    return None


def is_git_commit(argv: list) -> bool:
    """True when argv invokes `git commit` (git options like -c included)."""
    return any(_git_commit_index(seg) is not None for seg in _split_shell_segments(argv))


def segment_has_git_commit_dash_n(segment: list) -> bool:
    """True when this segment's git-commit arguments contain a bare -n.

    Structural, not string-matched: it does not require "git" and "commit"
    to be textually adjacent, so global options between them (-C, -c,
    --no-pager, --git-dir=...) cannot hide a -n the way the
    blockedFlagPatterns regex needing them literally adjacent can be made to.
    """
    commit_at = _git_commit_index(segment)
    if commit_at is None:
        return False
    return "-n" in _scan_segment(segment[commit_at + 1 :])


def block(reason: str) -> None:
    print(reason, file=sys.stderr)
    sys.exit(2)


def load(name: str) -> dict:
    path = POLICY_DIR / name
    try:
        policy = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        # Fail closed, and say what to fix. A policy this gate cannot read means
        # it cannot answer, and "cannot answer" is not "allow".
        block(f"policy unreadable at {path}: {err}")
        raise  # unreachable; keeps the type checker and the reader honest
    # A truncated or emptied policy - {} is valid JSON - parsed without error
    # and then blocked NOTHING: every pattern list came back via .get(key, []),
    # which reads a missing key the same as an intentionally empty one. Require
    # the shape every shipped policy actually has, so a policy this thin is as
    # loud as one that fails to parse.
    if not isinstance(policy, dict) or policy.get("version") != 1:
        block(f"policy at {path} is missing or has the wrong version (want 1)")
    if not str(policy.get("correctiveMessage", "")).strip():
        block(f"policy at {path} carries no correctiveMessage")
    return policy


def matches(patterns: list, command: str, *, source: str = "") -> str | None:
    for pattern in patterns:
        try:
            found = re.search(pattern, command)
        except re.error as err:
            # A malformed pattern used to be skipped silently: one typo in a
            # policy file quietly disabled that one rule while every gate
            # kept reporting ok. Fail loud instead - a policy this gate
            # cannot evaluate is the same "cannot answer" case load() already
            # treats as a block, not an allow.
            block(f"malformed pattern in {source or 'policy'}: {pattern!r} ({err})")
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

    # Scan only the option vector. For git commit, -m/-F/-C VALUES and post-`--`
    # positionals are message data, never bypass flags; a standalone dash-
    # prefixed element after the terminator (`-m x -n`) is still an option.
    parts = [str(part) for part in argv]
    # A structural check, ahead of the pattern-based ones below: -n survives
    # any global git option between `git` and `commit`, which the
    # blockedFlagPatterns regex requires to be textually adjacent.
    for segment in _split_shell_segments(parts):
        if segment_has_git_commit_dash_n(segment):
            corrective = str(
                load("agent-hook-bypass.json").get(
                    "correctiveMessage", "this command is not permitted here"
                )
            )
            block(
                "blocked by agent-hook-bypass.json (structural): -n on git "
                "commit. " + corrective
            )
    vec = option_vector(parts)
    options = " ".join(vec)

    for name, keys in POLICIES:
        policy = load(name)
        for key in keys:
            if hit := matches(policy.get(key, []), options, source=f"{name} ({key})"):
                block(
                    f"blocked by {name} ({key}): {hit!r}. "
                    + str(policy.get("correctiveMessage", "this command is not permitted here"))
                )
        # Blocked bare flags are matched as exact argv ELEMENTS, not substrings.
        # "-n" appears inside plenty of innocent arguments (and in -m message
        # values, which the option vector above already strips), and the pattern
        # list already carries the `git commit ... -n` case with the surrounding
        # context that makes it meaningful.
        exact = {str(flag) for flag in policy.get("blockedFlags", []) if str(flag) != "-n"}
        for part in vec:
            if str(part) in exact:
                block(
                    f"blocked by {name} (blockedFlags): {part!r}. "
                    + str(policy.get("correctiveMessage", "this command is not permitted here"))
                )

    sys.exit(0)


if __name__ == "__main__":
    main()
