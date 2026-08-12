package provider

// Hostile functional audit of the Round-4 ollama changes: fail-closed
// construction errors must name the provider and the actionable fix.
// TEST-ONLY.

import (
	"strings"
	"testing"
)

// Focus 6: every new construction-time fail-closed error names the provider
// (ollama) and points at the actionable fix (the loopback base_url).
func TestR4NewOllamaFailClosedErrorsNameProviderAndFix(t *testing.T) {
	installLocalhostResolver(t, "203.0.113.7")
	comp, err := NewOllama(Options{BaseURL: "http://localhost:11434/v1"})
	if err == nil {
		t.Fatal("expected fail-closed error under hostile localhost resolution")
	}
	if comp != nil {
		t.Fatalf("NewOllama returned a completer alongside error %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "ollama") {
		t.Fatalf("fail-closed error does not name the provider: %q", msg)
	}
	if !strings.Contains(msg, "base_url") {
		t.Fatalf("fail-closed error does not point at the fix: %q", msg)
	}
}
