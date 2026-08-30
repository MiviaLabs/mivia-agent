package provider

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestConfigWatchdogMirrorConstantsStayEqual pins the duplicated defaults in
// internal/config to this package's own watchdog defaults. internal/config
// cannot import internal/provider (import cycle), so it carries second-based
// mirror constants; this test is the one gate that keeps the two sides equal.
func TestConfigWatchdogMirrorConstantsStayEqual(t *testing.T) {
	if got := config.DefaultStreamIdleTimeoutSeconds * time.Second; got != DefaultStreamIdleTimeout {
		t.Fatalf("config.DefaultStreamIdleTimeoutSeconds = %s, want %s", got, DefaultStreamIdleTimeout)
	}
	if got := config.DefaultStreamFirstByteTimeoutSeconds * time.Second; got != DefaultStreamFirstByteTimeout {
		t.Fatalf("config.DefaultStreamFirstByteTimeoutSeconds = %s, want %s", got, DefaultStreamFirstByteTimeout)
	}
	if got := config.DefaultStreamContentIdleTimeoutSeconds * time.Second; got != DefaultStreamContentIdleTimeout {
		t.Fatalf("config.DefaultStreamContentIdleTimeoutSeconds = %s, want %s", got, DefaultStreamContentIdleTimeout)
	}
	if got := config.DefaultProviderHTTPTimeoutSeconds * time.Second; got != DefaultHTTPTimeout {
		t.Fatalf("config.DefaultProviderHTTPTimeoutSeconds = %s, want %s", got, DefaultHTTPTimeout)
	}
}
