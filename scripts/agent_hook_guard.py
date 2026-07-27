#!/usr/bin/env python3
"""Block agent attempts to bypass Git verification hooks (mivia)."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / ".ai" / "policy" / "agent-hook-bypass.json"

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


def iter_strings(value: Any) -> list[str]:
    values: list[str] = []
    if isinstance(value, str):
        values.append(value)
    elif isinstance(value, dict):
        for key, item in value.items():
            if isinstance(key, str):
                values.append(key)
            values.extend(iter_strings(item))
    elif isinstance(value, list):
        for item in value:
            values.extend(iter_strings(item))
    return values


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
    texts = iter_strings(payload)
    for flag in policy.get("blockedFlags", []):
        if not isinstance(flag, str):
            continue
        if flag == "-n":
            if any(re.search(r"(?i)git\s+commit\b[^\n]*\s-n\b", t) for t in texts):
                reasons.append("blocked flag -n")
            continue
        if any(flag in t for t in texts):
            reasons.append(f"blocked flag {flag}")

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
                continue
            if any(cre.search(t) for t in texts):
                reasons.append(f"blocked pattern ({key})")

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
