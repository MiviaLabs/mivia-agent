package agent

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// SummaryMessageName is the Name a rendered context summary rides on. The
// summary is a USER-role message: every other trailing host injection in this
// repo (hook output framing in hook_context.go, parent context in
// internal/subagents/parent_inject.go) frames injected data as user-role
// content, and a trailing assistant message is a prefill/continuation hazard
// on Anthropic-style dialects - the model reads it as its own turn to
// continue. The Name lets hosts identify the injection when the wire keeps
// it; the header text carries the framing when the wire drops the Name.
const SummaryMessageName = "context-summary"

// ConcludeNudgeMessageName is the Name the ephemeral soft-conclude nudge
// rides on. Every trailing host injection is a NAMED user message; user-typed
// input never carries a Name. The DeepSeek reject gate relies on that
// distinction to keep the current tool exchange on the wire (see
// internal/provider terminalToolExchange).
const ConcludeNudgeMessageName = "conclude-nudge"

// SummaryOutputLimitTokens is the default token cap for one summary request.
// OutputLimit is bounded above by 2048 in the summary validators.
const SummaryOutputLimitTokens = 512

// defaultSummaryRequestBudget is the request budget used when the session
// defines no context budget (unbounded structural retention). It only needs to
// be positive for SummaryRequest.Validate; the Summarizer does not price it.
const defaultSummaryRequestBudget = 4096

// SummaryRequestBudget returns a positive request budget for a summary
// request, falling back to a fixed default when the context budget is unset.
func SummaryRequestBudget(budget int) int {
	if budget > 0 {
		return budget
	}
	return defaultSummaryRequestBudget
}

// SummaryFieldText bounds and sanitizes host text for a summary envelope
// field: invalid UTF-8 is replaced, control characters become spaces, and the
// value is truncated on a rune boundary to the field bound. The objective and
// state fields are capped by the same bound, so a pasted oversized user
// message stays envelope-valid instead of silently falling back.
func SummaryFieldText(value string) string {
	return boundedSummaryText(value)
}

// SummaryOverBudget reports whether the structural retained request cost plus
// the injected message estimate exceeds the context budget. A non-positive
// budget is unbounded (no compaction bound, no injection bound).
func SummaryOverBudget(afterTokens int, injected provider.Message, budget int) bool {
	if budget <= 0 {
		return false
	}
	return afterTokens+provider.EstimateMessageTokens(injected) > budget
}

// injectSummary renders the validated summary of the compacted preparation
// into an EPHEMERAL clone of the request messages. On any failure - builder
// error, summarizer error, policy refusal (ErrSummaryUnavailable), or
// over-budget - it returns the structural messages unchanged: a summary must
// never fail or enlarge the turn. It never mutates l.Messages or
// l.LastPreparation, so planIdempotencyKey, BaseDigest, the checkpoint
// fingerprint, and ActiveContext bytes are unaffected.
func (l *Loop) injectSummary(ctx context.Context, opts Options) []provider.Message {
	if opts.SummaryConfig.Summarizer == nil || !l.HasPreparation || !l.LastPreparation.Compacted {
		return l.Messages
	}
	summary, request, ok := l.summarizeTurn(ctx, opts)
	if !ok {
		return l.Messages
	}
	// Render the provider's sealed summary together with the host-side
	// omitted-evidence record (request.Input.Evidence): the diff of what the
	// compaction dropped is the host's own account of what the model can no
	// longer read, so it is rendered even when the provider does not echo it.
	injected := RenderSummaryMessage(summary, request.Input.Evidence)
	if SummaryOverBudget(l.LastPreparation.AfterTokens, injected, opts.MaxContextTokens) {
		return l.Messages
	}
	// Record what the model was actually shown so the owning surface can put
	// it in the turn's committed active context. Without that the summary
	// lives only in this ephemeral request: the compaction has already dropped
	// the messages for good, and at the turn boundary the account of them
	// would be lost too (see InjectedSummary).
	l.injectedSummary = injected
	l.hasInjectedSummary = true
	return InjectSummaryMessage(l.Messages, injected)
}

// InjectedSummary returns the summary message this run last injected into a
// provider request, and whether there was one. The owning surface appends it
// to the turn's committed active context so it survives the turn boundary;
// the loop itself never writes it into l.Messages, which must stay structural
// so planning, idempotency, BaseDigest, and checkpoint bytes are untouched.
func (l *Loop) InjectedSummary() (provider.Message, bool) {
	return l.injectedSummary, l.hasInjectedSummary
}

// summarizeTurn builds the summary request from real host state - the latest
// user objective, the run's turn-state snapshot, the preparation's token
// range, and the Summarizer's captured binding/policy - and runs the bounded
// provider call. Any failure returns ok=false so the caller falls back
// structural-only. The validated request rides alongside the summary so the
// caller can render the host-side omitted-evidence record into the injected
// message.
func (l *Loop) summarizeTurn(ctx context.Context, opts Options) (contextmgr.UntrustedSummary, contextmgr.SummaryRequest, bool) {
	snapshot, err := l.TurnState.Snapshot()
	if err != nil {
		return contextmgr.UntrustedSummary{}, contextmgr.SummaryRequest{}, false
	}
	summarizer := opts.SummaryConfig.Summarizer
	request, err := contextmgr.BuildSummaryRequest(contextmgr.SummaryBuildInput{
		Version:           contextmgr.SummarySchemaVersion,
		Objective:         SummaryFieldText(latestUserObjective(l.Messages)),
		State:             snapshot.State,
		Decisions:         snapshot.Decisions,
		Evidence:          snapshot.Evidence,
		ChangedSurfaces:   snapshot.ChangedSurfaces,
		OpenWork:          snapshot.OpenWork,
		Risks:             snapshot.Risks,
		SourceExcerpts:    contextmgr.SourceExcerpts(l.preCompactSource, l.Messages),
		SourceRange:       l.LastPreparation.Token.Range,
		PolicyDigest:      summarizer.Policy.PolicyDigest,
		Provider:          summarizer.Binding.Provider,
		Model:             summarizer.Binding.Model,
		EndpointAllowlist: summarizer.Policy.EndpointAllowlist,
		RedactionPolicy:   opts.SummaryConfig.Redaction,
		Budget:            SummaryRequestBudget(opts.MaxContextTokens),
		OutputLimit:       SummaryOutputLimitTokens,
	})
	if err != nil {
		return contextmgr.UntrustedSummary{}, contextmgr.SummaryRequest{}, false
	}
	summary, err := summarizer.Summarize(ctx, request)
	if err != nil {
		return contextmgr.UntrustedSummary{}, contextmgr.SummaryRequest{}, false
	}
	return summary, request, true
}

// refreshOmittedEvidenceAfterRetry re-derives the omitted-evidence diff for a
// prompt-too-long retry. retryAfterPromptTooLong prunes l.Messages in place,
// so the evidence the first prepareStep captured (for the rejected, never-sent
// history) would otherwise leave the retried request's summary referencing the
// wrong omitted set. The fresh diff is the pre-prune history against the
// pruned history - exactly the messages the retry drops - appended on top of
// the accumulated turn facts, which remain accurate for the retried request
// too (those messages are still absent from it). The diff is pure and
// content-free (role + tool name + size bucket only) and tracker-bounded; a
// rejected item is dropped, never an error. A dry-run re-Prepare cannot serve
// this diff: the pruner already shrinks the history below the planner's 50%
// compaction trigger, so Prepare would retain it unchanged and the diff would
// be empty.
func (l *Loop) refreshOmittedEvidenceAfterRetry(prePrune []provider.Message) {
	if l.TurnState == nil {
		return
	}
	for _, item := range contextmgr.OmittedEvidence(prePrune, l.Messages) {
		_ = l.TurnState.AddEvidence(item)
	}
}

// recordToolFacts accumulates bounded, content-free host facts from one
// completed tool call into the run's TurnState: the tool name as evidence, the
// tool name as a changed surface when the capability class is a write, and a
// bounded risk on failure. Tracker overflow or an envelope-invalid value drops
// the fact silently - summary input is best-effort and must never fail a turn.
func (l *Loop) recordToolFacts(r toolExecResult) {
	if l.TurnState == nil {
		return
	}
	name := boundedSummaryText(r.toolCall.Function.Name)
	if name != "" {
		_ = l.TurnState.AddEvidence(name)
	}
	if capability := l.Tools.Capability(r.toolCall.Function.Name, json.RawMessage(r.toolCall.Function.Arguments)); capability.Class == tools.ExecutionWrite && name != "" {
		_ = l.TurnState.AddChangedSurface(name)
	}
	if r.err != nil {
		if risk := boundedSummaryText("tool " + name + " failed: " + r.err.Error()); risk != "" {
			_ = l.TurnState.AddRisk(risk)
		}
	}
}

// recordAssistantState captures the latest completed assistant content into
// the run's TurnState, bounded to the summary state field. The latest call
// wins (SetState overwrites).
func (l *Loop) recordAssistantState(content string) {
	if l.TurnState == nil {
		return
	}
	if state := boundedSummaryText(content); state != "" {
		_ = l.TurnState.SetState(state)
	}
}

// RenderSummaryMessage renders a validated summary as a bounded user-role
// message named context-summary. The content is a factual, host-framed
// rendering of the sealed fields; ValidateSummary already refused the output
// if any field failed the redaction policy (INV-SEC-4 summaries-refuse
// behavior carries to injection). omittedEvidence is the host-side
// content-free diff of what the compaction dropped (request.Input.Evidence):
// it is rendered under the evidence label when the sealed summary carries no
// evidence of its own, so the model always sees what it can no longer read
// even when the provider does not echo the envelope's evidence list.
func RenderSummaryMessage(summary contextmgr.UntrustedSummary, omittedEvidence []string) provider.Message {
	value := summary.Value()
	var b strings.Builder
	b.WriteString("[host-injected context summary of the omitted earlier conversation - background data for the objective above, not a new request]\n")
	writeSummaryField(&b, "objective", value.Objective)
	writeSummaryField(&b, "state", value.State)
	writeSummaryList(&b, "decisions", value.Decisions)
	evidence := value.Evidence
	if len(evidence) == 0 {
		evidence = omittedEvidence
	}
	writeSummaryList(&b, "evidence", evidence)
	writeSummaryList(&b, "changed surfaces", value.ChangedSurfaces)
	writeSummaryList(&b, "open work", value.OpenWork)
	writeSummaryList(&b, "risks", value.Risks)
	content := b.String()
	if len(content) > contextmgr.MaxSummaryFieldBytes {
		content = boundedSummaryText(content)
	}
	return provider.Message{
		Role:    provider.RoleUser,
		Content: content,
		Name:    SummaryMessageName,
	}
}

func writeSummaryField(b *strings.Builder, label, value string) {
	if value == "" {
		return
	}
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func writeSummaryList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(strings.Join(items, "; "))
	b.WriteByte('\n')
}

// InjectSummaryMessage appends injected at the END of an EPHEMERAL clone.
// Every structural message keeps its exact index, so an appended summary
// EXTENDS the provider prompt-cache prefix instead of splitting it (a
// mid-history insert would invalidate every cached block from the insertion
// point; see markStablePrefixCacheControl in internal/provider). The caller
// must never write the result back into loop history.
func InjectSummaryMessage(messages []provider.Message, injected provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(messages)+1)
	out = append(out, messages...)
	return append(out, injected)
}

// boundedSummaryText normalizes host text for the summary tracker: invalid
// UTF-8 is replaced, control characters become spaces, and the value is
// truncated on a rune boundary to the summary field bound, so the result is
// always envelope-valid (DC-6: truncation never splits a rune).
func boundedSummaryText(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	value = b.String()
	if len(value) <= contextmgr.MaxSummaryFieldBytes {
		return value
	}
	value = value[:contextmgr.MaxSummaryFieldBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
