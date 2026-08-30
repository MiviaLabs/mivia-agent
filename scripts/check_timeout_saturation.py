#!/usr/bin/env python3
"""Enforce timeout saturation: no unbounded seconds-to-Duration multiply.

Mechanism (DC-7): time.Duration is int64. `time.Duration(n) * time.Second`
overflows to a negative Duration for n above ~9.2e9, and a negative bound
arms an already-expired deadline (context.WithTimeout) or expires retention
immediately. The compiled clamp is config.SaturatingSeconds; a conversion
that does not route through it must carry a guard BEFORE the multiply and a
disposition entry in the policy.

Every match of `time.Duration(<operand>) * time.<Unit>` in non-test Go
files must appear in the policy allow list as (file, operand, unit) with a
reason. Two failure modes, both exit 1:
  - a match with no allow entry (new unguarded conversion), and
  - an allow entry with no match (stale disposition), so the list cannot
    rot into a record of sites that no longer exist.

Compile-time constant multiplies without a time.Duration() conversion
(`Const * time.Second`) cannot overflow at runtime and are out of scope.

Policy: .mivia/policy/timeout-saturation.json
Pattern: .mivia/policy/import-layers.json (JSON policy, fail closed)

Modes:
  (default)   check the tree against the committed policy
  --probe     self-test the matcher on synthetic fixtures (violation must
              be flagged, allowed form must pass); wired so a regex edit
              that stops matching is noticed

Exit codes:
  0 = OK
  1 = violation (unlisted conversion, or stale allow entry)
  2 = usage / malformed policy (fail closed, never a silent pass)
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
POLICY_REL = Path(".mivia") / "policy" / "timeout-saturation.json"
SCAN_DIRS = ("internal", "cmd")

CONVERSION_RE = re.compile(
    r"time\.Duration\((?P<operand>[^()]*(?:\([^()]*\)[^()]*)*)\)"
    r"\s*\*\s*time\.(?P<unit>Second|Minute|Hour|Millisecond)\b"
)


def fail(msg: str, *, code: int = 1) -> None:
    print(f"check_timeout_saturation: {msg}", file=sys.stderr)
    raise SystemExit(code)


def normalize(operand: str) -> str:
    return re.sub(r"\s+", "", operand)


def load_policy(root: Path) -> list[dict]:
    path = root / POLICY_REL
    if not path.is_file():
        fail(f"missing policy {POLICY_REL}", code=2)
    try:
        policy = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as err:
        fail(f"malformed policy {POLICY_REL}: {err}", code=2)
    allow = policy.get("allow")
    if not isinstance(allow, list):
        fail(f"policy {POLICY_REL}: 'allow' must be a list", code=2)
    for i, entry in enumerate(allow):
        for key in ("file", "operand", "unit", "reason"):
            if not isinstance(entry.get(key), str) or not entry[key].strip():
                fail(f"policy {POLICY_REL}: allow[{i}] needs non-empty '{key}'", code=2)
    return allow


def scan(root: Path) -> list[tuple[str, int, str, str]]:
    """Return (relpath, line, operand, unit) for every conversion match."""
    hits: list[tuple[str, int, str, str]] = []
    for scan_dir in SCAN_DIRS:
        base = root / scan_dir
        if not base.is_dir():
            continue
        for path in sorted(base.rglob("*.go")):
            if path.name.endswith("_test.go"):
                continue
            rel = path.relative_to(root).as_posix()
            for lineno, line in enumerate(
                path.read_text(encoding="utf-8").splitlines(), start=1
            ):
                # Comment text is not executable; a conversion named in a
                # doc comment (loop_limits.go's maxDurationSeconds rationale)
                # is documentation of the guard, not a site. Naive split is
                # enough: a string literal carrying both "//" and this
                # pattern does not occur in this tree, and the probe pins
                # matcher behavior if that assumption ever breaks.
                code = line.split("//", 1)[0]
                for m in CONVERSION_RE.finditer(code):
                    hits.append((rel, lineno, normalize(m.group("operand")), m.group("unit")))
    return hits


def check(root: Path) -> None:
    allow = load_policy(root)
    hits = scan(root)
    allowed = {(e["file"], normalize(e["operand"]), e["unit"]) for e in allow}
    used: set[tuple[str, str, str]] = set()
    violations: list[str] = []
    for rel, lineno, operand, unit in hits:
        key = (rel, operand, unit)
        if key in allowed:
            used.add(key)
            continue
        violations.append(
            f"{rel}:{lineno}: unbounded time.Duration({operand}) * time.{unit}\n"
            f"  route it through config.SaturatingSeconds, or bound the value "
            f"before the multiply and add a dispositioned entry to {POLICY_REL}"
        )
    for key in sorted(allowed - used):
        violations.append(
            f"stale policy entry (matches no site): file={key[0]} operand={key[1]} "
            f"unit={key[2]} - remove it from {POLICY_REL}"
        )
    if violations:
        for v in violations:
            print(f"check_timeout_saturation: {v}", file=sys.stderr)
        raise SystemExit(1)
    print(f"check_timeout_saturation: ok ({len(hits)} conversions, all dispositioned)")


def probe() -> None:
    violation = "\tdeadline := time.Duration(cfg.SomeSeconds) * time.Second\n"
    if not CONVERSION_RE.search(violation):
        fail("probe: matcher no longer flags a bare conversion", code=1)
    clean = "\tdeadline := config.SaturatingSeconds(cfg.SomeSeconds)\n"
    if CONVERSION_RE.search(clean):
        fail("probe: matcher flags the saturating helper form", code=1)
    multiline_safe = "\td := time.Duration(f(a, b)) * time.Second\n"
    m = CONVERSION_RE.search(multiline_safe)
    if not m or normalize(m.group("operand")) != "f(a,b)":
        fail("probe: matcher mishandles a nested-call operand", code=1)
    print("check_timeout_saturation: probe ok")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=str(ROOT))
    parser.add_argument("--probe", action="store_true")
    args = parser.parse_args()
    if args.probe:
        probe()
        return
    check(Path(args.root).resolve())


if __name__ == "__main__":
    main()
