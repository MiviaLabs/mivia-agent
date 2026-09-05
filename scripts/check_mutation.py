#!/usr/bin/env python3
"""Gate/tool: mutation testing for one package at a time.

Applies text-level operator mutations to a package's tracked source,
runs that package's own test target per mutant, and reports the kill
rate against a per-package floor. Ported from mivia-ai-sdk's
scripts/check_mutation.py and adapted to this repo's nested package
layout (internal/<pkg> and cmd/<pkg>, instead of flat top-level
directories) and its `mivia` module path. Stdlib-only plus the go
tool: site-finding shells out to a small embedded go/scanner helper
program, run through `go run`, never a third-party mutation library.
Tokenizer glue lives in scripts/mutation_tokenize.py.

Supersedes the old scripts/mutation_coverage.py stub, which only
checked whether go-mutesting was installed and printed a static
readiness table; it never ran a mutation.
"""
from __future__ import annotations

import argparse
import atexit
import json
import re
import signal
import subprocess
import sys
import tempfile
from collections import Counter
from pathlib import Path

from mutation_tokenize import MutationError, sites_for_file, sites_from_tokens

# Python's default SIGTERM handling kills the process without running
# `finally` blocks, unlike SIGINT. A killed sweep would then leave a
# mutated file on disk. Re-raising SIGTERM as KeyboardInterrupt routes
# it through the same finally-guaranteed restore path as Ctrl-C.
signal.signal(signal.SIGTERM, signal.default_int_handler)

ROOT = Path(__file__).resolve().parent.parent
POLICY_MUTATION_DIR = ROOT / ".mivia" / "policy" / "mutation"
LEGACY_DENYLIST_DIR = ROOT / "scripts" / "mutation_denylist"
DENYLIST_DIR = POLICY_MUTATION_DIR if POLICY_MUTATION_DIR.exists() else LEGACY_DENYLIST_DIR
TEST_TIMEOUT_SECONDS = 60

# Default sweep targets: the packages backing the invariants listed in
# .mivia/invariants.md, the same set scripts/mutation_coverage.py used
# to report readiness for (minus internal/subagents, which --pkg can
# still name explicitly).
CORE_PACKAGES = [
    "internal/cli",
    "internal/agent",
    "internal/tools",
    "internal/chat",
    "internal/config",
    "internal/hooks",
]

KILLED = "killed"
SURVIVED = "survived"
DISCARDED = "discarded"


def denylist_path(pkg: str, denylist_dir: Path | None = None) -> Path:
    """denylist_path maps a nested package (internal/cli) onto a flat
    JSON filename (internal_cli.json): a package name containing "/"
    cannot be a filename directly."""
    if denylist_dir is not None:
        return denylist_dir / f"{pkg.replace('/', '_')}.json"
    primary = POLICY_MUTATION_DIR / f"{pkg.replace('/', '_')}.json"
    if primary.exists():
        return primary
    legacy = LEGACY_DENYLIST_DIR / f"{pkg.replace('/', '_')}.json"
    if legacy.exists():
        return legacy
    return primary


def load_denylist(pkg: str, denylist_dir: Path | None = None) -> dict:
    """load_denylist reads a package's denylist and floor, or empty defaults."""
    path = denylist_path(pkg, denylist_dir)
    if not path.exists():
        return {"denylist": [], "floor": None}
    data = json.loads(path.read_text())
    data.setdefault("denylist", [])
    data.setdefault("floor", None)
    return data


def denylisted_spans(pkg_dir: Path, denylist: list[dict]) -> dict:
    """denylisted_spans resolves each denylist entry to its one exact
    match span in its named file. Fails loudly on zero or on more
    than one match; a stale or ambiguous entry never rubber-stamps a
    site silently."""
    spans: dict[Path, list[tuple[int, int]]] = {}
    for entry in denylist:
        file_path = pkg_dir / entry["file"]
        # Resolve in BYTES, not in a decoded str: the spans returned here are
        # compared against Site.start/Site.end (is_denylisted), which are
        # go/scanner byte offsets. Indexing a decoded str would return a
        # code-point offset, so any multi-byte character earlier in the file
        # shifts the span left, out from under the site it is meant to cover -
        # and the entry then either stops denylisting its own site (an audited
        # equivalent mutant runs anyway and reports SURVIVED) or slides over a
        # DIFFERENT, earlier site, which is then never mutated at all: a
        # coverage hole the sweep reports as clean. This is DC-36, and its own
        # probe says to slice the raw bytes the position came from.
        raw = file_path.read_bytes()
        snippet = entry["snippet"].encode("utf-8")
        count = raw.count(snippet)
        if count == 0:
            raise MutationError(
                f"denylist entry for {entry['file']}: snippet no longer matches: {entry['snippet']!r}"
            )
        if count > 1:
            raise MutationError(
                f"denylist entry for {entry['file']}: snippet matches {count} sites, "
                f"widen it to match one: {entry['snippet']!r}"
            )
        start = raw.index(snippet)
        spans.setdefault(file_path, []).append((start, start + len(snippet)))
    return spans


def is_denylisted(site, spans: dict) -> bool:
    """is_denylisted reports whether site falls inside a denylisted span."""
    for start, end in spans.get(site.path, []):
        if site.start >= start and site.end <= end:
            return True
    return False


def is_build_tag_gated(path: Path) -> bool:
    """is_build_tag_gated reports a `//go:build` constraint on path.

    A tag-gated file never compiles into the default build the kit
    tests against, so mutating it would always "survive" without
    meaning anything. The kit excludes it.
    """
    for line in path.read_text().splitlines():
        stripped = line.strip()
        if stripped == "":
            continue
        return stripped.startswith("//go:build")
    return False


def source_files(pkg_dir: Path) -> list[Path]:
    """source_files lists a package's mutable files: tracked, non-test,
    and not gated behind a build tag."""
    return sorted(
        p
        for p in pkg_dir.glob("*.go")
        if not p.name.endswith("_test.go") and not is_build_tag_gated(p)
    )


def collect_sites(pkg: str, denylist_dir: Path = DENYLIST_DIR) -> list:
    """collect_sites returns pkg's deterministic mutant list: sorted by
    file, then by byte offset, with denylisted sites already removed."""
    pkg_dir = ROOT / pkg
    data = load_denylist(pkg, denylist_dir)
    spans = denylisted_spans(pkg_dir, data["denylist"])
    sites = []
    for path in source_files(pkg_dir):
        for site in sites_for_file(path):
            if not is_denylisted(site, spans):
                sites.append(site)
    sites.sort(key=lambda s: s.sort_key())
    return sites


def test_target(pkg_dir: Path, pkg: str) -> str:
    """test_target picks the external test directory when it exists
    (<pkg>_test, mirroring Go's external-test-package convention),
    else the package's own directory."""
    ext = pkg_dir / f"{pkg_dir.name}_test"
    if ext.is_dir() and any(ext.glob("*.go")):
        return f"./{pkg}/{pkg_dir.name}_test"
    return f"./{pkg}"


def classify(build_ok: bool, test_outcome: str) -> str:
    """classify maps one mutant's build result and test outcome to a
    verdict. test_outcome is "pass", "fail", or "timeout"."""
    if not build_ok:
        return DISCARDED
    if test_outcome in ("fail", "timeout"):
        return KILLED
    return SURVIVED


def apply_mutation(original: bytes, site) -> bytes:
    """apply_mutation returns original with site's span replaced by site.new.

    site.start/site.end are BYTE offsets (go/scanner positions, via
    token.FileSet.Offset - always byte-based). Slicing a decoded str with
    them is only correct while every preceding byte is single-byte ASCII:
    a multi-byte UTF-8 character earlier in the file (an em-dash, a curly
    quote, any non-ASCII comment text) shifts the str's character indices
    out from under the byte offsets, so the slice would remove or replace
    the wrong span - sometimes producing a build failure (misclassified
    "discarded"), sometimes a no-op-looking edit that leaves the real
    mutation site untouched (misclassified "survived" even though a
    correct test kills the real mutant). Slicing the raw bytes instead
    keeps the offsets valid regardless of file content. site.new is always
    plain ASCII ("", "&&", "||", "==", "!=", "<", "<="), so
    .encode("utf-8") is exact and lossless.

    Pure and byte-in/byte-out so a test can assert the fix directly,
    without spawning go build/go test - see DC-36's gate."""
    return original[: site.start] + site.new.encode("utf-8") + original[site.end :]


def line_of_offset(data: bytes, offset: int) -> int:
    """line_of_offset returns the 1-based line number of a BYTE offset.

    Counts newlines in the raw bytes for the same reason apply_mutation
    slices them: counting in a decoded str would mis-locate every site
    that follows a multi-byte character, and sweep_diff matches this line
    number against the set of lines git reports as changed - so a wrong
    number silently drops a real mutation site from the sweep, or pulls in
    one that is not in the diff at all.

    Pure so a test can assert the fix directly - see DC-36's gate."""
    return data.count(b"\n", 0, offset) + 1


# BUILD_FAILURE_MARKERS are the substrings `go test` itself prints in
# stdout/stderr when a mutant fails to compile: "[build failed]" for the
# package (or its test binary) failing to build, "[setup failed]" for a
# package that fails to even load (e.g. an import cycle). Verified against
# a real `go test` run on a deliberately broken file - confirmed neither
# marker appears on an ordinary failing-test run, only on the two build/
# load failure modes go test itself distinguishes that way.
BUILD_FAILURE_MARKERS = ("[build failed]", "[setup failed]")


def run_mutant(site, original: bytes, pkg: str, pkg_dir: str) -> str:
    """run_mutant applies one mutation, runs pkg's test target once, and
    restores the original bytes no matter how the run ends.

    A standalone `go build ./pkg` used to run before this test call. It was
    redundant: compiling the test binary already fails a broken mutant the
    same way, and `go test` marks that failure in its own output with
    "[build failed]" or "[setup failed]" (see BUILD_FAILURE_MARKERS) - the
    same discarded verdict a separate build step produced, for one fewer
    compiler invocation per mutant."""
    site.path.write_bytes(apply_mutation(original, site))
    try:
        target = test_target(Path(pkg_dir), pkg)
        try:
            test = subprocess.run(
                ["go", "test", target],
                cwd=ROOT,
                capture_output=True,
                text=True,
                timeout=TEST_TIMEOUT_SECONDS,
            )
            if test.returncode != 0 and any(
                marker in test.stdout or marker in test.stderr
                for marker in BUILD_FAILURE_MARKERS
            ):
                print(f"discarded (build failed): {site.path.name}:{site.start} {site.kind}")
                return classify(False, "pass")
            outcome = "pass" if test.returncode == 0 else "fail"
        except subprocess.TimeoutExpired:
            outcome = "timeout"
        return classify(True, outcome)
    finally:
        site.path.write_bytes(original)


# COVERAGE_BLOCK_RE matches one data line of a `go tool cover` profile:
# "<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStmt> <count>".
# The leading "mode: <mode>" line and any blank line never match and are
# skipped by the caller.
COVERAGE_BLOCK_RE = re.compile(
    r"^(?P<file>.+):(?P<start_line>\d+)\.\d+,(?P<end_line>\d+)\.\d+ \d+ (?P<count>\d+)$"
)


def module_path() -> str:
    """module_path reads the module line from the repo's own go.mod, so
    coverage_key can build the import-path form `go tool cover` profiles
    key their per-file blocks by, without hardcoding the module name."""
    for line in (ROOT / "go.mod").read_text().splitlines():
        if line.startswith("module "):
            return line.split(None, 1)[1].strip()
    raise MutationError("go.mod: no module line found")


def coverage_key(path: Path) -> str:
    """coverage_key returns path's coverage-profile identifier: its
    import path, matching the "<file>" field `go tool cover` profiles
    use (always the module path plus the path relative to the repo
    root, regardless of the OS path separator)."""
    return f"{module_path()}/{path.relative_to(ROOT).as_posix()}"


def parse_coverage_profile(text: str) -> dict[str, list[tuple[int, int, int]]]:
    """parse_coverage_profile maps each covered file (by coverage_key's
    import-path form) to its (startLine, endLine, count) blocks, from a
    `go tool cover` profile's text. Unrecognized lines (the "mode:"
    header, blanks) are skipped rather than raised on, since a profile's
    exact line set is not this kit's contract to enforce."""
    blocks: dict[str, list[tuple[int, int, int]]] = {}
    for line in text.splitlines():
        m = COVERAGE_BLOCK_RE.match(line)
        if not m:
            continue
        blocks.setdefault(m.group("file"), []).append(
            (int(m.group("start_line")), int(m.group("end_line")), int(m.group("count")))
        )
    return blocks


def line_definitely_uncovered(blocks: list[tuple[int, int, int]], line_no: int) -> bool:
    """line_definitely_uncovered reports whether every coverage block
    overlapping line_no ran zero times.

    Returns False - never skip - when no block overlaps the line at all:
    an unmatched line is a profile gap (a generated file, a coverage
    version mismatch, an off-by-one in a hand-rolled parse), not proof
    the line is dead. Only a line every overlapping block agrees on as
    zero-run is provably unreachable by every test in the target; a
    mutation there can never be killed, so running the real build+test
    for it is guaranteed to reproduce the same SURVIVED verdict this
    returns immediately. This is the sole use of the coverage profile:
    it can only skip a mutant whose outcome is already certain, never
    guess at one whose outcome depends on running the tests."""
    overlapping = [count for start, end, count in blocks if start <= line_no <= end]
    if not overlapping:
        return False
    return all(count == 0 for count in overlapping)


def compute_coverage_blocks(pkg: str, pkg_dir: Path) -> dict[str, list[tuple[int, int, int]]] | None:
    """compute_coverage_blocks runs pkg's own test target once, unmutated,
    with -coverpkg=./pkg so an external <pkg>_test target still attributes
    coverage to the package under test, and returns its parsed per-file
    blocks.

    Returns None on any failure to produce a usable profile (a nonzero
    exit, a missing or unreadable profile file, a timeout): every caller
    treats None as "skip nothing", so a coverage-run problem only costs
    the speedup, never a correctness risk - the sweep falls back to its
    original always-run-the-mutant behaviour."""
    target = test_target(pkg_dir, pkg)
    tmp = tempfile.NamedTemporaryFile(
        prefix="mutation-cov-", suffix=".out", delete=False
    )
    cov_path = Path(tmp.name)
    tmp.close()
    try:
        result = subprocess.run(
            ["go", "test", f"-coverpkg=./{pkg}", f"-coverprofile={cov_path}", target],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=TEST_TIMEOUT_SECONDS * 4,
        )
        if result.returncode != 0 or not cov_path.exists():
            return None
        return parse_coverage_profile(cov_path.read_text())
    except (subprocess.TimeoutExpired, OSError):
        return None
    finally:
        try:
            cov_path.unlink()
        except OSError:
            pass


def site_is_dead_code(
    site, line_no: int, coverage: dict[str, list[tuple[int, int, int]]] | None
) -> bool:
    """site_is_dead_code reports whether coverage proves line_no is never
    executed by the test target the coverage profile was built for, so
    running the real build+test for site is certain to report SURVIVED -
    see line_definitely_uncovered. coverage=None (no usable profile)
    always returns False: a caller must never skip on uncertainty."""
    if coverage is None:
        return False
    blocks = coverage.get(coverage_key(site.path))
    if blocks is None:
        return False
    return line_definitely_uncovered(blocks, line_no)


def verify_restored(originals: dict[Path, bytes]) -> list[str]:
    """verify_restored returns a message per file whose bytes on disk are
    not the pre-sweep snapshot, after restoration was supposed to have run.

    This exists because a mutant left on disk is not merely a stale file: a
    mutating sweep runs inside the pre-commit hook, and that hook's gofmt
    step does `gofmt -w <file>; git add -- <file>` for any fully-staged Go
    file. That re-stages whatever is in the WORKING TREE at that instant, so
    a mutant present then is staged and committed - silently, because the
    sweep restores before it reports and therefore still prints 100%.

    That is not hypothetical. A commit shipped shouldSkipCanceledTask with
    its nil guard inverted this way; the fix became dead code, every gate
    passed, and it was caught only because `git status` happened to show one
    stray modified file afterwards. The risk is highest with several sweeps
    running against one checkout at once, where another run can have a file
    mutated exactly when this one's hook stages it.

    Restoration is best-effort by design (the restore path swallows write
    errors so one unwritable file cannot mask a whole run's results), so
    "we called write_bytes" is not evidence the bytes are back. This checks."""
    drifted = []
    for path, original in sorted(originals.items()):
        try:
            if path.read_bytes() != original:
                drifted.append(f"{path}: on-disk bytes are not the pre-sweep original")
        except OSError as err:
            drifted.append(f"{path}: could not be read back to verify restoration: {err}")
    return drifted


def restore_and_verify(originals: dict[Path, bytes]) -> None:
    """restore_and_verify rewrites every snapshot and then proves it landed,
    raising MutationError naming the files if any did not. Callers run this
    on the way out of a sweep so a leftover mutant fails the run loudly
    instead of being left for a commit to pick up - see verify_restored."""
    for path, original in originals.items():
        try:
            path.write_bytes(original)
        except OSError:
            pass
    drifted = verify_restored(originals)
    if drifted:
        raise MutationError(
            "mutation sweep did not restore every file it mutated; a leftover "
            "mutant can be staged by the pre-commit hook's gofmt re-add and "
            "committed silently. Restore these from git before committing:\n  "
            + "\n  ".join(drifted)
        )


def sweep(pkg: str, sample: int = None, denylist_dir: Path = DENYLIST_DIR) -> dict:
    """sweep runs every mutant (or the first sample) for pkg and
    returns the kill counts and rate. Every mutated file's original
    bytes are restored, including on interrupt or crash, through the
    finally block below."""
    pkg_dir = ROOT / pkg
    sites = collect_sites(pkg, denylist_dir)
    if sample is not None:
        sites = sites[:sample]
    # Snapshot all target file bytes BEFORE any mutation is applied
    originals: dict[Path, bytes] = {}
    for site in sites:
        if site.path not in originals:
            originals[site.path] = site.path.read_bytes()

    def restore_all() -> None:
        for path, original in originals.items():
            try:
                path.write_bytes(original)
            except Exception:
                pass

    atexit.register(restore_all)
    coverage = compute_coverage_blocks(pkg, pkg_dir)
    killed = survived = discarded = 0
    try:
        for site in sites:
            line_no = line_of_offset(originals[site.path], site.start)
            if site_is_dead_code(site, line_no, coverage):
                survived += 1
                print(
                    f"SURVIVED (no coverage, test skipped): {site.path.name}:{site.start} "
                    f"{site.kind} {site.old!r} -> {site.new!r}"
                )
                continue
            outcome = run_mutant(site, originals[site.path], pkg, str(pkg_dir))
            if outcome == KILLED:
                killed += 1
            elif outcome == SURVIVED:
                survived += 1
                print(
                    f"SURVIVED: {site.path.name}:{site.start} "
                    f"{site.kind} {site.old!r} -> {site.new!r}"
                )
            else:
                discarded += 1
    finally:
        # Restore AND prove it landed: an unrestored mutant here is a commit
        # hazard, not just a dirty file. See verify_restored.
        try:
            restore_and_verify(originals)
        finally:
            try:
                atexit.unregister(restore_all)
            except Exception:
                pass
    total = killed + survived
    rate = 100.0 * killed / total if total else 100.0
    return {"killed": killed, "survived": survived, "discarded": discarded, "rate": rate}


def resolve_floor(pkg: str, cli_floor=None, denylist_dir: Path | None = None) -> float | None:
    """resolve_floor honors a CLI --floor for one run only; otherwise it
    reads the package's own stored floor, or None if it has none yet.
    Normalizes fractional floors (e.g. 0.75) to percentages (75.0)."""
    val = cli_floor if cli_floor is not None else load_denylist(pkg, denylist_dir).get("floor")
    if val is not None:
        try:
            f = float(val)
            if 0 < f <= 1.0:
                return f * 100.0
            return f
        except (ValueError, TypeError):
            return None
    return None


def recognized_packages() -> set:
    """recognized_packages lists nested package directories holding
    non-test .go files: internal/<name> and cmd/<name>, this repo's
    two package roots (see AGENTS.md "Layout")."""
    names = set()
    for parent_name in ("internal", "cmd"):
        parent = ROOT / parent_name
        if not parent.is_dir():
            continue
        for d in parent.iterdir():
            if not d.is_dir() or d.name.startswith("."):
                continue
            for f in d.glob("*.go"):
                if not f.name.endswith("_test.go"):
                    names.add(f"{parent_name}/{d.name}")
                    break
    return names


def _probe_planted_site(pkg_dir: Path) -> list[str]:
    """Check: the planted `==` site is generated, and the comment and
    string-literal occurrences of "==" are excluded."""
    planted = pkg_dir / "planted.go"
    planted.write_text(
        'package probepkg\n\n'
        '// a == b in a comment must never become a site\n'
        'const s = "a == b"\n\n'
        'func f(a, b int) bool {\n'
        '\tif a == b {\n'
        '\t\treturn true\n'
        '\t}\n'
        '\treturn false\n'
        '}\n'
    )
    eq_sites = [s for s in sites_for_file(planted) if s.kind == "=="]
    if len(eq_sites) != 1:
        return [f"planted site: want 1 '==' candidate, got {len(eq_sites)}"]
    return []


def _probe_build_tag_exclusion(pkg_dir: Path) -> list[str]:
    """Check: a `//go:build` gated file is excluded from source_files,
    even though it holds an obvious mutation candidate."""
    tagged = pkg_dir / "tagged.go"
    tagged.write_text(
        '//go:build sometag\n\n'
        'package probepkg\n\n'
        'func h(a, b int) bool {\n'
        '\treturn a == b\n'
        '}\n'
    )
    if tagged in source_files(pkg_dir):
        return ["build-tag exclusion: a //go:build file was not excluded"]
    return []


def _probe_denylist(pkg_dir: Path) -> list[str]:
    """Checks: a denylisted site is skipped once its snippet is loaded;
    a missing or ambiguous snippet fails loudly instead of silently
    matching zero or more than one site."""
    problems = []
    denylisted = pkg_dir / "denylisted.go"
    denylisted.write_text(
        'package probepkg\n\n'
        'func g(x, y int) bool {\n'
        '\treturn x == y\n'
        '}\n'
    )
    entries = [{"file": "denylisted.go", "snippet": "x == y"}]
    spans = denylisted_spans(pkg_dir, entries)
    remaining = [s for s in sites_for_file(denylisted) if not is_denylisted(s, spans)]
    if remaining:
        problems.append("denylisted site: still present after filtering")

    try:
        denylisted_spans(pkg_dir, [{"file": "denylisted.go", "snippet": "no such text"}])
        problems.append("denylist: missing snippet did not raise")
    except MutationError:
        pass

    ambiguous = pkg_dir / "ambiguous.go"
    ambiguous.write_text(
        'package probepkg\n\n'
        'func h(a, b int) bool {\n'
        '\t_ = a == b\n'
        '\t_ = a == b\n'
        '\treturn true\n'
        '}\n'
    )
    try:
        denylisted_spans(ambiguous.parent, [{"file": "ambiguous.go", "snippet": "a == b"}])
        problems.append("denylist: ambiguous snippet did not raise")
    except MutationError:
        pass
    return problems


def _probe_not_guard(pkg_dir: Path) -> list[str]:
    """Check: a `!` token immediately followed by `=` is never a
    candidate. Real go/scanner output never produces this adjacency
    (it merges `!=` into one token); this exercises the defensive
    guard itself with a synthetic token list."""
    problems = []
    placeholder = pkg_dir / "planted.go"
    synthetic = [{"pos": 0, "end": 1, "tok": "!"}]
    guarded = sites_from_tokens(synthetic, b"!=", placeholder)
    if guarded:
        problems.append("dropped-! guard: '!' immediately before '=' produced a site")
    unguarded = sites_from_tokens(synthetic, b"! ", placeholder)
    if not unguarded:
        problems.append("dropped-! guard: '!' not before '=' produced no site")
    return problems


def _probe_classify() -> list[str]:
    """Checks: a failed build is discarded; a timeout or a failing test
    counts as killed; a passing test counts as survived."""
    problems = []
    if classify(False, "pass") != DISCARDED:
        problems.append("classify: a failed build must discard the mutant")
    if classify(True, "timeout") != KILLED:
        problems.append("classify: a timed-out test run must count as killed")
    if classify(True, "fail") != KILLED:
        problems.append("classify: a failing test run must count as killed")
    if classify(True, "pass") != SURVIVED:
        problems.append("classify: a passing test run must count as survived")
    return problems


def _probe_test_target(tmp_path: Path) -> list[str]:
    """Check: test-directory selection picks a package's own
    <pkg>_test directory when present, else the package itself."""
    problems = []
    with_ext = tmp_path / "internal" / "withext"
    (with_ext / "withext_test").mkdir(parents=True)
    (with_ext / "withext_test" / "case_test.go").write_text("package withext_test\n")
    got = test_target(with_ext, "internal/withext")
    if got != "./internal/withext/withext_test":
        problems.append(f"test_target: want external dir, got {got}")

    without_ext = tmp_path / "internal" / "noext"
    without_ext.mkdir(parents=True)
    got = test_target(without_ext, "internal/noext")
    if got != "./internal/noext":
        problems.append(f"test_target: want package dir, got {got}")
    return problems


def _probe_floor(tmp_path: Path) -> list[str]:
    """Check: the floor comes from the package's own JSON file when
    --floor is absent; --floor on the CLI overrides it for that run
    only and never writes the file. Also checks the nested-package
    filename mapping (internal/cli -> internal_cli.json)."""
    problems = []
    denylist_dir = tmp_path / "mutation_denylist"
    denylist_dir.mkdir()
    (denylist_dir / "internal_probepkg.json").write_text(
        json.dumps({"denylist": [], "floor": 77})
    )
    pkg = "internal/probepkg"
    if resolve_floor(pkg, None, denylist_dir) != 77:
        problems.append("resolve_floor: did not read the package's stored floor")
    if resolve_floor(pkg, 90, denylist_dir) != 90:
        problems.append("resolve_floor: a CLI floor did not override the stored floor")
    if load_denylist(pkg, denylist_dir)["floor"] != 77:
        problems.append("resolve_floor: a CLI override must not write the package file")
    return problems


def _probe_moved_lines() -> list[str]:
    """Check: take_moved consumes the deletion multiset exactly, never
    matches blank or case-altered text, and reports False when nothing
    matches."""
    problems = []
    counter = Counter({"x := 1": 1, "if err != nil {": 2})
    if not take_moved(counter, "\tx := 1"):
        problems.append("moved lines: an exact deleted match was not treated as a move")
    if take_moved(counter, "x := 1"):
        problems.append("moved lines: one deleted copy licensed two skips")
    if not take_moved(counter, "if err != nil {"):
        problems.append("moved lines: a second distinct deleted text did not match")
    if counter != Counter({"x := 1": 0, "if err != nil {": 1}):
        problems.append("moved lines: the deletion counter was not consumed exactly")
    if take_moved(counter, "   ") or take_moved(counter, ""):
        problems.append("moved lines: a blank line must never count as a move")
    if take_moved(counter, "X := 1"):
        problems.append("moved lines: matching must be exact, not case-folded")
    return problems


def _probe_parse_deleted_lines() -> list[str]:
    """Check: the -U0 deletion parser skips file headers (including
    /dev/null) and hunk headers, counts deleted lines stripped of git's
    single '-' prefix, and ignores blank deletions."""
    problems = []
    fixture = (
        "diff --git a/internal/x/x.go b/internal/x/x.go\n"
        "index 1111111..2222222 100644\n"
        "--- a/internal/x/x.go\n"
        "+++ b/internal/x/x.go\n"
        "@@ -1,4 +0,0 @@\n"
        "-package x\n"
        "-\n"
        "-- indented content\n"
    )
    got = parse_deleted_lines(fixture)
    if got != Counter({"package x": 1, "- indented content": 1}):
        problems.append(
            f"deleted lines: parsed {dict(got)!r}, want exactly the two non-blank deletions"
        )
    if parse_deleted_lines("--- /dev/null\n+++ b/internal/x/x.go\n") != Counter():
        problems.append("deleted lines: file headers must not count as deletions")
    return problems


def run_probe() -> bool:
    """run_probe exercises the kit's own invariants against planted
    fixtures: no fixture is a checked-in .go file. Returns True on
    success."""
    problems = []
    with tempfile.TemporaryDirectory(prefix="mutation-probe-") as tmp:
        tmp_path = Path(tmp)
        pkg_dir = tmp_path / "probepkg"
        pkg_dir.mkdir()
        problems += _probe_planted_site(pkg_dir)
        problems += _probe_build_tag_exclusion(pkg_dir)
        problems += _probe_denylist(pkg_dir)
        problems += _probe_not_guard(pkg_dir)
        problems += _probe_classify()
        problems += _probe_test_target(tmp_path)
        problems += _probe_floor(tmp_path)
        problems += _probe_moved_lines()
        problems += _probe_parse_deleted_lines()

    if problems:
        print("\n".join(problems))
        return False
    return True


def changed_go_files(diff_args: list[str]) -> list[Path]:
    r = subprocess.run(
        ["git", "diff", *diff_args, "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go"],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return []
    return [ROOT / f for f in r.stdout.strip("\0").split("\0") if f and not f.endswith("_test.go")]


def changed_lines_for_file(diff_args: list[str], path: Path) -> set[int]:
    try:
        rel_path = str(path.relative_to(ROOT))
    except ValueError:
        rel_path = str(path)
    r = subprocess.run(
        ["git", "diff", *diff_args, "-U0", "--", rel_path],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return set()
    lines = set()
    for line in r.stdout.splitlines():
        m = re.match(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", line)
        if m:
            start = int(m.group(1))
            count = int(m.group(2)) if m.group(2) is not None else 1
            if count > 0:
                lines.update(range(start, start + count))
    return lines


def take_moved(counter: Counter, line_text: str) -> bool:
    """take_moved reports whether line_text is a VERBATIM MOVE in the diff
    being swept, consuming one occurrence if so.

    A refactor commit moves existing code between files: git reports the old
    location as deletions and the new location as additions, so every operator
    in the moved body sits on a "changed line" and the sweep re-mutates code
    that the commit which first added it already swept - against the same
    package tests, for the same answer. Matching a site's line text against
    the diff's deletions (a multiset, so N deleted copies license exactly N
    skips) skips that redundant work while every genuinely new or edited line
    stays swept.

    Matching is stripped and exact: indentation may differ when code moves
    between scopes, but any other difference means the line was edited, and
    edited lines must be swept. Pure so a test can assert the contract."""
    text = line_text.strip()
    if not text:
        return False
    if counter.get(text, 0) > 0:
        counter[text] -= 1
        return True
    return False


def parse_deleted_lines(diff_text: str) -> Counter:
    """parse_deleted_lines counts, by stripped text, every non-blank deleted
    line in `git diff -U0` output.

    File headers (`--- a/path`, `--- /dev/null`) and hunk headers are skipped;
    any other '-' line is one deleted source line with git's single '-' prefix
    stripped, so a deleted line whose own text starts with '-' renders with two
    dashes and is still counted. Pure so a test can assert the parsing."""
    counter: Counter = Counter()
    for line in diff_text.splitlines():
        if line.startswith(("diff --git ", "index ", "@@")):
            continue
        if line.startswith(("--- a/", "--- b/", "--- /dev/null")):
            continue
        if line.startswith("-"):
            text = line[1:].strip()
            if text:
                counter[text] += 1
    return counter


def deleted_lines_counter(diff_args: list[str], rel_paths: list[str]) -> Counter:
    """deleted_lines_counter returns parse_deleted_lines over one git call
    covering every changed file in the sweep scope. A failed git call yields
    an empty counter: the sweep then treats no line as moved and mutates
    every changed-line site, which is the pre-move-aware behavior - never
    a silent pass."""
    if not rel_paths:
        return Counter()
    r = subprocess.run(
        ["git", "diff", *diff_args, "-U0", "--", *rel_paths],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return Counter()
    return parse_deleted_lines(r.stdout)


def sweep_diff(diff_args: list[str]) -> tuple[dict, bool]:
    """sweep_diff runs mutants on every changed line of the diff, per package,
    and returns (stats, failed). Sites on verbatim-moved lines - the same
    stripped bytes deleted elsewhere in this diff - are skipped: a refactor
    commit moving existing code between files would otherwise re-mutate the
    whole moved body against the same package tests the origin commit already
    swept, making commit cost proportional to text churn instead of semantic
    change. See take_moved."""
    files = changed_go_files(diff_args)
    if not files:
        print("mutation diff sweep: no changed non-test .go files in scope")
        return {"killed": 0, "survived": 0, "discarded": 0, "rate": 100.0}, False

    skipped = 0
    deleted = deleted_lines_counter(
        diff_args, [str(f.relative_to(ROOT)) for f in files]
    )
    file_lines: dict[Path, list[str]] = {}

    pkg_sites: dict[str, list] = {}
    for f in files:
        if is_build_tag_gated(f):
            continue
        try:
            rel = f.relative_to(ROOT)
            pkg = str(rel.parent)
        except ValueError:
            continue

        changed_lines = changed_lines_for_file(diff_args, f)
        if not changed_lines:
            continue

        file_bytes = f.read_bytes()
        data = load_denylist(pkg, DENYLIST_DIR)
        spans = denylisted_spans(ROOT / pkg, data.get("denylist", []))

        for site in sites_for_file(f):
            if is_denylisted(site, spans):
                continue
            line_no = line_of_offset(file_bytes, site.start)
            if line_no not in changed_lines:
                continue
            if f not in file_lines:
                file_lines[f] = file_bytes.decode("utf-8", errors="replace").split("\n")
            idx = line_no - 1
            if idx < len(file_lines[f]) and take_moved(deleted, file_lines[f][idx]):
                skipped += 1
                continue
            pkg_sites.setdefault(pkg, []).append((site, line_no))

    if skipped:
        print(
            f"mutation diff sweep: skipped {skipped} mutation site(s) on verbatim-moved "
            "lines (the same bytes are deleted elsewhere in this diff; they were "
            "already swept where they were first written)"
        )

    total_killed = total_survived = total_discarded = 0
    failed = False

    for pkg, sites_with_lines in pkg_sites.items():
        pkg_dir = ROOT / pkg
        coverage = compute_coverage_blocks(pkg, pkg_dir)
        originals: dict[Path, bytes] = {}
        try:
            for site, line_no in sites_with_lines:
                if site.path not in originals:
                    originals[site.path] = site.path.read_bytes()
                if site_is_dead_code(site, line_no, coverage):
                    total_survived += 1
                    failed = True
                    print(
                        f"SURVIVED on diff line (no coverage, test skipped): "
                        f"{site.path.name}:{line_no} {site.kind} {site.old!r} -> "
                        f"{site.new!r} (missing test assertion)"
                    )
                    continue
                outcome = run_mutant(site, originals[site.path], pkg, str(pkg_dir))
                if outcome == KILLED:
                    total_killed += 1
                elif outcome == SURVIVED:
                    total_survived += 1
                    failed = True
                    print(
                        f"SURVIVED on diff line: {site.path.name}:{line_no} "
                        f"{site.kind} {site.old!r} -> {site.new!r} (missing test assertion)"
                    )
                else:
                    total_discarded += 1
        finally:
            # This is the path the pre-commit hook runs, so it is the one
            # that can hand a mutant to `git add`. See verify_restored.
            restore_and_verify(originals)

    total = total_killed + total_survived
    rate = 100.0 * total_killed / total if total else 100.0
    print(f"mutation diff sweep: killed={total_killed} survived={total_survived} discarded={total_discarded} rate={rate:.2f}%")
    return {"killed": total_killed, "survived": total_survived, "discarded": total_discarded, "rate": rate}, failed


def main() -> int:
    parser = argparse.ArgumentParser(description="mutation kit: per-package kill rate")
    parser.add_argument("--pkg", help="package directory to mutate, e.g. internal/cli")
    parser.add_argument("--floor", type=float, help="override the stored floor for this run")
    parser.add_argument("--sample", type=int, help="run only the first N mutants")
    parser.add_argument("--probe", action="store_true", help="run the kit's own probe suite")
    parser.add_argument("--staged", action="store_true", help="sweep mutants on staged diff vs HEAD")
    parser.add_argument("--diff", action="store_true", help="sweep mutants on unstaged diff vs HEAD")
    parser.add_argument("--base", help="Base git ref for diff mutation sweep")
    parser.add_argument("--tip", default="HEAD", help="Tip git ref for diff mutation sweep")
    parser.add_argument(
        "--check-floors",
        action="store_true",
        help="sweep all packages with configured floors in .mivia/policy/mutation and fail if below floor",
    )
    parser.add_argument(
        "--all-core",
        action="store_true",
        help="sweep every package in CORE_PACKAGES instead of one --pkg",
    )
    args = parser.parse_args()

    if args.probe:
        return 0 if run_probe() else 1

    if args.check_floors:
        configured = []
        known_pkgs = recognized_packages()
        if POLICY_MUTATION_DIR.is_dir():
            for p in POLICY_MUTATION_DIR.glob("*.json"):
                try:
                    data = json.loads(p.read_text(encoding="utf-8"))
                    if data.get("floor") is not None:
                        pkg = data.get("package")
                        if not pkg:
                            stem = p.stem
                            for kp in known_pkgs:
                                if kp.replace("/", "_") == stem:
                                    pkg = kp
                                    break
                        if not pkg:
                            pkg = p.stem.replace("_", "/")
                        configured.append(pkg)
                except Exception:
                    continue
        if not configured:
            print("check_mutation: no package floors configured in .mivia/policy/mutation")
            return 0
        failed = False
        for pkg in sorted(configured):
            if pkg not in known_pkgs or not (ROOT / pkg).is_dir():
                print(f"check_mutation: invalid or missing package directory for {pkg}", file=sys.stderr)
                failed = True
                continue
            try:
                result = sweep(pkg, args.sample)
            except MutationError as exc:
                print(str(exc))
                return 1
            floor = resolve_floor(pkg, None)
            print(
                f"{pkg}: killed={result['killed']} survived={result['survived']} "
                f"discarded={result['discarded']} rate={result['rate']:.2f}%"
            )
            if floor is not None and result["rate"] < floor:
                print(f"{pkg}: kill rate {result['rate']:.2f}% below the {floor:.2f}% floor")
                failed = True
        return 1 if failed else 0

    if args.staged:
        _, failed = sweep_diff(["--cached"])
        return 1 if failed else 0

    if args.diff:
        _, failed = sweep_diff(["HEAD"])
        return 1 if failed else 0

    if args.base:
        _, failed = sweep_diff([f"{args.base}..{args.tip}"])
        return 1 if failed else 0

    targets = CORE_PACKAGES if args.all_core else ([args.pkg] if args.pkg else None)
    if not targets:
        print("--pkg, --all-core, --check-floors, --staged, or --diff is required unless --probe is set")
        return 2

    known = recognized_packages()
    for pkg in targets:
        if pkg not in known:
            print(f"unrecognized package: {pkg}")
            return 2

    failed = False
    for pkg in targets:
        try:
            result = sweep(pkg, args.sample)
        except MutationError as exc:
            print(str(exc))
            return 1

        floor = resolve_floor(pkg, args.floor)
        print(
            f"{pkg}: killed={result['killed']} survived={result['survived']} "
            f"discarded={result['discarded']} rate={result['rate']:.2f}%"
        )
        if floor is None:
            print(f"{pkg}: no floor set on the CLI or in its denylist file; exploratory run")
            continue
        if result["rate"] < floor:
            print(f"{pkg}: kill rate {result['rate']:.2f}% below the {floor}% floor")
            failed = True

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
