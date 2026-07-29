# Security And Privacy

Product: **mivia** (MiviaLabs). Applies to all agents and humans editing this repo.

## Secrets And Sensitive Data

- Never commit `.env`, credential files, API keys, tokens, private keys, provider payloads, raw prompts, or raw model outputs.
- Test fixtures use obviously fake values (`example-token`, `test-secret-placeholder`, `sk-test-not-real`).
- Do not log environment variables wholesale. Log explicit allowlisted names only.
- Do not put PII, tokens, or secrets in traces, metrics, analytics, snapshots, seed data, or error messages.
- Treat user codebases and workspace content handled by `mivia` as potentially sensitive; minimize retention in `.mivia/runs/`.

## Redaction

**Redaction is configuration, never code.** No credential pattern, key name or
value prefix may be compiled into the binary. `[privacy].redaction_patterns`
and `.redaction_key_names` are the only source; recommended values ship in
`.mivia/mivia.toml.example`. Implemented; see plan 10 for the reasoning.

- **A workspace that configures nothing redacts nothing.** This fails open by
  design: what counts as a secret is a property of a workspace, and four
  compiled lists guessing on the user's behalf drifted apart and were wrong in
  both directions. Do not "just add a small default" — that is how they grew.
- **One engine.** New code that needs redaction calls `internal/redact`; it does
  not write its own regex. A `regexp.MustCompile` containing a credential
  keyword outside `internal/redact` is a defect, and
  `TestNoCompiledRedactionPatterns` fails the build for it.
- **Runtime redaction is no longer a backstop.** Because it is off unless the
  workspace configures it, the authoring rules below and in rules 01, 20 and 30
  — do not log secrets, keep error messages scrubbed, keep excerpts short — are
  now the *first* line of defence rather than the second. Write as though
  nothing downstream will clean up after you, because by default nothing will.
- **`run_command` output is model-visible.** Its body is the tool result, so the
  policy decides what the model reads, not only what an operator reads.
- **`prompt` and `reasoning` are never redacted.** They are the agent's own
  instructions and deliberation, not the user's secrets. Eliding them made
  audit metadata useless for reconstructing agent behaviour while protecting
  nothing. A user may add them to their own key list; nothing shipped will.
- Redaction protects what a third party learns about the *user*. Limiting what
  a reader learns about *mivia* is a different problem and is not redaction's
  job — do not solve it here.

## Network

- Core `mivia` library and non-adapter commands must not make unexpected network calls.
- Tests must not hit the network by default. Use fakes, temp local processes, or explicit build tags for live tests.
- Any network-capable surface (adapters, MCP clients, update checks) must:
  - document the boundary in the canonical security/architecture doc,
  - scrub results before persistence through the configured redaction policy,
  - fail closed when credentials or allowlists are missing.
- No live external connector calls without explicit user/config enablement.

## Hooks And Protected Actions

Protected actions: commit (policy-gated), push, open PR, deploy, release, live smoke, destructive git rewrite.

- Hook handlers parse structured input and reject malformed protected-action payloads once enforcement exists.
- Hook output: short decisions and scrubbed reasons only — never raw prompt/model output. (This is a rule for what hooks *emit*; it is not implemented by the redaction policy, which never elides prompts.)
- Agents must not bypass Git hooks (see `.mivia/policy/agent-hook-bypass.json`).
- Quality stamps / policy decisions, when implemented, gate protected actions; do not invent bypass paths.

## Path And Process Safety

- Never follow symlinks outside the target repo for writes.
- Never write outside the workspace without explicit user request and path policy.
- Shell out only to allowlisted tools where policy defines allowlists; argv that may carry secrets is scrubbed through the configured redaction policy (none by default).
- Subagent / concurrent work follows `.mivia/rules/50-concurrency-subagents.md` (shared MCP, caps, no process farm).

## Authz And Tenancy (when product surfaces exist)

- Deny-by-default access control; explicit allowlists.
- No IDOR/BOLA via path or ID confusion between projects/workspaces.
- Authorization and negative-path tests required for any auth surface.

## Global Config Layer

- Optional global defaults may be read from `~/.agents/` (lowest priority).
- Project `.mivia/` rules and skills win over global rules/skills of the same name.
- Product must not write `~/.agents/` unless the user explicitly runs a command that documents that write.
- User-level config is machine-local unless the user explicitly syncs or commits it elsewhere.

## Privacy / PDPL Posture

Flag any change that collects, stores, processes, shares, exports, logs, or deletes personal data. Require purpose, data owner, retention, access model, deletion path, and audit trail in the canonical security doc before shipping. If legal interpretation is required, stop and ask counsel/DPO rather than guessing.
