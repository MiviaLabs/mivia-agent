# Security Policy

mivia is a local CLI agent. It reads, searches, and edits files in your project, and it runs the commands you allow. The security boundary is therefore critical: mivia must never read a secret it is not meant to see, must never expose a credential, and must never let an untrusted file change what it is allowed to do.

## Reporting a vulnerability

Do not open a public issue for a security problem.

Report privately through the GitHub Security Advisory flow:

1. Open the repository on GitHub.
2. Select **Security** → **Report a vulnerability**.
3. Describe the issue and include a minimal reproduction.

You can report any issue in scope below. We aim to acknowledge reports within 5 business days and to ship a fix or a mitigation within 30 days.

## Scope

These areas are in scope for private reporting:

- Secrets handling: API keys, env files, redaction, and log output.
- Command execution: the `run_command` allowlist and blocklist, workspace confinement, and environment variable filtering.
- File tools: path traversal, symlink handling, and workspace boundary escapes.
- Lifecycle hooks: the trust model for `PreToolUse`, `PostToolUse`, and `Stop` scripts.
- The provider boundary: what prompt and context data leaves the machine, and where it goes.

Anything outside this list is a normal bug: open a regular issue with the bug report template.

## What we ask of you

- Remove all secrets from your report before you submit it.
- Include the output of `mivia version --json` and your operating system.
- Do not publish the vulnerability until we ship a fix or decline to fix it.

## Safe harbor

We will not pursue a claim against a researcher who reports in good faith, who does not access data beyond what the report needs, and who does not disrupt the service or harm other users.
