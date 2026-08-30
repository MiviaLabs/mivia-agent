#!/usr/bin/env python3
"""Contract tests for check_timeout_saturation.py.

Each case builds a throwaway root (policy + Go files) and asserts the
gate's exit code and message, so a regression in the matcher, the
stale-entry arm, or the fail-closed policy handling is caught by
`make verify` without touching the real tree.
"""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "check_timeout_saturation.py"


def run_gate(root: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(GATE), "--root", str(root)],
        capture_output=True,
        text=True,
    )


def make_root(policy: dict | str, files: dict[str, str]) -> Path:
    root = Path(tempfile.mkdtemp(prefix="timeout-gate-"))
    policy_path = root / ".mivia" / "policy" / "timeout-saturation.json"
    policy_path.parent.mkdir(parents=True)
    if isinstance(policy, str):
        policy_path.write_text(policy, encoding="utf-8")
    else:
        policy_path.write_text(json.dumps(policy), encoding="utf-8")
    for rel, content in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
    return root


def expect(case: str, proc: subprocess.CompletedProcess, code: int, needle: str = "") -> None:
    if proc.returncode != code:
        raise SystemExit(
            f"test_check_timeout_saturation: {case}: exit {proc.returncode}, want {code}\n"
            f"stdout: {proc.stdout}\nstderr: {proc.stderr}"
        )
    if needle and needle not in (proc.stdout + proc.stderr):
        raise SystemExit(
            f"test_check_timeout_saturation: {case}: missing {needle!r} in output\n"
            f"stdout: {proc.stdout}\nstderr: {proc.stderr}"
        )


def main() -> None:
    entry = {
        "file": "internal/a/a.go",
        "operand": "cfg.Guarded",
        "unit": "Second",
        "reason": "clamped upstream",
    }
    allowed_go = "package a\n\nfunc f() { _ = time.Duration(cfg.Guarded) * time.Second }\n"
    violating_go = "package a\n\nfunc f() { _ = time.Duration(cfg.Rogue) * time.Second }\n"
    comment_go = "package a\n\n// time.Duration(secs)*time.Second wraps above the cap.\n"
    test_go = "package a\n\nfunc f() { _ = time.Duration(huge) * time.Second }\n"

    ok = make_root({"allow": [entry]}, {"internal/a/a.go": allowed_go})
    expect("allowed site passes", run_gate(ok), 0, "all dispositioned")

    bad = make_root({"allow": [entry]}, {"internal/a/a.go": allowed_go + violating_go})
    expect("new conversion fails", run_gate(bad), 1, "cfg.Rogue")

    stale = make_root({"allow": [entry]}, {"internal/a/a.go": "package a\n"})
    expect("stale entry fails", run_gate(stale), 1, "stale policy entry")

    comments = make_root({"allow": []}, {"internal/a/a.go": comment_go})
    expect("comment text ignored", run_gate(comments), 0)

    tests_skipped = make_root({"allow": []}, {"internal/a/a_test.go": test_go})
    expect("_test.go skipped", run_gate(tests_skipped), 0)

    malformed = make_root("{not json", {"internal/a/a.go": "package a\n"})
    expect("malformed policy fails closed", run_gate(malformed), 2, "malformed policy")

    probe = subprocess.run(
        [sys.executable, str(GATE), "--probe"], capture_output=True, text=True
    )
    expect("probe passes", probe, 0, "probe ok")

    print("test_check_timeout_saturation: ok (7 cases)")


if __name__ == "__main__":
    main()
