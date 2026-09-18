// Package controller is the workflow run controller: it admits and drives
// run steps, dispatches agents, applies the write-path blocklist, runs
// verifiers, and settles step outcomes against the durable ledger.
//
// It may import the other internal/workflows packages, internal/ledger, and
// the runtime packages (internal/agents, internal/coordinator, and their
// peers). The internal/cli and composition packages may import it.
package controller
