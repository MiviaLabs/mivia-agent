package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// summaryWiringResolved builds the smallest resolved config that can open the
// summary gate: a provider endpoint, a compiled [privacy] policy, and the
// [context.summary] flag under test.
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

func summaryWiringSession(t *testing.T, res *config.Resolved) *chat.Session {
	t.Helper()
	return chat.NewSession(res, stubAgentCompleter{})
}

// TestSummaryWiringDisabledByDefault pins the off switch: with the flag unset,
// setup stays structural-only and no summarizer exists.
func TestSummaryWiringDisabledByDefault(t *testing.T) {
	res := summaryWiringResolved(t, false)
	summarizer, policy, ok := summaryWiring(summaryWiringSession(t, res), res)
	if ok || summarizer != nil || policy.SummaryEnabled || policy.EndpointAllowlist != nil {
		t.Fatalf("disabled flag wired a summarizer: ok=%v summarizer=%v policy=%+v", ok, summarizer != nil, policy)
	}
}

// TestSummaryWiringDoesNotRequireRedaction pins the opt-out default. A
// workspace with no [privacy] section still gets a summarizer: compaction
// drops its messages permanently, so gating the only record of what was
// removed on an unrelated section meant those workspaces compacted blind.
//
// Redaction governs DURABILITY, not availability, so the captured policy must
// report RedactionConfigured honestly - that is what keeps summary content out
// of the checkpoint (Metadata persists a digest only). A wiring that claimed
// redaction was configured would silently persist content instead.
func TestSummaryWiringDoesNotRequireRedaction(t *testing.T) {
	res := summaryWiringResolved(t, true)
	res.RedactionPolicy = nil
	res.Privacy = config.PrivacyConfig{}
	summarizer, policy, ok := summaryWiring(summaryWiringSession(t, res), res)
	if !ok || summarizer == nil {
		t.Fatal("a workspace without [privacy] got no summarizer")
	}
	if policy.RedactionConfigured {
		t.Fatal("wiring claimed a configured redaction policy that does not exist")
	}
	if !policy.SummaryEnabled {
		t.Fatal("wiring did not enable the summary policy")
	}
}

// TestSummaryWiringRequiresEndpoint pins the network dependency: without a
// resolved provider endpoint the summary stays disabled. Setup never invents
// an endpoint (INV-AG-10 spirit: no invented refs, no invented allowlists).
func TestSummaryWiringRequiresEndpoint(t *testing.T) {
	res := summaryWiringResolved(t, true)
	res.BaseURL = ""
	summarizer, _, ok := summaryWiring(summaryWiringSession(t, res), res)
	if ok || summarizer != nil {
		t.Fatal("summary wiring enabled without a resolved provider endpoint")
	}
}

// TestSummaryWiringEnabled pins the full policy gate: the flag, the redaction
// policy, and the endpoint together produce a Summarizer whose captured
// PolicySnapshot satisfies every check Summarizer.available applies.
func TestSummaryWiringEnabled(t *testing.T) {
	res := summaryWiringResolved(t, true)
	session := summaryWiringSession(t, res)
	binding := session.CurrentBinding()
	summarizer, policy, ok := summaryWiring(session, res)
	if !ok || summarizer == nil {
		t.Fatal("enabled summary wiring produced no summarizer")
	}
	if !policy.SummaryEnabled || !policy.NetworkEnabled || !policy.RedactionConfigured {
		t.Fatalf("policy gate fields missing: %+v", policy)
	}
	if policy.Provider != binding.ProviderName || policy.Model != binding.Model {
		t.Fatalf("policy binding = %s/%s, want session binding %s/%s", policy.Provider, policy.Model, binding.ProviderName, binding.Model)
	}
	if policy.CredentialScope == "" {
		t.Fatal("policy has no credential scope")
	}
	if len(policy.EndpointAllowlist) != 1 || policy.EndpointAllowlist[0] != res.BaseURL {
		t.Fatalf("endpoint allowlist = %v, want [%s]", policy.EndpointAllowlist, res.BaseURL)
	}
	if len(policy.PolicyDigest) != 64 || strings.Trim(policy.PolicyDigest, "0123456789abcdef") != "" {
		t.Fatalf("policy digest %q is not 64 lowercase hex characters", policy.PolicyDigest)
	}
	if !reflect.DeepEqual(summarizer.Policy, policy) {
		t.Fatal("summarizer policy differs from the returned policy snapshot")
	}
	if summarizer.Binding.Provider != binding.ProviderName || summarizer.Binding.Model != binding.Model {
		t.Fatal("summarizer binding differs from the session binding")
	}
}

// TestSummaryPolicyDigestTracksPolicyContent pins that the digest changes when
// the redaction policy changes, so a policy switch cannot silently reuse a
// summarizer captured under the previous policy.
func TestSummaryPolicyDigestTracksPolicyContent(t *testing.T) {
	res := summaryWiringResolved(t, true)
	_, policy, ok := summaryWiring(summaryWiringSession(t, res), res)
	if !ok {
		t.Fatal("summary wiring refused an enabled config")
	}
	res.Privacy.RedactionPatterns = []string{`(?i)secret\s*=\s*\S+`}
	if _, next, ok := summaryWiring(summaryWiringSession(t, res), res); !ok || next.PolicyDigest == policy.PolicyDigest {
		t.Fatalf("digest did not change with the redaction policy: %q", next.PolicyDigest)
	}
}

// TestSummaryWiringUsesOverrideBinding pins the [context.summary]
// provider/model override path: the summary call binds the override
// provider/model and that provider's endpoint, never the session binding. A
// misconfigured override must degrade to structural-only (fail closed), not
// silently fall back to the session's model - the fallback is exactly what
// would keep charging the expensive session model for every compaction
// summary.
func TestSummaryWiringUsesOverrideBinding(t *testing.T) {
	res := summaryWiringResolved(t, true)
	provider, model := "openrouter", "cheap-model"
	res.Context.Summary.Provider = &provider
	res.Context.Summary.Model = &model
	res.ProviderRuntimes = map[string]config.ProviderRuntime{
		"stub": {ProviderName: "stub", BaseURL: "https://api.stub.invalid", APIKeyEnv: "STUB_KEY", APIKeySet: true},
		"openrouter": {
			ProviderName: "openrouter", BaseURL: "https://api.cheap.invalid",
			APIKeyEnv: "CHEAP_KEY", APIKeySet: true, APIKey: "cheap-key",
		},
	}
	session := summaryWiringSession(t, res)
	summarizer, policy, ok := summaryWiring(session, res)
	if !ok || summarizer == nil {
		t.Fatal("override wiring produced no summarizer")
	}
	if policy.Provider != "openrouter" || policy.Model != "cheap-model" {
		t.Fatalf("policy binding = %s/%s, want override openrouter/cheap-model", policy.Provider, policy.Model)
	}
	if len(policy.EndpointAllowlist) != 1 || policy.EndpointAllowlist[0] != "https://api.cheap.invalid" {
		t.Fatalf("endpoint allowlist = %v, want [https://api.cheap.invalid] (the override provider's endpoint, not the session's %s)", policy.EndpointAllowlist, res.BaseURL)
	}
	if summarizer.Binding.Provider != "openrouter" || summarizer.Binding.Model != "cheap-model" {
		t.Fatalf("summarizer binding = %s/%s, want override openrouter/cheap-model", summarizer.Binding.Provider, summarizer.Binding.Model)
	}
	if summarizer.Binding.Generation == 0 {
		t.Fatal("override summarizer binding has zero generation")
	}
	if !reflect.DeepEqual(summarizer.Policy, policy) {
		t.Fatal("summarizer policy differs from the returned policy snapshot")
	}
	if !policy.SummaryEnabled || !policy.NetworkEnabled || !policy.RedactionConfigured {
		t.Fatalf("policy gate fields missing: %+v", policy)
	}
	if len(policy.PolicyDigest) != 64 || strings.Trim(policy.PolicyDigest, "0123456789abcdef") != "" {
		t.Fatalf("policy digest %q is not 64 lowercase hex characters", policy.PolicyDigest)
	}
}

// TestSummaryWiringOverrideDegradesOnUnusableProvider pins fail-closed
// behavior for an override that cannot be built: a provider runtime with no
// usable credential keeps the summary structural-only and names the override
// as the cause. The session binding (whose model would be more expensive)
// must not be substituted.
func TestSummaryWiringOverrideDegradesOnUnusableProvider(t *testing.T) {
	res := summaryWiringResolved(t, true)
	provider, model := "openrouter", "cheap-model"
	res.Context.Summary.Provider = &provider
	res.Context.Summary.Model = &model
	res.ProviderRuntimes = map[string]config.ProviderRuntime{
		"openrouter": {ProviderName: "openrouter", BaseURL: "https://api.cheap.invalid", APIKeyEnv: "CHEAP_KEY", APIKeySet: false},
	}
	session := summaryWiringSession(t, res)
	summarizer, policy, ok := summaryWiring(session, res)
	if ok || summarizer != nil || policy.SummaryEnabled {
		t.Fatalf("unusable override wired a summarizer: ok=%v summarizer=%v policy=%+v", ok, summarizer != nil, policy)
	}
	if reason := SummaryDisabledReason(session, res); !strings.Contains(reason, "override") {
		t.Fatalf("SummaryDisabledReason = %q, want it to name the override", reason)
	}
}

// TestSummaryWiringOverrideMissingRuntimeDegrades pins the second fail-closed
// branch: an override whose provider has no runtime entry (hand-built
// Resolved bypassing load validation) stays structural-only.
func TestSummaryWiringOverrideMissingRuntimeDegrades(t *testing.T) {
	res := summaryWiringResolved(t, true)
	provider, model := "openrouter", "cheap-model"
	res.Context.Summary.Provider = &provider
	res.Context.Summary.Model = &model
	// No ProviderRuntimes entry for openrouter.
	session := summaryWiringSession(t, res)
	summarizer, _, ok := summaryWiring(session, res)
	if ok || summarizer != nil {
		t.Fatalf("override with no provider runtime wired a summarizer: ok=%v", ok)
	}
}

// TestSummaryWiringOverrideInvalidModelDegrades pins the fail-closed branch
// for an override model that is not a valid model identifier: the override
// cannot be built and compaction stays structural-only.
func TestSummaryWiringOverrideInvalidModelDegrades(t *testing.T) {
	res := summaryWiringResolved(t, true)
	provider, badModel := "openrouter", "bad\x00model"
	res.Context.Summary.Provider = &provider
	res.Context.Summary.Model = &badModel
	res.ProviderRuntimes = map[string]config.ProviderRuntime{
		"openrouter": {ProviderName: "openrouter", BaseURL: "https://api.cheap.invalid", APIKeyEnv: "CHEAP_KEY", APIKeySet: true, APIKey: "cheap-key"},
	}
	summarizer, _, ok := summaryWiring(summaryWiringSession(t, res), res)
	if ok || summarizer != nil {
		t.Fatalf("override with an invalid model wired a summarizer: ok=%v", ok)
	}
}

// TestSummaryWiringOverrideEmptyEndpointDegrades pins the fail-closed branch
// for an override provider runtime with no base URL. Without the endpoint
// check, the completer would silently fall back to the provider's default
// endpoint (NewOpenRouter substitutes the built-in descriptor URL), which
// would make the captured EndpointAllowlist dishonest - the policy would
// claim an endpoint the summary calls do not actually use.
func TestSummaryWiringOverrideEmptyEndpointDegrades(t *testing.T) {
	res := summaryWiringResolved(t, true)
	provider, model := "openrouter", "cheap-model"
	res.Context.Summary.Provider = &provider
	res.Context.Summary.Model = &model
	res.ProviderRuntimes = map[string]config.ProviderRuntime{
		"openrouter": {ProviderName: "openrouter", BaseURL: "", APIKeyEnv: "CHEAP_KEY", APIKeySet: true, APIKey: "cheap-key"},
	}
	summarizer, _, ok := summaryWiring(summaryWiringSession(t, res), res)
	if ok || summarizer != nil {
		t.Fatalf("override with an empty endpoint wired a summarizer: ok=%v", ok)
	}
}

// TestSummaryWiringOverrideZeroGenerationSessionDegrades pins the fail-closed
// branch for a session with no binding generation: NewSummarizer refuses the
// zero-generation BindingRevision, so the override stays structural-only
// instead of capturing an invalid revision.
func TestSummaryWiringOverrideZeroGenerationSessionDegrades(t *testing.T) {
	res := summaryWiringResolved(t, true)
	provider, model := "openrouter", "cheap-model"
	res.Context.Summary.Provider = &provider
	res.Context.Summary.Model = &model
	res.ProviderRuntimes = map[string]config.ProviderRuntime{
		"openrouter": {ProviderName: "openrouter", BaseURL: "https://api.cheap.invalid", APIKeyEnv: "CHEAP_KEY", APIKeySet: true, APIKey: "cheap-key"},
	}
	summarizer, _, ok := summaryWiring(&chat.Session{}, res)
	if ok || summarizer != nil {
		t.Fatalf("override with a zero-generation session wired a summarizer: ok=%v", ok)
	}
}

// TestSummaryDisabledReasonNamesMissingSessionBinding covers the session-path
// branch of SummaryDisabledReason (no override configured): a session with no
// resolved binding gets the binding reason, and the override branch is
// skipped.
func TestSummaryDisabledReasonNamesMissingSessionBinding(t *testing.T) {
	res := summaryWiringResolved(t, true)
	if reason := SummaryDisabledReason(&chat.Session{}, res); reason != "the session has no resolved provider/model binding" {
		t.Fatalf("SummaryDisabledReason = %q, want the missing-binding reason", reason)
	}
}

// TestEnableSessionContextWiresSummary drives the production setup path end to
// end: an enabled config routes a Summarizer into the context manager and the
// PolicySnapshot into SetContextManager. The committer stays structural-only
// on purpose: CommitPreparation fails the turn when the summary call fails,
// and a background metadata call must never destroy a finished turn.
func TestEnableSessionContextWiresSummary(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	var captured *contextmgr.ContextManager
	var capturedPolicy contextstate.PolicySnapshot
	original := setContextManagerForSetup
	setContextManagerForSetup = func(session *chat.Session, manager *contextmgr.ContextManager, principal contextstate.Principal, policies ...contextstate.PolicySnapshot) error {
		captured = manager
		if len(policies) > 0 {
			capturedPolicy = policies[0]
		}
		return session.SetContextManager(manager, principal)
	}
	t.Cleanup(func() { setContextManagerForSetup = original })

	if err := enableSessionContext(summaryWiringSession(t, res), t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("context manager was not routed through the setup seam")
	}
	if captured.Summarizer == nil {
		t.Fatal("enabled config wired no summarizer into the context manager")
	}
	committer, ok := captured.CheckpointPublisher.(contextmgr.PreparationCommitter)
	if !ok {
		t.Fatalf("checkpoint publisher = %T, want PreparationCommitter", captured.CheckpointPublisher)
	}
	if committer.Summarizer != nil || committer.SummaryBuilder != nil {
		t.Fatal("production wiring bound the commit-time summary seam, which fails turns on summary errors")
	}
	if !capturedPolicy.SummaryEnabled {
		t.Fatal("enabled config did not reach SetContextManager as a policy snapshot")
	}
}
