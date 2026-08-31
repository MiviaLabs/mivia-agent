package chatsync

import (
	"context"
	"sync"
	"time"
)

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
	inputCh     chan RemoteInput
	stopCh      chan struct{}
	doneCh      chan struct{}
	mu          sync.Mutex
	running     bool
}

const (
	defaultPollWaitSeconds = 25
	maxPollWaitSeconds     = 300
)

// NewInputPoller creates a new InputPoller.
func NewInputPoller(client *Client, sessionID string, waitSeconds int) *InputPoller {
	if waitSeconds <= 0 {
		waitSeconds = defaultPollWaitSeconds
	} else if waitSeconds > maxPollWaitSeconds {
		waitSeconds = maxPollWaitSeconds
	}
	return &InputPoller{
		client:      client,
		sessionID:   sessionID,
		waitSeconds: waitSeconds,
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

	go p.loop(ctx)
}

// Stop terminates the poller and waits for loop exit.
func (p *InputPoller) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	p.mu.Unlock()

	close(p.stopCh)
	<-p.doneCh
}

func (p *InputPoller) loop(ctx context.Context) {
	defer close(p.doneCh)

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
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(p.waitSeconds+5)*time.Second)
	defer cancel()

	next, err := p.client.NextInput(pollCtx, p.sessionID, p.waitSeconds)
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
	consumeCtx, cancelConsume := context.WithTimeout(ctx, 5*time.Second)
	defer cancelConsume()

	consumed, err := p.client.ConsumeInput(consumeCtx, p.sessionID, raw.ID)
	if err != nil {
		return
	}

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
}
