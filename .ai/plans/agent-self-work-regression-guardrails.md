# Agent Self-Work: Regression Guardrails

**Status:** Proposed (v2, revised after adversarial review)
**Author:** mivia self-work session
**Date:** 2026-07-27

---

## 1. Executive Summary

Agents working on this repo produce **fix commits at a high rate** — 30+ in the last batch. The recurring failure mode: **new features break existing invariants** that have no explicit regression test. The current safeguard stack (pre-commit hooks, semgrep, contract verifiers, generic-surface tests, unit tests) catches format, branding, security, and language-bias issues but is **silent on behavioral regressions**.

Evidence from recent history:

| Bug | Introduced by | Caught by existing tests? | Fixed with new test? |
|-----|--------------|--------------------------|----------------------|
| Bridge drain replaced by EventBus (blank TUI) | `feat(cli): consume EventBus events` | ❌ No — EventBus tested, bridge path not | ✅ `TestBridgePathAssistantToolsAndFinish` added in fix |
| Poll chain dies on quiet periods (UI freeze) | TUI revamp feat | ❌ No | ✅ `TestTuiTickMsgAlwaysRequeuesPoll`, `TestTuiTickMsgDrainsBridge` added in fix |
| Tool redaction on by default | privacy feature | ❌ No — wrong default | ✅ `TestRedactToolInputDefaultShowsArgs` added in fix |
| read_file offset/limit broken | initial tool impl | ❌ No — edge-case gap | ✅ extended tool tests in fix |

**Pattern:** Fix test is written *in the fix commit*, not before. The next refactor has no guardrail because no test enforces the invariant that was broken.

### Root Cause (not just symptom)

The symptom is "no regression test for the broken invariant." The root cause is that **agents do not systematically consider existing invariants when modifying code** — especially invariants in areas they didn't write. No document, rule, or test gate forces them to check "does my change break something someone else relies on?"

This plan addresses both:

- **Symptom:** Every fix must include a reproduction test (the regression fence)
- **Root cause:** Agents must consult the invariant manifest before touching sensitive areas, and invariant tests must be auto-run during commit

---

## 2. Current State

### What works well

- **Pre-commit hooks** — format, commit message, semgrep (10+ rules), file-size
- **Semgrep** — security, branding, no-TODO, no-git-hook-bypass, no-hardcoded-secrets
- **Generic surface tests** — `generic_surface_test.go`, `prompt_generic_test.go`
- **Unit test coverage** — all 14 packages pass (`go test ./...` clean)
- **Contract verifiers** — `project-runtime.yaml` with file-existence and bash verifiers
- **Structured commits** — `type(scope): subject` with length/format gating

### What's missing

1. **No regression test requirement** — fix commits have no requirement to include a reproduction test
2. **No invariant manifest** — no documented list of "things that must never break" with exact test names
3. **Adversarial review is optional** — `verify-change` / `adversarial` skills exist but are not gated
4. **Agent prompt unchanged** — `.ai/agent-prompt.md` and `.ai/skills/engineering-working-contract/SKILL.md` don't instruct agents to check invariants
5. **No automatic invariant validation** — no Makefile target, no CI gate, no hook auto-detect
6. **No metric for regression safety** — no way to know if the regression fence is working

---

## 3. Adversarial Review Results

Before writing the proposed changes, I challenged the v1 plan from three angles. Here are the concrete findings that shaped v2:

### 3.1 Invariant Test Names Were Wrong

The v1 plan referenced test names that don't exist or are vague:

| Claimed in v1 | Actual test | Problem |
|--------------|-------------|---------|
| `TestSecretPath*` in tools/ | `TestSearchLocalSkipsSecretPaths` in `searcher_test.go` | **Ghost test** — name doesn't exist. Also this tests `search` tool, not `listDir`/`glob` as the invariant claimed. No test exists for `listDir`/`glob` secrecy. |
| `phase1 poll tests` (vague) | `TestTuiTickMsgAlwaysRequeuesPoll`, `TestTuiTickMsgDrainsBridge` | Not actionable — wildcard/non-specific references |
| `delegate/dispatch tests` (vague) | `TestDelegateToolMultiStepTrue` etc. | Not actionable |
| `INV-SEC-2` area as `tools/privacy.go` | Code is in `agent/loop_tools_test.go` | Wrong area mapping |

**Fix:** The manifest must use exact test function names, not wildcards or area references.

### 3.2 Liveness vs Safety Confusion

Invariants like "poll chain never dies" (INV-TUI-2) are **liveness properties** — they assert that something *eventually* happens. Unit tests can only verify **safety properties** — that something *never* happens in the finite states tested. A unit test that calls `pollCmd()` and checks it returns a command cannot prove the chain never dies in production.

**Fix:** Separate invariants into two categories: **Safety** (verifiable now with unit tests) and **Liveness** (verified by integration tests, stress tests, or accepted as residual risk). Liveness invariants must note the limitation and what would constitute real evidence.

### 3.3 SKIP_INVARIANTS Is a Hole

v1 allowed `SKIP_INVARIANTS=1` to bypass the pre-commit invariant check for "quick WIP commits." An agent can trivially set this env var on every commit, making the gate meaningless.

**Fix:** Remove the skip variable. The only bypass is a dedicated pre-commit flag `--no-invariants` that logs to an audit trail (`.git/invariant-bypass-log`). Anyone reviewing the branch will see bypass records.

### 3.4 Only Fixes, Not Features

v1's regression test rule only applies to `fix`-type commits. But the data shows that **features** are what *introduce* the bugs — the `fix` commit only *restores* the invariant. If a feature commit breaks an invariant but isn't caught before merge, the regression test comes too late.

**Fix:** Both `feat` and `fix` commits must run invariant tests for affected areas. A `feat` commit that doesn't break anything passes. A `feat` commit that silently breaks an invariant is caught by the pre-commit gate, not by a later fix.

### 3.5 No Enforcement Mechanism

v1 says "every fix commit must include a regression test" but provides no mechanism to enforce it. It relies on the commit message body containing "Regression: none (trivial)" as self-documentation.

**Fix:** Add a commit-msg hook that checks: if commit type is `fix`, the commit body must contain a line matching `Regression:` followed by a test name or "none (<reason>)". If missing, the hook fails with instructions. This makes the rule mechanical, not aspirational.

### 3.6 Manifest Will Drift

Tests get renamed, split, or moved. The manifest maps invariant IDs to exact test function names. Without a validation step, the manifest will silently rot — entries will reference tests that no longer exist or no longer test what they claim.

**Fix:** Add a `make validate-invariants` target that extracts all `func Test` names from the Go code and cross-references them against the manifest. Any invariant referencing a non-existent test fails.

### 3.7 Phase 3 (Past Audit) Was Vague

v1 said "audit past 30 fix commits" but didn't specify criteria, owner, or triage process.

**Fix:** Remove Phase 3 from the plan. It's not actionable until the infrastructure (invariant manifest, validation targets, commit-msg hook) exists. The audit becomes a natural byproduct of the invariant validation target — run it against the codebase once, see which claimed invariants have no test, and file issues for the gaps.

### 3.8 Agent Prompt Not Updated

v1 didn't update `.ai/agent-prompt.md` or `.ai/skills/engineering-working-contract/SKILL.md` to instruct agents to consult invariants.

**Fix:** Add invariant consultation to the agent prompt and engineering working contract as a required step before modifying code in listed areas.

---

## 4. Proposed Changes

### 4.1 Create `.ai/invariants.md` — System Invariant Manifest

**New file** with exact test names and category (Safety/Liveness).

```markdown
# System Invariants

These are **non-negotiable** properties of the mivia agent that any change must preserve.
Before committing changes that touch the relevant area, run the corresponding invariant
test(s) and confirm they pass.

## Categories

- **Safety** — property that can be verified in a unit test on a finite state set.
  A failing test proves the invariant is violated.
- **Liveness** — property that asserts something *eventually* happens. Unit tests
  cannot fully prove liveness; they can only verify partial behavior. Treat liveness
  invariants as requiring integration/stress tests or accepted residual risk.

## TUI / Rendering

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-TUI-1 | Safety | Bridge drain is the exclusive runtime content source of truth for the TUI | `TestBridgePathAssistantToolsAndFinish` | |
| INV-TUI-2 | Liveness | pollCmd always re-queues itself regardless of data availability (no chain death) | `TestTuiTickMsgAlwaysRequeuesPoll`, `TestPollCmdUsesBridgeNotAdapterOnly` | |
| INV-TUI-3 | Safety | finishStream is idempotent — calling it twice does not produce duplicate blocks | `TestFinishStreamIdempotent`, `TestBridgeDrainNotDoubleProcessed` | |
| INV-TUI-4 | Safety | uiEventMsg always re-queues pollCmd in chat mode | `TestUIEventMsgStepUpdatesDetail`, `TestUIEventMsgErrorSetsStalled` | |
| INV-TUI-5 | Safety | Smoke journey end-to-end completes without panic | `TestTUISmoke_FullJourney` | |
| INV-TUI-6 | Liveness | Tool progress events are visible in TUI during parallel execution (tools don't look hung) | `TestStreamBridgeQueuedRunningDoesNotDoubleCountActiveTools` | |

## Agent Loop

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-AG-1 | Safety | Tool surface Description() and schema JSON validate against OpenAI schema | `TestSearchOpenAISchema`, `generic_surface_test.go` | |
| INV-AG-2 | Safety | run_command presents as last-resort (argv, not shell) | `TestToolSurfacePreferFilesystemOverRunCommand` | |
| INV-AG-3 | Safety | Session.SendUser is not data-racy on Messages field | `TestConcurrentSendUser*` in `chat/` | |
| INV-AG-4 | Safety | Multi-step subagent gets tool access; one-shot does not | `TestDelegateToolMultiStepTrue` | |
| INV-AG-5 | Safety | Tool argument redaction is opt-in, default shows args | `TestRedactToolInputDefaultShowsArgs` | |
| INV-AG-6 | Safety | Multi-step subagent uses tools when handler is multi_step | `TestNewSessionDispatcherRegistersMultiStepHandler` | |

## Security

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-SEC-1 | Safety | `search` tool skips secret paths (credentials, keys, .env, .ssh) | `TestSearchLocalSkipsSecretPaths` | |
| INV-SEC-2 | Safety | `search` tool URL validation blocks private IP literals | `TestSearchURLValidation`, `TestFetchURLBlocksPrivateIPLiterals` | |
| INV-SEC-3 | Safety | No hardcoded API keys or secrets in source | semgrep `mivia.generic.no-hardcoded-secrets` | |
| INV-SEC-4 | Safety | `search` tool skips binaries and symlinks | `TestSearchLocalSkipsBinary`, `TestSearchLocalSkipsSymlinks` | |

## Build / CI

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-BLD-1 | Safety | Module builds, vets, and tests pass | `go build`, `go vet`, `go test ./...` | |
| INV-BLD-2 | Safety | Commit messages follow `type(scope): subject` format ≤72 chars | `scripts/git-hooks/check-commit-subject` | |

## Known Limitations (Liveness Gap)

The following invariants are liveness properties that unit tests alone cannot fully verify:

- **INV-TUI-2** (poll chain never dies): A panic, goroutine leak, or blocked channel
  could silently stop the poll chain. Consider adding a watchdog goroutine that panics
  if no poll event fires within N seconds, or an integration test that runs the TUI
  under scripted I/O for a full turn cycle.
- **INV-TUI-6** (tools don't look hung): Parallel tool execution timing is
  non-deterministic in tests. Validate with manual smoke testing on real model calls.

## Maintenance

- When adding a new invariant, add a row here with an exact test function name.
- When renaming a test, update the invariant row in the same commit.
- Run `make validate-invariants` before committing changes to this file —
  it verifies every referenced test function exists in the codebase.
```

---

### 4.2 Change `.ai/rules/20-agent-quality.md`

**Add these sections after "Mutation Proofs":**

```markdown
## Regression Tests

*Applies to `fix`-type commits only. Feature commits must satisfy "Invariant Verification" below.*

Every fix commit (`type: fix`) must include at least one test that:

1. **Reproduces the bug** — the test MUST fail on the parent commit.
2. **Passes after the fix** is applied.
3. Is committed **in the same commit** (or a fixup squash).
4. Tests at the narrowest meaningful level — unit test preferred, integration only where unavoidable.

**Validation procedure (run, don't assume):**
```bash
# Step 1: Test fails on parent
git stash && git checkout HEAD~1 && go test -run <FixTestName> ./... -count=1
# → must fail. If it passes, the test does not reproduce the bug. Fix the test.

# Step 2: Test passes on fix
git checkout - && git stash pop
go test -run <FixTestName> ./... -count=1
# → must pass.

# Step 3: Record in commit body
# Add a line:
#   Regression: <FixTestName>
# or for trivial fixes:
#   Regression: none (trivial: <reason>)
```

**Enforcement:** The `commit-msg` hook checks that `fix`-type commits contain a
`Regression:` line in the body. If missing, the commit is blocked.

**Exception:** Trivial fixes (typo in comment, dead import removal, formatting-only)
do not require a regression test. Use `Regression: none (trivial: <reason>)` to
document the exception. The reason must be specific — "trivial: comment typo fix", not
just "trivial".

**Weak tests that satisfy the letter but not the spirit:**
- A test that calls a function but asserts nothing about the bug behavior
- A test that passes both before and after the fix (not a true reproduction)
- A test at the wrong abstraction level (integration when unit would reproduce)

If any reviewer can demonstrate that the regression test would pass on the parent
commit, the fix is not acceptable. The validation procedure in Step 1 is mandatory.

## Invariant Verification

Before committing changes to any of these areas, run the corresponding invariant
tests from `.ai/invariants.md`:

| Staged file pattern | Required invariant tests |
|---------------------|-------------------------|
| `internal/cli/` | `-run 'TestBridgePath\|TestPollCmd\|TestBridgeDrain\|TestUIEventMsg\|TestFinishStream\|TestSmoke'` |
| `internal/tools/` | `-run 'TestToolSurface\|TestRedact\|TestSecret\|TestGeneric\|TestSearchOpenAI'` |
| `internal/agent/` | `-run 'TestRedactTool\|TestDelegate\|TestMultiStep\|TestSendUser'` |
| `internal/chat/` | `-run 'TestSendUser\|TestConcurrent\|TestSession'` |
| `internal/config/` | `-run 'TestConfig\|TestRedact'` |

Run:
```bash
go test -run '<TestNameRegex>' ./internal/<pkg>/ -count=1 -timeout=60s
```

If any named invariant test fails, the commit is blocked until the invariant is restored
or the invariant document is formally updated (requires attestation in commit body:
`Invariant-Update: INV-XXX <reason>`).

For **liveness** invariants (noted in `.ai/invariants.md` as "Liveness"), a failing unit
test is sufficient to block. A passing unit test does not guarantee the liveness property
holds — document the residual risk in the commit message.
```

---

### 4.3 Add `make validate-invariants` Target to Makefile

```makefile
# Validate invariant manifest: every referenced test function must exist.
# Extracts func Test names from Go test files and cross-references against
# .ai/invariants.md. Fails if any invariant references a non-existent test.
.PHONY: validate-invariants
validate-invariants:
	@echo "=== Validating invariant manifest ==="
	@bash -c '\
		missing=0; \
		while IFS= read -r line; do \
			case "$$line" in \
				*'`Test'*) \
					testname=$$(echo "$$line" | sed -n "s/.*\`\\(Test[A-Za-z_]*\\)\`.*/\\1/p"); \
					if [ -n "$$testname" ]; then \
						if ! rg -q "func $$testname\\b" internal/ -g "*_test.go" 2>/dev/null; then \
							echo "  MISSING: $$testname (referenced in .ai/invariants.md)"; \
							missing=$$((missing + 1)); \
						fi; \
					fi; \
					;; \
			esac; \
		done < .ai/invariants.md; \
		if [ "$$missing" -gt 0 ]; then \
			echo "FAIL: $$missing invariant test(s) not found in codebase"; \
			exit 1; \
		fi; \
		echo "  All invariant tests found ✓"; \
	'
```

Also add to existing `.PHONY`:

```makefile
.PHONY: invariants
invariants: validate-invariants
	go test -run '$(TUI_INV)' ./internal/cli/ -count=1 -timeout=120s
	go test -run '$(TOOL_INV)' ./internal/tools/ -count=1 -timeout=60s
	go test -run '$(AGENT_INV)' ./internal/agent/ ./internal/chat/ -count=1 -timeout=60s
```

---

### 4.4 Update Commit-Msg Hook

Add to `scripts/git-hooks/commit-msg` (after existing subject format check):

```bash
# Regression test check for fix commits
if echo "$SUBJECT" | grep -qE '^fix\('; then
    if ! grep -qE '^Regression:' "$1" 2>/dev/null; then
        echo "ERROR: fix commits must include a Regression: line in the body."
        echo "  Add to commit body (leave blank line after subject):"
        echo "    Regression: TestFixName"
        echo "  For trivial fixes:"
        echo "    Regression: none (trivial: <reason>)"
        echo "  See .ai/rules/20-agent-quality.md"
        exit 1
    fi
fi
```

---

### 4.5 Update Agent Instructions

**In `.ai/agent-prompt.md`** (this file), add to the "How to orient" section:

```
- **Invariant manifest** at `.ai/invariants.md` — before changing code in
  `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, or
  `internal/config/`, read the manifest and run the corresponding invariant tests.
  A change that breaks a named invariant is blocked.
```

**In `.ai/skills/engineering-working-contract/SKILL.md`**, add to the "Execution" section:

```
6. Before committing changes to sensitive areas (cli, tools, agent, chat, config):
   a. Read `.ai/invariants.md` — identify which invariants cover your area.
   b. Run the corresponding invariant tests (see "Invariant Verification" in
      20-agent-quality.md).
   c. If any fail, restore the invariant or document the update with
      `Invariant-Update: INV-XXX <reason>`.
```

---

### 4.6 Pre-Commit Hook Invariant Gate (no bypass variable)

Add to `scripts/git-hooks/pre-commit`:

```bash
# Invariant test guard — no SKIP env var allowed.
# Only bypass: --no-invariants flag (logs to .git/invariant-bypass-log)
STAGED=$(git diff --cached --name-only --diff-filter=ACM)
for arg in "$@"; do
    if [ "$arg" = "--no-invariants" ]; then
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) BYPASS $USER $(git rev-parse HEAD)" >> .git/invariant-bypass-log
        echo "⚠ Invariant check bypassed (logged to .git/invariant-bypass-log)"
        exit 0
    fi
done

if echo "$STAGED" | grep -qE '^internal/cli/'; then
    echo " → Running TUI invariant tests..."
    go test -run 'TestBridgePath|TestPollCmd|TestBridgeDrain|TestUIEventMsg|TestFinishStream|TestSmoke' ./internal/cli/ -count=1 -timeout=120s || exit 1
fi
if echo "$STAGED" | grep -qE '^internal/tools/'; then
    echo " → Running tool invariant tests..."
    go test -run 'TestToolSurface|TestRedact|TestSecret|TestSearchOpenAI' ./internal/tools/ -count=1 -timeout=60s || exit 1
fi
if echo "$STAGED" | grep -qE '^internal/agent/'; then
    echo " → Running agent invariant tests..."
    go test -run 'TestRedactTool|TestDelegate|TestMultiStep|TestSendUser' ./internal/agent/ -count=1 -timeout=60s || exit 1
fi
if echo "$STAGED" | grep -qE '^internal/chat/'; then
    echo " → Running chat invariant tests..."
    go test -run 'TestSendUser|TestConcurrent' ./internal/chat/ -count=1 -timeout=60s || exit 1
fi
if echo "$STAGED" | grep -qE '^internal/config/'; then
    echo " → Running config invariant tests..."
    go test -run 'TestConfig|TestRedact' ./internal/config/ -count=1 -timeout=60s || exit 1
fi
```

---

## 5. Migration Phases

### Phase 1: Create Artifacts (immediate, this commit)

1. Create `.ai/invariants.md` with exact test names, categories, and known liveness gaps
2. Update `.ai/rules/20-agent-quality.md` — add Regression Tests + Invariant Verification
3. Update `.ai/agent-prompt.md` — add invariant instruction
4. Update `.ai/skills/engineering-working-contract/SKILL.md` — add invariant step
5. Update `Makefile` — add `validate-invariants` and `invariants` targets

### Phase 2: Wire Hooks (next commit cycle)

1. Add commit-msg hook check for `Regression:` line in fix commits
2. Add pre-commit invariant auto-detect (with `--no-invariants` bypass logging)
3. Run `make validate-invariants` to verify manifest matches codebase
4. Run `go test ./... -count=1` to confirm nothing breaks

### Phase 3: Closing the Liveness Gap (ongoing)

1. Document which invariants are liveness (not fully verifiable by unit tests)
2. For each liveness invariant, evaluate whether a stress test or integration test is feasible
3. If not feasible, accept the residual risk and document it
4. Revisit after any production incident involving a liveness failure

### Phase 4: Strengthen (stretch)

1. Add `make invariants` to `pre-push` hook
2. Explore automated mutation testing (go-mutesting) for core packages
3. Create a CI job that runs `make validate-invariants` on every PR
4. Add invariant coverage metric to `make invariants` output

---

## 6. Files Changed

| File | Change | Phase |
|------|--------|-------|
| `.ai/invariants.md` | **New** — system invariant manifest with exact test names | 1 |
| `.ai/rules/20-agent-quality.md` | Add sections: Regression Tests, Invariant Verification | 1 |
| `.ai/agent-prompt.md` | Add invariant instruction to orientation section | 1 |
| `.ai/skills/engineering-working-contract/SKILL.md` | Add invariant step to Execution section | 1 |
| `Makefile` | Add `validate-invariants`, `invariants` targets | 1 |
| `scripts/git-hooks/commit-msg` | Add regression line check for fix commits | 2 |
| `scripts/git-hooks/pre-commit` | Add invariant auto-detect with bypass logging | 2 |

---

## 7. Verification

```bash
# Phase 1 verification
make validate-invariants       # Must pass — all referenced tests exist
go test ./... -count=1         # Must pass — no behavioral change

# Phase 2 verification
echo "fix(cli): test commit" > /tmp/msg && echo "" >> /tmp/msg
scripts/git-hooks/commit-msg /tmp/msg || echo "correctly blocked (no Regression line)"

echo "Regression: TestFixName" >> /tmp/msg
scripts/git-hooks/commit-msg /tmp/msg && echo "correctly passed"

# Simulate staged cli file
echo "internal/cli/tui.go" | git diff --cached --name-only  # check pre-commit detection
```

---

## 8. What Success Looks Like

| Metric | Current | Target |
|--------|---------|--------|
| Fix commits with regression test | ~0% (test added after fix) | 100% |
| Feature commits that break invariants caught pre-commit | none (caught post-merge) | caught before commit |
| Invariant manifest drift (tests referenced but renamed/moved) | N/A (no manifest) | 0 drift entries (caught by `make validate-invariants`) |
| Bypass log entries | N/A | Tracked — if >5/commits bypassed, the gate is too expensive |
| Time to identify regression source | "after it shipped" | "before commit" |
