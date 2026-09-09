// remote_targeted_cancel_validate_test.go pins the validator boundary for
// the two targeted cancel kinds ("cancel_task" and "cancel_tool_call").
// They reuse the existing Body field to carry their target id, so unlike
// the untargeted "cancel" they REQUIRE a non-empty body - and they get no
// exemption from any other check the trust boundary applies.
package chatsync

import (
	"context"
	"strings"
	"testing"
	"time"
)

// assertRemoteInputDelivered drives one SessionInput through the poller and
// requires it to arrive on Inputs() with the kind and body it was sent
// with. The counterpart of assertNeverDelivered.
func assertRemoteInputDelivered(t *testing.T, sessionID string, in SessionInput, author string) {
	t.Helper()
	poller, _ := newRejectionPoller(t, sessionID, in, author)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())

	select {
	case ri := <-poller.Inputs():
		if ri.Kind != in.Kind || ri.Body != in.Body {
			t.Errorf("received input = %+v, want kind %q body %q", ri, in.Kind, in.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for a %q input to be delivered", in.Kind)
	}
}

// assertRemoteInputRefused drives one SessionInput through the poller and
// requires it to be refused with a reason containing wantReason.
func assertRemoteInputRefused(t *testing.T, sessionID string, in SessionInput, author, wantReason string) {
	t.Helper()
	poller, rejections := newRejectionPoller(t, sessionID, in, author)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, wantReason)
}

// TestInputPoller_AcceptsCancelTaskWithBody proves "cancel_task" is on the
// allowlist and passes validation when it carries its target row id.
func TestInputPoller_AcceptsCancelTaskWithBody(t *testing.T) {
	assertRemoteInputDelivered(t, "sess-1", SessionInput{
		ID: "inp-ct-1", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_task", Body: "call-1:task-a",
	}, "user-1")
}

// TestInputPoller_AcceptsCancelToolCallWithBody proves "cancel_tool_call"
// is on the allowlist with a one-part body (a MAIN-turn tool call id).
func TestInputPoller_AcceptsCancelToolCallWithBody(t *testing.T) {
	assertRemoteInputDelivered(t, "sess-1", SessionInput{
		ID: "inp-ctc-1", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_tool_call", Body: "tc-9",
	}, "user-1")
}

// TestInputPoller_AcceptsCancelToolCallWithTwoPartBody proves the
// space-separated two-part body (a subagent row id and a tool call id)
// survives validation intact - the space is not a control character and the
// body is not reshaped on the way through.
func TestInputPoller_AcceptsCancelToolCallWithTwoPartBody(t *testing.T) {
	assertRemoteInputDelivered(t, "sess-1", SessionInput{
		ID: "inp-ctc-2", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	}, "user-1")
}

// TestInputPoller_RejectsCancelTaskWithEmptyBody is the point of this file:
// adding kinds to the allowlist must NOT widen the empty-body exemption,
// which belongs to "cancel" alone. A cancel_task with no id names nothing.
func TestInputPoller_RejectsCancelTaskWithEmptyBody(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-ct-2", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_task", Body: "",
	}, "user-1", "empty body")
}

// TestInputPoller_RejectsCancelToolCallWithEmptyBody is the same proof for
// the other targeted kind.
func TestInputPoller_RejectsCancelToolCallWithEmptyBody(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-ctc-3", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_tool_call", Body: "",
	}, "user-1", "empty body")
}

// TestInputPoller_CancelTaskStillEnforcesSessionIDMatch proves the new kind
// gets no exemption from session ownership.
func TestInputPoller_CancelTaskStillEnforcesSessionIDMatch(t *testing.T) {
	assertRemoteInputRefused(t, "sess-mine", SessionInput{
		ID: "inp-ct-3", SessionID: "sess-other", AuthorUserID: "user-1",
		Kind: "cancel_task", Body: "call-1:task-a",
	}, "user-1", "session id mismatch")
}

// TestInputPoller_CancelToolCallStillEnforcesSessionIDMatch is the same for
// the finer kind.
func TestInputPoller_CancelToolCallStillEnforcesSessionIDMatch(t *testing.T) {
	assertRemoteInputRefused(t, "sess-mine", SessionInput{
		ID: "inp-ctc-4", SessionID: "sess-other", AuthorUserID: "user-1",
		Kind: "cancel_tool_call", Body: "tc-9",
	}, "user-1", "session id mismatch")
}

// TestInputPoller_CancelTaskStillEnforcesAuthorMatch proves a targeted
// cancel from anyone but the CLI's own verified principal is refused.
func TestInputPoller_CancelTaskStillEnforcesAuthorMatch(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-ct-4", SessionID: "sess-1", AuthorUserID: "attacker",
		Kind: "cancel_task", Body: "call-1:task-a",
	}, "user-1", "does not match")
}

// TestInputPoller_CancelToolCallStillEnforcesAuthorMatch is the same for
// the finer kind.
func TestInputPoller_CancelToolCallStillEnforcesAuthorMatch(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-ctc-5", SessionID: "sess-1", AuthorUserID: "attacker",
		Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	}, "user-1", "does not match")
}

// TestInputPoller_CancelToolCallStillEnforcesBodyCap proves the 8192-byte
// cap still applies: a targeted kind is not a way past it.
func TestInputPoller_CancelToolCallStillEnforcesBodyCap(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-ctc-6", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_tool_call", Body: strings.Repeat("a", maxRemoteInputBodyBytes+1),
	}, "user-1", "exceeds")
}

// TestInputPoller_CancelTaskStillEnforcesControlCharScan proves the
// control-character scan still applies to a targeted cancel's id.
func TestInputPoller_CancelTaskStillEnforcesControlCharScan(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-ct-5", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_task", Body: "call-1\x00task-a",
	}, "user-1", "control character")
}

// TestInputPoller_RejectsUnknownCancelKind pins the allowlist boundary
// after the addition: a plausible-looking sibling kind is still refused.
func TestInputPoller_RejectsUnknownCancelKind(t *testing.T) {
	assertRemoteInputRefused(t, "sess-1", SessionInput{
		ID: "inp-x-1", SessionID: "sess-1", AuthorUserID: "user-1",
		Kind: "cancel_turn", Body: "anything",
	}, "user-1", "unsupported kind")
}
