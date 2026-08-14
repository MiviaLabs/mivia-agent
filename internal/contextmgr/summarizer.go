package contextmgr

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// summaryTimeout bounds one summarize call. The request now carries up to
// 16 KiB of source excerpts, so the call needs more headroom than a bare
// envelope; a timeout still degrades to structural-only compaction.
const summaryTimeout = 20 * time.Second

// Summarizer binds one captured provider/model/policy snapshot to a summary
// request. It has no provider discovery or network fallback path.
type Summarizer struct {
	Provider SummaryProvider
	Binding  contextstate.BindingRevision
	Policy   contextstate.PolicySnapshot
	Timeout  time.Duration
}

func NewSummarizer(provider SummaryProvider, binding contextstate.BindingRevision, policy contextstate.PolicySnapshot) (Summarizer, error) {
	if provider == nil {
		return Summarizer{}, fmt.Errorf("%w: summary provider is missing", contextstate.ErrSummaryUnavailable)
	}
	if err := binding.Validate(); err != nil {
		return Summarizer{}, err
	}
	return Summarizer{Provider: provider, Binding: binding, Policy: clonePolicy(policy), Timeout: summaryTimeout}, nil
}

// Summarize executes one bounded provider call and validates its result before
// returning it. The caller's context remains the outer cancellation authority.
func (s Summarizer) Summarize(ctx context.Context, request SummaryRequest) (UntrustedSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := request.Validate(); err != nil {
		return UntrustedSummary{}, err
	}
	if err := s.available(request); err != nil {
		return UntrustedSummary{}, err
	}
	callContext, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	providerRequest := cloneSummaryRequest(request)
	// Classifier configuration is host policy, not model input. The provider
	// receives only the sealed bounded envelope and transport binding fields.
	providerRequest.RedactionPolicy = contextstate.RedactionPolicy{}
	result, err := s.Provider.Summarize(callContext, providerRequest)
	if err != nil {
		return UntrustedSummary{}, fmt.Errorf("summary provider: %w", err)
	}
	validated, err := ValidateSummary(result, request)
	if err != nil {
		return UntrustedSummary{}, err
	}
	return validated, nil
}

func (s Summarizer) timeout() time.Duration {
	if s.Timeout <= 0 || s.Timeout > summaryTimeout {
		return summaryTimeout
	}
	return s.Timeout
}

func (s Summarizer) available(request SummaryRequest) error {
	if s.Provider == nil {
		return fmt.Errorf("%w: summary provider is missing", contextstate.ErrSummaryUnavailable)
	}
	if err := s.Binding.Validate(); err != nil {
		return err
	}
	if !s.Policy.SummaryEnabled || !s.Policy.NetworkEnabled || !s.Policy.RedactionConfigured || !redactionConfigured(request.RedactionPolicy) || s.Policy.CredentialScope == "" {
		return fmt.Errorf("%w: summary policy is not explicitly enabled", contextstate.ErrSummaryUnavailable)
	}
	if request.Provider != s.Binding.Provider || request.Model != s.Binding.Model {
		return fmt.Errorf("%w: summary binding changed", contextstate.ErrStaleBinding)
	}
	if s.Policy.Provider != "" && request.Provider != s.Policy.Provider {
		return fmt.Errorf("%w: summary provider is outside the captured policy", contextstate.ErrStaleBinding)
	}
	if s.Policy.Model != "" && request.Model != s.Policy.Model {
		return fmt.Errorf("%w: summary model is outside the captured policy", contextstate.ErrStaleBinding)
	}
	if s.Policy.PolicyDigest != "" && request.Input.PolicyDigest != s.Policy.PolicyDigest {
		return fmt.Errorf("%w: summary policy changed", contextstate.ErrStaleBinding)
	}
	if len(request.EndpointAllowlist) == 0 || len(s.Policy.EndpointAllowlist) == 0 {
		return fmt.Errorf("%w: summary endpoint allowlist is required", contextstate.ErrSummaryUnavailable)
	}
	if !sameStrings(request.EndpointAllowlist, s.Policy.EndpointAllowlist) {
		return fmt.Errorf("%w: summary endpoint policy changed", contextstate.ErrStaleBinding)
	}
	return nil
}

func clonePolicy(policy contextstate.PolicySnapshot) contextstate.PolicySnapshot {
	policy.EndpointAllowlist = append([]string(nil), policy.EndpointAllowlist...)
	return policy
}

func cloneSummaryRequest(request SummaryRequest) SummaryRequest {
	request.Input.Decisions = append([]string(nil), request.Input.Decisions...)
	request.Input.Evidence = append([]string(nil), request.Input.Evidence...)
	request.Input.ChangedSurfaces = append([]string(nil), request.Input.ChangedSurfaces...)
	request.Input.OpenWork = append([]string(nil), request.Input.OpenWork...)
	request.Input.Risks = append([]string(nil), request.Input.Risks...)
	request.EndpointAllowlist = append([]string(nil), request.EndpointAllowlist...)
	request.RedactionPolicy.Patterns = append([]string(nil), request.RedactionPolicy.Patterns...)
	request.RedactionPolicy.KeyNames = append([]string(nil), request.RedactionPolicy.KeyNames...)
	request.SourceExcerpts = cloneSummaryExcerpts(request.SourceExcerpts)
	return request
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func redactionConfigured(policy contextstate.RedactionPolicy) bool {
	return policy.Configured && (len(policy.Patterns) > 0 || len(policy.KeyNames) > 0 || policy.Classifier != nil)
}
