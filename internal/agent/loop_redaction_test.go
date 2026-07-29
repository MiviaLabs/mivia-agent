package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// testRedactionPatterns is a *workspace* policy, not a compiled default. Since
// plan 10 nothing is a secret until configuration says so, so every test that
// asserts redaction fires has to bring its own policy; the assertions below
// exercise the engine and this call site's wiring, not a list shipped in the
// binary.
//
// Two deliberate departures from the recommended list in plan 10 §2, both
// documented there as hazards of pattern ordering and shape rather than of this
// package:
//
//  1. The bearer rule runs FIRST. The key-name rule's value part stops at the
//     first token, so on "Authorization: Bearer <tok>" it consumes only the
//     scheme word and leaves the credential in the preview. Removing the bearer
//     match first makes the order irrelevant.
//  2. The key-name rule tolerates the JSON quoting around `"token":"…"`. Tool
//     arguments reach the default (non-opt-in) preview path as raw JSON text,
//     where an unquoted `key\s*[:=]` rule never matches.
var testRedactionPatterns = []string{
	`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`,
	`(?i)(?:password|passwd|token|secret|api[_-]?key|authorization)"?\s*[:=]\s*"?[^\s",;]*`,
	`(?:sk-ant-|sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9._~-]+`,
	`(?is)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]+PRIVATE KEY-----|$)`,
}

// testRedactionKeyNames drives whole-value elision of JSON fields by key name.
var testRedactionKeyNames = []string{"password", "token", "secret", "api_key", "authorization"}

// installTestRedactionPolicy installs the process-wide policy for one test and
// restores the unconfigured (redacts-nothing) default afterwards, so a policy
// never leaks into a test asserting the fail-open posture. The policy is a
// process-wide global: no test in this package may call t.Parallel while
// depending on it.
func installTestRedactionPolicy(t *testing.T) {
	t.Helper()
	policy, err := redact.Compile(testRedactionPatterns, testRedactionKeyNames, "[redacted]")
	if err != nil {
		t.Fatalf("compile test redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(nil) })
}

// TestPreviewsWithoutPolicyRedactNothing documents the posture plan 10 §5 sells:
// an unconfigured workspace redacts nothing, anywhere. It is the load-bearing
// test for this call site — if a pattern list ever grows back into the binary,
// this is what fails.
func TestPreviewsWithoutPolicyRedactNothing(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	const credential = "sk-ant-aaaabbbbccccdddd"
	raw := `{"command":"curl -H 'Authorization: Bearer ` + credential + `'"}`
	if got := redactToolInput(raw); got != raw {
		t.Fatalf("unconfigured input preview altered the argv: %q", got)
	}
	body := "Authorization: Bearer " + credential
	if got := redactToolOutput(body); got != body {
		t.Fatalf("unconfigured output preview altered the body: %q", got)
	}
}

func TestRedactToolInputDefaultShowsArgs(t *testing.T) {
	// A configured policy is installed so this asserts what it always meant:
	// the opt-in flag, not the policy, is what hides non-credential arguments.
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	raw := `{"path":"x.txt","pattern":"visible-when-off"}`
	got := redactToolInput(raw)
	if !strings.Contains(got, "visible-when-off") {
		t.Fatalf("default should show args: %q", got)
	}
}

func TestRedactToolInputConfiguredPolicyRedactsCredentialsWithoutOptIn(t *testing.T) {
	// The opt-in flag buys stricter whole-field elision; it is not the switch
	// that decides whether the configured patterns apply. Previews fan out to
	// every event sink and log, so once a workspace has configured a policy its
	// patterns must fire on the non-opt-in path too.
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	cases := []struct {
		name   string
		raw    string
		secret string
		keep   string
	}{
		{
			name:   "env-prefixed command",
			raw:    `{"command":"AUTH_TOKEN=abcd1234efgh curl https://api.example.com"}`,
			secret: "abcd1234efgh",
			keep:   "curl",
		},
		{
			name:   "dotenv write",
			raw:    `{"path":".env","content":"API_KEY=zzz-super-secret\nPORT=8080"}`,
			secret: "zzz-super-secret",
			keep:   ".env",
		},
		{
			name:   "bearer header",
			raw:    `{"command":"curl -H 'Authorization: Bearer tok-abcdef123456'"}`,
			secret: "tok-abcdef123456",
			keep:   "curl",
		},
		{
			// The engine replaces a whole match with the placeholder, so the
			// matched key name is gone too — the old hardcoded scrubber kept it
			// ("password=[redacted]") via a capture group no configured pattern
			// can express. There is nothing left to keep here.
			name:   "malformed non-json argv",
			raw:    `password=hunter2-not-json`,
			secret: "hunter2-not-json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactToolInput(tc.raw)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("default path leaked credential: %q", got)
			}
			if tc.keep != "" && !strings.Contains(got, tc.keep) {
				t.Fatalf("default path over-redacted, want %q visible in %q", tc.keep, got)
			}
			if !utf8.ValidString(got) || len(got) > 256 {
				t.Fatalf("preview invalid/beyond cap: valid=%v len=%d", utf8.ValidString(got), len(got))
			}
		})
	}
}

func TestRedactToolInputOptInStillScrubsNestedArgvSecrets(t *testing.T) {
	// Opt-in must be a superset of the default: field-name elision alone misses
	// secrets embedded inside an innocuously named field such as "command".
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	got := redactToolInput(`{"command":"AUTH_TOKEN=abcd1234efgh curl https://api.example.com"}`)
	if strings.Contains(got, "abcd1234efgh") {
		t.Fatalf("opt-in path leaked credential: %q", got)
	}
}

// TestRedactToolInputOptInWithoutPolicyKeepsKeyNamedFields is the same posture
// on the opt-in path: whole-field elision by key name is policy-driven too, so
// with no policy a "token" field survives. The content byte-count elision is
// NOT policy-driven — it is preview-size control, not credential redaction —
// so it still applies.
func TestRedactToolInputOptInWithoutPolicyKeepsKeyNamedFields(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	got := redactToolInput(`{"token":"do-not-hide","content":"0123456789"}`)
	if !strings.Contains(got, "do-not-hide") {
		t.Fatalf("unconfigured opt-in path redacted a key-named field: %q", got)
	}
	if !strings.Contains(got, "[content 10 bytes]") {
		t.Fatalf("content size elision must survive independently of policy: %q", got)
	}
}

// TestRedactToolInputOptInElidesContentBySize keeps the byte-count preview
// local: it bounds how much file text lands in an event, which is a different
// job from hiding a credential, and a workspace turning redaction off must not
// start pushing whole file bodies through every EventBus sink.
func TestRedactToolInputOptInElidesContentBySize(t *testing.T) {
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	got := redactToolInput(`{"path":"a.txt","content":"hello world","nested":{"content":"abc"}}`)
	if !strings.Contains(got, "[content 11 bytes]") || !strings.Contains(got, "[content 3 bytes]") {
		t.Fatalf("content elision missing at one level: %q", got)
	}
	if strings.Contains(got, "hello world") || strings.Contains(got, `"abc"`) {
		t.Fatalf("content body leaked into the preview: %q", got)
	}
	if !strings.Contains(got, "a.txt") {
		t.Fatalf("non-content args should stay visible: %q", got)
	}
}

func TestConfiguredPolicyCoversBearerSchemeAfterHeaderName(t *testing.T) {
	// A key-name rule matches "Authorization:" before a bearer rule reaches
	// "Bearer", and its value part consumes only the scheme word — so a policy
	// that runs the key-name rule first replaces "Authorization: Bearer" and
	// leaves the credential in the preview. testRedactionPatterns puts the
	// bearer rule first for exactly this reason.
	installTestRedactionPolicy(t)
	got := redactToolOutput("Authorization: Bearer tok-abcdef123456 trailing")
	if strings.Contains(got, "tok-abcdef123456") {
		t.Fatalf("output preview leaked bearer credential: %q", got)
	}
	if !strings.Contains(got, "trailing") {
		t.Fatalf("over-redacted past the credential: %q", got)
	}
}

// A match is replaced by the configured placeholder and nothing else. The old
// hardcoded scrubber re-emitted the matched key name ("password=[redacted]"),
// which wrote a stray leading "=" over ordinary prose whenever the match had no
// key in front of it. The engine has no such branch; this pins that it stays
// gone in both shapes.
func TestConfiguredPolicyReplacesMatchWithPlaceholderOnly(t *testing.T) {
	installTestRedactionPolicy(t)
	got := redactToolOutput("Bearer of bad news, said the report")
	if strings.Contains(got, "=[redacted]") {
		t.Fatalf("stray key separator on a bare match: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("bare credential-shaped match should still be redacted: %q", got)
	}
	keyed := redactToolOutput(`{"api_key":"zzz-secret-value"}`)
	if strings.Contains(keyed, "zzz-secret-value") {
		t.Fatalf("keyed secret survived: %q", keyed)
	}
	if strings.Contains(keyed, "api_key=[redacted]") {
		t.Fatalf("placeholder must replace the whole match, key name included: %q", keyed)
	}
}

func TestToolPreviewRedactionAndUTF8Bounds(t *testing.T) {
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	input := `{"path":"safe.txt","nested":{"token":"input-secret"},"content":"prompt-secret"}`
	gotInput := redactToolInput(input)
	if strings.Contains(gotInput, "input-secret") || strings.Contains(gotInput, "prompt-secret") {
		t.Fatalf("input leaked secret: %q", gotInput)
	}
	if !utf8.ValidString(gotInput) || len(gotInput) > 256 {
		t.Fatalf("input preview invalid/beyond cap: valid=%v len=%d", utf8.ValidString(gotInput), len(gotInput))
	}
	malformed := redactToolInput(`token=malformed-secret`)
	if strings.Contains(malformed, "malformed-secret") {
		t.Fatalf("malformed input leaked secret: %q", malformed)
	}
	providerKey := "sk-ant-" + strings.Repeat("a", 20)
	output := redactToolOutput("Authorization: Bearer bearer-secret " + providerKey + "\n" + strings.Repeat("界", 400))
	if strings.Contains(output, "bearer-secret") || strings.Contains(output, providerKey) {
		t.Fatalf("output leaked credential: %q", output)
	}
	if !utf8.ValidString(output) || len(output) > 512 {
		t.Fatalf("output preview invalid/beyond cap: valid=%v len=%d", utf8.ValidString(output), len(output))
	}
}

func TestToolPreviewRedaction_RemovesCompletePrivateKeyBlock(t *testing.T) {
	installTestRedactionPolicy(t)
	begin := strings.Join([]string{"-----BEGIN RSA", " PRIVATE KEY-----"}, "")
	end := strings.Join([]string{"-----END RSA", " PRIVATE KEY-----"}, "")
	output := begin + "\nopaque-body\n" + end
	got := redactToolOutputForTool("search_replace", output)
	if strings.Contains(got, "opaque-body") || strings.Contains(got, "BEGIN RSA") {
		t.Fatalf("private key material leaked: %q", got)
	}
	incomplete := strings.Join([]string{"-----BEGIN RSA", " PRIVATE KEY-----\ntruncated-body"}, "")
	if got := redactToolOutputForTool("search_replace", incomplete); strings.Contains(got, "truncated-body") {
		t.Fatalf("incomplete private key material leaked: %q", got)
	}
}
