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

// NewInputPoller creates a new InputPoller.
func NewInputPoller(client *Client, sessionID string, waitSeconds int, stateDir ...string) *InputPoller {
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
		client:      client,
		sessionID:   sessionID,
		waitSeconds: waitSeconds,
		stateDir:    dir,
		inputCh:     make(chan RemoteInput, 16),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
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

	p.recoverPendingInput()
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

	consumed, err := p.client.ConsumeInput(consumeCtx, p.sessionID, raw.ID)
	if err != nil {
		return
	}

	_ = p.writePendingInput(consumed, true)

	ri := RemoteInput{
		ID:        consumed.ID,
		SessionID: consumed.SessionID,
		Kind:      consumed.Kind,
		Body:      consumed.Body,
		Received:  time.Now(),
	}

	select {
	case p.inputCh <- ri:
	case <-ctx.Done():
	case <-p.stopCh:
	}

	p.clearPendingInput()
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

	tmpPath := filepath.Join(p.stateDir, pendingInputFileName+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp pending input file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write tmp pending input file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync tmp pending input file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp pending input file: %w", err)
	}

	finalPath := filepath.Join(p.stateDir, pendingInputFileName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename pending input file: %w", err)
	}
	return nil
}

func (p *InputPoller) clearPendingInput() {
	if p.stateDir == "" {
		return
	}
	_ = os.Remove(filepath.Join(p.stateDir, pendingInputFileName))
}

func (p *InputPoller) recoverPendingInput() {
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

	if state.Consumed && state.Input != nil {
		ri := RemoteInput{
			ID:        state.Input.ID,
			SessionID: state.Input.SessionID,
			Kind:      state.Input.Kind,
			Body:      state.Input.Body,
			Received:  time.Now(),
		}
		select {
		case p.inputCh <- ri:
		default:
		}
	}
	_ = os.Remove(pendingPath)
}
