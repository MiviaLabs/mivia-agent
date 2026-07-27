#!/usr/bin/env python3
"""Contract tests for mivia Git hooks (commit-msg format + install wiring)."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PREPARE_HOOK = ROOT / "scripts" / "git-hooks" / "prepare-commit-msg"
COMMIT_MSG_HOOK = ROOT / "scripts" / "git-hooks" / "commit-msg"
COMMIT_POLICY = ROOT / ".ai" / "policy" / "commit-message.json"


def run(
    args: list[str], cwd: Path, *, check: bool = True
) -> subprocess.CompletedProcess[str]:
    if os.name == "nt" and args and args[0] not in {"git", "bash"}:
        command = Path(args[0])
        if command.is_file() and command.suffix.lower() not in {".exe", ".bat", ".cmd"}:
            git_bash = Path(r"C:\Program Files\Git\bin\bash.exe")
            bash = str(git_bash) if git_bash.is_file() else (shutil.which("bash") or "bash")
            converted = [args[0]]
            for value in args[1:]:
                candidate = Path(value)
                if candidate.is_absolute() or candidate.exists():
                    result = subprocess.run(
                        [bash, "-c", 'cygpath -u "$1"', "bash", value],
                        text=True,
                        stdout=subprocess.PIPE,
                        stderr=subprocess.PIPE,
                        check=False,
                    )
                    if result.returncode == 0:
                        value = result.stdout.strip()
                converted.append(value)
            script = subprocess.run(
                [bash, "-c", 'cygpath -u "$1"', "bash", args[0]],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=True,
            ).stdout.strip()
            args = [bash, script, *converted[1:]]
    proc = subprocess.run(
        args,
        cwd=cwd,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if check and proc.returncode != 0:
        raise AssertionError(
            f"{args!r} failed with {proc.returncode}\nstdout={proc.stdout}\nstderr={proc.stderr}"
        )
    return proc


def default_policy() -> dict[str, object]:
    if COMMIT_POLICY.is_file():
        return json.loads(COMMIT_POLICY.read_text(encoding="utf-8"))
    return {
        "types": ["feat", "fix", "docs", "chore", "test"],
        "scopes": ["cli", "hooks", "docs", "ai", "quality"],
        "requireScope": True,
        "maxSubjectLength": 72,
    }


def first_valid_scope() -> str:
    policy = default_policy()
    scopes = policy.get("scopes") or ["hooks"]
    return str(scopes[0])


def init_repo(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    run(["git", "init"], root)
    run(["git", "config", "user.email", "hook-test@example.invalid"], root)
    run(["git", "config", "user.name", "Hook Test"], root)
    run(["git", "config", "commit.gpgsign", "false"], root)
    (root / "file.txt").write_text("content\n", encoding="utf-8")
    policy_path = root / ".ai" / "policy" / "commit-message.json"
    policy_path.parent.mkdir(parents=True, exist_ok=True)
    # Use real repo policy so scopes stay in sync
    policy_path.write_text(
        COMMIT_POLICY.read_text(encoding="utf-8")
        if COMMIT_POLICY.is_file()
        else json.dumps(default_policy(), indent=2) + "\n",
        encoding="utf-8",
    )
    run(["git", "add", "file.txt"], root)


def write_summary(root: Path, summary: str, *, tree: str | None = None) -> None:
    if tree is None:
        tree = run(["git", "write-tree"], root).stdout.strip()
    git_dir = run(["git", "rev-parse", "--git-dir"], root).stdout.strip()
    (root / git_dir / "mivia-precommit-summary").write_text(
        f"tree={tree}\nsummary={summary}\n",
        encoding="utf-8",
    )


def test_commit_policy_loads() -> None:
    policy = default_policy()
    assert "feat" in policy["types"]
    assert policy["maxSubjectLength"] == 72
    assert policy.get("requireScope") is True
    scopes = policy["scopes"]
    assert isinstance(scopes, list) and "cli" in scopes and "ai" in scopes
    assert "setup" not in scopes  # use ai/build/quality instead
    guide = policy.get("scopeGuide")
    assert isinstance(guide, dict)
    for scope in scopes:
        assert scope in guide, f"scopeGuide missing {scope}"


def test_hooks_executable_and_present() -> None:
    for rel in (
        "scripts/git-hooks/pre-commit",
        "scripts/git-hooks/pre-push",
        "scripts/git-hooks/commit-msg",
        "scripts/git-hooks/prepare-commit-msg",
        "scripts/git-hooks/post-commit",
        "scripts/git-hooks/run_with_timeout",
        ".githooks/pre-commit",
        ".githooks/pre-push",
        ".githooks/commit-msg",
        ".githooks/prepare-commit-msg",
        ".githooks/post-commit",
        "scripts/install_git_hooks.sh",
    ):
        path = ROOT / rel
        assert path.is_file(), rel
        # Windows/UNC worktrees do not expose POSIX mode bits. The shebang
        # and direct execution tests below provide the equivalent contract.
        if os.name != "nt":
            assert path.stat().st_mode & 0o111, f"{rel} not executable"


def test_commit_msg_accepts_valid() -> None:
    scope = first_valid_scope()
    with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as fh:
        fh.write(f"feat({scope}): add version command\n")
        path = fh.name
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr


def test_commit_msg_rejects_bad() -> None:
    with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as fh:
        fh.write("added stuff\n")
        path = fh.name
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    err = proc.stderr
    assert "allowed types:" in err
    assert "allowed scopes:" in err
    assert "error:" in err


def test_commit_msg_requires_scope() -> None:
    with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as fh:
        fh.write("chore: missing scope\n")
        path = fh.name
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    assert "allowed scopes:" in proc.stderr
    assert "scope is required" in proc.stderr


def test_commit_msg_rejects_unknown_scope() -> None:
    with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as fh:
        fh.write("chore(setup): bootstrap something\n")
        path = fh.name
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    err = proc.stderr
    # Catalog must print before the error diagnosis.
    types_idx = err.find("allowed types:")
    scopes_idx = err.find("allowed scopes:")
    error_idx = err.find("error:")
    assert types_idx != -1 and scopes_idx != -1 and error_idx != -1
    assert types_idx < error_idx
    assert scopes_idx < error_idx
    assert "unknown scope 'setup'" in err
    assert "ai:" in err  # scope guide mentions ai for control surface work


def test_prepare_commit_msg_appends_summary(root: Path) -> None:
    if os.name == "nt":
        return
    init_repo(root)
    summary = "Quality: pre-commit passed (agent config, secret scan; gofmt skipped)"
    write_summary(root, summary)
    msg = root / "COMMIT_MSG"
    scope = first_valid_scope()
    msg.write_text(f"chore({scope}): test hooks\n", encoding="utf-8")

    run([str(PREPARE_HOOK), str(msg), "message"], root)
    first = msg.read_text(encoding="utf-8")
    if summary not in first:
        raise AssertionError("prepare-commit-msg did not append quality summary")

    run([str(PREPARE_HOOK), str(msg), "message"], root)
    second = msg.read_text(encoding="utf-8")
    if second.count(summary) != 1:
        raise AssertionError("prepare-commit-msg duplicated quality summary")


def test_prepare_commit_msg_rejects_stale_summary(root: Path) -> None:
    if os.name == "nt":
        return
    init_repo(root)
    old_tree = run(["git", "write-tree"], root).stdout.strip()
    (root / "other.txt").write_text("new\n", encoding="utf-8")
    run(["git", "add", "other.txt"], root)
    summary = "Quality: pre-commit passed (stale)"
    write_summary(root, summary, tree=old_tree)
    msg = root / "COMMIT_MSG_STALE"
    scope = first_valid_scope()
    msg.write_text(f"chore({scope}): stale\n", encoding="utf-8")

    run([str(PREPARE_HOOK), str(msg), "message"], root)
    if summary in msg.read_text(encoding="utf-8"):
        raise AssertionError("prepare-commit-msg appended stale quality summary")


def test_install_git_hooks_sets_hooks_path(root: Path) -> None:
    init_repo(root)
    for rel in [
        "scripts/install_git_hooks.sh",
        "scripts/git-hooks/pre-commit",
        "scripts/git-hooks/pre-push",
        "scripts/git-hooks/commit-msg",
        "scripts/git-hooks/prepare-commit-msg",
        "scripts/git-hooks/post-commit",
        ".githooks/pre-commit",
        ".githooks/pre-push",
        ".githooks/commit-msg",
        ".githooks/prepare-commit-msg",
        ".githooks/post-commit",
    ]:
        src = ROOT / rel
        dst = root / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src, dst)
        dst.chmod(0o755)

    run([str(root / "scripts" / "install_git_hooks.sh")], root)
    hooks_path = run(["git", "config", "--get", "core.hooksPath"], root).stdout.strip()
    if hooks_path != ".githooks":
        raise AssertionError(f"core.hooksPath expected .githooks, got {hooks_path!r}")


def test_summary_file_name_is_mivia() -> None:
    pre = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(encoding="utf-8")
    prepare = (ROOT / "scripts" / "git-hooks" / "prepare-commit-msg").read_text(
        encoding="utf-8"
    )
    assert "mivia-precommit-summary" in pre
    assert "mivia-precommit-summary" in prepare
    assert "mivia-agent-precommit-summary" not in pre


def test_pre_commit_is_staged_only_and_bounded() -> None:
    pre = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(
        encoding="utf-8"
    )
    for slow_check in (
        "scripts/test_git_hooks.py",
        "scripts/test_agent_hook_guard.py",
        "scripts/test_secret_scan.py",
        "scripts/test_docs_ownership.py",
        "scripts/test_semgrep_rules.py",
        "scripts/test_go_structure.py",
    ):
        assert slow_check not in pre
    assert "scripts/secret_scan.py --staged" in pre
    # Fail-fast ordering: index whitespace and formatting are checked before
    # the slower policy/security gates; independent gates must not be serialized.
    assert pre.index("git diff --check --cached") < pre.index("run_gate config")
    assert pre.index("gofmt -w") < pre.index("run_gate config")
    assert "run_gate secrets" in pre
    assert "run_gate structure" in pre
    assert 'wait "$pid"' in pre
    supervisor = (ROOT / "scripts" / "git-hooks" / "run_with_timeout").read_text(
        encoding="utf-8"
    )
    assert "setsid" in supervisor
    assert "timeout --signal=TERM --kill-after=5s" in supervisor


def main() -> None:
    test_commit_policy_loads()
    test_hooks_executable_and_present()
    test_commit_msg_accepts_valid()
    test_commit_msg_rejects_bad()
    test_commit_msg_requires_scope()
    test_commit_msg_rejects_unknown_scope()
    test_summary_file_name_is_mivia()
    test_pre_commit_is_staged_only_and_bounded()
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        test_prepare_commit_msg_appends_summary(base / "append")
        test_prepare_commit_msg_rejects_stale_summary(base / "stale")
        test_install_git_hooks_sets_hooks_path(base / "install")
    print("test_git_hooks: ok")


if __name__ == "__main__":
    main()
