# Agent Report Template v1

Canonical report contract for MiviaLabs product `mivia`.

Use this exact shape for verify, security, docs, concurrency, delivery, and handoff reports.
Do not add free-form Output headings. Do not invent extra top-level fields unless the user explicitly asks for a longer artifact.

Result enum is exactly `PASS`, `BLOCK`, `PARTIAL`, or `NOT_RUN`.
Keep every cell to one short sentence or `none`.

```md
ReportFormat: mivia-report/v1
Skill: <name>
Result: PASS|BLOCK|PARTIAL|NOT_RUN
Scope: <exact files/packages>
Baseline: <branch/commit/diff>
Summary: <one sentence>

| ID | Severity | Status | File:Line | Finding | Required Fix | Required Test | Mutation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| none | none | closed | none | none | none | none | none |

| Command | Result | Notes |
| --- | --- | --- |
| none | NOT_RUN | none |

ResidualRisk: none|<short exact risk>
NextAction: none|<exact task>
```

## Field Rules

- ReportFormat must be exactly `mivia-report/v1`.
- Skill must match the skill frontmatter `name`.
- Severity never gates approval; every open gap must be fixed.
- `PASS` requires zero gap rows and `ResidualRisk: none`.
- Gap statuses are `open`, `missing`, `shallow`, and `gated`.
- Low-severity gaps still require `BLOCK` or `PARTIAL` until fixed.
- `Result: PASS` only when all required checks ran, no finding row has a gap status, and no residual risk remains.
- `Result: BLOCK` when any correctness, security, coverage, docs, or verification gap can be fixed before merge or handoff.
- `Result: PARTIAL` when useful work completed but a named blocker, gated dependency, or user decision remains.
- `Result: NOT_RUN` only for a pure planning response or when verification could not start.
- `Severity` values are `critical`, `high`, `medium`, `low`, `info`, or `none`.
- `Status` values are `open`, `fixed`, `closed`, `missing`, `shallow`, `gated`, or `none`.
- `File:Line` must be an exact repo path with line number when possible, or `none`.
- `Required Test` must name the exact test file and test name for open gaps, or `none`.
- `Mutation` must name the guard or branch that should fail when changed, or `none`.

## Measurement Rules

- Never invent elapsed time, duration, tokens, cost, throughput, or efficiency numbers.
- Time, token, cost, throughput, and efficiency fields are evidence only when the runtime records them with provenance.
- If the runtime has no measured source, render `NOT_MEASURED` — not `0`, an estimate, or prose such as "efficient".
- Agent prose, Markdown tables, and review consensus are not trusted telemetry channels.

## Product Naming

- Product brand is **MiviaLabs** (one word).
- Product CLI binary is **`mivia`**.
- Repo path `github.com/MiviaLabs/mivia-agent` and historical kit name `mivia-agentkit` are allowed when referring to those repos only.
