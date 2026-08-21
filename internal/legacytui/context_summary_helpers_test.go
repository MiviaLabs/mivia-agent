package legacytui

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// resolvedWithPatterns and summaryWiringResolved are package-local copies of
// internal/cli's helpers of the same name (context_redaction_test.go,
// context_summary_setup_test.go): cli's staying summary-wiring tests need
// their own copy.
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

func summaryWiringResolved(t *testing.T, enabled bool) *config.Resolved {
	t.Helper()
	res := resolvedWithPatterns(t, []string{`(?i)token\s*=\s*\S+`}, nil)
	res.ProviderName = "stub"
	res.Model = "stub-model"
	res.BaseURL = "https://api.stub.invalid"
	res.SystemPrompt = "sys"
	res.Context.Summary.Enabled = &enabled
	return res
}
