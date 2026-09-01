#!/usr/bin/env python3
"""Gate: every Semgrep rule in semgrep/agent-standards.yml fires on its
violation fixture and stays silent on its clean fixture.

Pattern-matching text assertions (scripts/test_semgrep_rules.py) confirm the
YAML shape but never invoke Semgrep, so a rule can silently stop matching and
nothing here would notice. This script writes one violation + one clean
fixture per rule to a temp directory, runs Semgrep once across the whole
tree, and asserts each rule's findings land on exactly its own violation
fixture.

If Semgrep is not installed, the probe skips (matching how the Makefile's
`semgrep` / `semgrep-validate` targets treat the tool as optional)."""
from __future__ import annotations

import json
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONFIG = ROOT / "semgrep" / "agent-standards.yml"

# One entry per rule id in semgrep/agent-standards.yml. Each tuple is:
#   (rule_id, violation_relpath, violation_content, clean_relpath, clean_content)
# Paths are relative to the fixture tree root and must land inside that
# rule's `paths.include` globs (checked against the actual YAML below).
PROBES = [
    (
        "mivia.go.no-validstring-rune-boundary-backoff",
        "internal/probe-utf8-backoff/viol.go",
        'package probe\n\nimport "unicode/utf8"\n\n'
        "func cut(s string, n int) string {\n"
        "\ts = s[:n]\n"
        "\tfor len(s) > 0 && !utf8.ValidString(s) {\n"
        "\t\ts = s[:len(s)-1]\n"
        "\t}\n"
        "\treturn s\n}\n",
        "internal/probe-utf8-backoff/clean.go",
        'package probe\n\nimport "unicode/utf8"\n\n'
        "func cutOK(s string, n int) string {\n"
        "\ts = s[:n]\n"
        "\tfor len(s) > 0 {\n"
        "\t\tr, size := utf8.DecodeLastRuneInString(s)\n"
        "\t\tif r != utf8.RuneError || size > 1 {\n"
        "\t\t\tbreak\n"
        "\t\t}\n"
        "\t\ts = s[:len(s)-1]\n"
        "\t}\n"
        "\treturn s\n}\n",
    ),
    (
        # Same rule, second shape: the ValidString call sits on a line BELOW
        # the `for`, inside the loop body, in a loop that walks an index
        # DOWNWARD. This is the shape internal/hooks/protocol.go carried
        # before 04f36f5b; a rule keyed on the `for` line alone cannot see it.
        "mivia.go.no-validstring-rune-boundary-backoff",
        "internal/probe-utf8-backoff/viol_body.go",
        'package probe\n\nimport "unicode/utf8"\n\n'
        "func cutBody(s string, limit int) string {\n"
        "\tcut := s[:limit]\n"
        "\tfor i := limit - 1; i >= 0 && limit-i < utf8.UTFMax; i-- {\n"
        "\t\tif utf8.RuneStart(s[i]) {\n"
        "\t\t\tif utf8.ValidString(s[:i]) {\n"
        "\t\t\t\treturn s[:i]\n"
        "\t\t\t}\n"
        "\t\t\treturn cut\n"
        "\t\t}\n"
        "\t}\n"
        "\treturn cut\n}\n",
        "internal/probe-utf8-backoff/clean_body.go",
        'package probe\n\nimport "unicode/utf8"\n\n'
        "func declaredRuneLen(b byte) int { return int(b) }\n\n"
        "func cutBodyOK(s string, limit int) string {\n"
        "\tcut := s[:limit]\n"
        "\tfor i := limit - 1; i >= 0 && limit-i < utf8.UTFMax; i-- {\n"
        "\t\tif !utf8.RuneStart(s[i]) {\n"
        "\t\t\tcontinue\n"
        "\t\t}\n"
        "\t\tif declaredRuneLen(s[i]) > limit-i {\n"
        "\t\t\treturn s[:i]\n"
        "\t\t}\n"
        "\t\treturn cut\n"
        "\t}\n"
        "\treturn cut\n}\n",
    ),
    (
        "mivia.go.no-chat-principal-as-sync-handle",
        "internal/probe-sync-handle/viol.go",
        'package probe\n\nimport "github.com/MiviaLabs/mivia-agent/internal/chatsync"\n\n'
        "type sess struct{ SessionID string }\n\n"
        "func handle(s sess) chatsync.LocalHandle { return chatsync.LocalHandle(s.SessionID) }\n",
        "internal/probe-sync-handle/clean.go",
        'package probe\n\nimport "github.com/MiviaLabs/mivia-agent/internal/chatsync"\n\n'
        "func stored(id chatsync.SyncIdentity) chatsync.LocalHandle { return id.LocalHandle }\n",
    ),
    (
        "mivia.go.ui-no-harness-imports",
        "internal/ui/probe-isolation/viol.go",
        "package probe\n\nimport _ \"github.com/MiviaLabs/mivia-agent/internal/cli\"\n",
        "internal/ui/probe-isolation/clean.go",
        "package probe\n\nimport _ \"github.com/MiviaLabs/mivia-agent/internal/uikit/ports\"\n",
    ),
    (
        "mivia.go.hub-wire-no-raw-error-text",
        "internal/hub/probe-raw-error/viol.go",
        "package probe\n\nfunc errText(err error) string { return err.Error() }\n",
        "internal/hub/probe-raw-error/clean.go",
        "package probe\n\nimport \"github.com/MiviaLabs/mivia-agent/internal/chat\"\n\ntype ev struct{ Err error }\n\nfunc f(e ev) string { return chat.TurnErrorMessage(e.Err) }\n",
    ),
    (
        "mivia.generic.no-wildcard-bash-allow",
        ".claude/probe-wildcard-bash/viol.json",
        '{\n  "allow": [\n    "Bash(git *)"\n  ]\n}\n',
        ".claude/probe-wildcard-bash/clean.json",
        '{\n  "allow": [\n    "Bash(git status)"\n  ]\n}\n',
    ),
    (
        "mivia.generic.no-shell-metachar-bash-allow",
        ".claude/probe-shell-metachar/viol.json",
        '{\n  "allow": [\n    "Bash(git status; rm -rf /)"\n  ]\n}\n',
        ".claude/probe-shell-metachar/clean.json",
        '{\n  "allow": [\n    "Bash(git status)"\n  ]\n}\n',
    ),
    (
        "mivia.generic.no-semgrep-suppression",
        ".claude/probe-suppression/viol.md",
        "// nosemgrep: mivia.go.no-panic-in-internal\n",
        ".claude/probe-suppression/clean.md",
        "No suppression markers in this file.\n",
    ),
    (
        "mivia.generic.no-unresolved-drift-markers",
        ".claude/probe-drift/viol.md",
        "TODO: finish wiring this before merge\n",
        ".claude/probe-drift/clean.md",
        "Everything in this file is finished.\n",
    ),
    (
        "mivia.generic.brand-mivialabs",
        ".claude/probe-brand/viol.md",
        "This is Mivia Labs product documentation.\n",
        ".claude/probe-brand/clean.md",
        "This is MiviaLabs product documentation.\n",
    ),
    (
        "mivia.generic.product-binary-is-mivia",
        ".claude/probe-binary/viol.md",
        "Run ./mivia-agent to start the tool.\n",
        ".claude/probe-binary/clean.md",
        "The binary is mivia. mivia-agentkit was the legacy MVP name.\n",
    ),
    (
        "mivia.generic.no-git-hook-bypass-in-agent-config",
        ".claude/probe-hook-bypass/viol.md",
        "Always use --no-verify when committing.\n",
        ".claude/probe-hook-bypass/clean.md",
        "Git hooks must never be skipped; fix failures instead.\n",
    ),
    (
        "mivia.generic.no-skill-freeform-output-heading",
        ".claude/skills/probe-output-heading/SKILL.md",
        "# Probe Skill\n\n## Output\nFree-form text here.\n",
        ".claude/skills/probe-output-heading-clean/SKILL.md",
        "# Probe Skill\n\n## ReportFormat\nmivia-report/v1\n",
    ),
    (
        "mivia.generic.skills-require-mivia-report-v1",
        ".claude/skills/probe-report-format/SKILL.md",
        "# Probe Skill\n\nReportFormat: legacy-format\n",
        ".claude/skills/probe-report-format-clean/SKILL.md",
        "# Probe Skill\n\nReportFormat: mivia-report/v1\n",
    ),
    (
        "mivia.generic.architecture-review-must-stay-portable",
        ".mivia/skills/architecture-review/viol.md",
        "This skill follows the ADLC process.\n",
        ".mivia/skills/architecture-review/clean.md",
        "This skill reviews architecture using discovered project conventions and generic checks.\n",
    ),
    (
        "mivia.generic.no-process-per-subagent-default",
        "docs/probe-process-per-agent/viol.md",
        "We recommend process-per-agent as the default.\n",
        "docs/probe-process-per-agent/clean.md",
        "This design rejects process-per-agent as the default; each subagent runs as a goroutine.\n",
    ),
    (
        "mivia.rules.no-os-exec-per-agent-default",
        ".agents/rules/probe-os-exec.md",
        "We recommend os/exec per agent for isolation.\n",
        ".agents/rules/probe-os-exec-clean.md",
        "In-process goroutines handle concurrency; os/exec is reserved for external tools only.\n",
    ),
    (
        "mivia.go.no-hardcoded-secrets",
        "internal/probe/secrets.go",
        'package probe\n\nconst apiKey = "abcdefgh12345678"\n\nfunc f() {\n\t_ = apiKey\n}\n',
        "internal/probe/secrets_clean.go",
        'package probe\n\nimport "os"\n\nfunc f() {\n\tapiKey := os.Getenv("API_KEY")\n\t_ = apiKey\n}\n',
    ),
    (
        "mivia.go.no-panic-in-internal",
        "internal/probe/panic.go",
        'package probe\n\nfunc f() {\n\tpanic("boom")\n}\n',
        "internal/probe/panic_clean.go",
        "package probe\n\nfunc f() error {\n\treturn nil\n}\n",
    ),
    (
        "mivia.go.no-fatal-exit-in-internal",
        "internal/probe/fatal.go",
        'package probe\n\nimport "log"\n\nfunc f() {\n\tlog.Fatal("boom")\n}\n',
        "internal/probe/fatal_clean.go",
        'package probe\n\nimport "log"\n\nfunc f() {\n\tlog.Print("ok")\n}\n',
    ),
    (
        "mivia.go.no-value-builder-field-in-ui",
        "internal/ui/probe/builder.go",
        'package probe\n\nimport "strings"\n\ntype Model struct {\n\tName    string\n\tpending strings.Builder\n}\n',
        "internal/ui/probe/builder_clean.go",
        'package probe\n\ntype CleanModel struct {\n\tName    string\n\tpending string\n}\n',
    ),
    (
        "mivia.go.no-shell-exec",
        "internal/probe/shell.go",
        'package probe\n\nimport "os/exec"\n\nfunc f() {\n\texec.Command("bash", "-c", "echo hi")\n}\n',
        "internal/probe/shell_clean.go",
        'package probe\n\nimport "os/exec"\n\nfunc f() {\n\texec.Command("git", "status")\n}\n',
    ),
    (
        "mivia.go.no-world-writable-mode",
        "internal/probe/mode.go",
        'package probe\n\nimport "os"\n\nfunc f() {\n\tos.WriteFile("x", nil, 0777)\n}\n',
        "internal/probe/mode_clean.go",
        'package probe\n\nimport "os"\n\nfunc f() {\n\tos.WriteFile("x", nil, 0644)\n}\n',
    ),
    (
        "mivia.go.tests-use-t-tempdir",
        "internal/probe/tempdir_test.go",
        'package probe\n\nimport (\n\t"os"\n\t"testing"\n)\n\nfunc TestX(t *testing.T) {\n\tdir, _ := os.MkdirTemp("", "x")\n\t_ = dir\n}\n',
        "internal/probe/tempdir_clean_test.go",
        'package probe\n\nimport "testing"\n\nfunc TestX(t *testing.T) {\n\tdir := t.TempDir()\n\t_ = dir\n}\n',
    ),
    (
        "mivia.go.tests-no-time-sleep",
        "internal/probe/sleep_test.go",
        'package probe\n\nimport (\n\t"testing"\n\t"time"\n)\n\nfunc TestX(t *testing.T) {\n\ttime.Sleep(time.Second)\n}\n',
        "internal/probe/sleep_clean_test.go",
        'package probe\n\nimport "testing"\n\nfunc TestX(t *testing.T) {\n\t_ = 1\n}\n',
    ),
    (
        "mivia.go.no-empty-test",
        "internal/probe/empty_test.go",
        'package probe\n\nimport "testing"\n\nfunc TestEmpty(t *testing.T) {}\n',
        "internal/probe/empty_clean_test.go",
        'package probe\n\nimport "testing"\n\nfunc TestNonEmpty(t *testing.T) {\n\tif 1 != 1 {\n\t\tt.Fatal("fail")\n\t}\n}\n',
    ),
    (
        "mivia.go.provider-body-read-needs-watchdog",
        "internal/provider/probe-body-read/viol.go",
        "package probe\n\nimport (\n\t\"io\"\n\t\"net/http\"\n)\n\n"
        "func read(resp *http.Response) { _, _ = io.ReadAll(resp.Body) }\n",
        "internal/provider/probe-body-read/clean.go",
        "package probe\n\nimport (\n\t\"io\"\n)\n\n"
        "func read(body io.Reader) { _, _ = io.ReadAll(body) }\n",
    ),
    (
        "mivia.go.no-tautological-test-assertion",
        "internal/probe/taut_test.go",
        'package probe\n\nimport "testing"\n\ntype A struct{}\nfunc (A) True(t *testing.T, b bool) {}\nvar assert A\n\nfunc TestTaut(t *testing.T) {\n\tassert.True(t, true)\n}\n',
        "internal/probe/taut_clean_test.go",
        'package probe\n\nimport "testing"\n\ntype A struct{}\nfunc (A) True(t *testing.T, b bool) {}\nvar assert A\n\nfunc TestProper(t *testing.T) {\n\tx := 1\n\tassert.True(t, x == 1)\n}\n',
    ),
    (
        "mivia.go.no-direct-tool-execution-outside-dispatcher",
        "internal/agent/probe/exec.go",
        'package probe\n\nfunc f() {\n\tregistry.Execute(ctx, "tool", nil)\n}\n',
        "internal/agent/probe/exec_clean.go",
        'package probe\n\nfunc f() {\n\tdispatcher.Dispatch(ctx, "tool", nil)\n}\n',
    ),
    (
        "mivia.go.uikit-no-bubbletea-lipgloss",
        "internal/uikit/probe/bt.go",
        'package probe\n\nimport "github.com/charmbracelet/bubbletea"\n\nvar _ = bubbletea.Model(nil)\n',
        "internal/uikit/probe/bt_clean.go",
        'package probe\n\nimport "fmt"\n\nvar _ = fmt.Sprintf\n',
    ),
    (
        "mivia.go.ui-no-raw-print",
        "internal/ui/probe/print.go",
        'package probe\n\nimport "fmt"\n\nfunc f() {\n\tfmt.Println("hi")\n}\n',
        "internal/ui/probe/print_clean.go",
        'package probe\n\nimport "io"\n\nfunc f(w io.Writer) {\n\tw.Write([]byte("hi"))\n}\n',
    ),
    (
        "mivia.go.uikit-ui-no-init",
        "internal/uikit/probe/init.go",
        "package probe\n\nfunc init() {\n\t_ = 1\n}\n",
        "internal/uikit/probe/init_clean.go",
        "package probe\n\ntype T struct{}\n\nfunc New() *T {\n\treturn &T{}\n}\n",
    ),
    (
        "mivia.go.uikit-ui-no-package-level-sync-state",
        "internal/uikit/probe/state.go",
        'package probe\n\nimport "sync"\n\nvar mu sync.Mutex\n',
        "internal/uikit/probe/state_clean.go",
        'package probe\n\nvar names = map[string]string{"a": "b"}\n',
    ),
    (
        "mivia.go.no-hardcoded-mivia-skills-path",
        "internal/probe/mivia_skills.go",
        'package probe\n\nimport "path/filepath"\n\nfunc p(root string) string {\n\treturn filepath.Join(root, ".mivia", "skills", "shared")\n}\n',
        "internal/probe/mivia_skills_clean.go",
        'package probe\n\nimport "github.com/MiviaLabs/mivia-agent/internal/workspace"\n\nfunc p(root string) string {\n\treturn workspace.SkillsDir(root) + "/shared"\n}\n',
    ),
    (
        "mivia.go.no-truncation-call-inside-envelope-literal",
        "internal/chatsync/probe-trunc-order/viol.go",
        "package probe\n\n"
        "type Envelope struct{ Trunc *int }\n\n"
        "type payload struct {\n\tEnvelope\n\tDetail string\n}\n\n"
        "func applyTruncation(e *Envelope, field, value string, maxBytes int) string { return value }\n\n"
        "func build(env Envelope, detail string) *payload {\n"
        "\treturn &payload{Envelope: env, Detail: applyTruncation(&env, \"detail\", detail, 200)}\n}\n",
        "internal/chatsync/probe-trunc-order/clean.go",
        "package probe\n\n"
        "func buildClean(env Envelope, detail string) *payload {\n"
        "\td := applyTruncation(&env, \"detail\", detail, 200)\n"
        "\treturn &payload{Envelope: env, Detail: d}\n}\n",
    ),
    (
        "mivia.go.no-locked-field-reread",
        "internal/chatsync/probe-locked-reread/viol.go",
        "package probe\n\nimport \"sync\"\n\n"
        "type Poller struct {\n\tmu        sync.Mutex\n\tsessionID string\n}\n\n"
        "func (p *Poller) doConsume() string {\n"
        "\tp.mu.Lock()\n\tsessID := p.sessionID\n\tp.mu.Unlock()\n\n"
        "\tfirst := callNext(sessID)\n\t_ = first\n\n"
        "\treturn callConsume(p.sessionID)\n}\n\n"
        "func callNext(id string) string    { return id }\n"
        "func callConsume(id string) string { return id }\n",
        "internal/chatsync/probe-locked-reread/clean.go",
        "package probe\n\nimport \"sync\"\n\n"
        "type PollerOK struct {\n\tmu        sync.Mutex\n\tsessionID string\n}\n\n"
        "func (p *PollerOK) doConsumeOK() string {\n"
        "\tp.mu.Lock()\n\tsessID := p.sessionID\n\tp.mu.Unlock()\n\n"
        "\tfirst := callNextOK(sessID)\n\t_ = first\n\n"
        "\treturn callConsumeOK(sessID)\n}\n\n"
        "func callNextOK(id string) string    { return id }\n"
        "func callConsumeOK(id string) string { return id }\n",
    ),
]


def fail(msg: str) -> None:
    print(f"check_semgrep_probes: {msg}", file=sys.stderr)


def semgrep_available() -> bool:
    return shutil.which("semgrep") is not None


def write_fixtures(tmp: Path) -> None:
    # Semgrep applies a built-in default .semgrepignore when the target tree
    # has none of its own, and that default skips common test-file globs
    # (e.g. *_test.go) - exactly the files the tests-use-t-tempdir and
    # tests-no-time-sleep probes need scanned. Writing an explicit
    # .semgrepignore overrides the built-in default with just this rule.
    (tmp / ".semgrepignore").write_text(".git/\n")
    for _rule_id, vpath, vcontent, cpath, ccontent in PROBES:
        vfile = tmp / vpath
        cfile = tmp / cpath
        vfile.parent.mkdir(parents=True, exist_ok=True)
        cfile.parent.mkdir(parents=True, exist_ok=True)
        vfile.write_text(vcontent)
        cfile.write_text(ccontent)


def run_semgrep(tmp: Path) -> dict:
    proc = subprocess.run(
        [
            "semgrep",
            "--config",
            str(CONFIG),
            "--json",
            "--metrics",
            "off",
            "--disable-nosem",
            "-j",
            "2",
            str(tmp),
        ],
        capture_output=True,
        text=True,
    )
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError:
        fail("semgrep produced no parseable JSON output")
        print(proc.stderr, file=sys.stderr)
        sys.exit(1)


def group_findings(data: dict, tmp: Path) -> dict[str, set[str]]:
    """Map fixture relpath (posix, relative to tmp) -> set of rule ids that fired."""
    hits: dict[str, set[str]] = {}
    for result in data.get("results", []):
        path = Path(result["path"])
        try:
            relpath = path.resolve().relative_to(tmp.resolve()).as_posix()
        except ValueError:
            relpath = path.as_posix()
        check_id = result["check_id"]
        # Semgrep prefixes check_id with the config file's rule-id components;
        # our rule ids are already fully qualified (mivia.<group>.<name>), so
        # take the trailing match against known rule ids to normalize.
        rule_ids = {rid for rid, *_ in PROBES}
        matched = check_id if check_id in rule_ids else None
        if matched is None:
            for rid in rule_ids:
                if check_id.endswith(rid):
                    matched = rid
                    break
        hits.setdefault(relpath, set()).add(matched or check_id)
    return hits


def declared_rule_ids() -> set[str]:
    """Every rule id in the config, read without a YAML dependency."""
    ids = set()
    for line in CONFIG.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith("- id:"):
            ids.add(stripped.split(":", 1)[1].strip())
    return ids


def main() -> int:
    if not CONFIG.exists():
        fail(f"missing config: {CONFIG}")
        return 1

    # Completeness first, because it needs no Semgrep and it is the check
    # that catches a NEW rule. A rule with no probe is an unverified rule:
    # it can silently stop matching, and every assertion below would still
    # pass because none of them ever mentions it.
    declared = declared_rule_ids()
    probed = {rid for rid, *_ in PROBES}
    if missing := sorted(declared - probed):
        fail("rules declared in the config with no probe: " + ", ".join(missing))
        return 1
    if stale := sorted(probed - declared):
        fail("probes for rules no longer in the config: " + ", ".join(stale))
        return 1

    if not semgrep_available():
        print("check_semgrep_probes: semgrep not installed; skipping probe run")
        return 0

    tmp = Path(tempfile.mkdtemp(prefix="mivia-semgrep-probes-"))
    try:
        write_fixtures(tmp)
        data = run_semgrep(tmp)
        if data.get("errors"):
            fail(f"semgrep reported scan errors: {data['errors']}")
            return 1

        hits = group_findings(data, tmp)

        problems: list[str] = []
        for rule_id, vpath, _vcontent, cpath, _ccontent in PROBES:
            vhits = hits.get(vpath, set())
            chits = hits.get(cpath, set())
            if rule_id not in vhits:
                problems.append(
                    f"{rule_id}: violation fixture {vpath} did not fire "
                    f"(found: {sorted(vhits) or 'nothing'})"
                )
            if rule_id in chits:
                problems.append(f"{rule_id}: clean fixture {cpath} incorrectly fired")

        if problems:
            fail(f"{len(problems)} probe mismatch(es):")
            for p in problems:
                print(f"  - {p}", file=sys.stderr)
            return 1

        print(f"check_semgrep_probes: ok ({len(PROBES)} rules probed)")
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
