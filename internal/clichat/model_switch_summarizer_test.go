package clichat

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// namedStubCompleter is stubAgentCompleter with a caller-chosen Name, so a
// test can tell which of two summarizer bindings a session currently holds.
type namedStubCompleter struct{ name string }

func (c namedStubCompleter) Name() string { return c.name }
func (c namedStubCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "ok", nil
}
func (c namedStubCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (c namedStubCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}

// TestPublishModelSwitchRefreshesTheSummarizer pins the fix for the stale-
// summarizer residual: summaryWiring captures the session's binding once at
// setup, and SwitchBinding never rebuilt it on its own - so every summary
// after a mid-session /model switch kept running through the pre-switch
// model/completer until the session restarted. publishModelSwitch must now
// leave the summarizer bound to the model actually active after the switch.
func TestPublishModelSwitchRefreshesTheSummarizer(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	res.ProviderName = "provider-a"
	res.Model = "model-a"

	session := chat.NewSession(res, namedStubCompleter{name: "provider-a"})
	if err := enableSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	before, ok := session.CurrentSummarizerBinding()
	if !ok || before.Provider != "provider-a" || before.Model != "model-a" {
		t.Fatalf("setup summarizer binding = %+v, ok=%v, want provider-a/model-a", before, ok)
	}

	// Switch away from the config-default binding directly (mirrors the
	// buildModelBinding path publishModelSwitch's fallback branch takes for
	// an unconfigured provider), then refresh - the seam publishModelSwitch
	// itself calls after every successful switch.
	switched := chat.ModelBinding{
		ProviderName: "provider-b",
		Model:        "model-b",
		Completer:    namedStubCompleter{name: "provider-b"},
		Profile:      config.ModelSpec{Name: "model-b", ContextWindowTokens: chat.DefaultMaxContextTokens},
	}
	if err := session.SwitchBinding(switched); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	refreshSummarizerAfterModelSwitch(session, res)

	after, ok := session.CurrentSummarizerBinding()
	if !ok {
		t.Fatal("summarizer was cleared instead of refreshed")
	}
	if after.Provider != "provider-b" || after.Model != "model-b" {
		t.Fatalf("summarizer binding after switch = %+v, want provider-b/model-b (stale binding bug)", after)
	}
}
