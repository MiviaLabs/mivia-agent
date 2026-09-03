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


def test_tokenizer_offsets_survive_multibyte_utf8_before_site() -> None:
    # Regression guard: site.start/site.end are BYTE offsets (go/scanner
    # positions). An em-dash (3 bytes in UTF-8, 1 Python str character)
    # earlier in the file used to desync run_mutant's str-based slice
    # (text = original.decode("utf-8"); text[site.start:site.end]) from
    # the real byte span, silently mutating the wrong text. Byte-slicing
    # the raw bytes directly - what run_mutant and sweep_diff's line-
    # number lookup do now - must land exactly on "continue" regardless.
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "snippet.go"
        source = (
            "package p\n\n"
            "// a note with an em-dash — right here, three of them — —\n"
            "func f() {\n"
            "\tfor {\n"
            "\t\tcontinue\n"
            "\t}\n"
            "}\n"
        )
        f.write_bytes(source.encode("utf-8"))
        sites = mt.sites_for_file(f)
        continue_sites = [s for s in sites if s.kind == "CONTINUE"]
        assert len(continue_sites) == 1, continue_sites
        site = continue_sites[0]

        raw = f.read_bytes()
        assert raw[site.start : site.end] == b"continue", (
            f"byte-offset slice landed on {raw[site.start:site.end]!r}, "
            "not b'continue' - offsets and byte slicing disagree"
        )

        # The bug this guards against: decoding to str first and slicing
        # with the same (byte) offsets lands somewhere else entirely once
        # multi-byte characters precede the site.
        decoded = raw.decode("utf-8")
        mis_sliced = decoded[site.start : site.end]
        assert mis_sliced != "continue", (
            "expected byte/str offset drift to be present in this fixture "
            "(str-slicing should land on the WRONG text) - if this now "
            "passes, the fixture no longer demonstrates the bug this test "
            "exists to catch a regression of"
        )

        # The actual gate: assert on the PRODUCTION functions, not on a
        # reimplementation of them here. Asserting only the tokenizer's
        # offsets (above) tests something that was never broken - reverting
        # either fixed line left this whole suite green until these two.
        assert cm.apply_mutation(raw, site) == raw.replace(b"continue", b"", 1), (
            "apply_mutation did not remove the 'continue' the site names; "
            "it sliced the wrong span"
        )
        assert cm.line_of_offset(raw, site.start) == 6, (
            "line_of_offset mis-located the site; sweep_diff matches this "
            "number against git's changed lines, so a wrong one silently "
            "drops the site from the sweep"
        )


def test_denylist_spans_survive_multibyte_utf8_before_snippet() -> None:
    # Companion to the above for the third offset consumer: denylisted_spans
    # resolves each entry's span, and is_denylisted compares it against
    # go/scanner BYTE offsets. Resolving in a decoded str shifts the span
    # left once multi-byte characters precede the snippet, so the entry
    # stops covering its own site (an audited equivalent mutant runs anyway)
    # or slides onto an earlier one (a real site never runs, reported clean).
    with tempfile.TemporaryDirectory() as td:
        pkg = Path(td)
        f = pkg / "snippet.go"
        source = (
            "package p\n\n"
            "// three em-dashes — — — before the denylisted comparison\n"
            "func f(a, b int) bool {\n"
            "\treturn a == b\n"
            "}\n"
        )
        f.write_bytes(source.encode("utf-8"))

        spans = cm.denylisted_spans(pkg, [{"file": "snippet.go", "snippet": "a == b"}])
        eq_sites = [s for s in mt.sites_for_file(f) if s.kind == "=="]
        assert len(eq_sites) == 1, eq_sites

        assert cm.is_denylisted(eq_sites[0], spans), (
            "the denylisted '==' was not recognised as denylisted - the span "
            "was resolved in code points and drifted off its own site"
        )


def test_restore_and_verify_raises_when_a_mutant_is_left_behind() -> None:
    # A mutant left on disk is a commit hazard, not a dirty file: the
    # pre-commit hook's gofmt step re-stages fully-staged Go files from the
    # WORKING TREE, so a mutant present then is committed silently while the
    # sweep still reports 100% (it restores before it reports). One shipped
    # that way. restore_and_verify must fail loudly rather than trust that
    # calling write_bytes worked - the restore path swallows write errors.
    with tempfile.TemporaryDirectory() as td:
        good = Path(td) / "good.go"
        good.write_bytes(b"package p\n\nfunc f(a, b int) bool { return a == b }\n")
        originals = {good: good.read_bytes()}

        # A file mutated after the snapshot is put back, and reports clean.
        good.write_bytes(b"package p\n\nfunc f(a, b int) bool { return a != b }\n")
        cm.restore_and_verify(originals)
        assert good.read_bytes() == originals[good]

        # A file that cannot be restored must raise, naming the file, rather
        # than returning as though the restore had worked.
        unwritable = Path(td) / "gone" / "missing.go"
        try:
            cm.restore_and_verify({unwritable: b"package p\n"})
        except cm.MutationError as err:
            assert "missing.go" in str(err), err
        else:
            raise AssertionError("restore_and_verify accepted a file it could not restore")


def test_verify_restored_reports_only_drifted_files() -> None:
    with tempfile.TemporaryDirectory() as td:
        clean = Path(td) / "clean.go"
        drifted = Path(td) / "drifted.go"
        clean.write_bytes(b"package p\n")
        drifted.write_bytes(b"package p\n")
        originals = {clean: clean.read_bytes(), drifted: drifted.read_bytes()}

        assert cm.verify_restored(originals) == []

        drifted.write_bytes(b"package q\n")
        messages = cm.verify_restored(originals)
        assert len(messages) == 1, messages
        assert "drifted.go" in messages[0]


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
    res, failed = cm.sweep_diff(["HEAD..HEAD"])
    assert not failed
    assert res["killed"] == 0
    assert res["survived"] == 0


def test_policy_mutation_discovery_and_floor() -> None:
    with tempfile.TemporaryDirectory() as td:
        pdir = Path(td)
        (pdir / "pkg_a.json").write_text(json.dumps({"floor": 0.85, "denylist": []}))
        assert cm.resolve_floor("pkg/a", None, pdir) == 85.0
        assert cm.resolve_floor("pkg/a", 90.0, pdir) == 90.0
        assert cm.resolve_floor("pkg/a", 0.70, pdir) == 70.0
        assert cm.load_denylist("pkg/a", pdir)["floor"] == 0.85


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
