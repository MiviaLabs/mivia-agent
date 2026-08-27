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
//
// SetTurnWaiterForTest installs wg as the WaitGroup every per-turn
// goroutine calls Done() on. Pass nil to clear the seam.
func SetTurnWaiterForTest(wg *sync.WaitGroup) { turnWaiter = wg }
