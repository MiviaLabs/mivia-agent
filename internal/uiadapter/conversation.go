// Package uiadapter: Phase 2 Conversation and TurnHandle over chat.Session.
//
// Conversation wraps an existing *chat.Session into the ports.Conversation
// contract. The session is owned by the caller; the adapter holds no
// reference that outlives the caller closing the session and Send is the
// only mutating call. Concurrent Send calls are serialized by an internal
// mutex; the second caller blocks until the first finishes.
//
// TurnHandle exposes the buffered uievent.Event stream for one turn.
// Events() is closed exactly once when the turn ends. Cancel cancels the
// turn's per-turn context and closes the channel; it is safe to call
// after the turn has already ended.
//
// The synthetic-event ordering and Cancel/goroutine/tap coordination
// contract is documented on Send; the rationale moved to docs/ would
// belong there if it ever grows beyond a paragraph.
package uiadapter

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// turnBufferSize is the buffer of the per-turn event channel. It is
// chosen large enough that a moderately bursty turn does not block the
// agent loop while the UI is deserialising, but bounded so a runaway
// agent cannot grow memory without bound.
const turnBufferSize = 32

// turnWaiter is the package-private sync.WaitGroup every per-turn
// goroutine calls Done() on when it returns. It is installed by the
// SetTurnWaiterForTest test seam in export_test.go; production code
// never touches it. Reading from the goroutine rather than from a
// per-handle field keeps the seam in package-private scope: production
// callers of internal/uiadapter cannot install a waiter because the
// setter only exists in the test-export file.
var turnWaiter *sync.WaitGroup

// titleRuneLimit is the maximum rune count before deriveTitle ellipsises
// the title. Rune count, not byte count: a UI that renders the title is
// counting visible glyphs.
const titleRuneLimit = 60

// Conversation wraps a *chat.Session and satisfies ports.Conversation.
// Fields are unexported; NewConversation is the only construction path.
type Conversation struct {
	sess *chat.Session
	// turnMu serializes Send so two concurrent callers do not interleave
	// their agent events on the same session.
	turnMu sync.Mutex
	// titleMu guards lastTitle/titleDone/lastSessionID for memoisation
	// in Title().
	titleMu       sync.Mutex
	lastTitle     string
	lastSessionID string
	titleDone     bool
}

// NewConversation wraps an existing chat.Session. The caller owns the
// session and is responsible for its lifecycle; NewConversation stores
// the pointer verbatim and does not retain any other reference.
func NewConversation(sess *chat.Session) *Conversation {
	return &Conversation{sess: sess}
}

// Send starts one user turn on the wrapped session. The second caller
// blocks until the first finishes. The per-turn context is derived from
// ctx; cancelling it cancels the turn and closes the channel.
//
// Synthetic-event ordering and Cancel/goroutine/tap coordination contract:
// the very first event on the channel is KindTurnStart (TurnID=""
// because chat.Session only surfaces the turnID after SendUserWithEvent
// returns; the terminal turn.end carries the real TurnID). Tap-installed
// events stamp the real TurnID once known via a shared atomic.Pointer.
// An atomic.Bool closed is the single source of truth for "events is
// closed"; Cancel and the goroutine CAS-claim it; the tap drops on
// closed. Exactly one close occurs.
func (c *Conversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	if c.sess == nil {
		return nil, errors.New("uiadapter: Conversation.Send on nil session")
	}
	c.turnMu.Lock()
	turnCtx, cancelTurn := context.WithCancel(ctx)
	events := make(chan uievent.Event, turnBufferSize)
	// closed is the single source of truth for "events is closed". All
	// three senders (synthetic turn.start, tap, synthetic turn.end)
	// check closed.Load() first; Cancel and the per-turn goroutine
	// CAS-claim closed before calling close().
	closed := &atomic.Bool{}
	// turnIDPtr is read by the tap on the agent loop goroutine and
	// written by the goroutine running SendUserWithEvent as soon as the
	// turnID is known. atomic.Pointer[string] makes the read lock-free
	// and avoids a data race under -race.
	turnIDPtr := &atomic.Pointer[string]{}
	// seq is the per-turn event sequence number; the synthetic turn.start
	// gets Seq=1; subsequent events are atomic-incremented.
	var seq uint64
	emitSyntheticTurnStart(events, in.Text, &seq)

	handler := newTurnHandler(events, closed, turnIDPtr, &seq, turnCtx)
	previous := c.sess.SwapOnAgentEvent(handler)

	h := newTurnHandle(events, closed, cancelTurn, func() { c.sess.SwapOnAgentEvent(previous) })
	c.runTurnGoroutine(turnCtx, in, h, closed, turnIDPtr, &seq, cancelTurn)
	return h, nil
}

// emitSyntheticTurnStart sends the leading KindTurnStart with Seq=1 and
// TurnID="" so no agent event can race ahead of it on the per-turn
// channel. The channel buffer is sized to hold this event without
// blocking.
//
// The empty TurnID is the documented "empty-TurnID window" (see the
// package doc in event.go): chat.Session only surfaces the real ID
// after SendUserWithEvent returns, so the tap-installed events stamp
// the real ID via a shared atomic.Pointer once known. The terminal
// KindTurnEnd emitted by emitTurnEndIfWinner carries the real ID
// unconditionally, so renderers that index by TurnID should defer
// indexing until they see that event.
func emitSyntheticTurnStart(events chan<- uievent.Event, input string, seq *uint64) {
	atomic.AddUint64(seq, 1)
	events <- uievent.Event{
		Kind:   uievent.KindTurnStart,
		TurnID: "",
		Seq:    atomic.LoadUint64(seq),
		At:     time.Now(),
		Body:   uievent.TurnStartBody{Input: input},
	}
}

// newTurnHandler returns the per-turn agent-event handler that runs as
// the OnAgentEvent tap. It translates each agent.Event via
// uiadapter.TranslateEvent, stamps the real TurnID once known, and
// forwards onto the channel under a closed-check. The select on
// turnCtx.Done() drops the event rather than blocks the agent loop if
// the buffer is full and Cancel is mid-flight.
func newTurnHandler(events chan<- uievent.Event, closed *atomic.Bool, turnIDPtr *atomic.Pointer[string], seq *uint64, turnCtx context.Context) func(agent.Event) {
	return func(ev agent.Event) {
		if closed.Load() {
			return
		}
		for _, e := range TranslateEvent(ev) {
			if closed.Load() {
				return
			}
			n := atomic.AddUint64(seq, 1)
			if p := turnIDPtr.Load(); p != nil {
				e.TurnID = *p
			}
			e.Seq = n
			e.At = time.Now()
			select {
			case events <- e:
			case <-turnCtx.Done():
				return
			}
		}
	}
}

// newTurnHandle constructs the handle returned to the caller of Send.
// The restore closure detaches the per-turn tap from the session so the
// agent loop stops pushing events even before the goroutine returns;
// Cancel invokes it before closing the channel so a stray emit cannot
// panic on send-to-closed-channel.
func newTurnHandle(events chan uievent.Event, closed *atomic.Bool, cancel context.CancelFunc, restore func()) *turnHandle {
	return &turnHandle{
		idAtomic: &atomic.Pointer[string]{},
		events:   events,
		cancel:   cancel,
		closed:   closed,
		restore:  restore,
	}
}

// runTurnGoroutine runs the per-turn goroutine that drives
// SendUserWithEvent, restores the prior handler, emits the terminal
// turn.end if it wins the closed CAS, and releases turnMu so the next
// Send can proceed. cancelTurn is called at the very end so a stray
// emit from the agent loop after restore sees the cancelled context.
// If a turnWaiter WaitGroup was installed by SetTurnWaiterForTest, the
// goroutine calls Done() on it as the very last defer so the test can
// assert the goroutine has fully returned rather than only that the
// channel has closed (the latter is sequenced before this defer).
//
// The waiter pointer is captured into a local at goroutine start so
// concurrent SetTurnWaiterForTest calls across tests (a common pattern
// when -race runs reuse the package's shared global) do not race
// against the goroutine's read.
func (c *Conversation) runTurnGoroutine(turnCtx context.Context, in intent.Send, h *turnHandle, closed *atomic.Bool, turnIDPtr *atomic.Pointer[string], seq *uint64, cancelTurn context.CancelFunc) {
	waiter := turnWaiter
	go func() {
		defer h.restore()
		// Release turnMu only after the goroutine has fully finished so
		// the next Send waits for this turn to complete end-to-end.
		defer c.turnMu.Unlock()
		if waiter != nil {
			defer waiter.Done()
		}
		turnID, err := c.sess.SendUserWithEvent(turnCtx, in.Text, io.Discard, nil)
		h.idAtomic.Store(&turnID)
		turnIDPtr.Store(&turnID)
		c.emitTurnEndIfWinner(h, closed, seq, turnID, err)
		cancelTurn()
	}()
}

// emitTurnEndIfWinner CAS-claims closed. The winner emits the terminal
// turn.end with the real TurnID and then closes the channel. The
// non-blocking select protects against a full buffer at the moment of
// close (the receiver has stopped draining, which should not happen
// for the terminal event but is a belt-and-braces guard).
func (c *Conversation) emitTurnEndIfWinner(h *turnHandle, closed *atomic.Bool, seq *uint64, turnID string, err error) {
	if !closed.CompareAndSwap(false, true) {
		return
	}
	reason := "completed"
	if err != nil {
		if errors.Is(err, context.Canceled) {
			reason = "cancelled"
		} else {
			reason = "error"
			atomic.AddUint64(seq, 1)
			errEvent := uievent.Event{
				Kind:   uievent.KindNotice,
				TurnID: turnID,
				Seq:    atomic.LoadUint64(seq),
				At:     time.Now(),
				Body:   uievent.NoticeBody{Text: err.Error()},
			}
			select {
			case h.events <- errEvent:
			default:
			}
		}
	}
	atomic.AddUint64(seq, 1)
	endEvent := uievent.Event{
		Kind:   uievent.KindTurnEnd,
		TurnID: turnID,
		Seq:    atomic.LoadUint64(seq),
		At:     time.Now(),
		Body:   uievent.TurnEndBody{Reason: reason},
	}
	select {
	case h.events <- endEvent:
	default:
	}
	close(h.events)
}

// History returns a snapshot of the session's user/assistant turns at
// the moment of the call. Empty input returns nil (NOT an empty slice)
// so callers can distinguish "no history" from "history is empty".
func (c *Conversation) History() []ports.Message {
	if c.sess == nil {
		return nil
	}
	msgs := c.sess.MessagesCopy()
	out := make([]ports.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			out = append(out, ports.Message{Role: "user", Text: m.Content, At: m.CreatedAt})
		case provider.RoleAssistant:
			out = append(out, ports.Message{Role: "assistant", Text: m.Content, At: m.CreatedAt})
		}
		// system / tool roles are not exposed by the UI history view.
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Model reports the bound provider/model and its context window.
func (c *Conversation) Model() ports.ModelInfo {
	if c.sess == nil {
		return ports.ModelInfo{}
	}
	selection := c.sess.CurrentSelection()
	binding := c.sess.CurrentBinding()
	return ports.ModelInfo{
		Name:          selection.Model,
		Provider:      selection.ProviderName,
		ContextWindow: int64(binding.Profile.ContextWindowTokens),
	}
}

// ContextUsage reports the session's live prompt-cost estimate. Field
// mapping: InputTokens <- chat.ContextUsage.UsedTokens,
// OutputTokens <- chat.ContextUsage.OutputReserveTokens,
// CachedTokens = 0 (chat has no cache field; honest zero),
// CostUSD = 0 (chat has no cost field; honest zero).
// Percent from chat.ContextUsage is discarded.
func (c *Conversation) ContextUsage() ports.Usage {
	if c.sess == nil {
		return ports.Usage{}
	}
	u := c.sess.ContextUsage()
	return ports.Usage{
		InputTokens:  int64(u.UsedTokens),
		OutputTokens: int64(u.OutputReserveTokens),
		CachedTokens: 0,
		CostUSD:      0,
	}
}

// Title returns the session's display title, derived from the first user
// message. Memoised on first call for the current session ID.
func (c *Conversation) Title() string {
	c.titleMu.Lock()
	defer c.titleMu.Unlock()
	var currentID string
	if c.sess != nil {
		currentID = c.sess.SessionID
	}
	if c.titleDone && c.lastSessionID == currentID {
		return c.lastTitle
	}
	c.lastTitle = deriveTitle(c.sess.MessagesCopy())
	c.lastSessionID = currentID
	c.titleDone = true
	return c.lastTitle
}

// ID returns the active session's ID.
func (c *Conversation) ID() string {
	if c.sess == nil {
		return ""
	}
	return c.sess.SessionID
}

// deriveTitle finds the first user message, trims and collapses runs of
// whitespace to one space, and ellipsises past titleRuneLimit runes.
// Returns "" when no user message exists. The result is a derived
// preview, not authoritative: a future session-metadata path may
// override it.
func deriveTitle(msgs []provider.Message) string {
	var raw string
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			raw = m.Content
			break
		}
	}
	if raw == "" {
		return ""
	}
	// Collapse internal whitespace runs to a single space.
	collapsed := strings.Join(strings.Fields(raw), " ")
	runes := []rune(collapsed)
	if len(runes) > titleRuneLimit {
		collapsed = string(runes[:titleRuneLimit]) + "..."
	}
	return collapsed
}

// turnHandle is the per-turn handle returned by Send. Events() is closed
// exactly once when the turn ends (or is cancelled). Cancel cancels the
// turn's per-turn context; if the turn has already ended, Cancel is a
// no-op.
type turnHandle struct {
	idAtomic *atomic.Pointer[string]
	events   chan uievent.Event
	cancel   context.CancelFunc
	closed   *atomic.Bool
	restore  func()
}

// ID returns the turn ID assigned by chat.Session.SendUserWithEvent.
// The turnID is not known until SendUserWithEvent returns; the goroutine
// populates idAtomic when it does, so ID() returns "" until that point.
// Consumers should usually read the TurnID from the synthetic
// KindTurnEnd event, which is guaranteed to carry it.
func (h *turnHandle) ID() string {
	p := h.idAtomic.Load()
	if p == nil {
		return ""
	}
	return *p
}

// Events returns the buffered channel of uievent.Events for one turn.
// The channel is closed exactly once when the turn ends (completed,
// cancelled, or error).
func (h *turnHandle) Events() <-chan uievent.Event { return h.events }

// Cancel cancels the turn's per-turn context and closes the channel.
// Safe to call after the turn has ended. The atomic CAS on closed
// guarantees exactly one close.
//
// Ordering matters here:
//  1. cancel the per-turn context so the agent loop's blocking emits
//     observe ctx.Done() and bail out of the tap;
//  2. restore the previous OnAgentEvent so the agent loop is no longer
//     routed to events at all;
//  3. CAS closed and, if we won, close the channel.
//
// The per-turn goroutine competes on the same CAS: if it wins, it
// emits turn.end and closes; if Cancel wins, the goroutine drops
// turn.end (the test only requires that the channel closes).
func (h *turnHandle) Cancel() {
	if h.cancel != nil {
		h.cancel()
	}
	if h.restore != nil {
		h.restore()
	}
	if h.closed != nil {
		if h.closed.CompareAndSwap(false, true) {
			close(h.events)
		}
	}
}
