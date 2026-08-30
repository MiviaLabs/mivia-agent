#!/usr/bin/env python3
"""Contract tests for mivia Git hooks (commit-msg format + install wiring)."""

from __future__ import annotations

import inspect
import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PREPARE_HOOK = ROOT / "scripts" / "git-hooks" / "prepare-commit-msg"
COMMIT_MSG_HOOK = ROOT / "scripts" / "git-hooks" / "commit-msg"
COMMIT_POLICY = ROOT / ".mivia" / "policy" / "commit-message.json"
ISOLATED_GIT_ENV = ROOT / "scripts" / "git-hooks" / "run_without_git_env"


def run(
    args: list[str], cwd: Path, *, check: bool = True, env: dict[str, str] | None = None
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
        env=env,
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
    policy_path = root / ".mivia" / "policy" / "commit-message.json"
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
        "scripts/git-hooks/run_without_git_env",
        ".githooks/pre-commit",
        ".githooks/pre-push",
        ".githooks/commit-msg",
        ".githooks/prepare-commit-msg",
        ".githooks/post-commit",
        "scripts/install_git_hooks.sh",
        "scripts/git-hooks/strip_coauthor.py",
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


# A real test name: the hook now resolves every Regression name to a real
# func in a _test.go file, so a placeholder here would (correctly) fail.
FIX_TRAILERS = (
    "Regression: TestIsTransportStageTimeout\n"
    "Class: DC-2\n"
    "Sweep: searched every claim site, checked 3, found 0 further\n"
)


def write_msg(text: str) -> str:
    with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as fh:
        fh.write(text)
        return fh.name


def test_commit_msg_fix_requires_regression() -> None:
    scope = first_valid_scope()
    path = write_msg(f"fix({scope}): correct the widget\n")
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    assert "Regression:" in proc.stderr


def test_commit_msg_fix_accepts_regression_test() -> None:
    scope = first_valid_scope()
    path = write_msg(f"fix({scope}): correct the widget\n\n{FIX_TRAILERS}")
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr


def test_commit_msg_fix_accepts_regression_none() -> None:
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): typo in comment\n\n"
        "Regression: none (trivial)\nClass: none (comment only)\nSweep: none (comment only)\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr


def test_commit_msg_fix_requires_class_trailer() -> None:
    """A fix must name its recurring defect class or say none.

    Without this gate a fix closes one site of a known class and leaves the
    rest, which is how the repository produced 35-commit repeat chains.
    """
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestMyNewTest\nSweep: searched claim sites, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    assert "Class:" in proc.stderr
    assert "defect-taxonomy" in proc.stderr


def test_commit_msg_fix_requires_sweep_trailer() -> None:
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\nRegression: TestMyNewTest\nClass: DC-2\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    assert "Sweep:" in proc.stderr


def test_commit_msg_fix_rejects_empty_trailer_value() -> None:
    """A bare label with no value must not satisfy the gate."""
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestMyNewTest\nClass:\nSweep: searched claim sites, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    assert "Class:" in proc.stderr


def test_commit_msg_required_trailers_come_from_policy() -> None:
    """The gate is policy-driven, not hardcoded in the hook."""
    policy = json.loads(COMMIT_POLICY.read_text(encoding="utf-8"))
    assert policy["requiredTrailers"]["fix"] == ["Regression", "Class", "Sweep"]
    for label in policy["requiredTrailers"]["fix"]:
        assert policy["trailerHints"].get(label), label
        assert policy["trailerGuide"].get(label), label


def test_commit_msg_failure_lists_required_trailers() -> None:
    scope = first_valid_scope()
    path = write_msg(f"fix({scope}): correct the widget\n")
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode != 0
    assert "required trailers for fix: Regression, Class, Sweep" in proc.stderr


def test_commit_msg_feat_skips_regression() -> None:
    scope = first_valid_scope()
    with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as fh:
        fh.write(f"feat({scope}): add shiny new thing\n")
        path = fh.name
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr


CO_AUTHOR_CLAUDE = "Co-authored-by: Claude Opus 5 <noreply@anthropic.com>\n"
CO_AUTHOR_HOOK_TEST = "Co-authored-by: Hook Test <hook-test@example.invalid>\n"
CO_AUTHOR_MIVIA = "Co-authored-by: Mivia Agent <noreply@mivia.app>\n"


def test_commit_msg_strips_disallowed_coauthor() -> None:
    """A Co-authored-by line with a disallowed email is removed silently."""
    scope = first_valid_scope()
    path = write_msg(
        f"feat({scope}): drop claude trailer\n\n"
        "Body text.\n\n"
        f"{CO_AUTHOR_CLAUDE}"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr
    assert proc.stdout == ""  # silent on success
    text = Path(path).read_text(encoding="utf-8")
    assert "noreply@anthropic.com" not in text
    assert "Co-authored-by" not in text


def test_commit_msg_strips_hook_test_coauthor() -> None:
    scope = first_valid_scope()
    path = write_msg(
        f"feat({scope}): drop hook test trailer\n\n"
        "Body text.\n\n"
        f"{CO_AUTHOR_HOOK_TEST}"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr
    assert "hook-test@example.invalid" not in Path(path).read_text(encoding="utf-8")


def test_commit_msg_keeps_mivia_agent_coauthor() -> None:
    """noreply@mivia.app co-author lines are protected and stay."""
    scope = first_valid_scope()
    path = write_msg(
        f"feat({scope}): keep mivia trailer\n\n"
        "Body text.\n\n"
        f"{CO_AUTHOR_MIVIA}"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr
    assert "noreply@mivia.app" in Path(path).read_text(encoding="utf-8")


def test_commit_msg_keeps_other_trailers() -> None:
    """Only matching Co-authored-by lines are removed; other trailers survive."""
    scope = first_valid_scope()
    path = write_msg(
        f"feat({scope}): keep other trailers\n\n"
        f"{CO_AUTHOR_CLAUDE}"
        "Signed-off-by: Mac <mac@mivialabs.com>\n"
        "Refs: #123\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr
    text = Path(path).read_text(encoding="utf-8")
    assert "noreply@anthropic.com" not in text
    assert "Signed-off-by" in text
    assert "Refs: #123" in text


def test_strip_coauthor_stdin_mode() -> None:
    """stdin->stdout mode used by the history rewrite keeps non-matching lines."""
    proc = subprocess.run(
        ["python3", str(ROOT / "scripts" / "git-hooks" / "strip_coauthor.py"), "-"],
        input=(
            "feat(cli): sample subject\n\n"
            "Body text.\n\n"
            f"{CO_AUTHOR_CLAUDE}"
            f"{CO_AUTHOR_MIVIA}"
        ),
        text=True,
        capture_output=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr
    assert "noreply@anthropic.com" not in proc.stdout
    assert "Co-authored-by: Mivia Agent <noreply@mivia.app>" in proc.stdout


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
        "scripts/git-hooks/run_without_git_env",
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
    # Accept either relative ".githooks" or an absolute path ending in ".githooks".
    if hooks_path != ".githooks" and not hooks_path.endswith("/.githooks"):
        raise AssertionError(
            f"core.hooksPath expected '.githooks' (relative or absolute), got {hooks_path!r}"
        )
    auto_setup = run(["git", "config", "--get", "push.autoSetupRemote"], root).stdout.strip()
    assert auto_setup == "true", auto_setup


def test_install_sets_first_push_upstream_in_linked_worktree(root: Path) -> None:
    if os.name == "nt":
        return
    init_repo(root)
    run(["git", "commit", "-m", "chore(test): initial"], root)
    remote = root.parent / "origin.git"
    run(["git", "init", "--bare", str(remote)], root.parent)
    run(["git", "remote", "add", "origin", str(remote)], root)
    linked = root.parent / "linked"
    run(["git", "worktree", "add", "-b", "mivia/first-push", str(linked), "HEAD"], root)
    try:
        (linked / "worktree.txt").write_text("content\n", encoding="utf-8")
        run(["git", "add", "worktree.txt"], linked)
        run(["git", "commit", "-m", "chore(test): worktree"], linked)
        test_install_git_hooks_sets_hooks_path(root)
        empty_hooks = root / "empty-hooks"
        empty_hooks.mkdir()
        run(["git", "config", "core.hooksPath", str(empty_hooks)], root)
        run(["git", "push"], linked)
        upstream = run(
            ["git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"], linked
        ).stdout.strip()
        assert upstream == "origin/mivia/first-push", upstream
    finally:
        run(["git", "worktree", "remove", "--force", str(linked)], root)


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
    assert pre.index("update-index --cacheinfo") < pre.index("run_gate config")
    assert "run_gate secrets" in pre
    assert "run_gate structure" in pre
    assert 'wait "$pid"' in pre
    supervisor = (ROOT / "scripts" / "git-hooks" / "run_with_timeout").read_text(
        encoding="utf-8"
    )
    assert "setsid" in supervisor
    assert "timeout --signal=TERM --kill-after=5s" in supervisor


def extract_gofmt_block() -> str:
    """The gofmt/index block of pre-commit, from the staged-file mapfile
    through the outer if/fi that follows the gofmt-skipped summary, ready to
    run under a local require_cmd stub in a fixture repo."""
    source = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(encoding="utf-8")
    start = source.index("mapfile -d '' GO_FILES")
    marker = 'GOFMT_SUMMARY="gofmt skipped"'
    end = source.index(marker) + len(marker)
    fi = source.index("\nfi\n", end)
    return source[start : fi + len("\nfi\n")]


def test_pre_commit_does_not_overstage_partially_staged_go_file(root: Path) -> None:
    """Partially staged Go files must not have their unstaged hunks staged.

    Regression for pre-commit-readd-overstages-partially-staged-go-files: the
    old gofmt -w + git add -- over every staged Go file staged the whole
    working-tree file, silently overstaging unstaged hunks of partially staged
    files. The fixed block formats only the staged blob into the index entry
    (git hash-object -w + git update-index --cacheinfo) and leaves the working
    tree untouched, so the committed tree stays gofmt-clean while unstaged
    hunks remain unstaged.
    """
    if os.name == "nt":
        return
    if shutil.which("gofmt") is None:
        return
    init_repo(root)
    staged_body = 'package main\n\nfunc main() {\n    println("staged")\n}\n'
    unstaged_body = (
        'package main\n\nfunc main() {\n    println("staged")\n    println("unstaged")\n}\n'
    )
    go_file = root / "main.go"
    go_file.write_text(staged_body, encoding="utf-8")
    run(["git", "add", "main.go"], root)
    go_file.write_text(unstaged_body, encoding="utf-8")

    block = extract_gofmt_block()
    script = (
        "set -euo pipefail\n"
        'require_cmd() { command -v "$1" >/dev/null 2>&1 || return 1; }\n'
        + block
    )
    run(["bash", "-c", script], root, check=True)

    staged = run(["git", "show", ":main.go"], root).stdout
    assert '\tprintln("staged")' in staged, "index blob must be gofmt-formatted"
    assert '    println("staged")' not in staged
    cached = run(["git", "diff", "--cached"], root).stdout
    assert "unstaged" not in cached, "unstaged hunk must not be staged"
    unstaged = run(["git", "diff"], root).stdout
    assert "unstaged" in unstaged, "unstaged hunk must stay unstaged"
    assert go_file.read_bytes() == unstaged_body.encode("utf-8"), (
        "working-tree file must be byte-unchanged"
    )


def test_pre_commit_has_invariant_gate() -> None:
    pre = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(
        encoding="utf-8"
    )
    # --no-invariants bypass must be present
    assert "--no-invariants" in pre
    assert "invariant-bypass-log" in pre
    # Area-specific invariant test triggers must be present
    assert "internal/cli/" in pre
    assert "internal/tools/" in pre
    assert "internal/agent/" in pre
    assert "internal/chat/" in pre
    assert "internal/config/" in pre
    assert "internal/cliorchestrate/" in pre
    assert "TestTaskResultProducerConformance" in pre
    # Invariant summary must be in the quality line
    assert "INVARIANT_SUMMARY" in pre
    helper_call = 'run_verify() { "$ROOT/scripts/git-hooks/run_without_git_env" "$@"; }'
    assert helper_call in pre
    push = (ROOT / "scripts" / "git-hooks" / "pre-push").read_text(encoding="utf-8")
    assert helper_call in push


def test_pre_commit_full_script_runs_all_gates_with_staged_memory_db(root: Path) -> None:
    """Run the REAL scripts/git-hooks/pre-commit end to end in a linked git
    worktree of this repo, with a genuinely staged .mivia/memory.db change,
    confirming the memory.db auto-stage step coexists with every other gate
    (config, secrets, size, structure, semgrep, invariants) running together.

    A linked worktree (not a synthetic fixture) is required: the script's
    other gates live at $ROOT/scripts/... and must actually be present.
    """
    if os.name == "nt" or shutil.which("go") is None:
        return
    root.parent.mkdir(parents=True, exist_ok=True)
    # `git config --worktree` below needs this extension. A developer clone
    # that has run `git worktree add` before may already carry it, but a
    # fresh CI checkout never does - enable it explicitly so both start from
    # the same state.
    run(["git", "config", "extensions.worktreeConfig", "true"], ROOT)
    run(["git", "worktree", "add", "--detach", str(root)], ROOT)
    try:
        # Scope these to the WORKTREE only. This is a linked worktree of the
        # real repo (extensions.worktreeconfig=true), so a bare `git config`
        # would write to the shared common .git/config and permanently
        # override the operator's real identity for every worktree. --worktree
        # keeps the test identity in the worktree-local config file only.
        run(["git", "config", "--worktree", "user.email", "hook-test@example.invalid"], root)
        run(["git", "config", "--worktree", "user.name", "Hook Test"], root)
        run(["git", "config", "--worktree", "commit.gpgsign", "false"], root)

        mivia_db = root / ".mivia" / "memory.db"
        assert mivia_db.is_file(), "worktree must carry the real committed memory.db"

        # Real, valid mutation via the actual CLI (not a byte-level edit):
        # find a real existing id, then promote it - a genuine read-write
        # open plus (if not already core) a real row change.
        found = run(
            ["go", "run", "./cmd/mivia", "memory", "search", "the",
             "--workspace", str(root), "--limit", "1", "--json"],
            root,
        )
        results = json.loads(found.stdout)
        assert results, "expected at least one existing memory entry to promote"
        entry_id = results[0]["id"]
        run(
            ["go", "run", "./cmd/mivia", "memory", "promote", entry_id, "--workspace", str(root)],
            root,
        )
        run(["git", "add", ".mivia/memory.db"], root)

        result = run(
            [str(ROOT / "scripts" / "git-hooks" / "pre-commit")],
            root,
            check=False,
        )
        assert result.returncode == 0, (
            f"pre-commit failed:\nstdout={result.stdout}\nstderr={result.stderr}"
        )
        cached = run(["git", "diff", "--cached", "--name-only"], root).stdout
        assert ".mivia/memory.db" in cached, "memory.db auto-stage step did not run"
        combined = result.stdout + result.stderr
        assert "one or more parallel pre-commit gates failed" not in combined
    finally:
        run(["git", "worktree", "remove", "--force", str(root)], ROOT, check=False)


def test_pre_push_without_a_base_scans_all_tracked_files() -> None:
    """Run the pre-push base selection with no usable upstream or origin base."""
    if os.name == "nt":
        return
    source = (ROOT / "scripts" / "git-hooks" / "pre-push").read_text(encoding="utf-8")
    start = source.index("# Secret scan:")
    end = source.index("\nrun_verify python3 scripts/check_docs_ownership.py", start)
    selection = source[start:end]
    with tempfile.TemporaryDirectory() as tmp:
        fake_bin = Path(tmp) / "bin"
        fake_bin.mkdir()
        fake_git = fake_bin / "git"
        fake_git.write_text(
            """#!/usr/bin/env bash
set -euo pipefail
case \"$*\" in
  'rev-parse --abbrev-ref HEAD') printf 'mivia/no-upstream\\n' ;;
  'rev-list --first-parent -2 HEAD') printf 'first-parent\\n' ;;
  'merge-base HEAD first-parent') printf 'first-parent-base\\n'; exit 0 ;;
  *) exit 1 ;;
esac
""",
            encoding="utf-8",
        )
        fake_git.chmod(0o755)
        env = os.environ.copy()
        env["PATH"] = f"{fake_bin}{os.pathsep}{env['PATH']}"
        proc = run(
            [
                "bash",
                "-c",
                "set -euo pipefail\n"
                "run_verify() { printf '%s\\n' \"$@\"; }\n"
                + selection,
            ],
            ROOT,
            env=env,
        )
    assert proc.stdout.splitlines() == ["python3", "scripts/secret_scan.py", "--tracked"]
    assert "git rev-list --first-parent" not in selection


def test_isolated_git_env_preserves_main_and_worktree_indexes(root: Path) -> None:
    assert ISOLATED_GIT_ENV.is_file(), ISOLATED_GIT_ENV
    init_repo(root)
    run(["git", "commit", "-m", "chore(test): initial"], root)
    linked = root / "linked"
    run(["git", "worktree", "add", "-b", "mivia/hook-env", str(linked), "HEAD"], root)
    try:
        assert_nested_git_fixture_preserves_index(root)
        assert_nested_git_fixture_preserves_index(linked)
    finally:
        run(["git", "worktree", "remove", "--force", str(linked)], root)


def assert_nested_git_fixture_preserves_index(checkout: Path) -> None:
    before = run(["git", "write-tree"], checkout).stdout.strip()
    env = os.environ.copy()
    env["GIT_DIR"] = run(
        ["git", "rev-parse", "--path-format=absolute", "--git-dir"], checkout
    ).stdout.strip()
    env["GIT_WORK_TREE"] = str(checkout)
    env["GIT_INDEX_FILE"] = run(
        ["git", "rev-parse", "--path-format=absolute", "--git-path", "index"], checkout
    ).stdout.strip()
    env["GIT_COMMON_DIR"] = run(
        ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], checkout
    ).stdout.strip()
    fixture = """
        set -eu
        fixture=$(mktemp -d)
        trap 'rm -rf "$fixture"' EXIT
        cd "$fixture"
        git init -q
        git config user.email hook-test@example.invalid
        git config user.name 'Hook Test'
        printf 'test\\n' > README.md
        git add README.md
        git commit -qm fixture
    """
    run([str(ISOLATED_GIT_ENV), "bash", "-c", fixture], checkout, env=env)
    after = run(["git", "write-tree"], checkout).stdout.strip()
    assert after == before, f"outer index changed in {checkout}"


def extract_semgrep_detector() -> str:
    source = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(encoding="utf-8")
    start = source.index("semgrep_engine_failure() {")
    end = source.index("\n}\n", start) + 3
    return source[start:end]


def test_semgrep_engine_failure_detector() -> None:
    """Engine crashes (io_uring ENOMEM under low RLIMIT_MEMLOCK) are environment
    problems and must be detected distinctly from findings or config errors."""
    if os.name == "nt":
        return
    detector = extract_semgrep_detector()
    with tempfile.TemporaryDirectory() as tmp:
        helper = Path(tmp) / "detector.sh"
        helper.write_text(f"{detector}\nsemgrep_engine_failure \"$1\"\n", encoding="utf-8")
        crash = Path(tmp) / "crash.log"
        crash.write_text(
            "semgrep-core exited with 1!\n"
            "[ERROR] Error while running rules:\n"
            "  You are seeing this because the engine was killed.\n"
            "Uncaught exn in Core_scan.scan: Multiple exceptions:\n"
            "- Unix_error: Cannot allocate memory io_uring_queue_init \n",
            encoding="utf-8",
        )
        findings = Path(tmp) / "findings.log"
        findings.write_text(
            "Running 20 rules on 3 files\nfoo.go:1:3: possible bug\n1 finding\n",
            encoding="utf-8",
        )
        config_err = Path(tmp) / "config.log"
        config_err.write_text(
            "Configuration is invalid - found 1 configuration error(s), and 20 rule(s).\n",
            encoding="utf-8",
        )
        for log, expected in ((crash, 0), (findings, 1), (config_err, 1)):
            proc = run(["bash", str(helper), str(log)], ROOT, check=False)
            assert proc.returncode == expected, (log.name, proc.returncode, proc.stdout)


def test_pre_commit_semgrep_scans_staged_tree_directory_not_a_symlink_file_list() -> None:
    """pre-commit now scans the isolated staged-tree directory ($STAGED_ROOT)
    as a single target, not an explicit STAGED_FILES list built from `git
    diff --cached`. semgrep refuses to scan a symlink path passed directly
    (it errors with "Semgrep skips symbolic links... pass the target it
    points to directly" instead of producing findings), which used to fail
    the whole gate on a staged symlink (e.g. a skill alias under
    .claude/skills or .agents/skills pointing back into .mivia/skills) - a
    directory scan sidesteps that because semgrep's own walk skips symlinks
    on its own. Pin that the script scans STAGED_ROOT as a directory."""
    pre = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(encoding="utf-8")
    assert '"$STAGED_ROOT" >"$SEMGREP_LOG"' in pre, "semgrep must scan the STAGED_ROOT directory"
    assert "STAGED_FILES" not in pre, "the staged-file-list + symlink-filter approach should be gone"


def test_pre_commit_semgrep_engine_resilience() -> None:
    pre = (ROOT / "scripts" / "git-hooks" / "pre-commit").read_text(encoding="utf-8")
    assert "semgrep_engine_failure() {" in pre
    assert '--validate --config "$STAGED_ROOT/semgrep/agent-standards.yml" -j 1' in pre
    assert "engine unavailable" in pre
    assert "semgrep scan failed (findings or error)" in pre


def test_pre_push_semgrep_engine_resilience() -> None:
    push = (ROOT / "scripts" / "git-hooks" / "pre-push").read_text(encoding="utf-8")
    assert "semgrep_engine_failure() {" in push
    assert "--validate --config semgrep/agent-standards.yml -j 1" in push
    assert "engine unavailable" in push
    assert "semgrep scan failed (findings or error)" in push


def main() -> None:
    # Discovery by scan, not by a hand-maintained call list: seven trailer
    # CONTENT tests defined after the __main__ guard silently never ran
    # here. Zero-argument test_ functions run in sorted order; the
    # fixture-directory tests below take a Path and stay explicit.
    zero_arg = [
        v
        for k, v in sorted(globals().items())
        if k.startswith("test_")
        and callable(v)
        and inspect.signature(v).parameters == {}
    ]
    for t in zero_arg:
        t()
    # Fixture-directory tests take a Path, so the zero-arg scan cannot see
    # them: they stay an explicit registry, and the count below is derived
    # from it rather than written by hand (a literal would keep reporting
    # "7" while an eighth test sat unwired).
    fixture_tests = [
        (test_prepare_commit_msg_appends_summary, "append"),
        (test_prepare_commit_msg_rejects_stale_summary, "stale"),
        (test_install_git_hooks_sets_hooks_path, "install"),
        (test_isolated_git_env_preserves_main_and_worktree_indexes, "isolation"),
        (test_install_sets_first_push_upstream_in_linked_worktree, "first-push"),
        (test_pre_commit_does_not_overstage_partially_staged_go_file, "partial-staging"),
        (test_pre_commit_full_script_runs_all_gates_with_staged_memory_db, "full-script-worktree"),
    ]
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        for fixture_test, subdir in fixture_tests:
            fixture_test(base / subdir)
    print(
        f"test_git_hooks: ok ({len(zero_arg)} scanned + "
        f"{len(fixture_tests)} fixture tests)"
    )



# --- Trailer CONTENT validation -------------------------------------------
#
# Presence-only validation is how the three fix trailers decayed into
# decoration: `Sweep: x` passed, and a Regression naming a test nobody wrote
# passed too. These tests pin what each trailer must now actually say.


def test_commit_msg_rejects_regression_naming_a_missing_test() -> None:
    """A named test that does not exist is not a regression proof."""
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestThisNameWasNeverWritten\n"
        "Class: DC-2\n"
        "Sweep: searched every claim site, checked 3, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 1, proc.stdout
    assert "does not exist" in proc.stderr


def test_commit_msg_rejects_regression_naming_no_test() -> None:
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: covered by the existing suite\n"
        "Class: DC-2\n"
        "Sweep: searched every claim site, checked 3, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 1, proc.stdout
    assert "names no test" in proc.stderr


def test_commit_msg_rejects_free_prose_defect_class() -> None:
    """A class that is not in the taxonomy cannot be swept for or counted."""
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestIsTransportStageTimeout\n"
        "Class: some-new-sounding-mistake\n"
        "Sweep: searched every claim site, checked 3, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 1, proc.stdout
    assert "names no DC-n class" in proc.stderr


def test_commit_msg_rejects_unknown_defect_class_number() -> None:
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestIsTransportStageTimeout\n"
        "Class: DC-9999\n"
        "Sweep: searched every claim site, checked 3, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 1, proc.stdout
    assert "taxonomy does not define" in proc.stderr


def test_commit_msg_rejects_uncounted_sweep() -> None:
    """The sweep must report a count of other sites, not that a check happened."""
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestIsTransportStageTimeout\n"
        "Class: DC-2\n"
        "Sweep: checked, looks fine\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 1, proc.stdout
    assert "COUNT" in proc.stderr


def test_commit_msg_accepts_sweep_reporting_no_further_sites() -> None:
    """A genuine sweep that found nothing is a real result and must pass."""
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestIsTransportStageTimeout\n"
        "Class: DC-2\n"
        "Sweep: searched every runtime.Dispatcher claim site by mechanism; "
        "no other sites take a lease without a fence.\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr


def test_commit_msg_accepts_trailers_wrapped_across_lines() -> None:
    """Real trailers wrap; judging only the first line would misread them."""
    scope = first_valid_scope()
    path = write_msg(
        f"fix({scope}): correct the widget\n\n"
        "Regression: TestIsTransportStageTimeout,\n"
        "TestStdlibTimerErrorIdentities\n"
        "Class: DC-7\n"
        "Sweep: searched every errors.Is deadline decision site,\n"
        "checked 3, found 0 further\n"
    )
    proc = run([str(COMMIT_MSG_HOOK), path], ROOT, check=False)
    assert proc.returncode == 0, proc.stderr

if __name__ == "__main__":
    main()
