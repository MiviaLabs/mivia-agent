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
- Redacted diagnostics

## See also

- `.ai/rules/10-security-privacy.md`
- `.ai/skills/secure-change/SKILL.md`
