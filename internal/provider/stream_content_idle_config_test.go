package provider

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestNewForProviderAppliesContentIdleTimeout proves the resolved [provider]
// stream_content_idle_timeout_seconds reaches the process-wide content-idle
// bound when a client is constructed. The hand-built Resolved carries a
// provider runtime with APIKeySet=true because NewForProvider fails closed
// without a key. No t.Parallel: the test touches process-wide atomics.
func TestNewForProviderAppliesContentIdleTimeout(t *testing.T) {
	// Snapshot and restore every watchdog atomic NewForProvider can touch.
	withWatchdogTimeouts(t, streamIdleTimeout(), streamFirstByteTimeout())
	withStreamContentIdleTimeout(t, streamContentIdleTimeout())

	res := &config.Resolved{
		ProviderName: "llmproxycli",
		BaseURL:      "http://127.0.0.1:8317/v1",
		APIKeyEnv:    "CLIPROXY_API_KEY",
		APIKey:       "fake-key",
		APIKeySet:    true,
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"llmproxycli": {
				ProviderName: "llmproxycli",
				BaseURL:      "http://127.0.0.1:8317/v1",
				APIKeyEnv:    "CLIPROXY_API_KEY",
				APIKey:       "fake-key",
				APIKeySet:    true,
			},
		},
		StreamContentIdleTimeout: 55 * time.Second,
	}
	if _, err := NewForProvider(res, "llmproxycli"); err != nil {
		t.Fatal(err)
	}
	if got := streamContentIdleTimeout(); got != 55*time.Second {
		t.Fatalf("content-idle timeout = %s after NewForProvider, want the configured 55s", got)
	}
}
