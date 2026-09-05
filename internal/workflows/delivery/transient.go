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

// permanentMergePhrases identify gh merge errors that will never resolve by
// retrying: the PR is in a state that no amount of polling can fix.
var permanentMergePhrases = []string{
	"pull request is closed", // PR was closed, not merged
	"pull request not found", // PR or repo deleted
	"not authorized",         // token revoked or permission lost
	"permission denied",      // insufficient repo permissions
	"branch not found",       // branch was deleted out-of-band
	"merge conflict",         // content conflict (may be fixable but needs human action)
	"required status check",  // branch protection blocking (misconfigured or stuck)
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

// IsPermanentMergeError reports whether err from MergePullRequest represents
// a failure that retrying will never fix: the PR is closed, auth is broken,
// a branch was deleted, or a merge conflict exists. Retriable conditions
// (pending CI, review requirements, transient gh errors) return false so
// the caller can keep polling.
func IsPermanentMergeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, phrase := range permanentMergePhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// remotePushRejectionPhrases identify a push that ORIGIN refused, as opposed
// to one the repository's own pre-push hook declined. The two need different
// repair advice: a hook rejection is about the delivered tree and the agent
// can fix it in the worktree, while a remote rejection is about the state of
// the remote ref or the credential and no worktree edit reaches it.
var remotePushRejectionPhrases = []string{
	"non-fast-forward",                       // remote ref moved since the base was admitted
	"[rejected]",                             // git's own rejection marker
	"[remote rejected]",                      // a server-side hook declined
	"fetch first",                            // git's advice for a stale ref
	"permission denied",                      // credential lacks push rights
	"authentication failed",                  // credential rejected
	"could not read from remote repository",  // unreachable or unauthorized
	"repository not found",                   // remote deleted or renamed
	"does not appear to be a git repository", // remote URL is wrong
}

// isRemotePushRejection reports whether err is a push failure that origin (or
// its server-side hooks) refused, rather than a local pre-push hook decline.
func isRemotePushRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, phrase := range remotePushRejectionPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}
