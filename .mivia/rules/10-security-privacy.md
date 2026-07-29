# Security And Privacy

Product: **mivia** (MiviaLabs). Applies to all agents and humans editing this repo.

## Secrets And Sensitive Data

- Never commit `.env`, credential files, API keys, tokens, private keys, provider payloads, raw prompts, or raw model outputs.
- Test fixtures use obviously fake values (`example-token`, `test-secret-placeholder`, `sk-test-not-real`).
- Do not log environment variables wholesale. Log explicit allowlisted names only.
- Do not put PII, tokens, or secrets in traces, metrics, analytics, snapshots, seed data, or error messages.
- Treat user codebases and workspace content handled by `mivia` as potentially sensitive; minimize retention in `.mivia/runs/`.

## Network

- Core `mivia` library and non-adapter commands must not make unexpected network calls.
- Tests must not hit the network by default. Use fakes, temp local processes, or explicit build tags for live tests.
- Any network-capable surface (adapters, MCP clients, update checks) must:
  - document the boundary in the canonical security/architecture doc,
  - scrub results before persistence,
  - fail closed when credentials or allowlists are missing.
- No live external connector calls without explicit user/config enablement.

## Hooks And Protected Actions

Protected actions: commit (policy-gated), push, open PR, deploy, release, live smoke, destructive git rewrite.

- Hook handlers parse structured input and reject malformed protected-action payloads once enforcement exists.
- Hook output: short decisions and scrubbed reasons only — never raw prompt/model output.
- Agents must not bypass Git hooks (see `.mivia/policy/agent-hook-bypass.json`).
- Quality stamps / policy decisions, when implemented, gate protected actions; do not invent bypass paths.

## Path And Process Safety

- Never follow symlinks outside the target repo for writes.
- Never write outside the workspace without explicit user request and path policy.
- Shell out only to allowlisted tools where policy defines allowlists; scrub argv that may contain secrets.
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
