# Security Overview

## Principles

- No secrets in git (hook-enforced via `scripts/secret_scan.py`)
- Fail closed on protected verification paths
- Deny-by-default for powerful tools as product capabilities land
- No general PII collection without explicit design approval
- Never log credentials or raw provider payloads containing secrets

## Local gates

- Secret scan (staged + tracked)
- Semgrep agent standards
- Hook bypass guard for agent tools

## Product surface (as features land)

- Tool allowlists and auditability
- Isolation tiers for untrusted execution
- Tool argument redaction **opt-in** (default off for operator visibility):
  - TOML: `[privacy] redact_tool_args = true`
  - Env: `MIVIA_REDACT_TOOL_ARGS=1`
  - When off, `run_command` shows argv; event previews keep argument bodies (still size-capped)
- **Redaction is configuration-only, and off by default.** No credential
  pattern, key name or value prefix is compiled into the binary.
  `[privacy].redaction_patterns` and `.redaction_key_names` are the sole
  source; recommended values ship in `.mivia/mivia.toml.example`.

  **A workspace that configures neither redacts nothing** — tool previews,
  `run_command` output, event bodies and audit metadata pass through intact,
  including into the session transcript on disk. This fails open deliberately:
  what counts as a secret is a property of a workspace, and the four separate
  compiled lists this replaced had drifted apart, over-redacting ordinary prose
  while missing credentials none of them happened to name.

  `prompt` and `reasoning` are never redacted. They are the agent's own
  instructions and deliberation, not the user's secrets, and eliding them made
  audit metadata useless for reconstructing agent behaviour while protecting
  nothing.
- **Secret path filtering is configuration-only.** No pattern list is compiled
  into the binary: `[tools].secret_path_patterns` and `.secret_path_exceptions`
  are the sole source, recommended values ship in `.mivia/mivia.toml.example`,
  and a workspace that configures neither filters nothing. The filter keeps
  credentials out of model context by accident — it is **not** a boundary,
  because a shell invocation that builds a path at runtime reaches the file
  regardless. Config itself is deliberately agent-editable.

## Known and accepted: the workspace agent prompt is ungated

`.mivia/agent-prompt.md` is read verbatim as the **root agent's system prompt**
(`internal/cli/prompt.go` → `loadAgentPrompt` → `res.SystemPrompt`). It is not
gated behind any switch, and unlike workspace skills it is **not** wrapped as
untrusted content (contrast `internal/skills/loader.go`, which explicitly tells
the model the text is untrusted project content that cannot override system,
developer, safety or tool policies).

**Consequence:** cloning a repository and running `mivia chat` in it hands that
repository authorship of your root agent's system prompt. A hostile
`.mivia/agent-prompt.md` shapes every turn of that session.

This is a **known exposure, accepted deliberately** rather than an oversight —
the workspace prompt exists so a project can orient the agent, and gating it
would remove the feature for the case it was built for. Two mitigations are
yours to apply:

- Treat an unfamiliar repository the way you would treat any untrusted code:
  read `.mivia/agent-prompt.md` before running `mivia chat` in it.
- `mivia chat --no-tools` limits what a hostile prompt can direct, since the
  tool surface is what turns prompt influence into filesystem or command access.

Tracked in `.mivia/plans/04-workspace-namespace-mivia.md` §5, which records the
gating options if this is ever revisited.

## See also

- `.mivia/rules/10-security-privacy.md`
- `.mivia/skills/secure-change/SKILL.md`
