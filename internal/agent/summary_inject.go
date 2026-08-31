package agent

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
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

// SummaryOutputLimitTokens is the default token cap for one summary request.
// OutputLimit is bounded above by 2048 in the summary validators.
//
// 512 was too tight for the summary this host's own prompt asks for: a reply
// populating every envelope field at the sizes the skeleton requests
// re-encodes to roughly 2KB - about 514 estimated tokens - so realistic
// summaries were rejected just past the bound and compaction silently
// degraded to structural-only, destroying the dropped context's only record.
// 1024 leaves real headroom while staying well under the validators' 2048
// ceiling.
const SummaryOutputLimitTokens = 1024

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

// maxSummaryAttemptsPerCompaction bounds deferred summary attempts across
// step boundaries for a single compaction event.
const maxSummaryAttemptsPerCompaction = 2

type summaryAttempt struct {
	ok        bool
	reason    string
	retryable bool
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
	// One Summarize attempt per compaction event: the memo holds the RENDERED
	// message, so every later step of the turn injects byte-identical bytes
	// with no new summarizer request. A different key is a new compaction
	// event and resets attempt counters. A failed attempt is memoized
	// once it is non-retryable or when maxSummaryAttemptsPerCompaction is reached.
	key := l.turnCompactionKey
	if key == "" {
		key = compactionIdentity(l.LastPreparation.Token)
	}
	if !l.summaryMemoValid || l.summaryMemoKey != key {
		if l.summaryMemoKey != key {
			l.summaryMemoKey = key
			l.summaryMemoAttempts = 0
			l.summaryMemoValid = false
			l.summaryMemoHasMsg = false
			l.summaryMemoMessage = provider.Message{}
			l.summaryMemoReason = ""
		}
		l.summaryMemoAttempts++
		summary, request, attempt := l.summarizeTurn(ctx, opts)
		if attempt.ok {
			// Render the provider's sealed summary together with the host-side
			// omitted-evidence record (request.Input.Evidence): the diff of
			// what the compaction dropped is the host's own account of what
			// the model can no longer read, so it is rendered even when the
			// provider does not echo it.
			l.summaryMemoMessage = RenderSummaryMessage(summary, request.Input.Evidence)
			l.summaryMemoHasMsg = true
			l.summaryMemoValid = true
			l.summaryMemoReason = ""
		} else {
			l.summaryMemoHasMsg = false
			l.summaryMemoMessage = provider.Message{}
			l.summaryMemoReason = attempt.reason
			if !attempt.retryable || l.summaryMemoAttempts >= maxSummaryAttemptsPerCompaction {
				l.summaryMemoValid = true
			}
		}
	}
	if !l.summaryMemoHasMsg {
		l.summaryFailureReason = l.summaryMemoReason
		return l.Messages
	}
	injected := l.summaryMemoMessage
	if SummaryOverBudget(l.LastPreparation.AfterTokens, injected, opts.MaxContextTokens) {
		l.summaryFailureReason = contextmgr.SummaryReasonOverBudget
		return l.Messages
	}
	// Record what the model was actually shown so the owning surface can put
	// it in the turn's committed active context. Without that the summary
	// lives only in this ephemeral request: the compaction has already dropped
	// the messages for good, and at the turn boundary the account of them
	// would be lost too (see InjectedSummary).
	l.injectedSummary = injected
	l.hasInjectedSummary = true
	l.summaryFailureReason = ""
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

// invalidateSummaryMemo forces the next injectSummary to run a fresh
// Summarize. The prompt-too-long retry calls it: that retry prunes history
// host-side and re-derives the omitted evidence, so the memoized summary of
// the earlier compaction no longer describes what the retried request drops.
func (l *Loop) invalidateSummaryMemo() {
	l.summaryMemoValid = false
	l.summaryMemoHasMsg = false
	l.summaryMemoMessage = provider.Message{}
	l.summaryMemoReason = ""
	l.summaryMemoAttempts = 0
	l.summaryFailureReason = ""
}

// summarizeTurn builds the summary request from real host state - the latest
// user objective, the run's turn-state snapshot, the preparation's token
// range, and the Summarizer's captured binding/policy - and runs the bounded
// provider call. Any failure returns attempt.ok=false with the classified reason
// so the caller falls back structural-only. The validated request rides alongside
// the summary so the caller can render the host-side omitted-evidence record into
// the injected message.
func (l *Loop) summarizeTurn(ctx context.Context, opts Options) (contextmgr.UntrustedSummary, contextmgr.SummaryRequest, summaryAttempt) {
	snapshot, err := l.TurnState.Snapshot()
	if err != nil {
		return contextmgr.UntrustedSummary{}, contextmgr.SummaryRequest{}, summaryAttempt{
			ok: false, reason: contextmgr.SummaryReasonHostState, retryable: false,
		}
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
		return contextmgr.UntrustedSummary{}, contextmgr.SummaryRequest{}, summaryAttempt{
			ok: false, reason: contextmgr.SummaryReasonRequestInvalid, retryable: false,
		}
	}
	summary, err := summarizer.Summarize(ctx, request)
	if err != nil {
		return contextmgr.UntrustedSummary{}, contextmgr.SummaryRequest{}, summaryAttempt{
			ok:        false,
			reason:    contextmgr.ClassifySummaryFailure(err),
			retryable: contextmgr.RetryableSummaryFailure(err),
		}
	}
	return summary, request, summaryAttempt{ok: true}
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
// InjectSummaryMessage appends injected as the last message, first dropping
// any message already carrying SummaryMessageName.
//
// The drop makes injection idempotent per call, which matters specifically on
// the SDK backend: sdkPrepareTrim's Trim closure re-injects on every step of a
// multi-step turn, and the SDK's own run.go treats each Trim return as the
// run's real carried history (*history = trimmed) - so without this, a stale
// summary frame from an earlier step survives into a later step's messages as
// ordinary content, and this function would append a second, fresher copy
// beside it rather than replacing it, compounding by one frame per step for
// the rest of the turn. Injection is a single per-request frame by contract
// (findSummaryMessage/anyRequestCarriesSummary assume at most one); this
// keeps that true regardless of what the caller's slice already carries.
func InjectSummaryMessage(messages []provider.Message, injected provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(messages)+1)
	for _, m := range messages {
		if m.Name == SummaryMessageName {
			continue
		}
		out = append(out, m)
	}
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
	// Back off across the rune at the CUT BOUNDARY only (DC-6). ToValidUTF8
	// above already removed every invalid byte, so a whole-prefix check
	// (utf8.ValidString) cannot amputate here TODAY - but it would the moment
	// that sanitising step moves or goes away, and the failure would look like
	// an ordinary budget cut. Keep the safe form regardless.
	for len(value) > 0 {
		r, size := utf8.DecodeLastRuneInString(value)
		if r != utf8.RuneError || size > 1 {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
