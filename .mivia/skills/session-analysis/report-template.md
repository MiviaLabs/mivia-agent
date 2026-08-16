# mivia-report/v1 - session-analysis grammar

Use the canonical mivia-report/v1 envelope (`.mivia/templates/agent-report-v1.md`).
The Findings grammar below is extended for validated metadata-only session
analysis. Keep each field to one line or short bullet; do not add narrative
sections. The report NEVER contains message content, titles, admission values,
or payloads.

> MIGRATION (schema v1 → v2): this grammar file (`report-template.md`) and
> `queries.py` are the two sides of the v1→v2 contract. v2 adds the
> `derivation`, `store_accounting`, and `admissions_coverage.note` fields;
> nothing is removed or rekeyed, so v1 consumers keep parsing. The previous
> `workspace_id` derivation (8 hex chars) mis-scoped every run; the corrected
> derivation (16 hex chars) is disclosed in the output under `derivation`.

```md
ReportFormat: mivia-report/v1
Skill: session-analysis
Result: PASS|PARTIAL|NOT_RUN
Scope: metadata-only; ledger <path> (user_version <n>); workspace_id <id>; window <frame> (anchor: <per-arm anchors>); whole-store context: <n snapshots / n live / n routes>; file store out of scope
Summary: <one sentence>
Evidence:
- <tool call>: PASS|FAIL|NOT_RUN - <short note>
Signals:
- window counts: <snapshots> / <live> / <routes> (total <n>)
- distributions: turn_count (n=<n>, method=<method>) ... token_count (STALE) ... payload_bytes (current)
- stalled live (0 completed checkpoints): <n> | copies: <n> | orphan dir rows: <n>
- admissions coverage: <n> of <n> window sessions | stale (7d): snapshots <n> live <n> routes <n>
Findings:
- [SA-n] <severity> [SOURCES: <signal names that fed this finding>] <one-line observed claim> | Evidence: <signal + anchor, observation time> | Occurrences: <x of y> | Validation: CONFIRMED (subagent)|CONFIRMED (cross-check)|CONFIRMED (selftest)|INSUFFICIENT_EVIDENCE|NOT_VALIDATABLE | Remediation: <concrete process change and owner>
Recommendations:
- <process improvement, clearly labeled inferred when applicable>
ResidualRisk: none|<short exact risk>
NextAction: none|<exact action>
```

Validation tag semantics:

- `CONFIRMED (subagent)` - an independent validator re-derived the signal from
  the raw `queries.py` JSON (blind: no candidate findings shown).
- `CONFIRMED (cross-check)` - fallback two-output verification (re-run +
  internal consistency: window total = sum of arms, COUNT vs SUM, distribution
  n matches arm-1 count). Used when no delegation tools exist.
- `CONFIRMED (selftest)` - the fact is a query-parity property proven by the
  hermetic golden-DB selftest (never sufficient alone for data findings).
- `INSUFFICIENT_EVIDENCE` - excluded from Findings. Do not report it.
- `NOT_VALIDATABLE` - interpretation no tool can confirm; excluded from
  Findings, allowed in Recommendations when clearly labeled.

Mandatory scope disclosures (all in the Scope line):

- Anchor per arm: snapshots = last save (`chat_sessions.updated_at`); live =
  last completed checkpoint (`MAX(context_checkpoints.created_at)`); routes =
  last route update. Never "last activity" for the live arm.
- `token_count` / `turn_count` are STALE save-time estimates (compaction
  invalidates them); `payload_bytes` is current, post-compaction.
- `updated − created` is reported as first-to-last-save span, never duration.
- Live sessions with 0 completed checkpoints count as "updated now" (harness
  `CURRENT_TIMESTAMP` semantics) and are the stalled signal; snapshots are
  never stalled.
- The ledger is machine-shared; only rows for the derived `workspace_id` +
  `subject_id='local-user'` are included; the legacy file store is out of
  scope.

Result semantics:

- `PASS` - analysis complete; every finding validated; `ResidualRisk: none`.
- `PARTIAL` - analysis complete but limited (fallback validation used, or
  window/calibration caveats).
- `NOT_RUN` - python3/ledger/schema v11 unavailable (dependency failure), never
  a zero-session window.

Zero-session window: measured absence is a finding. State the window frame, the
calibration line (ledger totals vs workspace totals, other-workspace rows
present), the derived workspace_id, and the per-arm anchor translation table.

## Migration (schema v1 → v2)

| Field | Kind | v1 status | v2 status | Consumers |
| --- | --- | --- | --- | --- |
| `workspace_id` | string | present (WRONG: 8 hex chars) | present (corrected: 16 hex chars) | all signals |
| `derivation` | object | absent | NEW | validation, ops |
| `store_accounting` | object | absent | NEW | findings SA-2/SA-3 |
| `admissions_coverage.note` | string | absent | NEW | findings AR-4 |
| `admissions_coverage.*` | numbers | present | present (unchanged) | findings AR-4 |

Deployment checklist (execute in order, no downtime):

1. ship `queries.py` (new fields) with selftest green — the report gains
   `derivation` + `store_accounting` + `admissions_coverage.note`;
2. update consumers (findings templates, dashboards) to the v2 field names;
3. keep v1 field names parsing for one release (nothing was removed);
4. delete this section once all consumers are on v2.

Re-verify workspace scoping after deploy: the derived `workspace_id` must be
16 hex chars; compare against the harness ledger row
(`SELECT workspace_id FROM context_sessions LIMIT 1`).
