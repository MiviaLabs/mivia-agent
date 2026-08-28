package uiadapter

import "sync"

// Test-only seam for asserting per-turn goroutine lifetime. The
// production Conversation has no observable signal that the per-turn
// goroutine returned end-to-end; the channel close happens before the
// goroutine's final defers (cancelTurn, turnMu.Unlock), so a test that
// only observes close cannot distinguish "goroutine finished" from
// "goroutine still tearing down". A package-private WaitGroup the
// goroutine calls Done() on as its very last defer closes that gap.
//
// The seam is package-private (turnWaiter in conversation.go is
// unexported); production callers cannot install one because this file
// lives in package uiadapter, not uiadapter_test, and tests reach it
// through this exported helper. Precedent:
// internal/tools/export_test.go (Tavily test redirects).
func SetTurnWaiterForTest(wg *sync.WaitGroup) { turnWaiter = wg }

// NamespacedTaskIDForTest exposes namespacedTaskID for unit testing.
func NamespacedTaskIDForTest(namespace, rawID string) string {
	return namespacedTaskID(namespace, rawID)
}
