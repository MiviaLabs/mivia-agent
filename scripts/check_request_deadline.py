#!/usr/bin/env python3
"""Enforce a deadline on every provider request construction site.

Mechanism: a `provider.Request` built without `Timeout:` is bounded only by
whatever deadline the ambient context happens to carry. When that context
carries none, the request runs until the transport wall - minutes of a
silent, unattributable stall. Plain chat turns shipped exactly that way
until commit e2c6dc27: the agent path set the field, the two plain paths
did not, and nothing noticed.

The class is a CALL-SITE omission, which is why this is a script gate and
not another test. `internal/provider/completer_conformance_test.go`
already pins that every Completer implementation honors `req.Timeout`
(TestCompleterConformance_SpentRequestBudgetIsNotTransient), but a
conformance suite tests implementations - it never executes these
construction sites, so a sibling send path that forgets the field stays
invisible to it.

Every `provider.Request{...}` literal in non-test Go must set `Timeout:`
inside its own braces, or name its file in the policy allow list with the
bound that applies instead (typically a context deadline armed by the
caller). Two failure modes, both exit 1:
  - a literal with neither `Timeout:` nor an allow entry, and
  - an allow entry naming a file with no such literal (stale disposition),
    so the list cannot rot.

Policy: .mivia/policy/request-deadline.json
Pattern: .mivia/policy/timeout-saturation.json (JSON policy, fail closed)

Modes:
  (default)   check the tree against the committed policy
  --probe     self-test the literal scanner on synthetic fixtures

Exit codes:
  0 = OK
  1 = violation (undeclared literal, or stale allow entry)
  2 = usage / malformed policy error (fail closed, never a silent pass)
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
POLICY_REL = Path(".mivia") / "policy" / "request-deadline.json"
SCAN_DIRS = ("internal", "cmd")
LITERAL = "provider.Request{"
TIMEOUT_FIELD = re.compile(r"\bTimeout\s*:")


def fail(msg: str, *, code: int = 1) -> None:
    print(f"check_request_deadline: {msg}", file=sys.stderr)
    raise SystemExit(code)


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
        for key in ("file", "reason"):
            if not isinstance(entry.get(key), str) or not entry[key].strip():
                fail(f"policy {POLICY_REL}: allow[{i}] needs non-empty '{key}'", code=2)
    return allow


def own_fields(body: str) -> str:
    """Return only the literal's OWN field text, with nested braces removed.

    A `Timeout:` inside a nested composite (`Opts: Inner{Timeout: d}`) is
    that struct's field, not this request's, and counting it would pass an
    unbounded request. Everything at brace depth above one is dropped, so
    the caller's regex sees this literal's own fields alone.
    """
    kept: list[str] = []
    depth = 0
    for ch in body:
        if ch == "{":
            depth += 1
            continue
        if ch == "}":
            depth -= 1
            continue
        if depth == 1:
            kept.append(ch)
    return "".join(kept)


def literal_bodies(text: str) -> list[tuple[int, str]]:
    """Return (line number, body) for each provider.Request{...} literal.

    The body spans from the opening brace to its matching close, so a
    multi-line literal is judged whole. Strings and comments are not
    parsed: no Go source in this tree carries an unbalanced brace inside
    either within such a literal, and the probe pins the scanner.
    """
    found: list[tuple[int, str]] = []
    start = 0
    while True:
        idx = text.find(LITERAL, start)
        if idx < 0:
            return found
        open_brace = idx + len(LITERAL) - 1
        depth = 0
        end = len(text)
        for pos in range(open_brace, len(text)):
            ch = text[pos]
            if ch == "{":
                depth += 1
            elif ch == "}":
                depth -= 1
                if depth == 0:
                    end = pos
                    break
        found.append((text.count("\n", 0, idx) + 1, text[open_brace : end + 1]))
        start = end + 1


def scan(root: Path) -> list[tuple[str, int, bool]]:
    """Return (relpath, line, has_timeout) for every request literal."""
    hits: list[tuple[str, int, bool]] = []
    for scan_dir in SCAN_DIRS:
        base = root / scan_dir
        if not base.is_dir():
            continue
        for path in sorted(base.rglob("*.go")):
            if path.name.endswith("_test.go"):
                continue
            rel = path.relative_to(root).as_posix()
            text = path.read_text(encoding="utf-8")
            for lineno, body in literal_bodies(text):
                hits.append((rel, lineno, bool(TIMEOUT_FIELD.search(own_fields(body)))))
    return hits


def check(root: Path) -> None:
    allow = load_policy(root)
    allowed = {e["file"] for e in allow}
    hits = scan(root)
    used: set[str] = set()
    violations: list[str] = []
    for rel, lineno, has_timeout in hits:
        if has_timeout:
            continue
        if rel in allowed:
            used.add(rel)
            continue
        violations.append(
            f"{rel}:{lineno}: provider.Request literal sets no Timeout\n"
            f"  set Timeout on the request, or - when the caller arms a context "
            f"deadline instead - add a dispositioned entry to {POLICY_REL}"
        )
    for rel in sorted(allowed - used):
        violations.append(
            f"stale policy entry (no undeclared literal in this file): {rel} "
            f"- remove it from {POLICY_REL}"
        )
    if violations:
        for v in violations:
            print(f"check_request_deadline: {v}", file=sys.stderr)
        raise SystemExit(1)
    bounded = sum(1 for _, _, ok in hits if ok)
    print(
        f"check_request_deadline: ok ({len(hits)} request literals: "
        f"{bounded} set Timeout, {len(hits) - bounded} dispositioned)"
    )


def has_own_timeout(body: str) -> bool:
    return bool(TIMEOUT_FIELD.search(own_fields(body)))


def probe() -> None:
    single = "x := provider.Request{Model: m, Timeout: d}\n"
    bodies = literal_bodies(single)
    if len(bodies) != 1 or not has_own_timeout(bodies[0][1]):
        fail("probe: scanner missed a single-line Timeout field", code=1)

    missing = "x := provider.Request{Model: m}\n"
    bodies = literal_bodies(missing)
    if len(bodies) != 1 or has_own_timeout(bodies[0][1]):
        fail("probe: scanner reported a Timeout field that is not there", code=1)

    multiline = "x := provider.Request{\n\tModel: m,\n\tTimeout: d,\n}\n"
    bodies = literal_bodies(multiline)
    if len(bodies) != 1 or not has_own_timeout(bodies[0][1]):
        fail("probe: scanner mishandles a multi-line literal", code=1)

    # A Timeout on a NESTED struct is that struct's field, not the
    # request's: counting it would pass an unbounded request. The literal
    # after it must also be found, so the brace walk ends where it should.
    nested = "x := provider.Request{\n\tOpts: Inner{Timeout: d},\n}\ny := provider.Request{Model: m}\n"
    bodies = literal_bodies(nested)
    if len(bodies) != 2:
        fail("probe: scanner lost a literal after a nested struct", code=1)
    if has_own_timeout(bodies[0][1]):
        fail("probe: scanner counts a nested Timeout as the request's own", code=1)
    if has_own_timeout(bodies[1][1]):
        fail("probe: scanner leaked a nested Timeout into the next literal", code=1)

    empty = "return c.ChatStream(ctx, provider.Request{}, w)\n"
    bodies = literal_bodies(empty)
    if len(bodies) != 1 or has_own_timeout(bodies[0][1]):
        fail("probe: scanner mishandles an empty literal", code=1)
    print("check_request_deadline: probe ok")


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
