#!/usr/bin/env python3
"""Contract tests for agent hook bypass guards (mivia)."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GUARD = ROOT / "scripts" / "agent_hook_guard.py"
RUNNER = ROOT / "scripts" / "run_agent_hook_guard.sh"


def run_guard(agent: str, event: str, payload: dict) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(GUARD), agent, event],
        input=json.dumps(payload),
        text=True,
        capture_output=True,
        cwd=ROOT,
    )


def test_blocks_no_verify_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit --no-verify -m x"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_allows_clean_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "make test"},
        },
    )
    assert proc.returncode == 0


def test_prompt_injects_correction() -> None:
    proc = run_guard(
        "claude",
        "UserPromptSubmit",
        {
            "hook_event_name": "UserPromptSubmit",
            "prompt": "please git commit --no-verify",
        },
    )
    assert proc.returncode == 0
    assert "Do not bypass Git hooks" in proc.stdout


def test_codex_denies_with_json() -> None:
    proc = run_guard(
        "codex",
        "pre-tool-use",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m test --no-verify"},
        },
    )
    assert proc.returncode == 0
    payload = json.loads(proc.stdout)
    hook = payload.get("hookSpecificOutput")
    assert isinstance(hook, dict)
    assert hook.get("permissionDecision") == "deny"
    reason = str(hook.get("permissionDecisionReason", ""))
    assert "Do not bypass Git hooks" in reason


def test_env_payload_blocks_husky() -> None:
    proc = run_guard(
        "codex",
        "permission-request",
        {
            "hook_event_name": "PermissionRequest",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m x", "env": {"HUSKY": "0"}},
        },
    )
    assert proc.returncode == 0
    payload = json.loads(proc.stdout)
    hook = payload.get("hookSpecificOutput")
    assert isinstance(hook, dict) and hook.get("permissionDecision") == "deny"


def test_runner_blocks_before_binary() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        fake_bin = Path(tmp)
        fake = fake_bin / "mivia"
        fake.write_text(
            "#!/usr/bin/env sh\n"
            'if [ "$1" = "hook" ] && [ "$2" = "--help" ]; then exit 0; fi\n'
            "printf 'future binary ran\\n'\n",
            encoding="utf-8",
        )
        fake.chmod(0o755)
        env = os.environ.copy()
        env["PATH"] = f"{fake_bin}:{env['PATH']}"
        proc = subprocess.run(
            [str(RUNNER), "claude", "pre-tool-use"],
            input=json.dumps(
                {
                    "hook_event_name": "PreToolUse",
                    "tool_name": "Bash",
                    "tool_input": {"command": "git commit --no-verify -m x"},
                }
            ),
            cwd=ROOT,
            env=env,
            text=True,
            capture_output=True,
        )
    assert proc.returncode != 0
    assert "future binary ran" not in proc.stdout
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def main() -> None:
    test_blocks_no_verify_shell()
    test_allows_clean_shell()
    test_prompt_injects_correction()
    test_codex_denies_with_json()
    test_env_payload_blocks_husky()
    test_runner_blocks_before_binary()
    print("test_agent_hook_guard: ok")


if __name__ == "__main__":
    main()
