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
	if ev.AgentTask != "" {
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
		ts.streamSegment = ts.segment
		return ts.segment
	}
	if ts.streamed {
		return ts.streamSegment
	}
	return ts.segment
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
		Status:      toolEndStatus(ev.Detail),
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
			Status:      toolEndStatus(ev.Detail),
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
	env.Block = proseBlock(turnID+":"+ev.AgentTask+":assistant", ls.blockSegment(ev.Detail == "delta"))
	content := redactText(ev.Content)

	if ev.Detail == "delta" {
		if !p.opts.StreamAssistant || redactionActive() {
			return nil
		}
		// Recorded only once the delta is actually going out - see
		// projectAssistant for why the order matters. The step counter follows
		// the same rule.
		ls.streamed = true
		ls.fragments++
		ls.segmentAssistant++
		content = applyTruncation(&env, "text", content, BudgetDeltaText)
		payload := &SubagentAssistantDeltaPayload{
			Envelope: env,
			Text:     content,
			Index:    ls.fragments - 1,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentAssistantDelta, payload)}
	}

	text := content
	fragments := 0
	// streamUnrecoverable: a discard this lane never got onto the wire. The
	// viewer still holds the abandoned attempt, so the full text has to travel
	// for it to replace. Same rule as the root path.
	if ls.streamed && !ls.streamUnrecoverable {
		fragments = ls.fragments
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
	return []WireEvent{p.nextWireEvent(TypeSubagentAssistantMessage, payload)}
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
	// Withheld under a policy for the same reason as the root path: a
	// fragment-sized redaction boundary cannot catch a secret that spans two
	// fragments, and thinking has no whole-message form to fall back to.
	if p.opts.IncludeThinking && !redactionActive() {
		text = redactText(ev.Content)
		text = applyTruncation(&env, "text", text, BudgetDeltaText)
	}
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
	if ev.AgentTask != "" {
		env.Block = turnID + ":" + ev.AgentTask + ":assistant"
		ls := p.laneState(turnID, ev.AgentTask)
		ls.streamed, ls.fragments = false, 0
		p.advanceStepForReset(ls)
	} else {
		env.Block = turnID + ":assistant"
		ts := p.turn(turnID)
		ts.streamed, ts.fragments = false, 0
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
// The case that justifies the event is a hook that BLOCKED a tool call: a
// blocked call emits no tool.ended, so without this a remote reader watches a
// tool.started that never finishes and is never told a local policy stopped
// it.
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
