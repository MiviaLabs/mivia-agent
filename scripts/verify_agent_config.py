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


ROOT = Path(__file__).resolve().parents[1]

# Mirrors SanitizeModelFacingText(description, 200) in internal/skills/loader.go.
# A longer description is truncated mid-sentence in the model-facing skill surface,
# which degrades skill selection with no other signal that it happened.
SKILL_DESCRIPTION_MAX = 200

# Mirrors trigger caps in internal/skills/loader.go.
SKILL_TRIGGER_MAX = 64       # per trigger
SKILL_TRIGGERS_JOINED_MAX = 400  # joined block

# Mirrors knownSkillKeys in internal/skills/loader.go. Keep the two in sync:
# the loader hard-errors on anything else, so a key accepted here but rejected
# there would pass `make verify` and then fail at runtime.
SKILL_KNOWN_KEYS = {
    "name", "description", "triggers", "user-invocable", "argument-hint",
    "short-description", "tools",
}


def frontmatter_keys(body: str) -> list[str]:
    """Top-level keys in a SKILL.md frontmatter block.

    Mirrors the subset grammar in internal/skills/frontmatter.go: indented
    lines belong to a block sequence, comments and blanks are skipped.
    """
    lines = body.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if not lines or lines[0].strip() != "---":
        return []
    keys = []
    for line in lines[1:]:
        stripped = line.strip()
        if stripped == "---":
            break
        if not stripped or stripped.startswith("#") or line[:1] in (" ", "\t"):
            continue
        if ":" in stripped:
            keys.append(stripped.split(":", 1)[0].strip())
    return keys


def split_flow_items(inner: str) -> list[str]:
    """Split a flow sequence inner string with quote awareness.

    Matches the Go splitFlowSequence behaviour: commas inside single or
    double quotes are preserved.
    """
    inner = inner.strip()
    if not inner:
        return []
    items = []
    current: list[str] = []
    in_single = False
    in_double = False
    for ch in inner:
        if ch == '"' and not in_single:
            in_double = not in_double
            current.append(ch)
        elif ch == "'" and not in_double:
            in_single = not in_single
            current.append(ch)
        elif ch == "," and not in_single and not in_double:
            item = "".join(current).strip().strip("\"'")
            if item:
                items.append(item)
            current = []
        else:
            current.append(ch)
    item = "".join(current).strip().strip("\"'")
    if item or items:
        items.append(item)
    return items


def fail(msg: str) -> None:
    print(f"verify_agent_config: {msg}", file=sys.stderr)
    raise SystemExit(1)


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
    agents_dir = ROOT / ".agents" / "agents"
    if agents_dir.is_dir():
        for agent in sorted(agents_dir.glob("*.md")):
            if agent.name == "README.md":
                continue
            body = agent.read_text(encoding="utf-8")
            rel = str(agent.relative_to(ROOT))
            if body.startswith("---\n"):
                end = body.find("\n---\n", 4)
                if end != -1:
                    prompt_body = body[end + len("\n---\n"):].strip()
                    if prompt_body:
                        out.append((rel, prompt_body))
    return out


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
    match = re.search(
        r"^\s*core\s*=\s*\[(.*?)\]", config_path.read_text(encoding="utf-8"), re.S | re.M
    )
    if not match:
        return  # feature inert: nothing is deferred, nothing to check
    core = set(re.findall(r'"([^"]+)"', match.group(1)))
    if not core:
        return

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
    if not deferred:
        return

    for rel, body in model_facing_prompts():
        for tool in sorted(deferred):
            if re.search(r"\b" + re.escape(tool) + r"\b", body):
                fail(
                    f"{rel} instructs the model to use {tool!r}, but [tools] core "
                    f"in .mivia/mivia.toml defers it. A prompted tool whose schema "
                    f"is withheld costs a wasted turn every session (plan tools/07 "
                    f"was rejected on this). Add {tool!r} to core, or stop naming "
                    f"it in the prompt."
                )


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
        # accept "target:" or "target " in .PHONY / recipes
        if not re.search(rf"(?m)^[a-zA-Z0-9_.-]*{re.escape(target)}[a-zA-Z0-9_.-]*\s*:", makefile) and target not in makefile:
            fail(f"Makefile: missing target {target}")
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

    # Skill frontmatter when skills exist. .agents/skills is the shared
    # cross-tool mirror; .mivia/skills is the copy the compiled mivia binary
    # itself loads at runtime (internal/workspace.SkillsDir). Both are checked
    # so the two mirrors cannot silently diverge.
    skill_dirs = [d for d in (ROOT / ".agents" / "skills", ROOT / ".mivia" / "skills") if d.is_dir()]
    for skills_dir in skill_dirs:
        for skill_path in sorted(skills_dir.glob("*/SKILL.md")):
            body = skill_path.read_text(encoding="utf-8")
            name = skill_path.parent.name
            if not body.lstrip().startswith("---"):
                fail(f"{skill_path.relative_to(ROOT)}: missing YAML frontmatter")
            if f"name: {name}" not in body and f'name: "{name}"' not in body:
                fail(f"{skill_path.relative_to(ROOT)}: frontmatter name must be {name}")
            # Unknown keys are rejected by internal/skills/loader.go at load time.
            # Catch them here so `make verify` fails before the loader does.
            for key in frontmatter_keys(body):
                if key not in SKILL_KNOWN_KEYS:
                    fail(
                        f"{skill_path.relative_to(ROOT)}: unknown frontmatter key "
                        f"{key!r}; recognised: {sorted(SKILL_KNOWN_KEYS)} "
                        f"(rejected by internal/skills/loader.go)"
                    )
            # Check description length.
            for line in body.splitlines():
                if line.startswith("description:"):
                    description = line.split(":", 1)[1].strip()
                    if len(description) > SKILL_DESCRIPTION_MAX:
                        fail(
                            f"{skill_path.relative_to(ROOT)}: description is "
                            f"{len(description)} chars, max {SKILL_DESCRIPTION_MAX} "
                            f"(silently truncated by internal/skills/loader.go)"
                        )
                    break
            # Check trigger entries are non-empty and joined block within cap.
            in_triggers = False
            trigger_items = []
            for line in body.splitlines():
                stripped = line.strip()
                if stripped == "triggers:" or stripped.startswith("triggers: ["):
                    if stripped == "triggers:":
                        in_triggers = True
                    elif stripped.startswith("triggers: ["):
                        # Flow sequence: extract items. Handle trailing content after ].
                        inner = stripped[len("triggers: ["):]
                        # Find the closing bracket, handling trailing whitespace/comments.
                        bracket_idx = inner.find("]")
                        if bracket_idx >= 0:
                            inner = inner[:bracket_idx]
                        # Also strip any trailing comment before the bracket
                        # (already handled by find("]") above).
                        inner = inner.strip()
                        for part in split_flow_items(inner):
                            item = part.strip().strip("\"'")
                            if item:
                                trigger_items.append(item)
                    continue
                if in_triggers:
                    # Comments and blank lines are skipped in the Go parser
                    # but stay in the block - handle them the same way.
                    if stripped == "" or stripped.startswith("#"):
                        continue
                    if stripped.startswith("- "):
                        item = stripped[2:].strip()
                        if item:
                            trigger_items.append(item)
                    elif line.startswith("  ") or line.startswith("\t"):
                        # Still in block sequence (indented continuation).
                        continue
                    else:
                        in_triggers = False
            if trigger_items:
                joined = "\n".join(trigger_items)
                if len(joined) > SKILL_TRIGGERS_JOINED_MAX:
                    fail(
                        f"{skill_path.relative_to(ROOT)}: triggers joined block is "
                        f"{len(joined)} chars, max {SKILL_TRIGGERS_JOINED_MAX} "
                        f"(silently truncated by internal/skills/loader.go)"
                    )
                for item in trigger_items:
                    if not item:
                        fail(
                            f"{skill_path.relative_to(ROOT)}: trigger entry is empty"
                        )

    check_core_tier_covers_prompted_tools()

    print("verify_agent_config: ok")


if __name__ == "__main__":
    main()
