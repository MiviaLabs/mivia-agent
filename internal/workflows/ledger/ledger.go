// Package ledger provides durable persistence for workflow runs and the
// generic plan-and-task ledger (D8). It implements the workflow repository
// contract with SQLite projections following the established storage
// migration pattern.
//
// Phase 2 deliverable — ledger, isolated worktree, and lifecycle.
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
package ledger
