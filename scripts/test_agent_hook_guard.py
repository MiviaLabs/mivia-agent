#!/usr/bin/env python3
"""Contract tests for agent hook bypass guards (mivia)."""

from __future__ import annotations

import json
import os
import subprocess
import shutil
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GUARD = ROOT / "scripts" / "agent_hook_guard.py"
RUNNER = ROOT / "scripts" / "run_agent_hook_guard.sh"
RC_GUARD = ROOT / ".mivia" / "hooks" / "run-command-guard.py"


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


def run_command_guard(argv: list[str]) -> subprocess.CompletedProcess[str]:
    """Drive the mivia PreToolUse gate directly with a structured argv."""
    return subprocess.run(
        [sys.executable, str(RC_GUARD)],
        input=json.dumps(
            {"event": "PreToolUse", "tool": "run_command", "input": {"argv": argv}}
        ),
        text=True,
        capture_output=True,
        cwd=ROOT,
    )


def test_allows_message_containing_dash_n_argv() -> None:
    # (a) "-n" inside a -m message is data, not the -n flag.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-m", "fix(cli): handle -n flag"]},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_message_containing_no_verify_argv() -> None:
    # (b) "--no-verify" inside a -m message is documentation, not a flag.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {
                "argv": ["git", "commit", "-m", "feat(x): add --no-verify docs"]
            },
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_shell_message_containing_dash_n() -> None:
    # Fallback path (no structured argv): the quoted -m value is parsed as one
    # argv element, so "-n" inside it is data, not a flag.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": 'git commit -m "fix(cli): handle -n flag"'},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_shell_message_containing_no_verify() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": 'git commit -m "feat(x): add --no-verify docs"'},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_blocks_commit_dash_n_argv() -> None:
    # (c) The real -n flag on git commit is still blocked.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-n"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_commit_no_verify_argv() -> None:
    # (d) The real --no-verify flag is still blocked.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "--no-verify"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_core_hooks_path_argv() -> None:
    # (e) A real bypass - redirecting hooks via -c core.hooksPath - is blocked.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {
                "argv": ["git", "-c", "core.hooksPath=/tmp/hooks", "commit", "-m", "x"]
            },
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_run_command_guard_allows_message_containing_dash_n() -> None:
    proc = run_command_guard(["git", "commit", "-m", "fix(cli): handle -n flag"])
    assert proc.returncode == 0, proc.stderr


def test_run_command_guard_allows_message_containing_no_verify() -> None:
    proc = run_command_guard(["git", "commit", "-m", "feat(x): add --no-verify docs"])
    assert proc.returncode == 0, proc.stderr


def test_run_command_guard_still_blocks_commit_dash_n() -> None:
    proc = run_command_guard(["git", "commit", "-n"])
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_still_blocks_no_verify_after_message() -> None:
    # A real --no-verify AFTER -m still skips hooks and must be blocked.
    proc = run_command_guard(["git", "commit", "-m", "x", "--no-verify"])
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


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
        runner = str(RUNNER)
        command = [runner, "claude", "pre-tool-use"]
        if os.name == "nt":
            bash = r"C:\Program Files\Git\bin\bash.exe"
            if not Path(bash).is_file():
                bash = shutil.which("bash") or "bash"
            runner = subprocess.run(
                [bash, "-c", 'cygpath -u "$1"', "bash", runner],
                text=True,
                stdout=subprocess.PIPE,
                check=True,
            ).stdout.strip()
            command = [bash, runner, "claude", "pre-tool-use"]
        proc = subprocess.run(
            command,
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


# ---------------------------------------------------------------------------
# Hostile-audit regressions:
#   (1) bundled short-option -n must be blocked in any bundle (-an, -na, -nF),
#       while -Fn is -F with VALUE 'n' and stays allowed;
#   (2) every command in a compound shell string (&&, ;) must be vetted, not
#       hidden behind the first command's -m value;
#   (3) the token after -m/-F/-C is ALWAYS the value - even when it looks like
#       a flag (-m -n, -m --no-verify) - while -n AFTER the value is a real
#       option and stays blocked.
# ---------------------------------------------------------------------------


def test_blocks_bundled_dash_n_argv() -> None:
    # (1) -n inside a short-option bundle is still the -n flag.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-an", "-m", "x"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_bundled_dash_n_last_argv() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-na"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_bundled_dash_n_before_file_argv() -> None:
    # -nF is the -n flag bundled with -F, not -F with value 'n'.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-nF"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_allows_dash_f_with_n_value_argv() -> None:
    # -Fn is -F whose VALUE is 'n'; the value is data, never a flag.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-Fn", "-m", "x"]},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_blocks_bundled_dash_n_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -an -m x"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_bundled_dash_n_last_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -na"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_bundled_dash_n_before_file_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -nF"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_allows_dash_f_with_n_value_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -Fn -m x"},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_run_command_guard_blocks_bundled_dash_n() -> None:
    proc = run_command_guard(["git", "commit", "-an", "-m", "x"])
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_blocks_bundled_dash_n_last() -> None:
    proc = run_command_guard(["git", "commit", "-na"])
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_blocks_bundled_dash_n_before_file() -> None:
    proc = run_command_guard(["git", "commit", "-nF"])
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_allows_dash_f_with_n_value() -> None:
    proc = run_command_guard(["git", "commit", "-Fn", "-m", "x"])
    assert proc.returncode == 0, proc.stderr


def test_blocks_compound_husky_after_message_shell() -> None:
    # (2) A second command after && must not hide behind the first -m value.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m x && HUSKY=0 git commit -m y"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_compound_skip_env_after_message_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m x; SKIP_GIT_HOOKS=1 git commit -m y"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_compound_hooks_path_after_message_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {
                "command": "git commit -m x && git -c core.hooksPath=/tmp/h commit -m y"
            },
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_run_command_guard_blocks_compound_husky() -> None:
    proc = run_command_guard(
        ["git", "commit", "-m", "x", "&&", "HUSKY=0", "git", "commit", "-m", "y"]
    )
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_blocks_compound_hooks_path() -> None:
    proc = run_command_guard(
        [
            "git",
            "commit",
            "-m",
            "x",
            "&&",
            "git",
            "-c",
            "core.hooksPath=/tmp/h",
            "commit",
            "-m",
            "y",
        ]
    )
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_allows_message_dash_n_value_argv() -> None:
    # (3) -m consumes the next element as its VALUE even when it looks like a
    # flag; git runs hooks for `git commit -m -n`.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-m", "-n"]},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_message_no_verify_value_argv() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-m", "--no-verify"]},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_blocks_dash_n_after_message_value_argv() -> None:
    # -n AFTER the -m value is a real option and stays blocked.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "commit", "-m", "x", "-n"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_allows_message_dash_n_value_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m -n"},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_message_no_verify_value_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m --no-verify"},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_blocks_dash_n_after_message_value_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m x -n"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_run_command_guard_allows_message_dash_n_value() -> None:
    proc = run_command_guard(["git", "commit", "-m", "-n"])
    assert proc.returncode == 0, proc.stderr


def test_run_command_guard_allows_message_no_verify_value() -> None:
    proc = run_command_guard(["git", "commit", "-m", "--no-verify"])
    assert proc.returncode == 0, proc.stderr


def test_run_command_guard_blocks_dash_n_after_message_value() -> None:
    proc = run_command_guard(["git", "commit", "-m", "x", "-n"])
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


# ---------------------------------------------------------------------------
# Pipe-as-separator correctness regression:
#   SHELL_SEPARATORS must only contain tokens that are actual shell operators
#   in the parsing context where they are used. Structured argv lists from tool
#   inputs have already been shell-parsed, so '|' is data, not a shell operator.
#   Before the fix, '|' in SHELL_SEPARATORS caused _split_shell_segments to
#   incorrectly split structured argv at standalone '|' tokens, which could
#   cause false negatives when -m value consumption was disrupted.
# ---------------------------------------------------------------------------


def _option_vector(argv: list[str]) -> list[str]:
    """Import and call option_vector from agent_hook_guard for unit testing."""
    import importlib.util
    guard_path = ROOT / "scripts" / "agent_hook_guard.py"
    spec = importlib.util.spec_from_file_location("agent_hook_guard", str(guard_path))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.option_vector(argv)


def _rc_guard_option_vector(argv: list[str]) -> list[str]:
    """Import and call option_vector from run-command-guard for unit testing."""
    import importlib.util
    guard_path = RC_GUARD
    spec = importlib.util.spec_from_file_location("run_command_guard", str(guard_path))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.option_vector(argv)


def test_option_vector_pipe_is_not_a_shell_separator() -> None:
    # Standalone '|' in a structured argv is data, not a shell operator.
    # The option vector must NOT split at '|' tokens.
    vec = _option_vector(["git", "commit", "-m", "x", "|", "head", "-1"])
    assert "|" in vec, (
        "standalone '|' must be preserved in the option vector; "
        "it is not a shell separator in structured argv context"
    )
    # Verify the vector contains tokens from both sides of the pipe.
    assert "git" in vec
    assert "commit" in vec
    assert "head" in vec


def test_option_vector_preserves_m_value_across_pipe() -> None:
    # The -m value 'x' must be consumed even when '|' follows.
    vec = _option_vector(["git", "commit", "-m", "x", "|", "git", "commit", "-m", "y"])
    assert "x" not in vec, "-m value 'x' must be consumed from the option vector"
    assert "y" not in vec, "-m value 'y' must be consumed from the option vector"
    assert "|" in vec, "standalone '|' must remain in the option vector"


def test_option_vector_ampersand_ampersand_still_splits() -> None:
    # '&&' IS a real shell operator and must still cause a segment split.
    vec = _option_vector(["git", "commit", "-m", "x", "&&", "git", "commit", "-n"])
    # The -n from the second segment must be in the vector.
    assert "-n" in vec, "-n from second command after && must be detected"
    # The -m value 'x' must be consumed.
    assert "x" not in vec


def test_option_vector_semicolon_still_splits() -> None:
    # ';' IS a real shell operator and must still cause a segment split.
    vec = _option_vector(["git", "commit", "-m", "x", ";", "git", "commit", "-n"])
    assert "-n" in vec
    assert "x" not in vec


def test_option_vector_or_or_still_splits() -> None:
    # '||' IS a real shell operator and must still cause a segment split.
    vec = _option_vector(["git", "commit", "-m", "x", "||", "git", "commit", "-n"])
    assert "-n" in vec
    assert "x" not in vec


def test_run_command_guard_blocks_compound_hooks_path_pipe() -> None:
    proc = run_command_guard(
        [
            "git",
            "commit",
            "-m",
            "x",
            "|",
            "git",
            "-c",
            "core.hooksPath=/dev/null",
            "commit",
            "-m",
            "y",
        ]
    )
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_blocks_compound_husky_pipe() -> None:
    proc = run_command_guard(
        [
            "git",
            "commit",
            "-m",
            "x",
            "|",
            "HUSKY=0",
            "git",
            "commit",
            "-m",
            "y",
        ]
    )
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_blocks_compound_dash_n_pipe() -> None:
    proc = run_command_guard(
        [
            "git",
            "commit",
            "-m",
            "x",
            "|",
            "git",
            "commit",
            "-n",
            "-m",
            "y",
        ]
    )
    assert proc.returncode == 2, proc.stderr
    assert "agent-hook-bypass.json" in proc.stderr


def test_rc_guard_option_vector_pipe_is_not_a_shell_separator() -> None:
    # Regression: run-command-guard SHELL_SEPARATORS must match agent_hook_guard.
    # Standalone '|' must NOT cause a segment split in the sibling guard.
    vec = _rc_guard_option_vector(["git", "commit", "-m", "x", "|", "head", "-1"])
    assert "|" in vec, (
        "run-command-guard: standalone '|' must be preserved in the option vector; "
        "it is not a shell separator in structured argv context"
    )


def test_rc_guard_option_vector_preserves_m_value_across_pipe() -> None:
    # -m values on both sides of '|' must be consumed without segment splitting.
    vec = _rc_guard_option_vector(["git", "commit", "-m", "x", "|", "git", "commit", "-m", "y"])
    assert "x" not in vec, "run-command-guard: -m value 'x' must be consumed"
    assert "y" not in vec, "run-command-guard: -m value 'y' must be consumed"
    assert "|" in vec, "run-command-guard: standalone '|' must remain in the vector"


def main() -> None:
    test_blocks_no_verify_shell()
    test_allows_clean_shell()
    test_prompt_injects_correction()
    test_codex_denies_with_json()
    test_env_payload_blocks_husky()
    test_runner_blocks_before_binary()
    test_blocks_bundled_dash_n_argv()
    test_blocks_bundled_dash_n_last_argv()
    test_blocks_bundled_dash_n_before_file_argv()
    test_allows_dash_f_with_n_value_argv()
    test_blocks_bundled_dash_n_shell()
    test_blocks_bundled_dash_n_last_shell()
    test_blocks_bundled_dash_n_before_file_shell()
    test_allows_dash_f_with_n_value_shell()
    test_run_command_guard_blocks_bundled_dash_n()
    test_run_command_guard_blocks_bundled_dash_n_last()
    test_run_command_guard_blocks_bundled_dash_n_before_file()
    test_run_command_guard_allows_dash_f_with_n_value()
    test_blocks_compound_husky_after_message_shell()
    test_blocks_compound_skip_env_after_message_shell()
    test_blocks_compound_hooks_path_after_message_shell()
    test_run_command_guard_blocks_compound_husky()
    test_run_command_guard_blocks_compound_hooks_path()
    test_allows_message_dash_n_value_argv()
    test_allows_message_no_verify_value_argv()
    test_blocks_dash_n_after_message_value_argv()
    test_allows_message_dash_n_value_shell()
    test_allows_message_no_verify_value_shell()
    test_blocks_dash_n_after_message_value_shell()
    test_run_command_guard_allows_message_dash_n_value()
    test_run_command_guard_allows_message_no_verify_value()
    test_run_command_guard_blocks_dash_n_after_message_value()
    test_option_vector_pipe_is_not_a_shell_separator()
    test_option_vector_preserves_m_value_across_pipe()
    test_option_vector_ampersand_ampersand_still_splits()
    test_option_vector_semicolon_still_splits()
    test_option_vector_or_or_still_splits()
    test_run_command_guard_blocks_compound_hooks_path_pipe()
    test_run_command_guard_blocks_compound_husky_pipe()
    test_run_command_guard_blocks_compound_dash_n_pipe()
    test_rc_guard_option_vector_pipe_is_not_a_shell_separator()
    test_rc_guard_option_vector_preserves_m_value_across_pipe()
    print("test_agent_hook_guard: ok")


if __name__ == "__main__":
    main()
