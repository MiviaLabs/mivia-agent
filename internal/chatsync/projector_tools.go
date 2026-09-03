package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Tool and subagent projections. Split out of projector.go to keep that file
// under the 500-line structural budget (.mivia/policy/go-structure.json).

// closeStepOnToolStart registers the turn and, when ev actually STARTS a tool,
// closes the prose that preceded it: the model stopped talking and acted, so
// what it says next belongs to the next step.
//
// KindSubagentStart is a subagent's tool start; the other subagent kinds are
// run lifecycle and close nothing. The counter stepped is the dispatching
// run's own when the call is attributed, and the root turn's otherwise - two
// runs streaming at once must not step each other's.
func (p *Projector) closeStepOnToolStart(ev events.Event, turnID string) {
	ts := p.turn(turnID)
	if ev.Kind != events.KindToolStart && ev.Kind != events.KindSubagentStart {
		return
	}
	if isDispatched(ev) {
		ts = p.laneState(turnID, ev.AgentTask)
	}
	p.advanceStep(ts)
}

// blockSegment picks the segment a prose event belongs to. A delta belongs to
// the segment open right now, and records it. A settled aggregate belongs to
// the segment its own deltas streamed into, because the aggregate is published
// after the turn's last tool call has already advanced the counter past it.
// With nothing streamed the two coincide, so a non-streaming turn opens its one
// block exactly where it always did.
func (ts *turnState) blockSegment(isDelta bool) int {
	if isDelta {
		return ts.segment
	}
	if ts.streamed {
		return ts.streamSegment
	}
	return ts.segment
}

// recordDeltaSegment notes, once a delta has actually shipped, the segment
// it shipped into. The settled aggregate names the segment its SURVIVING
// deltas used, so the recording has to follow the gate that decides what
// shipped. Recording when the block id was merely picked let a delta that was
// suppressed (a mid-turn redaction policy) or lost (a failed append, repaired
// by rollbackOneDelta) drag the settle onto a segment that holds nothing.
//
// It also counts the delta for the block: every caller sits after the gate
// that decides what shipped, so the per-block count follows exactly the rule
// the segment does. A change of segment retires the previous block's count
// into prevBlockFragments, one entry deep like prevStreamSegment.
//
// shipped is the delta's text size, accumulated per block for the settle's
// `bytes`, and retired into prevBlockBytes alongside the count so a fallback
// restores both. A shipped delta also re-opens the block for settling: the block's
// aggregate, if one already went out, no longer describes it.
func (ts *turnState) recordDeltaSegment(segment int, shipped int) {
	if ts.streamSegment != segment {
		ts.prevStreamSegment = ts.streamSegment
		ts.prevBlockFragments = ts.blockFragments
		ts.prevBlockBytes = ts.blockBytes
		ts.streamSegment = segment
		ts.blockFragments = 0
		ts.blockBytes = 0
	}
	ts.blockFragments++
	ts.blockBytes += shipped
	ts.assistantSettled = false
}

// advanceStep closes the open segment of a stream, so the next prose opens a
// new block. A tool call is the boundary: it is the point where the model stops
// talking and acts.
//
// It is a no-op on a segment nothing shipped into. Advancing there would spend
// ids on silence - and a consumer that renders one message per id would show
// the gaps as blank messages.
func (p *Projector) advanceStep(ts *turnState) {
	if ts == nil || (ts.segmentAssistant == 0 && ts.segmentThinking == 0) {
		return
	}
	ts.segment = p.allocSegment()
	ts.segmentAssistant, ts.segmentThinking = 0, 0
}

// advanceStepForReset is advanceStep that remembers what it replaced, for a
// reset whose append may yet fail. It records nil when it advanced nothing,
// so the undo is exactly as conditional as the advance.
func (p *Projector) advanceStepForReset(ts *turnState) {
	ts.resetUndo = nil
	if ts.segmentAssistant == 0 && ts.segmentThinking == 0 {
		return
	}
	ts.resetUndo = &segmentUndo{segment: ts.segment, segmentAssistant: ts.segmentAssistant, segmentThinking: ts.segmentThinking}
	p.advanceStep(ts)
}

func (p *Projector) projectTool(env Envelope, ev events.Event) []WireEvent {
	env.Block = ev.ToolCallID
	name := applyTruncation(&env, "name", ev.Name, BudgetShortField)
	callID := applyTruncation(&env, "tool_call_id", ev.ToolCallID, BudgetShortField)

	if ev.Kind == events.KindToolStart {
		var input string
		if shouldIncludeToolIO(p.opts) {
			input = redactText(toolInputOf(ev))
			input = applyTruncation(&env, "input", input, BudgetToolInput)
		} else {
			env.Redacted = append(env.Redacted, "input")
		}
		payload := &ToolStartedPayload{
			Envelope:   env,
			ToolCallID: callID,
			Name:       name,
			InputBytes: len(toolInputOf(ev)),
			Input:      input,
		}
		return []WireEvent{p.nextWireEvent(TypeToolStarted, payload)}
	}

	var output string
	if shouldIncludeToolIO(p.opts) {
		output = redactText(toolOutputOf(ev))
		output = applyTruncation(&env, "output", output, BudgetToolOutput)
	} else {
		env.Redacted = append(env.Redacted, "output")
	}
	detail := applyTruncation(&env, "detail", ev.Detail, BudgetShortField)
	payload := &ToolEndedPayload{
		Envelope:    env,
		ToolCallID:  callID,
		Name:        name,
		Status:      toolEndStatus(detail),
		OutputBytes: len(toolOutputOf(ev)),
		Detail:      detail,
		Output:      output,
	}
	return []WireEvent{p.nextWireEvent(TypeToolEnded, payload)}
}

func (p *Projector) projectSubagent(env Envelope, ev events.Event) []WireEvent {
	env.Block = ev.ToolCallID
	name := applyTruncation(&env, "name", ev.Name, BudgetShortField)
	callID := applyTruncation(&env, "tool_call_id", ev.ToolCallID, BudgetShortField)

	switch ev.Kind {
	case events.KindSubagentBegin:
		task := redactText(ev.Detail)
		task = applyTruncation(&env, "task", task, BudgetShortField)
		payload := &SubagentStartedPayload{
			Envelope: env,
			Name:     name,
			Task:     task,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentStarted, payload)}
	case events.KindSubagentStart:
		var input string
		if shouldIncludeToolIO(p.opts) {
			input = redactText(toolInputOf(ev))
			input = applyTruncation(&env, "input", input, BudgetToolInput)
		} else {
			env.Redacted = append(env.Redacted, "input")
		}
		payload := &SubagentToolStartedPayload{
			Envelope:   env,
			ToolCallID: callID,
			Name:       name,
			InputBytes: len(toolInputOf(ev)),
			Input:      input,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentToolStarted, payload)}
	case events.KindSubagentEnd:
		var output string
		if shouldIncludeToolIO(p.opts) {
			output = redactText(toolOutputOf(ev))
			output = applyTruncation(&env, "output", output, BudgetToolOutput)
		} else {
			env.Redacted = append(env.Redacted, "output")
		}
		detail := applyTruncation(&env, "detail", ev.Detail, BudgetShortField)
		payload := &SubagentToolEndedPayload{
			Envelope:    env,
			ToolCallID:  callID,
			Name:        name,
			Status:      toolEndStatus(detail),
			OutputBytes: len(toolOutputOf(ev)),
			Detail:      detail,
			Output:      output,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentToolEnded, payload)}
	case events.KindSubagentHeartbeat:
		detail := applyTruncation(&env, "detail", ev.Detail, BudgetShortField)
		payload := &SubagentProgressPayload{
			Envelope: env,
			Detail:   detail,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentProgress, payload)}
	case events.KindSubagentDone:
		// The run is over, so its streaming state is dead weight. Retiring it
		// here keeps finished runs from occupying slots until they age out of
		// the LRU, which is what would otherwise let a LIVE lane be evicted
		// mid-stream: a lane that loses its state mid-run reports streamed as
		// false again, and its aggregate then ships the full text a viewer has
		// already received delta by delta.
		p.retireLane(ev.AgentTask)
		agentName := applyTruncation(&env, "name", ev.AgentName, BudgetShortField)
		status := applyTruncation(&env, "status", ev.Detail, BudgetShortField)
		payload := &SubagentEndedPayload{
			Envelope: env,
			Name:     agentName,
			Status:   status,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentEnded, payload)}
	default:
		return nil
	}
}

// projectSubagentAssistant projects one subagent's assistant output.
//
// It mirrors projectAssistant field for field, with two necessary differences:
//
//   - the wire types are the subagent ones, so a viewer can keep this text out
//     of the main transcript on the type string alone; and
//   - the fragment/byte counters come from laneState, not the turn's, so two
//     subagents streaming at once cannot corrupt each other's INV-1 accounting
//     or the root loop's.
//
// Redaction and truncation are the root path's, unchanged: a subagent's prose
// is not held to a weaker standard than the root loop's.
func (p *Projector) projectSubagentAssistant(env Envelope, turnID string, ev events.Event) []WireEvent {
	if ev.Content == "" {
		return nil
	}
	// Block is lane-scoped so a viewer groups each subagent's prose on its
	// own; the root's key is turnID+":assistant" and would merge them all.
	ls := p.laneState(turnID, ev.AgentTask)
	seg := ls.blockSegment(ev.Detail == "delta")
	env.Block = proseBlock(turnID+":"+ev.AgentTask+":assistant", seg)

	if ev.Detail == "delta" {
		if !p.opts.StreamAssistant {
			return nil
		}
		// The lane's own cross-fragment redactor, for the reason the root
		// path documents: a policy is session-wide, so a subagent's prose is
		// held to exactly the root's standard, and holding it per lane is
		// what stops two runs streaming at once from splicing each other's
		// tails together.
		if !ls.assistantStream.Pending() {
			ls.assistantHoldSegment = seg
		}
		shipped := ls.assistantStream.Push(ev.Content)
		if shipped == "" {
			return nil
		}
		// Recorded only once the delta is actually going out - see
		// projectAssistant for why the order matters. The step counter and
		// the settle block follow the same rule.
		ls.streamed = true
		ls.fragments++
		ls.segmentAssistant++
		ls.recordDeltaSegment(seg, len(shipped))
		shipped = applyTruncation(&env, "text", shipped, BudgetDeltaText)
		payload := &SubagentAssistantDeltaPayload{
			Envelope: env,
			Text:     shipped,
			Index:    ls.fragments - 1,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentAssistantDelta, payload)}
	}

	// The aggregate closes the block; the held tail goes out first, as a
	// delta, because INV-1 is about to empty this message's text.
	flushed := p.flushHeldAssistant(env, turnID+":"+ev.AgentTask+":assistant", ls, true, true)
	env.Block = proseBlock(turnID+":"+ev.AgentTask+":assistant", ls.blockSegment(false))
	content := redactText(ev.Content)

	text := content
	fragments := 0
	// streamUnrecoverable: a discard this lane never got onto the wire. The
	// viewer still holds the abandoned attempt, so the full text has to travel
	// for it to replace. Same rule as the root path.
	// Per block, as the root path counts - see projectAssistant.
	if ls.streamed && !ls.streamUnrecoverable && ls.blockFragments > 0 {
		fragments = ls.blockFragments
		text = "" // INV-1: text empty iff fragments > 0.
	} else {
		text = applyTruncation(&env, "text", text, BudgetAssistantText)
	}
	payload := &SubagentAssistantMessagePayload{
		Envelope:  env,
		Fragments: fragments,
		Bytes:     len(ev.Content),
		Status:    "completed",
		Text:      text,
	}
	return append(flushed, p.nextWireEvent(TypeSubagentAssistantMessage, payload))
}

// projectSubagentThinking projects one subagent's reasoning. Bytes always
// reports the real size so a viewer can show that an agent is thinking even
// when IncludeThinking withholds the text, exactly as projectThinking does.
func (p *Projector) projectSubagentThinking(env Envelope, turnID string, ev events.Event) []WireEvent {
	if ev.Content == "" {
		return nil
	}
	ls := p.laneState(turnID, ev.AgentTask)
	env.Block = proseBlock(turnID+":"+ev.AgentTask+":thinking", ls.segment)

	text := ""
	// Streamed through the lane's cross-fragment redactor, exactly as the
	// root path does: a fragment-sized redaction boundary cannot catch a
	// secret that spans two fragments, so the boundary is moved rather than
	// the stream suppressed.
	if p.opts.IncludeThinking {
		text = ls.thinkingStream.Push(ev.Content)
		text = applyTruncation(&env, "text", text, BudgetDeltaText)
	}
	// The lane accumulates for its own settled aggregate exactly as the root
	// does; see projector_thinking.go. Symmetry is not decoration here - a
	// redaction policy is session-wide, so a lane's reasoning is withheld
	// under precisely the conditions the root's is.
	ls.recordThinking(ev.Content, ls.segment, text != "")
	ls.segmentThinking++
	index := ls.thinkingFragments
	ls.thinkingFragments++
	payload := &SubagentThinkingDeltaPayload{
		Envelope: env,
		Bytes:    len(ev.Content),
		Index:    index,
		Text:     text,
	}
	return []WireEvent{p.nextWireEvent(TypeSubagentThinkingDelta, payload)}
}

// toolEndStatus derives the wire status from a tool end's detail.
//
// Callers pass the SANITISED, bounded detail, never ev.Detail. This field used
// to be built from the raw event while the identical string was sanitised and
// truncated into `detail` two lines away, so a NUL reached the wire on
// tool.ended and a long detail produced a payload six times over the column
// bound - both of them through the one field that skipped the choke point
// every other free-text field goes through.
func toolEndStatus(detail string) string {
	if detail == "" {
		return "ok"
	}
	return detail
}

// projectAssistantReset tells a viewer to discard the assistant text it has
// accumulated for one block, because the turn producing it is being re-driven
// from the beginning.
//
// It also clears the producing side's own streaming state. Without that, the
// replayed attempt would continue the abandoned attempt's fragment count, and
// INV-1 would then describe a block that holds two attempts' deltas.
func (p *Projector) projectAssistantReset(env Envelope, turnID string, ev events.Event) []WireEvent {
	// The block names the STREAM, with no segment suffix. A reset discards the
	// turn's assistant text wherever it landed, and by now that can span
	// several segments - one segment id cannot name them all. The stream id is
	// the prefix every one of them extends, which is exactly the scope meant.
	//
	// Advancing the segment is what keeps the replay honest: reusing the
	// abandoned attempt's id would let a consumer keyed on the id append the
	// replay to the text it was just told to discard.
	if isDispatched(ev) {
		env.Block = turnID + ":" + ev.AgentTask + ":assistant"
		ls := p.laneState(turnID, ev.AgentTask)
		ls.streamed, ls.fragments = false, 0
		ls.blockFragments, ls.prevBlockFragments = 0, 0
		ls.blockBytes, ls.prevBlockBytes, ls.assistantSettled = 0, 0, false
		// DISCARD, not flush: the consumer is being told to throw this
		// block's text away, so shipping the held tail would deliver words
		// into a block that is about to be emptied. This is the one place a
		// held tail may be dropped without losing anything.
		ls.assistantStream.Discard()
		p.advanceStepForReset(ls)
	} else {
		env.Block = turnID + ":assistant"
		ts := p.turn(turnID)
		ts.streamed, ts.fragments = false, 0
		ts.blockFragments, ts.prevBlockFragments = 0, 0
		ts.blockBytes, ts.prevBlockBytes, ts.assistantSettled = 0, 0, false
		ts.assistantStream.Discard()
		p.advanceStepForReset(ts)
	}
	// Truncate BEFORE the literal. Go evaluates the fields in order, so
	// `Envelope: env` copies env first and any trunc record applyTruncation
	// then writes lands on a value nobody reads.
	reason := applyTruncation(&env, "reason", ev.Detail, BudgetShortField)
	payload := &AssistantResetPayload{
		Envelope: env,
		Reason:   reason,
	}
	return []WireEvent{p.nextWireEvent(TypeAssistantReset, payload)}
}

// projectHook projects one lifecycle hook run.
//
// A hook row is the operator's audit record of a policy program's verdict:
// which program ran, in which phase, against which call, and whether it
// refused. A blocked call still reports its own failed tool.ended, so this
// row is not what repairs a dangling tool row - it is what says WHY the call
// was stopped, which no other event carries.
//
// The typed payload is required. Phase, Program, Tool and Denied live on
// agent.Event and are not carried by the bus's generic string conversion, so
// an event arriving here without events.HookEvent cannot say whether the call
// was refused - and a hook row that cannot answer that is not worth sending.
func (p *Projector) projectHook(env Envelope, ev events.Event) []WireEvent {
	if ev.Hook == nil {
		return nil
	}
	phase := applyTruncation(&env, "phase", ev.Hook.Phase, BudgetShortField)
	program := applyTruncation(&env, "program", ev.Hook.Program, BudgetShortField)
	tool := applyTruncation(&env, "tool", ev.Hook.Tool, BudgetShortField)
	callID := applyTruncation(&env, "tool_call_id", ev.ToolCallID, BudgetShortField)

	// A hook's output is text a local program printed, which is the same class
	// as tool output and rides the same gate. Withholding still reports the
	// byte count, so a reader can tell silence from suppression.
	// ev.Hook.Output, never ev.Output: the generic field appends an operator
	// diagnostic that names the hook's absolute path, and a filesystem path
	// describes the operator's machine.
	stdout := ev.Hook.Output
	var output string
	if shouldIncludeToolIO(p.opts) && !redactionActive() {
		output = applyTruncation(&env, "output", redactText(stdout), BudgetToolOutput)
	} else if stdout != "" {
		env.Redacted = append(env.Redacted, "output")
	}

	payload := &HookRanPayload{
		Envelope:    env,
		Phase:       phase,
		Program:     program,
		Tool:        tool,
		ToolCallID:  callID,
		Blocked:     ev.Hook.Denied,
		OutputBytes: len(stdout),
		Output:      output,
	}
	return []WireEvent{p.nextWireEvent(TypeHookRan, payload)}
}

// toolInputOf and toolOutputOf pick the unbounded body the emitter carries
// beside the operator preview (events.Event.InputBody / OutputBody), and
// fall back to the preview for an emitter that predates them. The projector
// applies its own budget and records the cut in trunc.fields; reading the
// preview here meant every read_file shipped as 512 bytes reporting
// output_bytes 512 with no marker, and a viewer could not tell a small file
// from a cut one.
func toolInputOf(ev events.Event) string {
	if ev.InputBody != "" {
		return ev.InputBody
	}
	return ev.Input
}

func toolOutputOf(ev events.Event) string {
	if ev.OutputBody != "" {
		return ev.OutputBody
	}
	return ev.Output
}
