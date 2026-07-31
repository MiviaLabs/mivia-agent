# mivia

Local CLI AI agent from **MiviaLabs**.

| | |
|---|---|
| Product | mivia |
| Binary | `mivia` |
| Module | `github.com/MiviaLabs/mivia-agent` |
| Status | Greenfield successor to the `mivia-agentkit` MVP |

## Start here

### Use mivia

`mivia` is a local coding agent. Configure a provider key, check the setup,
then start a chat:

```bash
mivia doctor
mivia chat -p "Help me understand this repository"
```

Read the canonical product guides for the full setup and operating model:

- [Product overview](docs/product/overview.md)
- [Configuration](docs/product/config.md)
- [Coding agent mode](docs/product/agent.md)
- [Security and privacy](docs/security/overview.md)

### Contribute to mivia

This repository builds the `mivia` binary. From a source checkout:

```bash
make install-hooks
make verify
make build
./mivia version
```

See [contributing](docs/contributing.md), [development hooks](docs/development/hooks.md), and [the architecture overview](docs/architecture/overview.md).

## For coding agents

Read **`AGENTS.md`** and **`.mivia/INDEX.md`** first.

Standing doctrine:

- `.mivia/doctrines/engineering-working-contract.md`

Canonical skills:

- `verify-code-change` (from mivia-agent-skills)
- `bug-audit` (from mivia-agent-skills; confirmed bugs only)
- repo skills: `docs-update`, `secure-change`, `concurrency-review`, `architecture-review`, `feature-delivery`

## Design notes

- Subagents are concurrent tasks with shared pools, not one process per agent.
- Docs ownership is machine-enforced (`docs/OWNERS.yaml`).
- Hooks block secret leaks and verification bypass.
