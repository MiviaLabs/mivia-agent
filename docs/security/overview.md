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
  - When off, `run_command` shows argv; event previews keep argument bodies (still size-capped; secret-like path blocks and output secret scrubbing remain)
- Redacted diagnostics for secrets patterns (API keys) remain always-on for output scrub
- **Secret path filtering is configuration-only.** No pattern list is compiled
  into the binary: `[tools].secret_path_patterns` and `.secret_path_exceptions`
  are the sole source, recommended values ship in `.mivia/mivia.toml.example`,
  and a workspace that configures neither filters nothing. The filter keeps
  credentials out of model context by accident — it is **not** a boundary,
  because a shell invocation that builds a path at runtime reaches the file
  regardless. Config itself is deliberately agent-editable.

## See also

- `.mivia/rules/10-security-privacy.md`
- `.mivia/skills/secure-change/SKILL.md`
