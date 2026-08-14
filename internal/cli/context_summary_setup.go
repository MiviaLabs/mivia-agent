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
)

// summaryCredentialScope labels the credential class every summary request
// rides. The host resolves provider keys from the environment only, so the
// summary call uses the same credential scope as the main turns.
const summaryCredentialScope = "env-api-key"

// summaryWiring builds the config-gated LLM summarizer for one session setup.
// The gate has three conditions, all explicit: the [context.summary] flag, a
// configured [privacy] redaction policy, and a resolved provider endpoint.
// Missing any one keeps every compaction path structural-only. A false return
// is a policy state, never an error: setup must not fail because a summary
// cannot run.
//
// The summarizer captures the session binding once, here. A model switch
// later in the session does not rebuild it; summaries keep the startup model
// until a new session starts.
func summaryWiring(sess *chat.Session, res *config.Resolved) (*contextmgr.Summarizer, contextstate.PolicySnapshot, bool) {
	if sess == nil || res == nil || !res.Context.Summary.Enabled {
		return nil, contextstate.PolicySnapshot{}, false
	}
	redaction := contextRedactionPolicy(res)
	if !redaction.Configured {
		return nil, contextstate.PolicySnapshot{}, false
	}
	endpoint := strings.TrimSpace(res.BaseURL)
	if endpoint == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	binding := sess.CurrentBinding()
	if binding.Completer == nil || binding.ProviderName == "" || binding.Model == "" {
		return nil, contextstate.PolicySnapshot{}, false
	}
	policy := contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: true, NetworkEnabled: true,
		Provider: binding.ProviderName, Model: binding.Model,
		CredentialScope:   summaryCredentialScope,
		EndpointAllowlist: []string{endpoint},
		PolicyDigest: summaryPolicyDigest(binding.ProviderName, binding.Model, endpoint,
			res.Privacy.RedactionPatterns, res.Privacy.RedactionKeyNames),
	}
	adapter, err := contextmgr.NewLLMSummaryProvider(binding.Completer)
	if err != nil {
		return nil, contextstate.PolicySnapshot{}, false
	}
	summarizer, err := contextmgr.NewSummarizer(adapter, contextstate.BindingRevision{
		Provider: binding.ProviderName, Model: binding.Model, Generation: binding.ModelGeneration,
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
