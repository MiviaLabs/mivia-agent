#!/usr/bin/env python3
"""Self-tests for scripts/check_mutation.py and scripts/mutation_tokenize.py.

Exercises the tokenizer/mutator against small in-memory Go snippets
(no `go build`/`go test`, so this stays fast) and runs the script's
own `--probe` self-test mode as a subprocess. Mirrors the manual
test-runner convention used by scripts/test_go_structure.py and
scripts/test_check_provider_docs.py: no pytest dependency, run
directly with `python3 scripts/test_check_mutation.py`.
"""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

import check_mutation as cm
import mutation_tokenize as mt

ROOT = Path(__file__).resolve().parents[1]
CHECK = ROOT / "scripts" / "check_mutation.py"


def test_sites_from_tokens_covers_every_operator_pair() -> None:
    tokens = [
        {"pos": 0, "end": 2, "tok": "=="},
        {"pos": 3, "end": 5, "tok": "!="},
        {"pos": 6, "end": 7, "tok": "<"},
        {"pos": 8, "end": 10, "tok": "<="},
        {"pos": 11, "end": 13, "tok": "&&"},
        {"pos": 14, "end": 16, "tok": "||"},
    ]
    data = b"== != < <= && ||"
    sites = mt.sites_from_tokens(tokens, data, Path("snippet.go"))
    got = {(s.old, s.new) for s in sites}
    assert got == {
        ("==", "!="),
        ("!=", "=="),
        ("<", "<="),
        ("<=", "<"),
        ("&&", "||"),
        ("||", "&&"),
    }, got


def test_sites_from_tokens_drops_bang_before_equals() -> None:
    # A real go/scanner run never emits a "!" token immediately before
    # "=" (it merges into one "!=" NEQ token); this guard is defensive
    # and is exercised directly with a synthetic token list.
    tokens = [{"pos": 0, "end": 1, "tok": "!"}]
    guarded = mt.sites_from_tokens(tokens, b"!=", Path("snippet.go"))
    assert guarded == [], "a '!' immediately before '=' must not become a site"
    unguarded = mt.sites_from_tokens(tokens, b"! ", Path("snippet.go"))
    assert len(unguarded) == 1, "a bare '!' must still become a NOT site"


def test_sites_from_tokens_covers_continue() -> None:
    tokens = [{"pos": 0, "end": 8, "tok": "continue"}]
    sites = mt.sites_from_tokens(tokens, b"continue", Path("snippet.go"))
    assert len(sites) == 1 and sites[0].kind == "CONTINUE"


def test_tokenizer_skips_comments_and_strings() -> None:
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "snippet.go"
        f.write_text(
            'package p\n\n'
            '// a == b must never become a site\n'
            'const s = "a == b"\n\n'
            'func f(a, b int) bool {\n'
            '\treturn a == b\n'
            '}\n'
        )
        sites = mt.sites_for_file(f)
        eq_sites = [s for s in sites if s.kind == "=="]
        assert len(eq_sites) == 1, f"want exactly 1 real '==' site, got {len(eq_sites)}"


def test_classify_matrix() -> None:
    assert cm.classify(False, "pass") == cm.DISCARDED
    assert cm.classify(True, "timeout") == cm.KILLED
    assert cm.classify(True, "fail") == cm.KILLED
    assert cm.classify(True, "pass") == cm.SURVIVED


def test_denylist_path_maps_nested_packages_to_flat_filenames() -> None:
    p = cm.denylist_path("internal/cli", Path("/tmp/does-not-matter"))
    assert p.name == "internal_cli.json", p


def test_denylisted_spans_fails_loudly_on_zero_or_ambiguous_matches() -> None:
    with tempfile.TemporaryDirectory() as td:
        pkg_dir = Path(td)
        (pkg_dir / "f.go").write_text("package p\nfunc f(a, b int) bool { return a == b }\n")

        try:
            cm.denylisted_spans(pkg_dir, [{"file": "f.go", "snippet": "no such text"}])
            raise AssertionError("a missing snippet must raise MutationError")
        except mt.MutationError:
            pass

        (pkg_dir / "g.go").write_text(
            "package p\nfunc g(a, b int) bool {\n\t_ = a == b\n\t_ = a == b\n\treturn true\n}\n"
        )
        try:
            cm.denylisted_spans(pkg_dir, [{"file": "g.go", "snippet": "a == b"}])
            raise AssertionError("an ambiguous snippet must raise MutationError")
        except mt.MutationError:
            pass

        spans = cm.denylisted_spans(pkg_dir, [{"file": "f.go", "snippet": "a == b"}])
        assert spans, "an unambiguous snippet must resolve to a span"


def test_recognized_packages_includes_core_packages() -> None:
    known = cm.recognized_packages()
    for pkg in cm.CORE_PACKAGES:
        assert pkg in known, f"{pkg} missing from recognized_packages(): {sorted(known)[:10]}..."


def run(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(CHECK), *args],
        cwd=ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
    )


def test_probe_mode_passes() -> None:
    proc = run(["--probe"])
    assert proc.returncode == 0, proc.stdout


def test_cli_requires_pkg_or_probe() -> None:
    proc = run([])
    assert proc.returncode == 2, proc.stdout


def test_cli_rejects_unrecognized_package() -> None:
    proc = run(["--pkg", "internal/does-not-exist"])
    assert proc.returncode == 2, proc.stdout
    assert "unrecognized package" in proc.stdout, proc.stdout


def test_sweep_diff_empty_scope() -> None:
    res, failed = cm.sweep_diff(["HEAD"])
    assert not failed
    assert res["killed"] == 0
    assert res["survived"] == 0



def main() -> int:
    tests = [
        v
        for k, v in sorted(globals().items())
        if k.startswith("test_") and callable(v)
    ]
    failures = 0
    for t in tests:
        try:
            t()
        except AssertionError as exc:
            failures += 1
            print(f"FAIL {t.__name__}: {exc}")
        except Exception as exc:  # noqa: BLE001 - test harness reports all
            failures += 1
            print(f"ERROR {t.__name__}: {exc!r}")
    if failures:
        print(f"{failures} test(s) failed")
        return 1
    print(f"test_check_mutation: ok ({len(tests)} tests)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
