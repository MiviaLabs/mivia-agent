package demoharness

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// credentialMarkers are the substrings that mean "what follows looks
// like a secret value", the same shapes settings-screen.md §5 names:
// --token=, --api-key=, key=, and an HTTP Authorization header.
var credentialMarkers = []string{"token=", "api-key=", "api_key=", "apikey=", "Bearer ", "password="}

// approvedFakePatterns are the only strings allowed to follow a
// credential marker in this repo's own fixtures, per
// .agents/rules/10-security-privacy.md's "obviously fake values" rule.
var approvedFakePatterns = []string{"sk-test-not-real", "example"}

// TestSeedCredentialShapedValuesAreObviouslyFake is the seed lint
// docs/design/settings-screen.md §5 calls for.
// scripts/secret_scan.py is deliberately high-confidence-only (AKIA,
// ghp_, PEM blocks) and will not flag a realistic-looking fake MCP
// argument, so this test is the only gate that ever inspects this
// package's committed testdata-equivalent seed strings for looking
// like a real credential.
func TestSeedCredentialShapedValuesAreObviouslyFake(t *testing.T) {
	for _, args := range collectStrings(seedMCPServers()) {
		checkNoUnapprovedCredentialShape(t, "seedMCPServers", args)
	}
	for _, s := range collectStrings(seedProviders()) {
		checkNoUnapprovedCredentialShape(t, "seedProviders", s)
	}
	for _, s := range collectStrings(seedAgents()) {
		checkNoUnapprovedCredentialShape(t, "seedAgents", s)
	}
	for _, s := range collectStrings(seedAutomations()) {
		checkNoUnapprovedCredentialShape(t, "seedAutomations", s)
	}
}

func checkNoUnapprovedCredentialShape(t *testing.T, source, s string) {
	t.Helper()
	for _, marker := range credentialMarkers {
		i := strings.Index(s, marker)
		if i < 0 {
			continue
		}
		approved := false
		for _, pat := range approvedFakePatterns {
			if strings.Contains(s, pat) {
				approved = true
				break
			}
		}
		if !approved {
			t.Errorf("%s: %q carries %q with no approved fake pattern (%v) after it - "+
				"looks like a real credential", source, s, marker, approvedFakePatterns)
		}
	}
}

func collectStrings(v any) []string {
	switch x := v.(type) {
	case []ports.MCPServerView:
		var out []string
		for _, s := range x {
			out = append(out, s.Command, s.Endpoint)
			out = append(out, s.Args...)
		}
		return out
	case []ports.ProviderView:
		var out []string
		for _, p := range x {
			out = append(out, p.Name, p.BaseURL, p.APIKeyEnv)
		}
		return out
	case []ports.AgentView:
		var out []string
		for _, a := range x {
			out = append(out, a.Name, a.Description)
		}
		return out
	case []ports.Automation:
		var out []string
		for _, a := range x {
			out = append(out, a.Name, a.Description)
			if a.Trigger.Schedule != nil {
				out = append(out, a.Trigger.Schedule.Cron, a.Trigger.Schedule.TZ)
			}
		}
		return out
	}
	return nil
}

// TestEndpointNeverCarriesUserinfoOrQuery pins settings-screen.md §5's
// URL-splits-at-projection rule: MCPServerView.Endpoint must be
// scheme://host/path only, never the full URL a real config could
// carry ("https://user:pw@host?api_key=...").
func TestEndpointNeverCarriesUserinfoOrQuery(t *testing.T) {
	for _, s := range seedMCPServers() {
		if strings.ContainsAny(s.Endpoint, "@?") {
			t.Errorf("server %q: Endpoint %q looks like it carries userinfo or a query string", s.ID, s.Endpoint)
		}
	}
}

// TestApplyErrorsNeverEchoServerArgs is the taint test at the fake's
// boundary: every error path an Apply call can take is exercised with
// a canary planted in a server's Args, and the canary must never
// surface in a SaveEvent.Message - the one string a real adapter would
// also have to sanitise (settings-screen.md §5 point 4).
func TestApplyErrorsNeverEchoServerArgs(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	const canary = "sk-test-not-real-DO-NOT-LEAK"
	settings := h.SettingsAdapters()
	handle, err := settings.MCP.Apply(context.Background(), ports.ScopeUser, ports.UpsertMCPServer{
		Server: ports.MCPServerView{ID: "canary-server", Args: []string{"--token=" + canary}},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainSaveHandle(t, handle, canary)

	// Trigger every documented MCP failure path and check each message.
	failing := []ports.MCPEdit{
		ports.RemoveMCPServer{ID: "does-not-exist"},
		ports.SetMCPServerEnabled{ID: "does-not-exist", On: true},
	}
	for _, e := range failing {
		h, err := settings.MCP.Apply(context.Background(), ports.ScopeUser, e)
		if err != nil {
			t.Fatal(err)
		}
		drainSaveHandle(t, h, canary)
	}
}

func drainSaveHandle(t *testing.T, h ports.SaveHandle, forbidden string) {
	t.Helper()
	for ev := range h.Events() {
		if strings.Contains(ev.Message, forbidden) {
			t.Errorf("SaveEvent.Message leaked the canary: %q", ev.Message)
		}
	}
}
