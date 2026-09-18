// Package orchestrationnotify queues lifecycle notifications from workflow
// runs and drains them into provider messages for the session's next turn.
//
// It may import internal/ledger for lifecycle events and internal/provider
// for message types. The orchestration packages may import it.
package orchestrationnotify
