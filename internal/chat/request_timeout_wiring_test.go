package chat

import (
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestBuildAgentTurnOptionsUsesConfiguredRequestTimeout pins the wiring
// config -> session -> snapshot -> agent.Options: a Resolved that carries a
// [chat] request_timeout_seconds value must reach the agent loop as
// Options.RequestTimeout, replacing the old compiled 15-minute literal.
func TestBuildAgentTurnOptionsUsesConfiguredRequestTimeout(t *testing.T) {
	res := &config.Resolved{
		ProviderName:       "test",
		Model:              "test-model",
		ChatRequestTimeout: 1200 * time.Second,
	}
	sess := NewSession(res, &fakeCompleter{out: "ok"})
	if sess.RequestTimeout != 1200*time.Second {
		t.Fatalf("session request timeout = %s, want 1200s", sess.RequestTimeout)
	}
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if snapshot.requestTimeout != 1200*time.Second {
		t.Fatalf("snapshot request timeout = %s, want session value 1200s", snapshot.requestTimeout)
	}
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	if opts.RequestTimeout != 1200*time.Second {
		t.Fatalf("agent.Options.RequestTimeout = %s, want the configured 1200s", opts.RequestTimeout)
	}
}

// TestBuildAgentTurnOptionsDefaultsZeroRequestTimeout proves a session built
// without config (hand-built Resolved, zero ChatRequestTimeout) falls back
// to DefaultRequestTimeout instead of handing the loop a zero deadline.
func TestBuildAgentTurnOptionsDefaultsZeroRequestTimeout(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	if opts.RequestTimeout != DefaultRequestTimeout {
		t.Fatalf("agent.Options.RequestTimeout = %s, want fallback %s", opts.RequestTimeout, DefaultRequestTimeout)
	}
}
