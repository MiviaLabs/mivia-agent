# Security And Privacy

## Secrets And Sensitive Data

- Never commit `.env`, credential files, API keys, tokens, private keys, or provider payloads.
- Test fixtures use obviously fake values (`example-token`, `test-secret-placeholder`).
- Do not log environment variables wholesale. Log explicit allowlisted names only.
- Do not put PII, tokens, or secrets in traces, metrics, analytics, snapshots, seed data, or error messages.

## Network And File I/O

- Use `localhost` or `127.0.0.1` for network tests. No external network calls in test paths.
- File writes during testing go through `t.TempDir()` or `os.TempDir()` — never user-writable system paths.
- Generated scripts, hooks, or test fixtures must not contain secrets or internal infrastructure addresses.

## Fail-Closed For Protected Actions

- If an auth/token/credential check cannot determine the caller's authority, **fail closed** — deny the action.
- Never fall back to "allow" or "default permit" for protected operations.
- Protected actions include: write outside workspace, network calls, subprocess execution, reading secret-like paths.

## Prohibited

- No secrets in commits, logs, error messages, traces, or output.
- No hardcoded API keys or tokens anywhere in the tree.
- No bypass of security checks for convenience (testing, debugging, CI).
