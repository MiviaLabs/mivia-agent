package chat

import "github.com/MiviaLabs/mivia-agent/internal/provider"

// summaryMessageName mirrors agent.SummaryMessageName, the sentinel Name the
// compaction memo carries. It is duplicated rather than imported because
// internal/agent builds on this package's session; TestSummaryMessageNameMatchesAgent
// fails the build's tests if the two ever drift.
const summaryMessageName = "context-summary"

// ContextBreakdown splits the prompt estimate into the parts a reader can act
// on. The split answers one question: of what is filling the window, how much
// can compaction actually remove.
//
// System, ToolSchemas, Memory and Summary are the FLOOR - present on every
// turn regardless of what was said, and untouched by compaction. Prose,
// ToolResults and Reasoning are the CONVERSATION, which compaction eats. A
// session that opens at 30% is not a session that talked too much; it is one
// carrying an expensive floor, and only the floor's own rows say which part.
//
// Every field is in tokens and already calibrated, so the fields sum to the
// same UsedTokens the gauge shows (see scaleTo). Reporting raw parts beside a
// calibrated total would put two disagreeing numbers for one quantity on the
// same screen - the exact failure the gauge-versus-trigger comment on
// ContextUsage records.
type ContextBreakdown struct {
	System int
	// ToolSchemas is the cost of the compiled-in tools; ExternalSchemas is
	// the cost of the ones a server supplied. They are separate because only
	// the second is removable by turning something off, which is the whole
	// point of reporting a schema cost at all.
	ToolSchemas     int
	ExternalSchemas int
	// ToolCount and ExternalToolCount are numbers of schemas, not token
	// costs: "8k across 19 tools" is what makes the cost actionable, because
	// the unit anyone can remove is a tool or a server, never a token.
	ToolCount         int
	ExternalToolCount int
	Memory            int
	Summary           int
	Prose             int
	ToolResults       int
	Reasoning         int
}

// Floor is the part of the estimate compaction cannot reclaim.
func (b ContextBreakdown) Floor() int {
	return b.System + b.ToolSchemas + b.ExternalSchemas + b.Memory + b.Summary
}

// Conversation is the part compaction reclaims.
func (b ContextBreakdown) Conversation() int {
	return b.Prose + b.ToolResults + b.Reasoning
}

// Total is the whole estimate: Floor plus Conversation.
func (b ContextBreakdown) Total() int { return b.Floor() + b.Conversation() }

// breakdown prices messages and tool schemas into buckets, using exactly the
// per-message and per-schema charges EstimatePromptCost sums, so the returned
// Total equals that function's result for the same inputs. The request frame
// lands on System: it is fixed per-request overhead, which is what that bucket
// already means.
func breakdown(
	messages []provider.Message,
	toolSpecs []provider.ToolSpec,
	external map[string]string,
	profile provider.ContextAccountingProfile,
) (ContextBreakdown, error) {
	b := ContextBreakdown{System: provider.RequestFrameTokens}
	for _, spec := range toolSpecs {
		// Priced one at a time so each schema lands in the bucket for its
		// origin. EstimateToolSchemaCost over a single-element slice is the
		// same charge it makes inside the whole-list call, so the parts still
		// add up to what the estimator would have reported.
		cost, err := provider.EstimateToolSchemaCost([]provider.ToolSpec{spec})
		if err != nil {
			return ContextBreakdown{}, err
		}
		if _, fromServer := external[toolSpecName(spec)]; fromServer {
			b.ExternalSchemas += cost
			b.ExternalToolCount++
			continue
		}
		b.ToolSchemas += cost
		b.ToolCount++
	}
	for index := range messages {
		msg := messages[index]
		cost := provider.EstimateMessageTokensAt(messages, index, profile)
		switch {
		case msg.Role == provider.RoleSystem:
			b.System += cost
		case msg.Name == MemoryContextMessageName:
			b.Memory += cost
		case msg.Name == summaryMessageName:
			b.Summary += cost
		case msg.Role == provider.RoleTool:
			b.ToolResults += cost
		default:
			reasoning := provider.EstimateReasoningTokensAt(messages, index, profile)
			b.Reasoning += reasoning
			b.Prose += cost - reasoning
		}
	}
	return b, nil
}

// toolSpecName digs the function name out of an OpenAI tool spec. A spec
// whose shape does not match returns "", which no server tool is keyed by, so
// an unreadable spec is charged as compiled-in rather than misattributed to a
// server the operator would then go looking for.
func toolSpecName(spec provider.ToolSpec) string {
	fn, ok := spec["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return name
}

// scaleTo rescales every bucket so the fields sum to exactly total, the
// calibrated number the gauge reports. Integer division loses at most one
// token per bucket, so the drift is handed to the largest bucket - the one
// where a token of rounding is invisible - rather than left as a gap that
// would make the rows visibly fail to add up.
//
// A zero or negative raw total (no messages, no tools) scales to nothing:
// there is no proportion to preserve and every bucket is already zero.
func (b ContextBreakdown) scaleTo(total int) ContextBreakdown {
	raw := b.Total()
	if raw <= 0 || total <= 0 || raw == total {
		if raw <= 0 {
			return b.countsOnly()
		}
		return b
	}
	scaled := b.countsOnly()
	fields := b.fields()
	out := scaled.fields()
	for i, v := range fields {
		*out[i] = int(int64(*v) * int64(total) / int64(raw))
	}
	if drift := total - scaled.Total(); drift != 0 {
		largest := out[0]
		for _, f := range out[1:] {
			if *f > *largest {
				largest = f
			}
		}
		*largest += drift
	}
	return scaled
}

// fields lists the token buckets in a stable order for the arithmetic in
// scaleTo. ToolCount is deliberately absent: it is a count of schemas, not a
// token cost, and scaling it would corrupt it.
func (b *ContextBreakdown) fields() []*int {
	return []*int{&b.System, &b.ToolSchemas, &b.ExternalSchemas, &b.Memory, &b.Summary, &b.Prose, &b.ToolResults, &b.Reasoning}
}

// countsOnly is an empty breakdown that keeps the schema counts, which are
// not token costs and so survive any rescaling of the costs.
func (b ContextBreakdown) countsOnly() ContextBreakdown {
	return ContextBreakdown{ToolCount: b.ToolCount, ExternalToolCount: b.ExternalToolCount}
}

// calibratedBreakdown is breakdown followed by scaleTo(used): the buckets a
// caller can display beside a used-token total without the two disagreeing.
func calibratedBreakdown(
	messages []provider.Message,
	toolSpecs []provider.ToolSpec,
	external map[string]string,
	profile provider.ContextAccountingProfile,
	used int,
) ContextBreakdown {
	raw, err := breakdown(messages, toolSpecs, external, profile)
	if err != nil {
		// The same schema-marshal failure EstimatePromptCost reports. The
		// gauge still has a total to show, so the breakdown degrades to the
		// tool counts alone rather than taking the section down with it.
		return ContextBreakdown{ToolCount: len(toolSpecs) - len(external), ExternalToolCount: len(external)}
	}
	return raw.scaleTo(used)
}
