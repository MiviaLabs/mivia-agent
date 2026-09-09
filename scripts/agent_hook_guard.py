#!/usr/bin/env python3
"""Block agent attempts to bypass Git verification hooks (mivia)."""

from __future__ import annotations

import json
import re
import shlex
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / ".mivia" / "policy" / "agent-hook-bypass.json"

EVENT_NAMES = {
    "user-prompt-submit": "UserPromptSubmit",
    "pre-tool-use": "PreToolUse",
    "permission-request": "PermissionRequest",
    "stop": "Stop",
}
SUPPORTED_AGENTS = {"agents", "claude", "codex"}
PROMPT_EVENTS = {"UserPromptSubmit"}
BLOCK_EVENTS = {"PreToolUse", "PermissionRequest"}
SHELL_TOOLS = {"bash", "shell", "command", "run_terminal_command"}


def load_policy() -> dict[str, Any]:
    try:
        policy = json.loads(POLICY_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"invalid hook bypass policy: {exc}") from exc
    if policy.get("version") != 1:
        raise ValueError("invalid hook bypass policy version")
    if "Do not bypass Git hooks" not in str(policy.get("correctiveMessage", "")):
        raise ValueError("invalid corrective message")
    return policy


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

# Interpreters whose `-c <string>` payload is itself a shell command line,
# never data. Matched by basename so a full path (/bin/bash, /usr/bin/sh)
# is recognized the same as the bare name.
SHELL_INTERPRETERS = {"sh", "bash", "zsh", "dash", "ash", "ksh"}

# git global options that consume the NEXT argv element as their value, in
# BOTH short and long forms (mirrors .mivia/hooks/run-command-guard.py,
# confirmed against git.c handle_options and empirically against the real
# git binary): -C/--git-dir/--work-tree/--namespace/--super-prefix/
# --attr-source each take a directory, path, or tree-ish; -c/--config-env
# take a key[=value]. --exec-path is deliberately excluded: given with no
# "=", git treats it as boolean and never reaches a subcommand.
GIT_GLOBAL_VALUE_OPTIONS = {
    "-C", "--git-dir", "--work-tree", "--namespace", "--super-prefix",
    "-c", "--config-env", "--attr-source",
}


def _interpreter_name(tok: str) -> str:
    return tok.rsplit("/", 1)[-1]


def _git_commit_index(segment: list[str]) -> int | None:
    """Index of the `commit` SUBCOMMAND token, if this segment invokes it.

    Mirrors .mivia/hooks/run-command-guard.py's _git_commit_index. Scans
    past global options (and their values) to find the actual subcommand
    position, rather than a bare scan for the first literal "commit" token
    anywhere - that naive form misclassifies `git branch commit` (a branch
    NAMED commit) as a commit invocation, and under-classifies
    `git -c commit commit -n` (a decoy "commit" used as a -c VALUE, with the
    real subcommand one token later).
    """
    if "git" not in segment:
        return None
    i = segment.index("git") + 1
    n = len(segment)
    while i < n:
        tok = segment[i]
        if tok in GIT_GLOBAL_VALUE_OPTIONS:
            i += 2
            continue
        if tok.startswith("-"):
            i += 1
            continue
        return i if tok == "commit" else None
    return None


def _takes_message_value(tok: str) -> bool:
    """True when the exact option `tok` consumes the NEXT argv element as a
    message/file value (`--` ends option parsing for git commit)."""
    return tok in GIT_COMMIT_VALUE_OPTIONS or tok == "--"


def _split_shell_segments(argv: list[str]) -> list[list[str]]:
    """Split argv into per-command segments at shell separators.

    `git commit -m x && HUSKY=0 git commit -m y` is TWO commands; without a
    split, the first -m value swallows every following token, hiding the
    second command's bypass flags. Splitting restores scanning per command.
    """
    segments: list[list[str]] = []
    current: list[str] = []
    for tok in argv:
        if tok in SHELL_SEPARATORS:
            if current:
                segments.append(current)
                current = []
        else:
            current.append(tok)
    if current:
        segments.append(current)
    return segments


def _expand_shell_c(tokens: list[str], *, _depth: int = 0) -> list[str]:
    """Splice a `sh -c '<command>'` payload's tokens into the scan stream.

    Mirrors .mivia/hooks/run-command-guard.py's _expand_shell_c: a `sh -c`
    payload is a single argv/command-string element that is never itself
    token-split, so the structural git-commit -n walk (and the regex
    backstop, which also needs "git" and "commit" textually adjacent) never
    saw a command wrapped this way. Expansion is additive - the wrapper
    tokens and the raw payload string stay in the output - and recursive, so
    a payload that itself wraps another `sh -c` is still covered.
    """
    if _depth > 20:
        return list(tokens)
    out: list[str] = []
    i = 0
    n = len(tokens)
    while i < n:
        tok = tokens[i]
        out.append(tok)
        if (
            _interpreter_name(tok) in SHELL_INTERPRETERS
            and i + 2 < n
            and tokens[i + 1] == "-c"
        ):
            payload = tokens[i + 2]
            out.append(payload)
            inner = _split_command(payload)
            if inner:
                out.extend(_expand_shell_c(inner, _depth=_depth + 1))
            i += 3
            continue
        i += 1
    return out


def _scan_segment(segment: list[str]) -> list[str]:
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
    vec: list[str] = []
    i = 0
    n = len(segment)
    while i < n:
        tok = segment[i]
        if tok == "--":
            # Post-`--` elements are positional data, but dash-prefixed
            # elements are still scanned as options (fail closed).
            vec.append(tok)
            i += 1
            while i < n and not segment[i].startswith("-"):
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


def _option_segments(argv: list[str]) -> list[list[str]]:
    """One scanned option vector per shell segment.

    option_vector() flattens these for the general blockedFlags exact-match
    check: -m/-F/-C and their long forms consume their value token in EVERY
    segment, git commit or not, so a value literal can never surface as a
    flag regardless of context (see
    test_n_reporting_matches_segment_shape_seeded). bypass_reasons() does
    NOT use this scanned form for the git-commit -n structural check - see
    _segment_has_git_commit_dash_n, which needs RAW segments and does its
    own commit_at-scoped scan so a global `-C <path>` before `commit` can
    never be misread as commit's own value-consuming `-C`.
    """
    return [_scan_segment(segment) for segment in _split_shell_segments(argv)]


def option_vector(argv: list[str]) -> list[str]:
    """Tokens that can carry bypass flags; option VALUES are excluded.

    Compound shell strings are vetted per command: the argv is split at shell
    separators (&&, ;, ||) and each segment is scanned on its own, so a
    second `git commit` after a separator can never hide behind the first
    command's -m value. Within a command, -m/-F/-C consume exactly one value
    token (even a dash-prefixed one) and scanning resumes.
    """
    vec: list[str] = []
    for segment in _option_segments(argv):
        vec.extend(segment)
    return vec


def is_git_commit(argv: list[str]) -> bool:
    """True when argv invokes `git commit` (git options like -c included)."""
    return any(_git_commit_index(seg) is not None for seg in _split_shell_segments(argv))


def _structured_argv(payload: dict[str, Any]) -> list[str] | None:
    """The structured argv array from tool_input, or None when unavailable."""
    tool_input = payload.get("tool_input") or payload.get("toolInput")
    candidates: list[Any] = []
    if isinstance(tool_input, dict):
        candidates.append(tool_input.get("argv"))
        inner = tool_input.get("input")
        if isinstance(inner, dict):
            candidates.append(inner.get("argv"))
    top = payload.get("input")
    if isinstance(top, dict):
        candidates.append(top.get("argv"))
    for argv in candidates:
        if isinstance(argv, list):
            return [str(a) for a in argv]
    return None


def _fallback_strings(payload: dict[str, Any]) -> list[str]:
    """Command/prompt strings scanned when no structured argv is available."""
    out: list[str] = []
    tool_input = payload.get("tool_input") or payload.get("toolInput")
    if isinstance(tool_input, dict):
        cmd = tool_input.get("command")
        if isinstance(cmd, str) and cmd.strip():
            out.append(cmd)
    prompt = payload.get("prompt")
    if isinstance(prompt, str) and prompt.strip():
        out.append(prompt)
    return out


def _env_strings(payload: dict[str, Any]) -> list[str]:
    """Env assignment keys/values from env dicts (never message text)."""
    out: list[str] = []

    def walk(value: Any) -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                if isinstance(key, str) and key.lower() in {
                    "env",
                    "environment",
                    "env_vars",
                    "environment_variables",
                }:
                    if isinstance(item, dict):
                        for ek, ev in item.items():
                            out.append(str(ek))
                            out.append(str(ev))
                    elif isinstance(item, list):
                        out.extend(str(x) for x in item)
                else:
                    walk(item)
        elif isinstance(value, list):
            for item in value:
                walk(item)

    walk(payload)
    return out


def _split_command(text: str) -> list[str] | None:
    """Tokenize a command string; None when it cannot be parsed."""
    try:
        return shlex.split(text)
    except ValueError:
        return None


def _segment_has_git_commit_dash_n(segment: list[str]) -> bool:
    """True when this RAW (unscanned) segment's git-commit arguments contain
    a bare -n.

    Structural, via _git_commit_index, not a bare index() scan: a bare scan
    for the first literal "commit" token misclassified `git branch commit`
    (a branch NAMED commit) as a commit invocation, and missed a long-form
    global option (--git-dir=..., --attr-source ...) sitting between "git"
    and "commit" whose VALUE token isn't "commit" either - both fixed by
    walking global options the same way run-command-guard.py's sibling
    function does. Takes the RAW segment (not the caller's pre-scanned
    option vector): scanning here from commit_at onward, on top of an
    already-scanned vec, would apply commit's value-consuming grammar a
    second time to tokens a GLOBAL option (e.g. -C) already shifted.
    """
    commit_at = _git_commit_index(segment)
    if commit_at is None:
        return False
    return "-n" in _scan_segment(segment[commit_at + 1 :])


def _flag_hits(vec: list[str], segments: list[list[str]], policy: dict[str, Any]) -> list[str]:
    """Exact-flag and git-commit -n hits over the option vector only.

    -n is a real bypass only when it follows `git commit` inside one command;
    git global options (-C, --git-dir, -c, --work-tree) may sit between git
    and commit, so the check is structural (per segment, git then commit then
    -n in order) rather than an adjacency regex over the joined vector. The
    exact blockedFlags loop below skips -n; the structural check owns it.
    """
    hits: list[str] = []
    for flag in policy.get("blockedFlags", []):
        if not isinstance(flag, str) or flag == "-n":
            continue
        if flag in vec:
            hits.append(f"blocked flag {flag}")
    for segment in segments:
        if _segment_has_git_commit_dash_n(segment):
            hits.append("blocked flag -n")
            break
    return hits


def _pattern_hits(texts: list[str], policy: dict[str, Any]) -> list[str]:
    """Pattern hits over the supplied scan texts."""
    hits: list[str] = []
    pattern_keys = (
        "blockedPatterns",
        "blockedFlagPatterns",
        "blockedEnvPatterns",
        "blockedCommandPatterns",
    )
    for key in pattern_keys:
        for pat in policy.get(key, []) or []:
            if not isinstance(pat, str):
                continue
            try:
                cre = re.compile(pat)
            except re.error:
                hits.append(f"invalid blocked pattern ({key})")
                continue
            if any(cre.search(t) for t in texts):
                hits.append(f"blocked pattern ({key})")
    return hits


def has_blocked_env(value: Any, policy: dict[str, Any]) -> bool:
    blocked_env = policy.get("blockedEnv", {})
    legacy_env = policy.get("blockedLegacyEnv", [])
    if isinstance(value, dict):
        for key, item in value.items():
            if isinstance(key, str):
                upper = key.upper()
                if upper in blocked_env and str(item).strip().strip("'\"") == str(blocked_env[upper]):
                    return True
                if upper in {str(n).upper() for n in legacy_env} and str(item).strip().lower() not in {
                    "",
                    "0",
                    "false",
                    "none",
                }:
                    return True
            if has_blocked_env(item, policy):
                return True
    elif isinstance(value, list):
        return any(has_blocked_env(item, policy) for item in value)
    return False


def bypass_reasons(payload: dict[str, Any], policy: dict[str, Any]) -> list[str]:
    reasons: list[str] = []

    argv = _structured_argv(payload)
    if argv is not None:
        # Structured argv: check the option vector only. Message values (-m/-F
        # argument VALUES) are data and are never scanned for flags. The -n
        # structural check needs the per-segment grouping; non-git-commit argv
        # gets no segments so the -n loop stays inert (git log -n 5, grep -n).
        # Expand first: a `sh -c '<command>'` element is itself an unsplit
        # command line, and every check below operates on tokens.
        argv = _expand_shell_c(argv)
        if is_git_commit(argv):
            vec = option_vector(argv)
            # Raw, unscanned segments for the -n structural check: see
            # _segment_has_git_commit_dash_n for why it must not receive an
            # already-scanned vec.
            segments = _split_shell_segments(argv)
        else:
            vec = argv
            segments = []
        texts = [" ".join(vec)]
    else:
        # No structured argv: fall back to scanning command/prompt strings,
        # parsing git commit argv so quoted -m values stay data.
        vec: list[str] = []
        segments: list[list[str]] = []
        texts: list[str] = []
        for raw in _fallback_strings(payload):
            tokens = _split_command(raw)
            if tokens is None:
                # Unparseable (unbalanced quotes): scan the raw text, fail closed.
                texts.append(raw)
                continue
            tokens = _expand_shell_c(tokens)
            if is_git_commit(tokens):
                parsed = option_vector(tokens)
                segments.extend(_split_shell_segments(tokens))
            else:
                parsed = tokens
            vec.extend(parsed)
            texts.append(" ".join(parsed))
    texts.extend(_env_strings(payload))

    reasons.extend(_flag_hits(vec, segments, policy))
    reasons.extend(_pattern_hits(texts, policy))

    husky_zero = re.compile(
        r"(?i)(?:^|[\s;])(?:export\s+|env\s+)?HUSKY\s*=\s*['\"]?0['\"]?(?=$|[\s;])"
    )
    if any(husky_zero.search(t) for t in texts) or has_blocked_env(payload, policy):
        reasons.append("blocked skip environment")

    for name in policy.get("blockedLegacyEnv", []):
        if not isinstance(name, str):
            continue
        assign = re.compile(
            rf"(?i)(?:^|[\s;])(?:export\s+|env\s+)?"
            rf"{re.escape(name)}\s*=\s*['\"]?(?:1|true|yes)['\"]?(?=$|[\s;])"
        )
        if any(assign.search(t) for t in texts):
            reasons.append(f"blocked legacy {name}")

    skip_assign = re.compile(
        r"(?i)(?:^|[\s;])(?:export\s+|env\s+)?"
        r"(?:LEFTHOOK|SKIP_GIT_HOOKS|GIT_HOOKS)\s*=\s*['\"]?(?:0|1|true|yes|skip)['\"]?"
        r"(?=$|[\s;])"
    )
    if any(skip_assign.search(t) for t in texts):
        reasons.append("blocked generic hook-skip environment")

    return sorted(set(reasons))


def is_shell_tool(payload: dict[str, Any]) -> bool:
    tool_name = payload.get("tool_name") or payload.get("toolName") or payload.get("tool")
    if isinstance(tool_name, str) and tool_name.lower() in SHELL_TOOLS:
        return True
    tool_input = payload.get("tool_input") or payload.get("toolInput")
    return isinstance(tool_input, dict) and isinstance(tool_input.get("command"), str)


def event_name(raw: str, payload: dict[str, Any]) -> str:
    pe = payload.get("hook_event_name")
    if isinstance(pe, str) and pe:
        return pe
    return EVENT_NAMES.get(raw, raw)


def emit_context(agent: str, event: str, message: str) -> int:
    if agent == "claude":
        # Simple text path for Claude prompt inject (also used by unit tests).
        print(message)
        return 0
    print(
        json.dumps(
            {
                "hookSpecificOutput": {
                    "hookEventName": event,
                    "additionalContext": message,
                }
            },
            separators=(",", ":"),
        )
    )
    return 0


def emit_block(agent: str, event: str, message: str, reasons: list[str]) -> int:
    if agent == "claude":
        print(message, file=sys.stderr)
        print("Blocked: " + ", ".join(reasons), file=sys.stderr)
        return 2

    payload: dict[str, Any] = {
        "hookSpecificOutput": {
            "hookEventName": event,
            "permissionDecision": "deny",
            "permissionDecisionReason": message,
        }
    }
    if agent == "agents":
        payload["decision"] = "block"
        payload["reason"] = message
    print(json.dumps(payload, separators=(",", ":")))
    return 0


def main(argv: list[str]) -> int:
    if len(argv) < 3:
        print("usage: agent_hook_guard.py <agents|claude|codex> <event> [payload.json]", file=sys.stderr)
        return 2

    agent, event = argv[1], argv[2]
    if agent not in SUPPORTED_AGENTS:
        print(f"unsupported agent surface: {agent}", file=sys.stderr)
        return 2

    raw_payload = sys.stdin.read() if len(argv) < 4 else Path(argv[3]).read_text(encoding="utf-8")
    try:
        policy = load_policy()
        payload = json.loads(raw_payload) if raw_payload.strip() else {}
    except (ValueError, json.JSONDecodeError) as exc:
        msg = f"Malformed agent hook payload; protected action denied: {exc}"
        print(msg, file=sys.stderr)
        return 2

    if not isinstance(payload, dict):
        payload = {"payload": payload}

    ev = event_name(event, payload)
    if ev in BLOCK_EVENTS and not is_shell_tool(payload):
        # Non-shell tools are not blocked here (other guards may apply later).
        return 0

    reasons = bypass_reasons(payload, policy)
    if not reasons:
        return 0

    message = f"{policy['correctiveMessage']} Detected: {', '.join(reasons)}."
    if ev in PROMPT_EVENTS:
        return emit_context(agent, ev, message)
    if ev in BLOCK_EVENTS:
        return emit_block(agent, ev, message, reasons)
    return emit_context(agent, ev, message)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
