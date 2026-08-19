package demoharness

import "github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"

// turnHandle is the ports.TurnHandle for one Send call.
type turnHandle struct {
	id     string
	events <-chan uievent.Event
	cancel func()
}

func (h *turnHandle) ID() string                   { return h.id }
func (h *turnHandle) Events() <-chan uievent.Event { return h.events }
func (h *turnHandle) Cancel()                      { h.cancel() }
