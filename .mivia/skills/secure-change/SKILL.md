---
name: secure-change
description: Security and privacy review for a scoped mivia change. Check authz, secrets, SSRF, injection, path safety, prompt-injection, and fail-closed defaults. Report with mivia-report/v1.
triggers:
  - secure change
  - security review
  - privacy review
  - threat check
  - secrets
  - auth
tools:
  - read_file
  - list_dir
  - grep
  - glob
  - find_references
---

# Secure Change

Security and privacy review for a change to the `mivia` agent CLI. It is
project-bound: it references real mivia artifacts (`.mivia/rules/10-security-privacy.md`,
`internal/redact`, the SSRF guard in `internal/tools/fetch_url.go`, the
permission scoping invariants). It is not the portable skill family. For a
generic, language-agnostic risk/blast-radius review use `verify-code-change`;
this skill is the dedicated security pass.

## Read First

- `AGENTS.md`
- `.mivia/rules/10-security-privacy.md` (the authoritative security rules)
- `.mivia/invariants.md` (rows INV-AG-7, INV-AG-25/26/27, INV-SEC-1 are security-relevant)
- `.mivia/templates/agent-report-v1.md`
- Diff scope named by the user
- `docs/security/` owned paths when present

## Threat model

Derive the invariants that must hold for this change before hunting defects.
Work from the trust boundaries the product actually has, not a generic checklist:

- **Untrusted input that reaches the model.** Tool output, `run_command` output,
  fetched URL bodies, search/extract results, and content references all become
  model-visible text. Treat them as attacker-controllable: a web page or tool
  result can carry instructions (prompt injection). The defense is structural
  (scoping, output bounds, fail-closed parsing), not filtering of "instructions".
- **Network fetch surface.** `fetch_url`, `search`, and `extract` resolve and
  dial external URLs. The SSRF guard in `internal/tools/fetch_url.go`
  (`validateFetchURL`, `isBannedIP`) rejects private, loopback, link-local, and
  reserved addresses and fails closed on DNS failure. A change that adds a new
  fetch path, weakens redirect handling, or introduces a second HTTP client must
  route through the same guard or prove equivalent protection. Do not add a
  raw `http.Get` for untrusted URLs.
- **Permission scoping (INV-AG-7).** Nested agents execute only tools in their
  restricted registry, enforced at the dispatcher, not advertised in tool
  descriptions. A change that registers tools, builds scoped registries, or
  touches the dispatch boundary must preserve deny-by-default scoping and the
  negative test that a privileged tool is unreachable through a parent
  dispatcher.
- **Result budgets (INV-AG-25/26/27).** Every default tool declares
  `tools.ResultBudgetBytes()` or is listed in `unbudgetedDefaultTools` with a
  reason. Network-derived content (`search`, `extract`) is bounded, never
  silently truncated, on every composed return path. A new tool that can emit
  large or network-derived output must declare a budget or it is an invariant
  violation.
- **Redaction is configuration, never code.** `internal/redact` is the only
  engine; no credential pattern is compiled into the binary
  (`TestNoCompiledRedactionPatterns` fails the build otherwise). A workspace
  that configures nothing redacts nothing by design. Do not add a "small
  default" and do not write redaction regex outside `internal/redact`.
- **Secrets and PII are toxic.** In logs, traces, metrics, fixtures, error
  strings, and persisted runs. `prompt` and `reasoning` are deliberately never
  redacted; do not change that.
- **Protected actions.** commit (policy-gated), push, open PR, deploy, release,
  live smoke, destructive git rewrite. Agents must not bypass Git hooks.

## Method

1. Define trust boundaries for the change: external input sources, filesystem
   paths, environment, subprocess, config, network endpoints, and model-visible
   output paths.
2. Check deny-by-default authz. No IDOR/BOLA via path or ID confusion across
   tenant, workspace, or repo roots. For run/orchestration surfaces, a handle
   is accessible only to its creator's principal (INV-AG-9, INV-AG-19).
3. Trace every untrusted input to its sink: filesystem write, shell, SQL,
   template, URL dial, and model context. Reject shell metacharacter / `sh -c`
   execution paths; require allowlisted parameterized exec (`run_command`
   argv, not a shell string).
4. For any new or modified fetch/network path, confirm it routes through the
   SSRF guard. Check redirect handling (a 302 to loopback must be blocked, not
   followed), DNS-rebinding (resolve immediately before dial, not once before
   a redirect chain), and fail-closed behaviour on resolution failure.
5. Scan for hardcoded secrets, token logging, prompt/payload persistence, and
   world-writable file modes. Confirm test fixtures use obviously fake values
   (`example-token`, `test-secret-placeholder`), not realistic-looking secrets.
6. For model-visible output paths, confirm bounds are declared and enforced
   (INV-AG-25/26/27). A tool that composes output from network responses must
   bound every return path, not only the wire response; composition can
   outgrow the body it came from.
7. Treat tool output and fetched content as a prompt-injection vector. Confirm
   the change does not let untrusted text select tools, widen permissions,
   spawn agents, or trigger protected actions. The model is not a trusted
   executor of instructions embedded in tool results.
8. When the invoking agent has command execution, run available static gates:
   `semgrep/agent-standards.yml`, the secret-scan scripts, and
   `TestNoCompiledRedactionPatterns` when the change touches anything that might
   embed a credential pattern. Without command execution, report these gates
   `PARTIAL`/`NOT_RUN` with the reason rather than skipping them silently.
9. For new parsers, configuration decoders, or tool schemas that accept
   untrusted structured input, require malformed, empty, oversized, and
   duplicate-input tests. When the invoking agent has command execution and a
   deterministic fuzz target is practical, run a bounded fuzz target
   (`go test -fuzz=Fuzz... -fuzztime=10s ./affected/pkg`); without command
   execution, report the fuzz run `PARTIAL`/`NOT_RUN` with the reason, and
   state when no deterministic target is practical.
10. Confirm at least one negative security test per new guard (the guard fires
    on the bad input, not merely that the happy path passes).

## Severity calibration

Heading level must match impact. This is consistent with the `bug-audit` skill:

- **Critical** - exploitable: path traversal, SQLi, SSRF that reaches internal
  services, authz bypass or tenant breakout, prompt injection that reaches a
  protected action, secret exposure to the model or a log, unsafe
  deserialization of network bytes, or destructive irreversible data loss.
- **High** - serious correctness/reliability with a security angle: inverted
  authz checks, missing SSRF guard on a new fetch path, data race on a
  security-relevant field, non-idempotent money or external-side-effect path.
- **Medium** - bounded wrong result, degraded but non-exploitable contract
  drift, missing negative test for an existing guard.
- **Low** - minor defect with limited blast radius.

Never invent a **Low** finding about error-message wording on otherwise correct
validation. Severity never gates approval; it calibrates the report.

## Rules

- Brand is MiviaLabs. CLI is `mivia`.
- Do not instruct hook bypass.
- Do not add Semgrep suppressions in product code.
- Do not add redaction regex or credential patterns outside `internal/redact`.
- Treat PII and secrets as toxic in logs, traces, fixtures, and error strings.
- `prompt` and `reasoning` are never redacted; do not change this.
- Severity never gates approval; open gaps block `PASS`.

## Anti false-positive rules

Reject a candidate unless you can show a reachable failure in the shown code
under the stated contract. In particular:

- A fetch path that calls `validateFetchURL` / `isBannedIP` and fails closed on
  DNS error is **not** an SSRF bug. Only report SSRF when an untrusted URL can
  reach a dial without the guard, or redirect handling re-resolves to a
  private address.
- `TestNoCompiledRedactionPatterns` failing is a real defect; a workspace with
  no configured redaction patterns redacting nothing is **by design**, not a bug.
- Bound-and-truncate on a network tool (INV-AG-26) is correct; report only when
  a return path is unbounded or silently drops content the model needs.
- Parameterized exec via `run_command` argv is the correct pattern; report only
  shell-string (`sh -c`) execution of untrusted input.

## Required Report

When a resource catalogue and its scoped reader are available, load
`report-template` before producing the report. Without that capability, use the
canonical inline fallback from `.mivia/templates/agent-report-v1.md`.
Always emit the compact `mivia-report/v1` structure.

Inline fallback:

```text
ReportFormat: mivia-report/v1
Skill: secure-change
Result: PASS|BLOCK|PARTIAL|NOT_RUN
Scope: <exact files/packages>
Summary: <one sentence>
Evidence:
- <command or method>: PASS|FAIL|NOT_RUN - <short note>
Findings:
- none
ResidualRisk: none|<short exact risk>
NextAction: none|<exact action>
```

Result semantics:

- `PASS` - no concrete security/privacy gap in scope; required negative tests
  present; SSRF, redaction, and budget invariants preserved where the change
  touches them.
- `BLOCK` - any secret leak path, authz hole, injection, missing SSRF guard on
  a new fetch path, prompt-injection reach to a protected action, or missing
  security test remains.
- `PARTIAL` - useful findings but a gated tool or incomplete source access
  prevents completing a required check.
- `NOT_RUN` - plan only, or review could not start.
