#!/usr/bin/env python3
"""Contract test for check_subprocess_stdin.py.

A gate with no probe stops being a gate the moment its matcher drifts, and
nothing notices. These cases pin both directions: the shapes it must reject,
and the shapes it must NOT reject (a false positive on a correct call teaches
authors to work around the gate).

The last case is the behavioural half. The static gate matches a shape; this
one runs the real thing against a stdin that never reaches EOF and asserts it
still terminates, which is the property the shape is a proxy for.
"""

from __future__ import annotations

import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
GATE = REPO / "scripts" / "check_subprocess_stdin.py"

VIOLATIONS = [
    (
        "rg with no path and no stdin=",
        'import subprocess\nsubprocess.run(["rg", "-n", "x"], capture_output=True)\n',
    ),
    (
        "rg with a path but no stdin=",
        'import subprocess\nsubprocess.run(["rg", "-n", "x", "."], capture_output=True)\n',
    ),
    (
        "grep with no path",
        'import subprocess\nsubprocess.run(["grep", "-n", "x"], stdin=subprocess.DEVNULL)\n',
    ),
    (
        "the flag-value trap: last token is a flag's value, not a path",
        'import subprocess\nsubprocess.run(["rg", "-g", "*_test.go"], stdin=subprocess.DEVNULL)\n',
    ),
]

CLEAN = [
    (
        "explicit path and stdin=DEVNULL",
        'import subprocess\nsubprocess.run(["rg", "-n", "x", "."], stdin=subprocess.DEVNULL)\n',
    ),
    (
        "a tool that does not read stdin",
        'import subprocess\nsubprocess.run(["git", "status"], capture_output=True)\n',
    ),
    (
        "a non-literal command is not analysable and must not be guessed at",
        "import subprocess\ncmd = build()\nsubprocess.run(cmd, capture_output=True)\n",
    ),
]


def run_gate_over(tmp: Path) -> subprocess.CompletedProcess:
    """Run the gate with scripts/ pointed at a throwaway tree."""
    # The COPY in tmp, not the real one: the gate locates the tree it scans
    # from its own __file__, so running the real path would scan the real repo
    # and every fixture would silently pass.
    return subprocess.run(
        [sys.executable, str(tmp / "scripts" / "check_subprocess_stdin.py")],
        cwd=tmp,
        capture_output=True,
        text=True,
        stdin=subprocess.DEVNULL,
        timeout=60,
    )


def stage(tmp: Path, body: str) -> None:
    """Materialise a minimal repo shape the gate can scan."""
    (tmp / "scripts").mkdir(parents=True, exist_ok=True)
    (tmp / ".mivia" / "policy").mkdir(parents=True, exist_ok=True)
    (tmp / ".mivia" / "policy" / "subprocess-stdin.json").write_text(
        (REPO / ".mivia" / "policy" / "subprocess-stdin.json").read_text(encoding="utf-8"),
        encoding="utf-8",
    )
    (tmp / "scripts" / "check_subprocess_stdin.py").write_text(
        GATE.read_text(encoding="utf-8"), encoding="utf-8"
    )
    (tmp / "scripts" / "candidate.py").write_text(body, encoding="utf-8")


def check_shapes() -> int:
    failures = 0
    for name, body in VIOLATIONS:
        with tempfile.TemporaryDirectory() as d:
            tmp = Path(d)
            stage(tmp, body)
            res = run_gate_over(tmp)
            if res.returncode == 0:
                print(f"FAIL: gate accepted a violation: {name}\n{res.stdout}")
                failures += 1
    for name, body in CLEAN:
        with tempfile.TemporaryDirectory() as d:
            tmp = Path(d)
            stage(tmp, body)
            res = run_gate_over(tmp)
            if res.returncode != 0:
                print(f"FAIL: gate rejected a correct call: {name}\n{res.stdout}")
                failures += 1
    return failures


def check_real_scripts_terminate() -> int:
    """The behavioural half: the two scripts this class shipped in must finish.

    They are run with a stdin that stays OPEN and empty - the exact condition
    under which the defect blocks forever. A timeout here is the failure; the
    exit code is irrelevant, because this asserts liveness, not correctness.
    """
    failures = 0
    for name in ("validate_invariants.py", "invariant_coverage.py"):
        holder = subprocess.Popen(["sleep", "60"], stdout=subprocess.PIPE)
        try:
            subprocess.run(
                [sys.executable, str(REPO / "scripts" / name)],
                cwd=REPO,
                stdin=holder.stdout,
                capture_output=True,
                text=True,
                timeout=90,
            )
        except subprocess.TimeoutExpired:
            print(f"FAIL: scripts/{name} did not terminate with an open, empty stdin")
            failures += 1
        finally:
            holder.kill()
            holder.wait()
    return failures


def main() -> int:
    failures = check_shapes() + check_real_scripts_terminate()
    if failures:
        print(f"FAIL: {failures} contract failure(s)")
        return 1
    print(
        f"test_check_subprocess_stdin: ok ({len(VIOLATIONS)} violation shape(s), "
        f"{len(CLEAN)} clean shape(s), 2 script(s) proven to terminate)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
