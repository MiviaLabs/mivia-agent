#!/usr/bin/env python3
"""Contract tests for agent hook bypass guards (mivia)."""

from __future__ import annotations

import importlib.util
import json
import re
import os
import random
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


def flags_with_no_backing_pattern() -> list[str]:
    """blockedFlags entries that no blockedFlagPatterns regex also catches.

    Derived, never hand-listed: a hand-listed copy of a computed set is the
    same defect one level up.
    """
    policy = json.loads(
        (ROOT / ".mivia" / "policy" / "agent-hook-bypass.json").read_text(encoding="utf-8")
    )
    flags = policy.get("blockedFlags") or []
    patterns = [re.compile(p) for p in policy.get("blockedFlagPatterns") or []]
    return [f for f in flags if not any(p.search(f"git commit {f} -m x") for p in patterns)]


def test_the_exact_flag_list_is_load_bearing() -> None:
    """Pins the blockedFlags mechanism itself, not the regexes that shadow it.

    Almost every blocked flag is also matched by a blockedFlagPatterns regex,
    so the exact-flag loop could be deleted, or a policy entry dropped, with
    the whole suite green. The flags below have no backing pattern, and the
    assertion is on the reason string, so neither mechanism can stand in for
    the other.
    """
    unbacked = flags_with_no_backing_pattern()
    assert unbacked, (
        "every blockedFlags entry is also pattern-matched, so this test pins "
        "nothing. Keep one flag covered by the list alone, or assert the "
        "reason string for a pattern-backed flag instead."
    )
    for flag in unbacked:
        proc = run_guard(
            "claude",
            "PreToolUse",
            {
                "hook_event_name": "PreToolUse",
                "tool_name": "Bash",
                "tool_input": {"command": f"git commit {flag} -m x"},
            },
        )
        assert proc.returncode != 0, f"{flag} was not blocked"
        assert f"blocked flag {flag}" in (proc.stderr + proc.stdout), (
            f"{flag} was blocked, but not by the exact-flag list"
        )


def test_every_blocked_flag_is_actually_blocked() -> None:
    """Sweep the whole policy list, so a new entry cannot ship untested."""
    policy = json.loads(
        (ROOT / ".mivia" / "policy" / "agent-hook-bypass.json").read_text(encoding="utf-8")
    )
    flags = policy.get("blockedFlags") or []
    assert flags, "blockedFlags is empty: the exact-flag mechanism would be inert"
    for flag in flags:
        proc = run_guard(
            "claude",
            "PreToolUse",
            {
                "hook_event_name": "PreToolUse",
                "tool_name": "Bash",
                "tool_input": {"command": f"git commit {flag} -m x"},
            },
        )
        assert proc.returncode != 0, f"{flag} was not blocked"


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


# ---------------------------------------------------------------------------
# Git-global-option -n bypass regression: git options between `git` and
# `commit` (-C path, --git-dir=..., -c key=value, --work-tree=...) used to
# defeat the old adjacency regex (git\s+commit ... -n), so `git -C /tmp/x
# commit -n` skipped hooks unblocked. The -n check is now structural per shell
# segment (git then commit then -n in order), which also never flags -n on
# non-commit commands (git log -n 5) or global options without -n.
# ---------------------------------------------------------------------------


def test_blocks_commit_dash_n_after_git_c_argv() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": ["git", "-C", "/tmp/x", "commit", "-n", "-m", "y"]},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_commit_dash_n_after_git_dir_argv() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {
                "argv": ["git", "--git-dir=/tmp/g", "commit", "-n", "-m", "y"]
            },
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_commit_dash_n_after_git_config_argv() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {
                "argv": ["git", "-c", "user.email=x", "commit", "-n", "-m", "y"]
            },
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_commit_dash_n_after_git_c_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git -C /tmp/x commit -n -m y"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_blocks_commit_dash_n_after_git_dir_shell() -> None:
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git --git-dir=/tmp/g commit -n -m y"},
        },
    )
    assert proc.returncode != 0
    assert "Do not bypass Git hooks" in (proc.stderr + proc.stdout)


def test_allows_git_global_option_without_dash_n_shell() -> None:
    # A global option WITHOUT -n must stay allowed.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git -C /tmp commit -m x"},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_git_log_dash_n_shell() -> None:
    # -n on a non-commit git command is legitimate and must stay allowed.
    proc = run_guard(
        "claude",
        "PreToolUse",
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": "git log -n 5"},
        },
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


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


def test_run_command_guard_does_not_shred_a_later_segments_flags() -> None:
    """A git-commit segment's short-option bundling must not leak into a
    later shell segment.

    _scan_segment used to run over the WHOLE argv once any segment matched
    `git ... commit`, so `-fuzz` in a second, unrelated segment shredded into
    -f -u -z -z and could not match resource-exhaustion.json. The command is
    still blocked here, but by that policy's own pattern, not defeated by
    git commit's option grammar leaking across a shell separator.
    """
    proc = run_command_guard(
        ["git", "commit", "-m", "x", "&&", "go", "test", "-fuzz", "FuzzX"]
    )
    assert proc.returncode == 2, proc.stderr
    assert "resource-exhaustion.json" in proc.stderr


def test_run_command_guard_catches_dash_n_across_a_global_option() -> None:
    """-n must be caught even when a global git option sits before commit.

    The pattern-based check requires "git" and "commit" to be textually
    adjacent; a global option between them defeated it.
    The structural check added alongside this test does not require adjacency.
    """
    for argv in (
        ["git", "-C", "/tmp", "commit", "-n", "-m", "x"],
        ["git", "--no-pager", "commit", "-n", "-m", "x"],
    ):
        proc = run_command_guard(argv)
        assert proc.returncode == 2, f"{argv} -> {proc.stderr}"
        assert "agent-hook-bypass.json" in proc.stderr


def test_run_command_guard_does_not_misclassify_a_branch_named_commit() -> None:
    """A literal "commit" token that is not the subcommand must not count.

    _git_commit_index used to scan for the first "commit" token anywhere
    after "git", so `git branch commit -n` (a branch literally named commit)
    was misread as a git-commit invocation and its own -n falsely blocked.
    """
    proc = run_command_guard(["git", "branch", "commit", "-n"])
    assert proc.returncode == 0, proc.stderr


def test_run_command_guard_finds_commit_past_a_decoy_value() -> None:
    """A "commit" used as a -c VALUE is not the subcommand; the real one,
    one token later, still is."""
    proc = run_command_guard(["git", "-c", "commit", "commit", "-n"])
    assert proc.returncode == 2, proc.stderr


def test_run_command_guard_does_not_misclassify_log_dash_n() -> None:
    """`git log -n5` is not git commit; -n5 is a bundled count, not a bare
    bypass flag, and log is not commit regardless."""
    proc = run_command_guard(["git", "-C", "/tmp", "log", "-n5"])
    assert proc.returncode == 0, proc.stderr


def test_run_command_guard_allows_dash_capital_c_before_commit() -> None:
    """git's own -C <dir> (change directory) is not commit's reuse-message.

    Before this fix, the char-bundling parser ran over the whole argv and had
    no notion of where `commit` starts, so a global -C before commit could be
    consumed as if it were commit's -C (reuse-message) option.
    """
    proc = run_command_guard(["git", "-C", "/some/other/repo", "status"])
    assert proc.returncode == 0, proc.stderr


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


def test_n_reporting_matches_segment_shape_seeded() -> None:
    """Seeded deterministic property test over generated argv shapes.

    Fixed seed, ~2000 iterations, sub-second: the deterministic stand-in for a
    host fuzz gate on the argv parser (option_vector/_scan_segment/_flag_hits
    are pure functions over token lists). Asserts
      (a) no value token of -m/-F/-C/--message/--file/--reuse-message/
          --reedit-message ever surfaces in the option vector as a flag, and
      (b) -n is reported iff some single segment's option vector contains
          git, then commit, then -n in order (covers git global options
          between git and commit, and never flags non-commit -n such as
          `git log -n 5` or `grep -n`).
    """
    guard_path = ROOT / "scripts" / "agent_hook_guard.py"
    spec = importlib.util.spec_from_file_location("agent_hook_guard", str(guard_path))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)

    # phrase = (kind, tokens, value_tokens). Value literals are generated
    # uniquely per iteration so a surfaced value is unambiguous (flag-like
    # values -m -n, -Fn are covered by the dedicated value-is-data tests).
    git_phrases = [
        ("git", ["git"], []),
        ("git_global", ["git", "-C", "/tmp/x"], ["/tmp/x"]),
        ("git_global", ["git", "--git-dir=/tmp/g"], []),
        ("git_global", ["git", "-c", "user.email=x"], []),
        ("git_global", ["git", "--work-tree=/tmp/w"], []),
    ]
    commit_phrases = [("commit", ["commit"], [])]
    n_phrases = [
        ("n_flag", ["-n"], []),
        ("n_flag", ["-an"], []),
        ("n_flag", ["-na"], []),
    ]
    other_phrases = [
        ("no_verify", ["--no-verify"], []),
        ("log_n", ["log", "-n", "5"], []),
        ("grep_n", ["grep", "-n", "x"], []),
    ]
    pools = [git_phrases, commit_phrases, n_phrases, other_phrases]
    value_options = [
        "-m",
        "-F",
        "-C",
        "--message",
        "--file",
        "--reuse-message",
        "--reedit-message",
    ]

    def vec_has_git_commit_n(vec: list[str]) -> bool:
        try:
            i_git = vec.index("git")
            i_commit = vec.index("commit", i_git + 1)
            vec.index("-n", i_commit + 1)
        except ValueError:
            return False
        return True

    rng = random.Random(20260812)
    policy = mod.load_policy()
    for _ in range(2000):
        raw_segments: list[list[str]] = []
        value_tokens: list[str] = []
        for _seg in range(rng.randint(1, 3)):
            segment: list[str] = []
            for _phrase in range(rng.randint(2, 8)):
                if rng.random() < 0.3:
                    # Value-taking option with a unique value literal.
                    option = rng.choice(value_options)
                    literal = f"v{len(value_tokens)}"
                    value_tokens.append(literal)
                    segment.extend([option, literal])
                else:
                    kind, tokens, values = rng.choice(rng.choice(pools))
                    segment.extend(tokens)
                    value_tokens.extend(values)
            raw_segments.append(segment)

        argv: list[str] = []
        for index, segment in enumerate(raw_segments):
            if index:
                argv.append(rng.choice(["&&", ";"]))
            argv.extend(segment)

        # (a) no value token ever surfaces in the option vector as a flag.
        all_vec: list[str] = []
        for segment in raw_segments:
            all_vec.extend(mod.option_vector(segment))
        for value_token in value_tokens:
            assert value_token not in all_vec, (
                f"value token {value_token!r} surfaced as a flag in {argv!r}"
            )

        # (b) -n is reported iff some single segment's option vector contains
        # git, then commit, then -n in order.
        expected = any(
            vec_has_git_commit_n(mod.option_vector(segment)) for segment in raw_segments
        )
        payload = {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"argv": argv},
        }
        reasons = mod.bypass_reasons(payload, policy)
        assert ("blocked flag -n" in reasons) == expected, (argv, reasons)


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


def test_blocks_unbounded_go_fuzz() -> None:
    # go test -fuzz with default parallelism spawns one worker per core with
    # unbounded memory; the OOM kill takes down the whole desktop cgroup.
    proc = run_command_guard(["go", "test", "./internal/agent/", "-fuzz", "FuzzX"])
    assert proc.returncode != 0, proc.stderr + proc.stdout
    assert "resource-exhaustion.json" in (proc.stderr + proc.stdout)


def test_blocks_go_fuzz_with_fuzztime_only() -> None:
    # -fuzztime bounds duration, not workers, so it does not exempt a run.
    proc = run_command_guard(
        ["go", "test", "./...", "-fuzz", "FuzzX", "-fuzztime", "90s"]
    )
    assert proc.returncode != 0, proc.stderr + proc.stdout


def test_allows_capped_go_fuzz() -> None:
    proc = run_command_guard(
        ["go", "test", "./internal/agent/", "-fuzz", "FuzzX", "-parallel", "2", "-fuzztime", "60s"]
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_allows_seeded_go_test_smoke_run() -> None:
    proc = run_command_guard(["go", "test", "./internal/..."])
    assert proc.returncode == 0, proc.stderr + proc.stdout


def main() -> None:
    # Discovery by scan, not by a hand-maintained call list: eleven tests
    # defined after the __main__ guard silently never ran here. Every
    # test_ function is zero-argument in this runner; sorted order keeps
    # runs deterministic.
    tests = [
        v
        for k, v in sorted(globals().items())
        if k.startswith("test_") and callable(v)
    ]
    for t in tests:
        t()
    print(f"test_agent_hook_guard: ok ({len(tests)} tests)")


if __name__ == "__main__":
    main()
