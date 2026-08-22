package clichat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

func resolvedWithPatterns(t *testing.T, patterns, keyNames []string) *config.Resolved {
	t.Helper()
	compiled, err := redact.Compile(patterns, keyNames, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	return &config.Resolved{
		RedactionPolicy: compiled,
		Privacy:         config.PrivacyConfig{RedactionPatterns: patterns, RedactionKeyNames: keyNames},
	}
}

// TestContextRedactionPolicyReachesTheProjector is the regression for a
// configured privacy control that did nothing: SetContextRedactionPolicy had no
// production caller, so [privacy] patterns classified tool previews and event
// bodies while durable context payloads went unclassified.
func TestContextRedactionPolicyReachesTheProjector(t *testing.T) {
	res := resolvedWithPatterns(t, []string{`(?i)token\s*=\s*\S+`}, nil)
	policy := contextRedactionPolicy(res)
	if !policy.Configured {
		t.Fatal("configured [privacy] patterns did not reach the context policy")
	}
	if policy.Redactor == nil {
		t.Fatal("context policy has no redactor, so a flagged payload could never be stored")
	}
	if got := string(policy.Redactor([]byte("here is token=hunter2 ok"))); strings.Contains(got, "hunter2") {
		t.Fatalf("redactor left the secret in place: %q", got)
	}
}

// TestContextRedactionPolicyUsesTheProcessRedactor pins that the durable path
// redacts through the SAME compiled policy as every other surface, rather than
// a second implementation that can drift from it.
func TestContextRedactionPolicyUsesTheProcessRedactor(t *testing.T) {
	patterns := []string{`(?i)secret-[a-z0-9]+`}
	res := resolvedWithPatterns(t, patterns, nil)
	const raw = "value secret-abc123 end"
	want := res.RedactionPolicy.Text(raw)
	if got := string(contextRedactionPolicy(res).Redactor([]byte(raw))); got != want {
		t.Fatalf("context redactor = %q, process redactor = %q", got, want)
	}
}

// TestUnconfiguredWorkspaceKeepsMetadataOnlyPayloads keeps the fail-open
// default (INV-SEC-2): configuring nothing must not start storing message
// bytes, and must not classify anything either.
func TestUnconfiguredWorkspaceKeepsMetadataOnlyPayloads(t *testing.T) {
	for name, res := range map[string]*config.Resolved{
		"nil resolved":   nil,
		"no policy":      {},
		"empty patterns": resolvedWithPatterns(t, nil, nil),
	} {
		if got := contextRedactionPolicy(res); got.Configured || got.Redactor != nil {
			t.Fatalf("%s produced a configured context policy: %+v", name, got)
		}
	}
}

// TestConfiguredSessionStoresRedactedPayloads drives the wired policy through
// the projection boundary the session actually uses.
func TestConfiguredSessionStoresRedactedPayloads(t *testing.T) {
	res := resolvedWithPatterns(t, []string{`(?i)token\s*=\s*\S+`}, nil)
	principal, err := contextstate.NewPrincipal("workspace", "session", "local-user")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := contextstate.SanitizeSourcePayload(t.Context(), principal, []byte("here is token=hunter2 ok"), contextRedactionPolicy(res))
	if err != nil {
		t.Fatalf("a secret-bearing message refused the turn: %v", err)
	}
	if strings.Contains(string(payload.Bytes), "hunter2") {
		t.Fatalf("durable payload carries the secret: %q", payload.Bytes)
	}
	if !payload.Dereferenceable {
		t.Fatalf("redacted payload was not stored: %+v", payload)
	}
}
