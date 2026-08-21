package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// summaryCredentialScope labels the credential class every summary request
// rides. The host resolves provider keys from the environment only, so the
// summary call uses the same credential scope as the main turns.
const summaryCredentialScope = "env-api-key"

// SummaryDisabledReason names the first unmet condition keeping compaction
// structural-only, or "" when summarization is wired. A workspace that has not
// configured it gets an instant /compact that makes no LLM call, which is
// correct but indistinguishable from a broken summarizer: the operator sees a
// compaction succeed while the summary they expected never runs. The false
// return stays a policy state rather than an error; this only makes the state
// legible.
func SummaryDisabledReason(sess *chat.Session, res *config.Resolved) string {
	if sess == nil || res == nil {
		// Nothing to report rather than a guess: silence is safer than a
		// wrong reason on a caller that has no resolved configuration.
		return ""
	}
	if !res.Context.Summary.SummaryEnabled() {
		return "[context.summary] enabled is not set"
	}
	override := res.Context.Summary.Provider != nil && res.Context.Summary.Model != nil
	if !override {
		if strings.TrimSpace(res.BaseURL) == "" {
			return "the provider endpoint (base_url) did not resolve"
		}
		binding := sess.CurrentBinding()
		if binding.Completer == nil || binding.ProviderName == "" || binding.Model == "" {
			return "the session has no resolved provider/model binding"
		}
	}
	if _, _, ok := summaryWiring(sess, res); !ok {
		if override {
			return "the [context.summary] provider/model override could not be built (is the provider configured with a usable API key?)"
		}
		return "the summarizer could not be built from the resolved policy"
	}
	return ""
}

// summaryWiring builds the LLM summarizer for one session setup. It is
// opt-OUT: [context.summary] defaults to enabled, so the remaining condition
// is a resolved provider endpoint plus a usable provider/model binding.
// Missing either keeps every compaction path structural-only. A false return
// is a policy state, never an error: setup must not fail because a summary
// cannot run - but SummaryDisabledReason names the cause so it is never
// silent.
//
// A [context.summary] provider/model override replaces the session binding
// for the summary call (the cost-containment escape hatch for cheap
// summarizer models). It is built through newProviderCompleter, which is
// fail-closed: an unknown provider, a provider with no configured runtime, or
// a provider with no usable credential all refuse here, so the override never
// silently falls back to the session binding and its (expensive) model.
//
// The summarizer captures the binding once, here. A model switch later in the
// session does not rebuild it; summaries keep the startup binding until a new
// session starts.
func summaryWiring(sess *chat.Session, res *config.Resolved) (*contextmgr.Summarizer, contextstate.PolicySnapshot, bool) {
	if sess == nil || res == nil || !res.Context.Summary.SummaryEnabled() {
		return nil, contextstate.PolicySnapshot{}, false
	}
	// Override path: [context.summary] provider/model. Load-time validation
	// (Resolved.Validate) guarantees both keys are set together and the
	// provider is declared under [providers]; the checks are repeated here
	// because summaryWiring is also reached with hand-built Resolved values.
	if res.Context.Summary.Provider != nil && res.Context.Summary.Model != nil {
		return summaryWiringOverride(sess, res)
	}
	endpoint := strings.TrimSpace(res.BaseURL)
	if endpoint == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	binding := sess.CurrentBinding()
	if binding.Completer == nil || binding.ProviderName == "" || binding.Model == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	return buildSummaryWiring(sess, res, binding.Completer, binding.ProviderName, binding.Model,
		binding.ModelGeneration, endpoint)
}

// summaryWiringOverride resolves the [context.summary] provider/model override
// into a completer and delegates to buildSummaryWiring. newProviderCompleter
// is fail-closed: an unknown provider, a provider with no configured runtime,
// or a provider with no usable credential all refuse here, so the override
// never silently falls back to the session binding and its (expensive) model.
func summaryWiringOverride(sess *chat.Session, res *config.Resolved) (*contextmgr.Summarizer, contextstate.PolicySnapshot, bool) {
	provider := strings.ToLower(strings.TrimSpace(*res.Context.Summary.Provider))
	model, err := config.NormalizeModelName(*res.Context.Summary.Model)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	runtime, ok := res.ProviderRuntimes[provider]
	if !ok {
		return nil, contextstate.PolicySnapshot{}, false
	}
	completer, err := newProviderCompleter(res, provider, model)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	endpoint := strings.TrimSpace(runtime.BaseURL)
	if endpoint == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	return buildSummaryWiring(sess, res, completer, provider, model,
		sess.CurrentBinding().ModelGeneration, endpoint)
}

// buildSummaryWiring captures one resolved provider/model binding into a
// Summarizer and its PolicySnapshot: the binding fields, the endpoint
// allowlist, and the policy digest that makes a later policy change refuse
// requests minted under the old capture.
func buildSummaryWiring(sess *chat.Session, res *config.Resolved, completer provider.Completer,
	providerName, model string, generation uint64, endpoint string) (*contextmgr.Summarizer, contextstate.PolicySnapshot, bool) {
	// [privacy] is no longer a precondition. It governs what the checkpoint
	// may persist (see PolicySnapshot.RedactionConfigured below, which stays
	// honest), not whether the summary may run at all - requiring it meant a
	// workspace without that section compacted with no record of what it
	// dropped.
	redaction := contextRedactionPolicy(res)
	policy := contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: redaction.Configured, NetworkEnabled: true,
		Provider: providerName, Model: model,
		CredentialScope:   summaryCredentialScope,
		EndpointAllowlist: []string{endpoint},
		PolicyDigest: summaryPolicyDigest(providerName, model, endpoint,
			res.Privacy.RedactionPatterns, res.Privacy.RedactionKeyNames),
	}
	adapter, err := contextmgr.NewLLMSummaryProvider(completer, sess.SessionID)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	summarizer, err := contextmgr.NewSummarizer(adapter, contextstate.BindingRevision{
		Provider: providerName, Model: model, Generation: generation,
	}, policy)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	return &summarizer, policy, true
}

// summaryPolicyDigest binds the captured policy to the exact content it was
// captured under. The summary envelope validator requires a 64-character
// digest, and Summarizer.available compares request and captured digests, so
// a digest that tracks provider, model, endpoint, and redaction content makes
// a policy change refuse requests minted under the old policy.
func summaryPolicyDigest(providerName, model, endpoint string, patterns, keyNames []string) string {
	parts := make([]string, 0, 5)
	parts = append(parts, providerName, model, endpoint,
		strings.Join(patterns, "\x00"), strings.Join(keyNames, "\x00"))
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(strconv.Itoa(len(part))))
		digest.Write([]byte(":"))
		digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}
