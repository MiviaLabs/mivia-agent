package uiadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// subagentTaskRoute is the coordinator identity backing one registered
// callID: the run it belongs to and its own task ID within that run, the
// pair CancelSubagentTask needs to reach coordinator.Coordinator.CancelTask.
type subagentTaskRoute struct {
	runID  string
	taskID string
}

// SubagentThreads implements ports.SubagentThreads by dynamically resolving
// threads registered during runtime subagent executions.
type SubagentThreads struct {
	mu      sync.Mutex
	threads map[string]ports.Conversation
	// routes maps a registered callID to the coordinator run/task identity
	// backing it (see RegisterTaskRoute), so CancelSubagentTask can resolve
	// a UI-facing callID down to what coordinator.Coordinator.CancelTask
	// needs. A callID with no route (never registered by a caller that knew
	// the coordinator identity) cannot be canceled through this path.
	routes map[string]subagentTaskRoute
	// coord is the coordinator this registry cancels tasks through, wired
	// by SetCoordinator. Nil until set - CancelSubagentTask then reports a
	// clear error instead of silently no-oping.
	coord coordinator.Coordinator
}

// Compile-time check that SubagentThreads satisfies ports.SubagentThreads.
var _ ports.SubagentThreads = (*SubagentThreads)(nil)

// NewSubagentThreads creates a new SubagentThreads registry.
func NewSubagentThreads() *SubagentThreads {
	return &SubagentThreads{
		threads: make(map[string]ports.Conversation),
	}
}

// RegisterThread adds or replaces an active conversation thread for a tool call ID.
func (s *SubagentThreads) RegisterThread(callID string, conv ports.Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[callID] = conv
}

// SetCoordinator wires the coordinator.Coordinator instance
// CancelSubagentTask cancels tasks through. Safe to call once at
// construction; a nil coordinator is the zero value's behavior (every
// CancelSubagentTask call then errors clearly instead of silently no-oping).
func (s *SubagentThreads) SetCoordinator(c coordinator.Coordinator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coord = c
}

// RegisterTaskRoute records the coordinator run/task identity backing a
// registered callID, so a later CancelSubagentTask(callID) call can reach
// coordinator.Coordinator.CancelTask. A caller that dispatches a coordinator
// task and knows its own callID, runID, and taskID together calls this at
// dispatch time; a callID with no route was never wired this way (e.g. a
// reconstruction from persisted history, which carries no live coordinator
// identity) and CancelSubagentTask reports ok=false for it.
func (s *SubagentThreads) RegisterTaskRoute(callID, runID, taskID string) {
	if callID == "" || runID == "" || taskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes == nil {
		s.routes = map[string]subagentTaskRoute{}
	}
	s.routes[callID] = subagentTaskRoute{runID: runID, taskID: taskID}
}

// CancelSubagentTask stops the coordinator's execution of the ONE dispatched
// task backing callID, leaving its sibling tasks and the parent run
// untouched. See ports.SubagentThreads.CancelSubagentTask's doc comment for
// how this differs from TurnHandle.Cancel()/ActiveTurn().Cancel() (which
// only detach a UI listener from the live event stream).
func (s *SubagentThreads) CancelSubagentTask(callID string) (bool, error) {
	s.mu.Lock()
	route, ok := s.routes[callID]
	coord := s.coord
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	if coord == nil {
		return false, fmt.Errorf("uiadapter: no coordinator wired to cancel subagent task %q", callID)
	}
	h := coord.HandleForRun(route.runID)
	if h == nil {
		return false, fmt.Errorf("uiadapter: run %q for subagent task %q is no longer active", route.runID, callID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, route.taskID); err != nil {
		return false, err
	}
	return true, nil
}

// registerReconstructed registers a reconstruction under key, but never at
// the cost of richer live state: an existing registration that is not
// itself a reconstruction (a live streaming conversation, or any foreign
// ports.Conversation) always wins and the reconstruction is dropped for
// that key. Replacing an older reconstruction with a fresh one is an
// idempotent refresh and is allowed. This is what keeps a History() replay
// (screen construction, session switch, transcript reset) from displacing
// an in-flight or fully-streamed subagent thread with a prompt+summary
// stub built from persisted tool-call JSON.
func (s *SubagentThreads) registerReconstructed(key string, conv *SubagentTranscriptConversation) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.threads[key]; ok {
		stc, isTranscript := existing.(*SubagentTranscriptConversation)
		if !isTranscript || !stc.isReconstructed() {
			return
		}
	}
	s.threads[key] = conv
}

// DroppedEvents reports how many events this conversation could not hand to a
// listener. Non-zero means the live view is behind what History() holds, not
// that content was lost.
func (c *SubagentTranscriptConversation) DroppedEvents() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Thread retrieves the conversation thread for a given tool call ID.
func (s *SubagentThreads) Thread(callID string) (ports.Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.threads[callID]
	return c, ok
}

// HandleEvent receives subagent-originated agent.Events, translates them,
// records their history, and routes them to the matching SubagentTranscriptConversation.
func (s *SubagentThreads) HandleEvent(ev agent.Event, opts TranslateOptions) {
	if ev.Origin.IsZero() {
		return
	}
	// Origin.Agent is an agent NAME, not a per-run identity: two different
	// dispatch_tasks entries commonly route to the same named agent (e.g.
	// "general-purpose" is the default). Keying on it unconditionally
	// merges unrelated tasks into one shared conversation object -
	// whichever finishes first fires EventSubagentDone, which seals the
	// shared object (SubagentTranscriptConversation.done), and every later
	// event for the OTHER, still-running task is then silently dropped by
	// RecordEvent's !c.done guard even though its sidebar row (keyed
	// independently by TaskID) keeps updating normally. Only fall back to
	// Agent when there is no TaskID to key on at all - the reason this key
	// exists (371c35d5) is a caller that has nothing else to look up by,
	// not a general alias for every event.
	keys := []string{ev.Origin.TaskID, ev.ToolCallID}
	if ev.Origin.TaskID == "" {
		keys = append(keys, ev.Origin.Agent)
	}
	var hasKey bool
	for _, k := range keys {
		if k != "" {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return
	}

	conv := s.getOrCreate(keys, ev.Origin.Agent)
	translated := TranslateEventWithOptions(ev, opts)
	for _, e := range translated {
		conv.RecordEvent(e)
	}
}

func (s *SubagentThreads) getOrCreate(keys []string, title string) *SubagentTranscriptConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if c, ok := s.threads[k]; ok {
			if stc, ok := c.(*SubagentTranscriptConversation); ok {
				for _, other := range keys {
					if other != "" {
						s.threads[other] = stc
					}
				}
				return stc
			}
		}
	}
	if title == "" {
		for _, k := range keys {
			if k != "" {
				title = k
				break
			}
		}
	}
	stc := NewSubagentTranscriptConversation(title, ports.ModelInfo{Name: title}, nil)
	for _, k := range keys {
		if k != "" {
			s.threads[k] = stc
		}
	}
	return stc
}

// SubagentTranscriptConversation represents a subagent transcript thread.
type SubagentTranscriptConversation struct {
	title     string
	model     ports.ModelInfo
	mu        sync.Mutex
	history   []ports.Message
	listeners []chan uievent.Event
	active    bool
	// done marks that this thread's own terminal event (KindTurnEnd or a
	// "subagent done" notice) has already fired. It guards the top of
	// RecordEvent from letting a straggler event (a late tool_end, a
	// delayed forwarded delta arriving from a salvage/cleanup window)
	// resurrect active on a thread that will never produce another
	// terminal event. A genuine new KindTurnStart on a reused conversation
	// object (see getOrCreate) clears it again, matching a real restart.
	done bool
	// reconstructed marks a conversation built from persisted tool-call
	// JSON (subagent_reconstruct.go) rather than from live events. Only
	// reconstructed conversations may be replaced by a later
	// reconstruction (registerReconstructed); a live event landing on a
	// reconstructed conversation clears the flag, because from then on it
	// carries state no replay can rebuild.
	reconstructed bool
	// dropped counts events a listener's channel could not accept.
	//
	// The channel is bounded and the send is non-blocking, so a burst that
	// outruns the UI's drain is shed. That is the right trade for a live view
	// - a slow render must not stall the agent - but until this counter
	// existed the loss was SILENT, and the dialog showed a truncated answer
	// that looked complete. History() keeps every event regardless
	// (applyEvent runs before this), so a non-zero count means "the live view
	// is behind", not "content is gone".
	//
	// The session event bus solves the same problem by dropping the OLDEST
	// entry and counting it (internal/events/subscription.go trySend). This
	// drops the newest, which is a different trade and is left as it was;
	// what it was missing is the count.
	dropped uint64
}

// NewSubagentTranscriptConversation creates a new thread conversation.
func NewSubagentTranscriptConversation(title string, model ports.ModelInfo, history []ports.Message) *SubagentTranscriptConversation {
	var histCopy []ports.Message
	if len(history) > 0 {
		histCopy = make([]ports.Message, len(history))
		copy(histCopy, history)
	}
	return &SubagentTranscriptConversation{
		title:   title,
		model:   model,
		history: histCopy,
	}
}

// newReconstructedConversation creates a thread conversation rebuilt from
// persisted tool-call JSON, marked so the registry knows it may be
// refreshed by a later replay but must never displace live state.
func newReconstructedConversation(title string, model ports.ModelInfo, history []ports.Message) *SubagentTranscriptConversation {
	c := NewSubagentTranscriptConversation(title, model, history)
	c.reconstructed = true
	return c
}

func (c *SubagentTranscriptConversation) isReconstructed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconstructed
}

func isDoneNotice(e uievent.Event) bool {
	if e.Kind == uievent.KindNotice {
		if b, ok := e.Body.(uievent.NoticeBody); ok {
			return strings.HasPrefix(b.Text, "subagent done")
		}
	}
	return false
}

// RecordEvent records one translated uievent into message history and notifies listeners.
func (c *SubagentTranscriptConversation) RecordEvent(e uievent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.done {
		c.active = true
	}
	// A live event is evidence this conversation now carries state a
	// persisted-history replay cannot rebuild; stop treating it as a
	// replaceable reconstruction.
	c.reconstructed = false
	c.applyEvent(e)
	c.notifyListeners(e)
}

// applyEvent folds one translated uievent into message history.
func (c *SubagentTranscriptConversation) applyEvent(e uievent.Event) {
	switch e.Kind {
	case uievent.KindTurnStart:
		// A genuine new turn on a reused conversation object (getOrCreate
		// can hand the same object back for a later event sharing any
		// registration key) is a real restart, not a straggler - reset
		// done so RecordEvent starts setting active again.
		c.done = false
		c.active = true
		body, _ := e.Body.(uievent.TurnStartBody)
		c.history = append(c.history, ports.Message{
			Role: "user",
			Text: body.Input,
			At:   e.At,
		})
	case uievent.KindReasoning:
		body, _ := e.Body.(uievent.ReasoningDeltaBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		c.history[lastIdx].Reasoning += body.Text
	case uievent.KindToolStart:
		body, _ := e.Body.(uievent.ToolStartBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		argsBytes, _ := json.Marshal(body.Args)
		c.history[lastIdx].ToolCalls = append(c.history[lastIdx].ToolCalls, ports.ToolCall{
			ID:        body.ToolCallID,
			Name:      body.Name,
			Arguments: string(argsBytes),
		})
	case uievent.KindToolEnd:
		body, _ := e.Body.(uievent.ToolEndBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		for i := range c.history[lastIdx].ToolCalls {
			if c.history[lastIdx].ToolCalls[i].ID == body.ToolCallID {
				c.history[lastIdx].ToolCalls[i].Output = body.Result
				break
			}
		}
		if body.Diff != nil {
			c.history[lastIdx].Diffs = append(c.history[lastIdx].Diffs, *body.Diff)
		}
	case uievent.KindTextDelta:
		body, _ := e.Body.(uievent.TextDeltaBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		c.history[lastIdx].Text += body.Text
	case uievent.KindTextEnd:
		body, _ := e.Body.(uievent.TextEndBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		if c.history[lastIdx].Text == "" {
			c.history[lastIdx].Text = body.Text
		}
	case uievent.KindAssistantReset:
		// A schema retry discards the reply it is replacing, and this dialog
		// is the ONLY viewer a subagent's own reset ever reaches: the root
		// transcript filters every subagent kind but tool output, so nothing
		// downstream can repair what is kept here.
		//
		// Both shapes were wrong without this. Streaming concatenated the
		// rejected reply with its replacement. Not streaming was worse: the
		// text-end arm above writes only into an EMPTY message, so the
		// rejected reply stayed and the accepted one was dropped.
		//
		// Only the message's text goes. Tool calls and their diffs record work
		// that really ran and is not re-driven; reasoning is not re-sent by
		// the retry, so clearing it would lose it outright.
		if len(c.history) > 0 {
			lastIdx := len(c.history) - 1
			if c.history[lastIdx].Role == "assistant" {
				c.history[lastIdx].Text = ""
			}
		}
	}
}

// notifyListeners fans e out to active listeners, then - on this thread's own
// terminal event - marks it done and closes every listener so ActiveTurn()
// callers see the channel close as the done signal. A straggler event
// arriving after this point is handled by applyEvent/RecordEvent's !c.done
// guard, not here: it must not reopen this terminal state.
func (c *SubagentTranscriptConversation) notifyListeners(e uievent.Event) {
	for _, ch := range c.listeners {
		select {
		case ch <- e:
		default:
			c.dropped++
		}
	}
	if e.Kind == uievent.KindTurnEnd || isDoneNotice(e) {
		c.active = false
		c.done = true
		for _, ch := range c.listeners {
			close(ch)
		}
		c.listeners = nil
	}
}

func (c *SubagentTranscriptConversation) ensureLastAssistantMessage(at time.Time) {
	if len(c.history) == 0 || c.history[len(c.history)-1].Role != "assistant" {
		c.history = append(c.history, ports.Message{
			Role: "assistant",
			At:   at,
		})
	}
}

// ActiveTurn returns a live event subscription for the active subagent.
func (c *SubagentTranscriptConversation) ActiveTurn() (ports.TurnHandle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return nil, false
	}
	ch := make(chan uievent.Event, 32)
	c.listeners = append(c.listeners, ch)
	id := fmt.Sprintf("%s-live", c.title)
	h := &subagentTurnHandle{
		id:     id,
		events: ch,
		cancel: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, l := range c.listeners {
				if l == ch {
					c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
					close(ch)
					break
				}
			}
		},
	}
	return h, true
}

// Send records user messages in the thread and emits transcript stream events.
func (c *SubagentTranscriptConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.history = append(c.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	ch := make(chan uievent.Event, 32)
	c.listeners = append(c.listeners, ch)
	c.mu.Unlock()

	id := fmt.Sprintf("%s-turn-%d", c.title, len(c.history))
	h := &subagentTurnHandle{
		id:     id,
		events: ch,
		cancel: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, l := range c.listeners {
				if l == ch {
					c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
					close(ch)
					break
				}
			}
		},
	}
	return h, nil
}

// History returns a copy of the thread history.
func (c *SubagentTranscriptConversation) History() []ports.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ports.Message, len(c.history))
	copy(out, c.history)
	return out
}

// Model returns the subagent model information.
func (c *SubagentTranscriptConversation) Model() ports.ModelInfo {
	return c.model
}

// ContextUsage returns token usage for the subagent thread.
func (c *SubagentTranscriptConversation) ContextUsage() ports.Usage {
	return ports.Usage{}
}

// Title returns the title of the subagent thread.
func (c *SubagentTranscriptConversation) Title() string {
	if strings.TrimSpace(c.title) == "" {
		return "Subagent Thread"
	}
	return c.title
}

// ID returns the subagent thread ID.
func (c *SubagentTranscriptConversation) ID() string {
	return c.title
}

type subagentTurnHandle struct {
	id     string
	events chan uievent.Event
	cancel func()
	once   sync.Once
}

func (h *subagentTurnHandle) ID() string                   { return h.id }
func (h *subagentTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *subagentTurnHandle) Cancel() {
	h.once.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})
}

// CancelToolCall is not wired for subagent threads in this slice
// (per-tool-call cancellation for subagents is a separate slice); it
// always reports a miss.
func (h *subagentTurnHandle) CancelToolCall(string) bool { return false }
