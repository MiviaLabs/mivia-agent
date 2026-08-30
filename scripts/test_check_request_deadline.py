#!/usr/bin/env python3
"""Contract tests for check_request_deadline.py.

Each case builds a throwaway root (policy + Go files) and asserts the
gate's exit code and message, so a regression in the literal scanner, the
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
GATE = ROOT / "scripts" / "check_request_deadline.py"


def run_gate(root: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(GATE), "--root", str(root)],
        capture_output=True,
        text=True,
    )


def make_root(policy: dict | str, files: dict[str, str]) -> Path:
    root = Path(tempfile.mkdtemp(prefix="request-deadline-gate-"))
    policy_path = root / ".mivia" / "policy" / "request-deadline.json"
    policy_path.parent.mkdir(parents=True)
    policy_path.write_text(
        policy if isinstance(policy, str) else json.dumps(policy), encoding="utf-8"
    )
    for rel, content in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
    return root


def expect(case: str, proc: subprocess.CompletedProcess, code: int, needle: str = "") -> None:
    if proc.returncode != code:
        raise SystemExit(
            f"test_check_request_deadline: {case}: exit {proc.returncode}, want {code}\n"
            f"stdout: {proc.stdout}\nstderr: {proc.stderr}"
        )
    if needle and needle not in (proc.stdout + proc.stderr):
        raise SystemExit(
            f"test_check_request_deadline: {case}: missing {needle!r} in output\n"
            f"stdout: {proc.stdout}\nstderr: {proc.stderr}"
        )


def main() -> None:
    bounded = "package a\n\nfunc f() { _ = provider.Request{Model: m, Timeout: d} }\n"
    unbounded = "package a\n\nfunc g() { _ = provider.Request{Model: m} }\n"
    multiline = "package a\n\nfunc f() {\n\t_ = provider.Request{\n\t\tModel: m,\n\t\tTimeout: d,\n\t}\n}\n"
    nested_only = "package a\n\nfunc g() { _ = provider.Request{Opts: Inner{Timeout: d}} }\n"

    ok = make_root({"allow": []}, {"internal/a/a.go": bounded})
    expect("bounded literal passes", run_gate(ok), 0, "1 set Timeout")

    ml = make_root({"allow": []}, {"internal/a/a.go": multiline})
    expect("multi-line bounded literal passes", run_gate(ml), 0)

    bad = make_root({"allow": []}, {"internal/a/a.go": unbounded})
    expect("unbounded literal fails", run_gate(bad), 1, "sets no Timeout")

    nested = make_root({"allow": []}, {"internal/a/a.go": nested_only})
    expect("nested Timeout does not count", run_gate(nested), 1, "sets no Timeout")

    dispositioned = make_root(
        {"allow": [{"file": "internal/a/a.go", "reason": "ctx armed by caller"}]},
        {"internal/a/a.go": unbounded},
    )
    expect("dispositioned literal passes", run_gate(dispositioned), 0, "1 dispositioned")

    stale = make_root(
        {"allow": [{"file": "internal/a/a.go", "reason": "ctx armed by caller"}]},
        {"internal/a/a.go": bounded},
    )
    expect("stale entry fails", run_gate(stale), 1, "stale policy entry")

    tests_skipped = make_root({"allow": []}, {"internal/a/a_test.go": unbounded})
    expect("_test.go skipped", run_gate(tests_skipped), 0)

    malformed = make_root("{not json", {"internal/a/a.go": bounded})
    expect("malformed policy fails closed", run_gate(malformed), 2, "malformed policy")

    probe = subprocess.run(
        [sys.executable, str(GATE), "--probe"], capture_output=True, text=True
    )
    expect("probe passes", probe, 0, "probe ok")

    print("test_check_request_deadline: ok (9 cases)")


if __name__ == "__main__":
    main()
