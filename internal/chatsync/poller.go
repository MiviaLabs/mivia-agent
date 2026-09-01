package chatsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const pendingInputFileName = "pending_input.json"

type pendingInputState struct {
	Input    *SessionInput `json:"input"`
	Consumed bool          `json:"consumed"`
}

// RemoteInput represents a received and consumed user input from the remote viewer.
type RemoteInput struct {
	ID        string
	SessionID string
	Kind      string
	Body      string
	Received  time.Time
}

// InputPoller polls for remote user inputs and executes the exactly-once handshake.
type InputPoller struct {
	client      *Client
	sessionID   string
	waitSeconds int
	stateDir    string
	inputCh     chan RemoteInput
	stopCh      chan struct{}
	doneCh      chan struct{}
	mu          sync.Mutex
	running     bool

	// authorUserID resolves the CLI's own authenticated principal; see
	// AuthorUserIDProvider and resolveAuthorUserID. nil fails every input
	// closed (no verification possible).
	authorUserID     AuthorUserIDProvider
	authorIDResolved bool
	authorID         string
	authorIDErr      error

	// onRejected, when set, is invoked (synchronously, on the poll goroutine)
	// for every input validateRemoteInput refuses. It exists so a host can
	// surface the refusal instead of it being silent - see
	// SessionOptions.OnInputRejected.
	onRejected func(id, sessionID, reason string)
}

const (
	defaultPollWaitSeconds = 25
	maxPollWaitSeconds     = 300
	// pollDeadlineSlackSeconds is the settled per-request budget on top of the
	// server-side park (`waitSeconds + 10s`, chat-sync-cli-slice.md section 6).
	// The code carried 5s, which can expire on a healthy park plus normal
	// network latency and turn a successful long poll into a retry storm.
	pollDeadlineSlackSeconds = 10
)

// NewInputPoller creates a new InputPoller. authorUserID resolves the CLI's
// own authenticated principal for verifying who queued each input; nil
// verifies nothing and so refuses every input (fail closed).
func NewInputPoller(client *Client, sessionID string, waitSeconds int, authorUserID AuthorUserIDProvider, stateDir ...string) *InputPoller {
	if waitSeconds <= 0 {
		waitSeconds = defaultPollWaitSeconds
	} else if waitSeconds > maxPollWaitSeconds {
		waitSeconds = maxPollWaitSeconds
	}
	dir := ""
	if len(stateDir) > 0 {
		dir = stateDir[0]
	}
	return &InputPoller{
		client:       client,
		sessionID:    sessionID,
		waitSeconds:  waitSeconds,
		stateDir:     dir,
		authorUserID: authorUserID,
		inputCh:      make(chan RemoteInput, 16),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// SetOnRejected installs the callback pollOnce and recoverPendingInput
// invoke for every input validateRemoteInput refuses. Must be called before
// Start; nil (the default) means refusals stay silent to the host.
func (p *InputPoller) SetOnRejected(fn func(id, sessionID, reason string)) {
	p.onRejected = fn
}

// Inputs returns the channel receiving consumed remote inputs.
func (p *InputPoller) Inputs() <-chan RemoteInput {
	return p.inputCh
}

// Start launches the polling loop.
func (p *InputPoller) Start(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	p.recoverPendingInput(ctx)
	go p.loop(ctx)
}

// SetSessionID updates the session ID polled by this runner.
func (p *InputPoller) SetSessionID(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessionID = sessionID
}

// Stop terminates the poller and waits for loop exit, bounded by ctx.
//
// Closing stopCh also cancels the in-flight long poll, so the loop normally
// exits at once. The ctx arm is the backstop for a request the transport is
// slow to abandon: a shutdown deadline the caller set must bound this call,
// never the poll deadline.
func (p *InputPoller) Stop(ctx context.Context) {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	close(p.stopCh)
	p.mu.Unlock()

	select {
	case <-p.doneCh:
	case <-ctx.Done():
	}
}

func (p *InputPoller) loop(ctx context.Context) {
	defer close(p.doneCh)
	defer close(p.inputCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		default:
		}

		p.pollOnce(ctx)
	}
}

func (p *InputPoller) pollOnce(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(p.waitSeconds+pollDeadlineSlackSeconds)*time.Second)
	defer cancel()

	// A parked long poll must abandon its request the moment Stop is called,
	// otherwise shutdown waits out the whole poll deadline.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-p.stopCh:
			cancel()
		case <-watchDone:
		}
	}()

	p.mu.Lock()
	sessID := p.sessionID
	p.mu.Unlock()

	next, err := p.client.NextInput(pollCtx, sessID, p.waitSeconds)
	if err != nil {
		select {
		case <-ctx.Done():
		case <-p.stopCh:
		case <-time.After(500 * time.Millisecond):
		}
		return
	}

	if next == nil || next.Input == nil {
		return
	}

	raw := next.Input
	_ = p.writePendingInput(raw, false)

	consumeCtx, cancelConsume := context.WithTimeout(ctx, 5*time.Second)
	defer cancelConsume()

	// sessID, not p.sessionID: the same value NextInput just used above,
	// read once under the lock. A second unprotected read of p.sessionID
	// here would race against SetSessionID (data race under -race) and,
	// worse, could send this ConsumeInput against a DIFFERENT session than
	// the one raw.ID was fetched from if SetSessionID ran in between.
	consumed, err := p.client.ConsumeInput(consumeCtx, sessID, raw.ID)
	if err != nil {
		// The server never confirmed the consume, so nothing was committed;
		// the pending_input.json this wrote above (Consumed: false) is
		// discarded on the next recovery pass by design - see
		// TestInputPoller_CrashRecovery_UnconsumedInputDiscarded.
		return
	}

	_ = p.writePendingInput(consumed, true)
	p.deliver(ctx, consumed)
}

// deliver validates an already-server-consumed SessionInput and, only on
// success, places it on Inputs() and records it in the delivered-ids
// ledger. clearPendingInput runs ONLY once that terminal outcome is known -
// the send onto Inputs() succeeding, or a validation refusal that will never
// change on retry - never merely because the server-side consume succeeded.
// If the delivery select instead exits via ctx.Done()/stopCh (shutdown
// mid-send, nobody draining Inputs() yet), pending_input.json is
// deliberately left in place: restart recovery is what gets this input a
// real second chance, exactly the property
// TestInputPoller_UndeliveredConsumedInputSurvivesShutdown pins.
//
// "Placed on Inputs()" is NOT the same claim as "the instruction ran". This
// package is a leaf (settled decision 7): it has no visibility past its own
// channel into whether a reader ever pulls the value out and acts on it. A
// crash after this select's send succeeds but before some downstream reader
// (internal/uiadapter's pumpRemoteInputs, then the UI's own conv.Send) has
// actually consumed and executed it loses the instruction silently - the
// durable record is already cleared and the delivered-ids ledger already
// prevents a restart from replaying it. This mirrors the same
// not-durable-until-actually-run property a LOCALLY typed message already
// has while queued behind an active turn (Screen.queue/sessionState.queue
// are plain in-memory slices too); closing it end to end would need an
// acknowledgement flowing back from the UI into this leaf package, which
// does not exist today and is a real architectural change, not a bug fix.
func (p *InputPoller) deliver(ctx context.Context, consumed *SessionInput) {
	ri, reason := p.validateRemoteInput(ctx, consumed)
	if reason != "" {
		if p.onRejected != nil {
			p.onRejected(consumed.ID, consumed.SessionID, reason)
		}
		p.clearPendingInput()
		return
	}
	ri.Received = time.Now()

	select {
	case p.inputCh <- ri:
		p.recordDelivered(ri.ID)
		p.clearPendingInput()
	case <-ctx.Done():
	case <-p.stopCh:
	}
}

func (p *InputPoller) writePendingInput(input *SessionInput, consumed bool) error {
	if p.stateDir == "" || input == nil {
		return nil
	}
	state := pendingInputState{
		Input:    input,
		Consumed: consumed,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal pending input: %w", err)
	}
	return writeFileDurably(p.stateDir, pendingInputFileName, data)
}

func (p *InputPoller) clearPendingInput() {
	if p.stateDir == "" {
		return
	}
	_ = os.Remove(filepath.Join(p.stateDir, pendingInputFileName))
}

// recoverPendingInput replays a pending_input.json left behind by a process
// that ended between "the server confirmed consume" and "the input was
// confirmed delivered". It runs synchronously inside Start, before the poll
// loop goroutine starts, on a channel nothing else has touched yet - the
// buffered send below cannot lose a value to a full channel, per this
// method's own precondition (a fresh, cap-16, nothing-sent-yet inputCh).
func (p *InputPoller) recoverPendingInput(ctx context.Context) {
	if p.stateDir == "" {
		return
	}
	pendingPath := filepath.Join(p.stateDir, pendingInputFileName)
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		return
	}
	var state pendingInputState
	if err := json.Unmarshal(data, &state); err != nil {
		_ = os.Remove(pendingPath)
		return
	}

	if !state.Consumed || state.Input == nil {
		_ = os.Remove(pendingPath)
		return
	}

	// The crash happened AFTER the ledger write but BEFORE this file was
	// removed: it was already handed to Inputs() last run. Redelivering it
	// now would run the same instruction a second time.
	if p.alreadyDelivered(state.Input.ID) {
		_ = os.Remove(pendingPath)
		return
	}

	ri, reason := p.validateRemoteInput(ctx, state.Input)
	if reason != "" {
		if p.onRejected != nil {
			p.onRejected(state.Input.ID, state.Input.SessionID, reason)
		}
		_ = os.Remove(pendingPath)
		return
	}
	ri.Received = time.Now()

	select {
	case p.inputCh <- ri:
		p.recordDelivered(ri.ID)
	default:
		// See this method's doc comment: unreachable on a fresh channel.
		// Left undelivered on the off chance it is ever reached, the record
		// must survive for the next attempt rather than being discarded.
		return
	}
	_ = os.Remove(pendingPath)
}
