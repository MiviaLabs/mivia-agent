# Workflows

Workflows make the Mivia Agent harness repeatable without turning repository configuration into an execution authority.

A workflow is a repository-authored TOML definition in `.mivia/workflows/`. It selects already-resolved agents and registered host capabilities, then declares a bounded, sequential state machine. The host controls credentials, tools, commands, Git state, and publication.

## v1 scope

Workflows v1 supports:

- explicit discovery and validation of `.mivia/workflows/*.toml`;
- sequential agent steps, independent agent gates, deterministic evidence gates, and human gates;
- typed evidence passed through explicit, byte-bounded context bindings;
- finite repair loops;
- immutable run snapshots and durable resume;
- optional terminal pull-request delivery.

Workflow-level parallel branches, automatic triggers, model-facing invocation, arbitrary shell or HTTP actions, and external Git providers are not part of v1.

## Trust and authority

A workflow file is untrusted repository input. It may name an existing agent or registered verifier profile, but it cannot define or override:

- a model provider, endpoint, credential, tool allowlist, skill permission, or agent authority;
- a shell command, URL, environment variable, or secret;
- a Git base target outside runtime policy;
- publication permission.

A reviewer must return schema-valid structured evidence. Prose is never a routing signal.

## Execution isolation

A write-capable run uses a host-owned worktree at a recorded base commit. It never writes to the caller's checkout. Runs snapshot the compiled workflow, selected templates and schemas, inputs, resolved agent digests, verifier digests, and delivery policy before execution. Resume uses that snapshot, not a changed workspace file.

## Publication

Pull-request delivery is a terminal host policy, not a workflow step and not an agent tool.

A workflow may choose `none`, `draft`, or `ready` delivery. Publication additionally requires the invoking user to grant `--allow-publish`, an approved successful terminal state, a non-empty isolated-worktree diff, and an idempotency check. Without that grant, an otherwise eligible run finishes as `delivery_pending`.

## Reference flow

The first shipped workflow will express:

```text
plan → implement → independent review → bounded repair loop
     → deterministic verification → human approval → draft PR
```

The process—not a model verdict—is the source of truth.
