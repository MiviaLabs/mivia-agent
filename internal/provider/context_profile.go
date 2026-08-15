package provider

// ReasoningBilling describes when a provider's server-side context accounting
// counts a historical assistant message's ReasoningContent toward billed
// prompt tokens.
type ReasoningBilling int

const (
	// ReasoningBillingAllTurns charges ReasoningContent on every assistant
	// message the client holds, regardless of turn age. This is the
	// conservative default (the zero value): overestimating never causes a
	// request a provider would have accepted to be rejected locally, so every
	// provider that declares nothing gets this value.
	ReasoningBillingAllTurns ReasoningBilling = iota
	// ReasoningBillingTerminalExchange charges ReasoningContent only on the
	// terminal (still-open) tool exchange; ReasoningContent on an
	// already-resolved, earlier tool round is free. Some reasoning-replay
	// providers document that a previous round's reasoning_content is not
	// itself replayed on the wire on later requests, so it is never billed
	// (see api-docs.deepseek.com/guides/reasoning_model).
	ReasoningBillingTerminalExchange
	// ReasoningBillingNever charges no ReasoningContent, ever.
	ReasoningBillingNever
)

// ContextAccountingProfile describes how a provider's server bills prompt
// context beyond raw text length. It is declared per provider at
// construction time (CompatOptions.ContextAccounting), the same way
// RequiresReasoningReplay, ReasoningDialect, and CacheUsageEnabled/
// CacheMarkersEnabled are: a small, explicit trait set on the client at
// construction and read without synchronization afterward.
//
// The zero value is the conservative default (bill everything), so a
// provider that declares nothing behaves exactly as before this type
// existed.
//
// internal/contextmgr and the agent loop carry this value opaquely from
// Completer.ContextAccounting() to the estimators in context.go, which are
// the only code that interprets its fields. Add a field here (not a second,
// parallel plumbing path) when a provider needs another context-billing
// distinction, such as a tool-schema billing quirk or a different cache
// granularity.
type ContextAccountingProfile struct {
	// ReasoningBilling selects how historical assistant ReasoningContent is
	// charged. See ReasoningBilling's constants.
	ReasoningBilling ReasoningBilling
}
