// Package mivia is the migration record for the session-analysis schema
// v1 -> v2 change.
//
// WHAT CHANGED (schema v1 -> v2; contract files under
// .agents/skills/session-analysis/):
//
//   - queries.py now emits three new report fields:
//     "derivation" (workspace_id algorithm disclosure, hex_chars=16),
//     "store_accounting" (context_sessions lifecycle breakdown:
//     total/tombstoned/alive/never_published_source_sequence_0/
//     published_source_sequence_gt0), and "admissions_coverage.note"
//     (an empty admitted-tool set deletes the admission row; see
//     internal/storage/session_admissions.go:38-43).
//   - report-template.md grew a Findings "[SOURCES: ...]" column, a
//     MIGRATION note, and a "## Migration" section with the deployment
//     checklist.
//   - The previous workspace_id derivation (8 hex chars) mis-scoped every
//     run against the harness ledger (16 hex chars); the corrected
//     derivation is disclosed in the output under "derivation".
//
// WHY NO go.mod REPLACE: nothing in this module imports the skill package,
// so a fake "replace" directive would be an inert footgun that silently
// redirects any future real dependency on that path. The migration is
// recorded here and in report-template.md instead; queries.py, this file,
// and report-template.md together form the v1->v2 contract.
//
// Consumers: findings templates and dashboards reading report JSON should
// move to the v2 field names; v1 field names keep parsing (nothing was
// removed or rekeyed).
package mivia
