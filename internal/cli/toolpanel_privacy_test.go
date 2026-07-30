package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// installTestRedactionPolicy configures a workspace-style redaction policy for
// the duration of one test.
//
// The policy is process-wide state (see internal/redact), so every test that
// installs one must stay sequential: `go test` resumes parallel tests only
// after the sequential pass has finished, so a serial test's policy is torn
// down before any parallel test observes it. Do not add t.Parallel() to a test
// that calls this, and do not call it from a parallel test.
func installTestRedactionPolicy(t *testing.T) {
	t.Helper()
	policy, err := redact.Compile([]string{
		// Bearer first: the generic key/value rule below would otherwise consume
		// "Authorization: Bearer" and leave the credential itself behind.
		`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`,
		`(?i)(?:["']?)(?:api[_-]?key|authorization|password|secret|token|private[_-]?key)(?:["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s}]+)`,
		`(?is)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]+PRIVATE KEY-----|$)`,
	}, []string{"password", "token", "secret", "api_key", "authorization"}, redact.DefaultPlaceholder)
	if err != nil {
		t.Fatalf("compile test redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(nil) })
}

// TestRedactPreviewWithoutPolicyPassesThrough documents the fail-open default:
// an unconfigured workspace redacts nothing in the TUI preview path. See
// .mivia/plans/archived/10-configurable-redaction.md §2 and §5.
func TestRedactPreviewWithoutPolicyPassesThrough(t *testing.T) {
	redact.SetPolicy(nil)
	for _, in := range []string{
		"api_key=secret-value",
		"Authorization: Bearer abc.def",
		fakePEMBlock(),
	} {
		if got := redactPreview(in); got != in {
			t.Fatalf("unconfigured workspace redacted %q -> %q", in, got)
		}
	}
}

func TestRedactPreviewRedactsWithConfiguredPolicy(t *testing.T) {
	installTestRedactionPolicy(t)
	got := redactPreview(fakePEMBlock())
	if strings.Contains(got, "MIIBOgIB") || strings.Contains(got, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("private key block leaked: %q", got)
	}
	if !strings.Contains(got, redact.DefaultPlaceholder) {
		t.Fatalf("expected placeholder %q: %q", redact.DefaultPlaceholder, got)
	}
}

func TestWritePreviewSectionRedactsSensitiveValuesAndCapsLines(t *testing.T) {
	installTestRedactionPolicy(t)
	var b strings.Builder
	writePreviewSection(&b, "input", strings.Repeat("line\n", 20)+"api_key=secret-value\nAuthorization: Bearer abc.def", 80, 6, false)
	out := b.String()
	if strings.Contains(out, "secret-value") || strings.Contains(out, "abc.def") {
		t.Fatalf("preview leaked sensitive value: %q", out)
	}
	if !strings.Contains(out, redact.DefaultPlaceholder) {
		t.Fatalf("expected redaction marker: %q", out)
	}
	if !strings.Contains(out, "more") {
		t.Fatalf("expected line cap marker: %q", out)
	}
}

// TestWritePreviewSectionWithoutPolicyShowsValues is the counterpart: the line
// cap still applies, but nothing is elided when no policy is configured.
func TestWritePreviewSectionWithoutPolicyShowsValues(t *testing.T) {
	redact.SetPolicy(nil)
	var b strings.Builder
	writePreviewSection(&b, "input", strings.Repeat("line\n", 20)+"api_key=secret-value", 80, 6, false)
	out := b.String()
	if !strings.Contains(out, "secret-value") {
		t.Fatalf("unconfigured workspace redacted preview: %q", out)
	}
	if !strings.Contains(out, "more") {
		t.Fatalf("expected line cap marker: %q", out)
	}
}

// fakePEMBlock builds a private-key-shaped fixture at runtime. As a literal it
// trips scripts/secret_scan.py, which cannot distinguish a test fixture from a
// real leak — and should not have to.
func fakePEMBlock() string {
	return "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIBOgIB\n-----END " + "RSA PRIVATE KEY-----"
}
