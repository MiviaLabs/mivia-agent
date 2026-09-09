package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestPublishAndTakeToolScopeNotice pins publishToolScopeNotice directly:
// every call overwrites the single slot, and take drains it exactly once.
func TestPublishAndTakeToolScopeNotice(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, nil)
	pool := NewSessionPool(sess, res, nil, true)

	pool.publishToolScopeNotice("first")
	pool.publishToolScopeNotice("second") // overwrites, never accumulates

	if got := pool.takeToolScopeNotice(); got != "second" {
		t.Fatalf("takeToolScopeNotice = %q, want the last published notice", got)
	}
	if got := pool.takeToolScopeNotice(); got != "" {
		t.Fatalf("second drain = %q, want empty (single-slot)", got)
	}
}
