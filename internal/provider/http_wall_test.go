package provider

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// withHTTPClientTimeout sets the process-wide http.Client wall for one test
// and restores the previous value at cleanup, mirroring withWatchdogTimeouts.
// Tests that touch the atomic must not run in parallel.
func withHTTPClientTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := httpClientTimeout()
	SetHTTPClientTimeout(d)
	t.Cleanup(func() { SetHTTPClientTimeout(prev) })
}

// clientWallOf returns the constructed http.Client wall of a Completer.
func clientWallOf(t *testing.T, c Completer) time.Duration {
	t.Helper()
	switch v := c.(type) {
	case *OpenAICompat:
		return v.client.Timeout
	case *AnthropicCompleter:
		return v.httpClient.Timeout
	}
	t.Fatalf("completer %T carries no known http.Client", c)
	return 0
}

// TestSetHTTPClientTimeout proves a non-positive update never resets the
// wall to zero - a zero http.Client.Timeout means no wall at all.
func TestSetHTTPClientTimeout(t *testing.T) {
	withHTTPClientTimeout(t, 33*time.Minute)

	SetHTTPClientTimeout(0)
	if got := httpClientTimeout(); got != 33*time.Minute {
		t.Fatalf("wall changed to %s on a zero update, want unchanged 33m", got)
	}
	SetHTTPClientTimeout(-1)
	if got := httpClientTimeout(); got != 33*time.Minute {
		t.Fatalf("wall changed to %s on a negative update, want unchanged 33m", got)
	}
	SetHTTPClientTimeout(44 * time.Minute)
	if got := httpClientTimeout(); got != 44*time.Minute {
		t.Fatalf("wall = %s, want 44m", got)
	}
}

// TestEveryCompleterBuildsWithTheConfiguredWall proves every Completer
// implementation constructs its http.Client from the process-wide wall, not
// from a compiled literal - the exact sibling-drift class the conformance
// suite exists for.
func TestEveryCompleterBuildsWithTheConfiguredWall(t *testing.T) {
	withHTTPClientTimeout(t, 31*time.Minute)
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.build("http://127.0.0.1:0")
			if got := clientWallOf(t, c); got != 31*time.Minute {
				t.Fatalf("%s client wall = %s, want the configured 31m", tc.name, got)
			}
		})
	}
}

// deepseekResolved builds the minimal hand-built Resolved NewForProvider
// accepts for deepseek (APIKeySet must be true - construction fails closed
// without a key).
func deepseekResolved(wall time.Duration) *config.Resolved {
	return &config.Resolved{
		ProviderName: "deepseek",
		BaseURL:      "https://api.deepseek.com/v1",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		APIKey:       "fake-key",
		APIKeySet:    true,
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"deepseek": {
				ProviderName: "deepseek",
				BaseURL:      "https://api.deepseek.com/v1",
				APIKeyEnv:    "DEEPSEEK_API_KEY",
				APIKey:       "fake-key",
				APIKeySet:    true,
			},
		},
		ProviderHTTPTimeout: wall,
	}
}

// TestNewForProviderRaisesClientWall proves the resolved derived wall
// reaches the constructed client, and a zero (hand-built Resolved) leaves
// the compiled default in place.
func TestNewForProviderRaisesClientWall(t *testing.T) {
	withHTTPClientTimeout(t, DefaultHTTPTimeout)

	comp, err := NewForProvider(deepseekResolved(31*time.Minute), "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if got := clientWallOf(t, comp); got != 31*time.Minute {
		t.Fatalf("client wall = %s, want the resolved 31m", got)
	}

	// Zero is non-positive: SetHTTPClientTimeout ignores it, and the client
	// keeps the compiled default.
	SetHTTPClientTimeout(DefaultHTTPTimeout)
	comp, err = NewForProvider(deepseekResolved(0), "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if got := clientWallOf(t, comp); got != DefaultHTTPTimeout {
		t.Fatalf("client wall = %s after a zero resolution, want DefaultHTTPTimeout %s (non-positive ignored)", got, DefaultHTTPTimeout)
	}
}
