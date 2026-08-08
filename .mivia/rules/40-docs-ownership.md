# Docs Ownership (Single Source of Truth)

**CRITICAL.** Agents and contributors must not create parallel or duplicate documentation. One topic → one canonical document. Ownership is machine-declared.

## Source Of Truth

| Concern | Authority |
|---------|-----------|
| Which path owns a topic | `docs/OWNERS.yaml` |
| Machine enforcement rules | `.mivia/policy/docs-ownership.json` |
| Human procedure (this file) | `.mivia/rules/40-docs-ownership.md` |

`docs/OWNERS.yaml` is the **sole registry** of topic → canonical path → owner. If a topic is missing, add or update an entry in `docs/OWNERS.yaml` first; do not create an orphan doc.

## Hard Rules

1. **Update in place.** If a canonical doc exists for the topic, edit that file. Do not create `docs/foo-v2.md`, `docs/foo-new.md`, `FOO.md` at repo root, or a second explanation under `.mivia/` / `AGENTS.md` / skill bodies.
2. **No parallel docs.** Forbidden patterns include (non-exhaustive): same title under two paths; “notes”, “scratch”, “copy”, “backup”, “final”, “revised” siblings of a canonical path; duplicating architecture content into skills as a second full guide.
3. **No agent-invented trees.** Do not create `docs/agent-notes/`, `docs/tmp/`, `docs/wip/`, `docs/adr/`, or dated dump folders for durable knowledge. Use the existing canonical trees (`docs/architecture/`, `docs/development/`, `docs/product/`, `docs/security/`, plans under agreed locations).
4. **Cross-link, do not copy.** Skills, rules, and comments reference the canonical path. At most a short summary (≤ ~10 lines) may appear elsewhere, with an explicit pointer to the canonical file.
5. **ADRs are prohibited.** Record architectural decisions in the registered canonical architecture document; do not create or retain `docs/adr/`.
6. **Plans are not product docs, and they do not live here.** Every `.md` file in this repository must be documentation a user reads or an instruction an agent follows. Implementation plans, task files, and progress reports are neither, so they belong in the sibling `mivia-agent-plans` repository. When the work ships, promote the durable truth into the OWNERS-registered canonical doc, an invariant, or the code.
7. **Root instruction files stay thin.** `AGENTS.md`, `CLAUDE.md`, README sections point into `.mivia/` and `docs/`; they do not grow a full second handbook.

## Required Workflow For Agents

Before writing any new markdown under `docs/` or root:

1. Read `docs/OWNERS.yaml`.
2. Search for existing titles/paths covering the topic (`rg`, docs tree, INDEX).
3. If a canonical path exists → edit it (or open a PR/task to edit it).
4. If no topic exists → add an `OWNERS.yaml` entry **and** create exactly one new file at the registered path.
5. If ownership is unclear → stop and ask; do not guess a second location.
6. Run docs-ownership checks once `docs-ownership.json` is wired to hooks/CI.

## Docs Tree (Expected)

```text
docs/
  OWNERS.yaml          # ownership registry (required)
  architecture/        # system design (canonical designs)
  development/         # contributor/dev workflow
  product/             # product behavior, CLI UX, roadmap summaries
  security/            # threat model, privacy, secure config
```

Additional directories only via explicit OWNERS registration and product decision.

## What Agents Must Not Do

- Create `README-agent.md`, `NOTES.md`, `SUMMARY.md`, or skill-local full copies of architecture.
- “Helpfully” write a new overview when `docs/product/` or `docs/architecture/` already covers it.
- Move a topic by adding a new file without updating `docs/OWNERS.yaml` and retiring/redirecting the old path in the same change.
- Store durable docs only inside chat transcripts or `.mivia/runs/`.

## Ownership Changes

- Changing the canonical path for a topic requires: update `docs/OWNERS.yaml`, move/redirect content, fix inbound links, and delete or stub the old path in the same change set.
- Owner field must be a real team/role or CODEOWNERS-compatible identity used by MiviaLabs for this product.

## Skill Coupling

Use skill `docs-update` for doc edits. That skill must load this rule and `.mivia/policy/docs-ownership.json` and refuse to create files that violate them.
