#!/usr/bin/env python3
"""Verify the mivia agent control surface (lean, fail closed).

Required:
  AGENTS.md, .agents/INDEX.md, rules, policies, hooks, semgrep, docs/OWNERS.yaml,
  Makefile targets referenced by AGENTS.md / install flow.
"""

from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path


from verify_common import ROOT, fail, rel_to_root  # noqa: E402
from verify_skill_tree import (  # noqa: E402
    check_claude_skill_aliases,
    check_no_dead_skill_tree,
    check_skill_dir,
)


def text(rel: str) -> str:
    return (ROOT / rel).read_text(encoding="utf-8")


def require_file(rel: str) -> None:
    if not (ROOT / rel).is_file():
        fail(f"missing {rel}")


def require_exec(rel: str) -> None:
    path = ROOT / rel
    if not path.is_file():
        fail(f"missing {rel}")
    if not os.access(path, os.X_OK):
        fail(f"{rel}: must be executable")


# Mirrors V1Events() in internal/hooks/config.go. Keep the two in sync: an event
# accepted here but rejected there would pass `make verify` and then fail at
# runtime, which is the failure mode this gate exists to prevent.
HOOK_V1_EVENTS = {"PreToolUse", "PostToolUse", "Stop"}

# Configs this gate can see. A user's ~/.mivia/mivia.toml is deliberately not
# among them: it is not in the repository, and a machine-local file is not
# something a build can vouch for.
HOOK_CONFIG_PATHS = [".mivia/mivia.toml", ".mivia/mivia.toml.example"]


def check_hook_events() -> None:
    """Every [[hooks]] event in a repo-visible config is a known v1 event.

    An unknown event name is rejected at load by internal/hooks, so shipping one
    in our own example file would ship a config our own parser refuses.
    """
    try:
        import tomllib
    except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
        return
    for rel in HOOK_CONFIG_PATHS:
        path = ROOT / rel
        if not path.is_file():
            continue
        try:
            with path.open("rb") as handle:
                data = tomllib.load(handle)
        except tomllib.TOMLDecodeError as exc:
            fail(f"{rel}: not valid TOML: {exc}")
        for index, group in enumerate(data.get("hooks") or []):
            if not isinstance(group, dict):
                fail(f"{rel}: hooks[{index}] must be a table")
            event = group.get("event")
            if event not in HOOK_V1_EVENTS:
                fail(
                    f"{rel}: hooks[{index}] event {event!r} is not a v1 lifecycle "
                    f"event; expected one of {sorted(HOOK_V1_EVENTS)}"
                )


# .mivia/mivia.toml binds this repository's own workflow agent steps. Two of
# its keys protect the controls that protect everything else, and nothing else
# in the tree asserts either one:
#
#   - write_path_blocklist must hold the live Git hook paths, the config file
#     itself, and .mivia/policy. install_git_hooks.sh points core.hooksPath at
#     .githooks, so blocking .git alone leaves the hooks writable. Omitting the
#     config file lets a workflow agent step empty the list and restore write
#     access to all of them.
#   - AGENTS.md names the PreToolUse run-command-guard hook as one of the three
#     layers enforcing "never bypass Git hooks". check_hook_events only
#     validates a declared event name, so deleting the declaration passed.
#
# A commit once trimmed the blocklist to the three hook paths and justified it
# with the commit-time gates. None of those gates reads this key.
SELF_PROTECTING_BLOCKLIST = (
    ".git",
    ".githooks",
    "scripts/git-hooks",
    # The hooks are only as good as what they run. With scripts/ writable, a
    # workflow agent step rewrites verify_agent_config.py to exit 0 and every
    # gate the protected hook invokes passes, .githooks untouched. The Makefile
    # decides which gates run at all.
    "scripts/verify_agent_config.py",
    "scripts/agent_hook_guard.py",
    "scripts/secret_scan.py",
    "Makefile",
    ".mivia/mivia.toml",
    ".mivia/policy",
    # .mivia/hooks holds run-command-guard.py, the PreToolUse guard's own
    # program (layer 3). .claude/settings.json registers that guard as the
    # handler (layer 2's declaration). Either writable lets a workflow agent
    # step silently disable enforcement while this very check stays green -
    # this tuple's own name claims coverage it did not have until now.
    ".mivia/hooks",
    ".claude/settings.json",
)

RUN_COMMAND_GUARD = "run-command-guard.py"


def check_workflow_self_protection() -> None:
    try:
        import tomllib
    except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
        return
    rel = ".mivia/mivia.toml"
    path = ROOT / rel
    if not path.is_file():
        fail(f"missing {rel}")
    with path.open("rb") as handle:
        data = tomllib.load(handle)
    blocklist = ((data.get("tools") or {}).get("write_path_blocklist")) or []
    if not isinstance(blocklist, list):
        fail(f"{rel}: tools.write_path_blocklist must be a list")
    entries = {str(item).strip().strip("/") for item in blocklist}
    removals = {
        str(item).strip().strip("/")
        for item in (((data.get("tools") or {}).get("write_path_blocklist_remove")) or [])
    }
    for want in SELF_PROTECTING_BLOCKLIST:
        # A parent entry covers its children: 'scripts' blocks
        # 'scripts/git-hooks'. Exact membership would force a redundant entry
        # and reject a strictly stronger list.
        if not any(want == e or want.startswith(e + "/") for e in entries):
            fail(
                f"{rel}: tools.write_path_blocklist does not cover {want!r}. "
                f"A workflow agent step can then write it, and for "
                f"'.mivia/mivia.toml' that means emptying this key and "
                f"restoring write access to every other entry."
            )
        if any(r == want or want.startswith(r + "/") for r in removals):
            fail(
                f"{rel}: tools.write_path_blocklist_remove removes {want!r}, "
                "so the effective workflow denylist does not protect it."
            )
    for group in data.get("hooks") or []:
        if not isinstance(group, dict) or group.get("event") != "PreToolUse":
            continue
        for handler in group.get("handlers") or []:
            argv = (handler or {}).get("argv") or []
            if not argv or RUN_COMMAND_GUARD not in str(argv[0]):
                continue
            # The declaration is not the control. internal/hooks/exec.go
            # resolveProgram resolves a relative argv[0] against .mivia/, so
            # check the file the config actually names: asserting only the
            # string in argv let the guard be deleted with every gate green.
            # A guard bound to another tool is not this guard. parseMatcher
            # in internal/hooks/config.go treats an absent or empty matcher as
            # match-all, so only a non-empty matcher has to be checked.
            matcher = group.get("matcher")
            if matcher is not None and not isinstance(matcher, str):
                fail(
                    f"{rel}: PreToolUse matcher must be a string; "
                    f"internal/hooks/config.go refuses {matcher!r}, and "
                    f"internal/hooksession downgrades that to a warning, "
                    f"which drops every lifecycle hook in this config."
                )
            # Only nil and "" are match-all in parseMatcher. A
            # whitespace-only pattern compiles and matches nothing.
            if isinstance(matcher, str) and matcher != "":
                try:
                    bound = re.search(matcher, "run_command") is not None
                except re.error:
                    fail(f"{rel}: PreToolUse matcher {matcher!r} does not compile.")
                if not bound:
                    fail(
                        f"{rel}: the PreToolUse guard is bound to matcher "
                        f"{matcher!r}, which does not match 'run_command'. The "
                        f"hook then never fires on the tool it exists to gate."
                    )
            if (handler or {}).get("on_timeout") != "block":
                fail(
                    f"{rel}: the PreToolUse guard has on_timeout="
                    f"{(handler or {}).get('on_timeout')!r}. A guard that fails "
                    f"open on timeout is not a control."
                )
            program = (ROOT / ".mivia" / str(argv[0])).resolve()
            if not program.is_file():
                fail(
                    f"{rel}: the PreToolUse handler names {argv[0]!r}, which "
                    f"does not exist. The hook then fails at run time, and the "
                    f"never-bypass-Git-hooks layer AGENTS.md names is absent."
                )
            if not os.access(program, os.X_OK):
                fail(f"{rel}: {argv[0]!r} is not executable, so the hook cannot run.")
            return
    fail(
        f"{rel}: no PreToolUse handler runs {RUN_COMMAND_GUARD}. AGENTS.md "
        f"names it as one of the three layers enforcing the never-bypass-Git-"
        f"hooks rule, and no other check asserts the declaration is present."
    )


# The session-tool catalog in internal/clichat/session_tool_catalog.go is the
# single source of truth for the dispatcher-owned tools every root binding
# advertises: the pinned wire tools[] array (advertisedToolSpecs) ships the
# catalog as a tail after the core block, with load_tools gated on the binding
# deferring something. [tools] core can never defer any of them, because the
# tier split is computed from the pre-scope base BEFORE the dispatcher
# registers them onto the scoped execution registry (plan
# tools-advertising/01) - so a prompt may name them freely. Deriving the set
# here keeps the gate in lockstep with the Go side: adding or renaming a
# session tool in the catalog automatically updates the exemption, and a
# catalog that stops parsing fails closed instead of passing silently.


def session_tool_catalog_names() -> set[str]:
    path = ROOT / "internal" / "clichat" / "session_tool_catalog.go"
    if not path.is_file():
        fail("missing internal/clichat/session_tool_catalog.go")
    body = path.read_text(encoding="utf-8")
    block = re.search(r"sessionToolCatalog\s*=\s*\[\]sessionToolSpec\{(.*?)\n\}", body, re.S)
    if not block:
        fail("could not parse sessionToolCatalog in internal/clichat/session_tool_catalog.go")
    names = set(re.findall(r'Name:\s*"([a-z_]+)"', block.group(1)))
    if not names:
        fail("sessionToolCatalog parsed to an empty name set")
    return names


# read_skill_resource is exempt alongside the catalog: it is injected per
# skill activation (injectSkillResourceTool in
# internal/clichat/skill_resource_tool.go) into a skill-scoped clone, outside any
# core/deferred decision the tier split makes, so no root binding can defer it
# either.
NON_DEFERRABLE_TOOLS = session_tool_catalog_names() | {"read_skill_resource"}


def workspace_tool_names() -> set[str]:
    """The workspace tool catalogue from AllToolNames in internal/tools/names.go.

    AllToolNames mixes string literals with exported constants
    (MultiEditToolName and friends), so the constants are resolved from their own
    declarations rather than skipped. A name missed here would be treated as "not
    a tool", which would let a prompt naming a deferred tool pass unnoticed - the
    exact failure this gate exists to catch.
    """
    names_go = ROOT / "internal" / "tools" / "names.go"
    if not names_go.is_file():
        return set()
    block = re.search(r"func AllToolNames\(\).*?\n\}", names_go.read_text(encoding="utf-8"), re.S)
    if not block:
        return set()
    entries = block.group(0)
    found = set(re.findall(r'"([a-z_]+)"', entries))
    for const in sorted(set(re.findall(r"\b([A-Z][A-Za-z]*ToolName)\b", entries))):
        for go_file in sorted((ROOT / "internal" / "tools").glob("*.go")):
            hit = re.search(
                r"const\s+" + re.escape(const) + r"\s*=\s*\"([a-z_]+)\"",
                go_file.read_text(encoding="utf-8"),
            )
            if hit:
                found.add(hit.group(1))
                break
        else:
            fail(f"could not resolve tool-name constant {const} in internal/tools")
    return found


def check_agents_directory() -> list[Path]:
    """Ensure .agents/agents exists and contains at least one *.md definition (fail-closed)."""
    agents_dir = ROOT / ".agents" / "agents"
    if not agents_dir.is_dir():
        fail(".agents/agents: directory missing (fail-closed check)")
    agent_files = [a for a in sorted(agents_dir.glob("*.md")) if a.name != "README.md"]
    if not agent_files:
        fail(".agents/agents: expected at least one *.md agent definition")
    return agent_files


def model_facing_prompts() -> list[tuple[str, str]]:
    """Prose the model is instructed by, as (source, text) pairs.

    Only instructions count, never declarations. An agent listing a deferred tool
    in tools = [...] is fine: the declaration bounds authority, deferral only
    withholds the schema, and the model can still reach it via load_tools. A
    prompt SENTENCE telling the model to use it is the defect, because that
    instruction cannot be followed on the turn it is read.

    Both sources are read because a workspace agent's own system_prompt
    supersedes the compiled default for the session it binds.
    """
    out = []
    for literal in re.findall(r"`([^`]*)`", text("internal/clichat/prompt.go")):
        out.append(("internal/clichat/prompt.go", literal))
    agent_files = check_agents_directory()
    for agent in agent_files:
        body = agent.read_text(encoding="utf-8")
        rel = str(agent.relative_to(ROOT))
        if body.startswith("---\n"):
            end = body.find("\n---\n", 4)
            if end != -1:
                prompt_body = body[end + len("\n---\n"):].strip()
                if prompt_body:
                    out.append((rel, prompt_body))
    return out


def makefile_defines_target(makefile: str, target: str) -> bool:
    """True when `target` opens a rule that has prerequisites or a recipe.

    Make allows several targets on one rule line, so the name may appear
    anywhere before the colon. A `.PHONY:` line is excluded: it declares a
    target, it does not define one.
    """
    lines = makefile.split("\n")
    for index, line in enumerate(lines):
        if line.startswith("\t") or ":" not in line:
            continue
        # A comment is prose, not a rule. The word `verify` inside
        # `# verifier-integration is no longer a verify prerequisite` used to
        # satisfy this check, so deleting the whole verify: rule passed while
        # `make verify` printed "Nothing to be done".
        if line.lstrip().startswith("#"):
            continue
        head, _, rest = line.partition(":")
        if "#" in head:
            continue
        if head.startswith(".PHONY") or head.startswith("."):
            continue
        if target not in head.split():
            continue
        # `verify := x`, `verify ::= x` and `verify ?= x` are assignments. The
        # partition on ":" leaves head ending in the operator's first half.
        if head.rstrip().endswith(("=", "!", "?", "+")):
            continue
        prereqs = rest.strip()
        if prereqs.startswith(";"):
            return bool(prereqs[1:].strip())
        if prereqs.startswith("=") or prereqs.startswith(":="):
            continue  # `verify := x` seen as head "verify " rest "= x"
        # A target-specific variable (`verify: CFLAGS=-g`) sets a variable for
        # a rule defined elsewhere; on its own it defines nothing.
        if prereqs and not _is_target_specific_variable(prereqs):
            return True
        for following in lines[index + 1 :]:
            if following.startswith("\t"):
                return True  # has a recipe
            if following.lstrip().startswith("#"):
                continue  # a comment between target and recipe is still one rule
            if following.strip():
                break
    return False


def _is_target_specific_variable(prereqs: str) -> bool:
    """True for `NAME=value` / `NAME := value` and nothing else."""
    return bool(re.match(r"^[A-Za-z_][A-Za-z0-9_]*\s*[:+?]?=", prereqs))


LOCKED_LIST_HEAD = "Additional tools below are authorized"


def check_locked_list_excludes_core(config_path: Path, core: set[str]) -> None:
    """No tool in [tools] core may be advertised as locked in a prompt.

    A core tool is always advertised. Telling the model to call load_tools for
    one costs the same wasted turn as deferring a prompted tool, in the other
    direction.
    """
    try:
        import tomllib
    except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
        return
    with config_path.open("rb") as handle:
        data = tomllib.load(handle)
    prompt = ((data.get("chat") or {}).get("system_prompt")) or ""
    if LOCKED_LIST_HEAD not in prompt:
        return
    tail = prompt.split(LOCKED_LIST_HEAD, 1)[1]
    for line in tail.split("\n"):
        stripped = line.strip()
        if not stripped.startswith("- "):
            continue
        name = stripped[2:].split(":", 1)[0].strip()
        if name in core:
            fail(
                f"{rel_to_root(config_path)}: [chat] system_prompt lists "
                f"{name!r} as a locked tool, but [tools] core advertises it "
                f"already. The model is told to load_tools something it can "
                f"call, which wastes the same turn a deferred prompted tool "
                f"does."
            )


def agent_core_override(body: str) -> set[str] | None:
    """The agent's own tools_core list, or None when it declares no override.

    internal/config/agents.go lets one agent replace the global core tier. The
    global-only check passed while such an agent's prompt named tools its own
    core deferred - the same defect one scope down.
    """
    block = body.split("---")
    if len(block) < 3:
        return None
    names: list[str] = []
    in_key = False
    for line in block[1].split("\n"):
        if re.match(r"^tools_core\s*:", line):
            in_key = True
            inline = line.split(":", 1)[1].strip()
            if inline.startswith("["):
                return {
                    n.strip().strip("\"'")
                    for n in inline.strip("[]").split(",")
                    if n.strip()
                }
            continue
        if in_key:
            stripped = line.strip()
            if stripped.startswith("- "):
                names.append(stripped[2:].strip().strip("\"'"))
                continue
            if stripped:
                break
    return set(names) if in_key else None


def check_core_tier_covers_prompted_tools() -> None:
    """Every tool a system prompt tells the model to use must be in [tools] core.

    Deferring a prompted tool is a defect, not a preference: the prompt orders a
    call whose schema was withheld, so the model burns a turn discovering
    load_tools before it can comply - every session, for as long as the mismatch
    stands. Plan tools/07 was rejected on exactly this, and the compiled default
    prompt still names every orchestration tool, which is safe only because those
    are non-deferrable.
    """
    config_path = ROOT / ".mivia" / "mivia.toml"
    if not config_path.is_file():
        return
    # Parse the config, do not pattern-match it. The regex here read only
    # double-quoted entries, and this repository's own [tools] core uses TOML
    # single quotes, so `core` came back empty and the whole check below - the
    # unknown-tool rule and the prompted-deferred-tool rule - was dead code
    # that reported ok on a tree with ten deferred tools.
    try:
        import tomllib
    except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
        return
    with config_path.open("rb") as handle:
        data = tomllib.load(handle)
    tools_table = data.get("tools")
    if not isinstance(tools_table, dict) or "core" not in tools_table:
        return  # feature inert: nothing is deferred, nothing to check
    raw = tools_table["core"]
    if not isinstance(raw, list) or not all(isinstance(item, str) for item in raw):
        fail(
            ".mivia/mivia.toml [tools] core must be a list of tool-name strings; "
            "a shape this gate cannot read must fail, not pass silently"
        )
    core = {item for item in raw if item}
    if not core:
        fail(
            ".mivia/mivia.toml [tools] core is declared but empty, which defers "
            "every tool. Remove the key to disable the tier split instead"
        )

    known = workspace_tool_names()
    if not known:
        fail(
            "could not read the tool catalogue from internal/tools/names.go; the "
            "core-tier check cannot run, and passing silently would defeat it"
        )
    unknown = core - known
    if unknown:
        fail(
            f".mivia/mivia.toml [tools] core names unknown tool(s) {sorted(unknown)}; "
            f"core is intersected with the agent's effective tools, so a typo "
            f"silently defers the tool it was meant to keep"
        )
    deferred = known - core - NON_DEFERRABLE_TOOLS
    # No early return on an empty deferred set: that is exactly the state
    # where every entry in a hand-maintained "locked tools" list is wrong,
    # and check_locked_list_excludes_core below must still run.
    for rel, body in model_facing_prompts():
        # An agent may replace the global tier with its own tools_core
        # (internal/config/agents.go). Checking only the global set let such an
        # agent's prompt name tools its OWN core defers - the same defect one
        # scope down, invisible to a global-only check.
        source = ROOT / rel
        override = (
            agent_core_override(source.read_text(encoding="utf-8"))
            if source.is_file()
            else None
        )
        # `is not None`, not truthiness: ToolsCore is *[]string in
        # internal/config/agents_parse.go, so an explicit empty list is an
        # override that defers EVERY tool, not an absent one.
        agent_deferred = (
            (known - override - NON_DEFERRABLE_TOOLS)
            if override is not None
            else deferred
        )
        scope = (
            "its own tools_core"
            if override is not None
            else "[tools] core in .mivia/mivia.toml"
        )
        for tool in sorted(agent_deferred):
            if re.search(r"\b" + re.escape(tool) + r"\b", body):
                fail(
                    f"{rel} instructs the model to use {tool!r}, but {scope} "
                    f"defers it. A prompted tool whose schema is withheld costs "
                    f"a wasted turn every session (plan tools/07 was rejected "
                    f"on this). Add {tool!r} to core, or stop naming it in the "
                    f"prompt."
                )

    # The inverse direction. A prompt that tells the model to load_tools a
    # tool that is already core costs the same wasted turn a deferred
    # prompted tool does. This must run unconditionally, not only when
    # `deferred` is non-empty - a core set covering everything deferrable is
    # exactly the state where every "locked" entry in a hand-copied prompt
    # list is guaranteed wrong.
    check_locked_list_excludes_core(config_path, core)


def main() -> None:
    # Prefer declarative list when present.
    required_list = ROOT / ".mivia" / "policy" / "required-paths.json"
    if required_list.is_file():
        try:
            data = json.loads(required_list.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            fail(f"invalid required-paths.json: {exc}")
        paths = data.get("paths")
        if not isinstance(paths, list) or not paths:
            fail("required-paths.json must contain a non-empty paths list")
        missing = [p for p in paths if not (ROOT / p).exists()]
        if missing:
            fail("missing required paths:\n  " + "\n  ".join(missing))
    else:
        for rel in [
            "AGENTS.md",
            ".agents/INDEX.md",
            ".mivia/policy/commit-message.json",
            ".mivia/policy/agent-hook-bypass.json",
            "docs/OWNERS.yaml",
            "semgrep/agent-standards.yml",
            "scripts/verify_agent_config.py",
            "scripts/secret_scan.py",
            "scripts/check_docs_ownership.py",
            "scripts/agent_hook_guard.py",
            "scripts/run_agent_hook_guard.sh",
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
            "Makefile",
        ]:
            require_file(rel)

    # Rules surface
    rules = list((ROOT / ".agents" / "rules").glob("*.md")) if (ROOT / ".agents" / "rules").is_dir() else []
    if not rules:
        fail(".agents/rules: expected at least one *.md rule file")

    # Executable hooks
    for rel in [
        ".githooks/pre-commit",
        ".githooks/pre-push",
        ".githooks/commit-msg",
        ".githooks/prepare-commit-msg",
        ".githooks/post-commit",
        "scripts/git-hooks/pre-commit",
        "scripts/git-hooks/pre-push",
        "scripts/git-hooks/commit-msg",
        "scripts/git-hooks/prepare-commit-msg",
        "scripts/git-hooks/post-commit",
        "scripts/git-hooks/run_without_git_env",
        "scripts/install_git_hooks.sh",
        "scripts/run_agent_hook_guard.sh",
        "scripts/agent_hook_guard.py",
        "scripts/secret_scan.py",
        "scripts/check_docs_ownership.py",
        "scripts/verify_agent_config.py",
    ]:
        if (ROOT / rel).is_file():
            require_exec(rel)

    # AGENTS.md product identity
    agents = text("AGENTS.md")
    if "mivia" not in agents.casefold():
        fail("AGENTS.md must identify product mivia")
    if "docs/OWNERS.yaml" not in agents:
        fail("AGENTS.md must reference docs/OWNERS.yaml")
    if "scripts/verify_agent_config.py" not in agents and "make verify" not in agents:
        fail("AGENTS.md must reference verify path (scripts/verify_agent_config.py or make verify)")
    if re.search(r"Binary:\*\* `mivia-agent`|binary:\s*`mivia-agent`", agents, re.I):
        fail("AGENTS.md must not set product binary to mivia-agent")

    # INDEX.md hooks/surface pointers
    if (ROOT / ".agents" / "INDEX.md").is_file():
        index = text(".agents/INDEX.md")
        for needle in [".githooks", "semgrep", "docs/OWNERS.yaml", "scripts/"]:
            if needle not in index:
                fail(f".agents/INDEX.md: missing {needle}")

    # Policies
    commit = json.loads(text(".mivia/policy/commit-message.json"))
    for key in ("types", "scopes", "maxSubjectLength"):
        if key not in commit:
            fail(f"commit-message.json missing {key}")
    if commit.get("maxSubjectLength") != 72:
        fail("commit-message.json maxSubjectLength must be 72")
    if commit.get("requireScope") is not True:
        fail("commit-message.json requireScope must be true")
    scopes = commit.get("scopes") or []
    if not isinstance(scopes, list) or not scopes:
        fail("commit-message.json scopes must be a non-empty list")
    if "setup" in scopes:
        fail("commit-message.json must not include vague scope 'setup' (use ai/hooks/quality/build)")
    guide = commit.get("scopeGuide") or {}
    if not isinstance(guide, dict):
        fail("commit-message.json scopeGuide must be an object")
    for scope in scopes:
        if scope not in guide:
            fail(f"commit-message.json scopeGuide missing entry for {scope}")
    # commit-msg hook must surface scopes on failure
    commit_hook = text("scripts/git-hooks/commit-msg")
    for needle in ("allowed types:", "allowed scopes:", "scopeGuide", "unknown scope"):
        if needle not in commit_hook:
            fail(f"scripts/git-hooks/commit-msg: missing agent-facing {needle!r}")

    # PR-title policy (delivery PR metadata). Mirrors the delivery engine's
    # loader (internal/workflows/delivery/prtitle.go): the shipped
    # feature-delivery workflow declares this file, so the gate requires it,
    # parses it as TOML, and rejects malformed rule shapes with a clear line.
    pr_title_path = ROOT / ".mivia" / "policy" / "pr-title.toml"
    if not pr_title_path.is_file():
        fail(".mivia/policy/pr-title.toml: missing (the shipped feature-delivery workflow declares it)")
    try:
        import tomllib
    except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
        fail(".mivia/policy/pr-title.toml: cannot validate on Python < 3.11 (tomllib unavailable)")
    try:
        with pr_title_path.open("rb") as handle:
            pr_title = tomllib.load(handle)
    except tomllib.TOMLDecodeError as exc:
        fail(f".mivia/policy/pr-title.toml: not valid TOML: {exc}")
    title_rule = pr_title.get("title")
    summary_rule = pr_title.get("summary")
    if not isinstance(title_rule, dict):
        fail(".mivia/policy/pr-title.toml: missing [title] table")
    if not isinstance(summary_rule, dict):
        fail(".mivia/policy/pr-title.toml: missing [summary] table")
    pattern = title_rule.get("pattern")
    if not isinstance(pattern, str) or not pattern:
        fail(".mivia/policy/pr-title.toml: title.pattern must be a non-empty string")
    pr_scopes = title_rule.get("scopes")
    if not isinstance(pr_scopes, list) or not pr_scopes:
        fail(".mivia/policy/pr-title.toml: title.scopes must be a non-empty list")
    for scope in pr_scopes:
        if not isinstance(scope, str) or not scope.strip():
            fail(f".mivia/policy/pr-title.toml: title.scopes must contain only non-empty strings, got {scope!r}")
    # pr-title.toml's own header says it "mirrors the repo's own commit
    # convention" - enforce that literally, not just by prose. A scope valid
    # for a commit but rejected for its PR title (or vice versa) sends a
    # repair loop into an unwinnable retry: this is the live bug that burned
    # run wfr-W55NJLGNPF4HVM63's whole delivery-repair budget on scope
    # "events", which commit-message.json allowed and pr-title.toml did not.
    pr_scope_set = set(pr_scopes)
    commit_scope_set = set(scopes)
    if pr_scope_set != commit_scope_set:
        missing = sorted(commit_scope_set - pr_scope_set)
        extra = sorted(pr_scope_set - commit_scope_set)
        detail = []
        if missing:
            detail.append(f"missing from pr-title.toml: {missing}")
        if extra:
            detail.append(f"present in pr-title.toml but not commit-message.json: {extra}")
        fail(
            "pr-title.toml title.scopes must match commit-message.json scopes exactly "
            f"({'; '.join(detail)})"
        )
    pr_types_match = re.search(r"\(\?P<type>([^)]+)\)", pattern)
    if not pr_types_match:
        fail(".mivia/policy/pr-title.toml: title.pattern must name a (?P<type>...) group")
    pr_types = pr_types_match.group(1).split("|")
    commit_types = commit.get("types") or []
    if set(pr_types) != set(commit_types):
        missing = sorted(set(commit_types) - set(pr_types))
        extra = sorted(set(pr_types) - set(commit_types))
        detail = []
        if missing:
            detail.append(f"missing from pr-title.toml pattern: {missing}")
        if extra:
            detail.append(f"present in pr-title.toml pattern but not commit-message.json: {extra}")
        fail(
            "pr-title.toml title.pattern's type group must match commit-message.json types exactly "
            f"({'; '.join(detail)})"
        )
    # Positive-integer bounds with min <= max, when present. The delivery
    # engine treats an absent (or zero/negative) bound as UNLIMITED, so a
    # policy may omit a field; a present value must be a sane positive
    # integer. bool is excluded because it subclasses int.
    for table_name, rule, pairs in (
        ("title", title_rule, [("min_chars", "max_chars")]),
        ("summary", summary_rule, [("min_chars", "max_chars"), ("min_sentences", "max_sentences")]),
    ):
        for lo_key, hi_key in pairs:
            lo = rule.get(lo_key)
            hi = rule.get(hi_key)
            for key, val in ((lo_key, lo), (hi_key, hi)):
                if val is None:
                    continue
                if not isinstance(val, int) or isinstance(val, bool) or val <= 0:
                    fail(f".mivia/policy/pr-title.toml: {table_name}.{key} must be a positive integer, got {val!r}")
            if lo is not None and hi is not None and lo > hi:
                fail(f".mivia/policy/pr-title.toml: {table_name}.{lo_key} ({lo}) exceeds {table_name}.{hi_key} ({hi})")

    bypass = json.loads(text(".mivia/policy/agent-hook-bypass.json"))
    if bypass.get("version") != 1:
        fail("agent-hook-bypass.json version must be 1")
    if "Do not bypass Git hooks" not in str(bypass.get("correctiveMessage", "")):
        fail("agent-hook-bypass.json missing corrective message")
    flags = bypass.get("blockedFlags") or []
    if "--no-verify" not in flags:
        fail("agent-hook-bypass.json must block --no-verify")

    owners = text("docs/OWNERS.yaml")
    if "topics:" not in owners:
        fail("docs/OWNERS.yaml must define topics:")

    check_hook_events()

    # Hook wiring
    install = text("scripts/install_git_hooks.sh")
    if "core.hooksPath" not in install or ".githooks" not in install:
        fail("install_git_hooks.sh must set core.hooksPath .githooks")
    for hook in ["pre-commit", "pre-push", "commit-msg", "prepare-commit-msg", "post-commit"]:
        wrapper = text(f".githooks/{hook}")
        if f"scripts/git-hooks/{hook}" not in wrapper:
            fail(f".githooks/{hook}: must exec scripts/git-hooks/{hook}")

    pre_commit = text("scripts/git-hooks/pre-commit")
    for needle in [
        "scripts/verify_agent_config.py",
        "scripts/secret_scan.py --staged",
        "scripts/check_docs_ownership.py",
        "gofmt -w",
        "git diff --check --cached",
        "mivia-precommit-summary",
        "Quality: pre-commit passed",
    ]:
        if needle not in pre_commit:
            fail(f"scripts/git-hooks/pre-commit: missing {needle}")

    pre_push = text("scripts/git-hooks/pre-push")
    for needle in [
        "scripts/verify_agent_config.py",
        "scripts/secret_scan.py",
        "scripts/check_docs_ownership.py",
        "cmd/mivia",
        "go test ./...",
        "go vet ./...",
    ]:
        if needle not in pre_push:
            fail(f"scripts/git-hooks/pre-push: missing {needle}")

    if "run_without_git_env" not in pre_commit or "run_without_git_env" not in pre_push:
        fail("Git verification hooks must run children without the outer Git environment")

    prepare = text("scripts/git-hooks/prepare-commit-msg")
    if "mivia-precommit-summary" not in prepare:
        fail("prepare-commit-msg must use mivia-precommit-summary")

    post = text("scripts/git-hooks/post-commit")
    if re.search(r"https?://", post):
        fail("post-commit must not perform network I/O")

    # Makefile targets referenced by AGENTS / local flow
    if not (ROOT / "Makefile").is_file():
        fail("Makefile: missing")
    makefile = text("Makefile")
    for target in [
        "install-hooks",
        "verify",
        "pre-commit",
        "pre-push",
        "secret-scan",
        "docs-check",
        "semgrep",
        "test",
        "build",
    ]:
        # A real rule, not a mention. The old test was two proxies joined by
        # `or`: a regex that let `verify-fast:` satisfy `verify`, and a bare
        # substring that any word in the help text satisfied. Deleting a whole
        # recipe left the gate green while `make verify` silently skipped the
        # target. Require the name to open a rule, and require that rule to
        # carry a recipe line or prerequisites.
        if not makefile_defines_target(makefile, target):
            fail(
                f"Makefile: no rule defines target {target}. A .PHONY listing "
                f"or a mention in the help text is not a rule: `make {target}` "
                f"would print \"Nothing to be done\" and every gate it runs "
                f"would be skipped."
            )
    if "cmd/mivia" not in makefile:
        fail("Makefile: build must target cmd/mivia")

    # Semgrep baseline
    if (ROOT / "semgrep" / "agent-standards.yml").is_file():
        sg = text("semgrep/agent-standards.yml")
        if "rules:" not in sg:
            fail("semgrep/agent-standards.yml: missing rules")
        for rule_id in [
            "mivia.generic.no-wildcard-bash-allow",
            "mivia.generic.no-semgrep-suppression",
            "mivia.generic.no-git-hook-bypass-in-agent-config",
        ]:
            if rule_id not in sg:
                fail(f"semgrep/agent-standards.yml: missing {rule_id}")

    # .agents/skills is the only workspace skill home. The compiled mivia
    # binary loads it at runtime: internal/workspace.SkillsDir(root) returns
    # <root>/.agents/skills.
    #
    check_no_dead_skill_tree(ROOT)
    # check_skill_dir owns the missing-directory guard, so both entry points
    # inherit it. A copy here would drift from the one in verify_skill_tree.
    check_skill_dir(ROOT / ".agents" / "skills")
    check_claude_skill_aliases(ROOT)

    check_workflow_self_protection()
    check_agents_directory()
    check_core_tier_covers_prompted_tools()

    print("verify_agent_config: ok")


if __name__ == "__main__":
    main()
