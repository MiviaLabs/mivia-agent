package delivery

// Transport-fault classification for delivery attempts. provider.IsTransient
// covers model-provider transport faults and its phrase list stays in that
// domain; git and gh die with their own texts, so the delivery settle paths
// (CLI settleDeliveryError, engine routeDeliveryRepair) need this classifier
// composed alongside it. A transport fault is not a condition in the change:
// no repair agent is dispatched and no failure record is written - the run
// stays delivery_pending and a later deliver succeeds once the network is
// back. Deadline and cancellation errors are deliberately NOT transport
// faults here; the attempt-bound guards own those.

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

// transportFaultPhrases name how a git or gh network call died, and nothing
// else. The list stays short on purpose: matching text is weaker than
// matching a type, so every phrase must be unambiguous (compare
// provider/transient.go's discipline). In particular there is NO bare
// "timed out" entry - a slow hook or an evidence command that timed out is a
// condition in the environment, not a network death.
var transportFaultPhrases = []string{
	"could not resolve host",   // git: DNS resolution failed
	"connection timed out",     // git: TCP connect timeout
	"connection refused",       // git: nothing listening on the port
	"connection reset by peer", // git: peer dropped the connection
	"network is unreachable",   // git: no route to the host
	"failed to connect",        // gh: connect failure
}

// IsTransportFault reports whether err is a git/gh transport fault: a
// wrapped net/syscall error of the connection-death kinds, or an error whose
// text carries one of the transport fault phrases. It never matches a bare
// context deadline or cancellation.
func IsTransportFault(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, phrase := range transportFaultPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}
