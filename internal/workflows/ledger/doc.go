// Package ledger provides durable persistence for workflow runs and the
// generic plan-and-task ledger. It implements the workflow repository
// contract with SQLite projections following the established storage
// migration pattern.
//
// Task ledger: one mechanism serves many scopes: sessions, workflow steps,
// agents, workflows and runs all store plans and task statuses through the
// same API. The engine stores, transitions and queries; consumers define
// their own status vocabulary. Statuses are OPAQUE strings: this package
// never interprets them, only validates non-empty and journals transitions.
//
// Durability: every mutation appends one event to a shared storage.Store
// (the same primitive the workflow ledger builds on). The in-memory
// projection is rebuilt from the event log on catch-up, so state survives
// restarts and each mutation is atomic with its journal entry.
//
// It may import internal/ledger/core, internal/storage, internal/runtime,
// internal/redact, and internal/textutil. The internal/workflows packages
// and internal/cli/** may import it.
package ledger
