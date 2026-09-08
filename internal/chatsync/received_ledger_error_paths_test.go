package chatsync

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMarkReceived_EmptyStateDirOrIDIsANoOp pins the early guard.
func TestMarkReceived_EmptyStateDirOrIDIsANoOp(t *testing.T) {
	(&InputPoller{}).MarkReceived("inp-1")
	p := &InputPoller{stateDir: t.TempDir()}
	p.MarkReceived("")
	if got := p.readReceivedIDs(); len(got) != 0 {
		t.Errorf("readReceivedIDs after a no-op mark = %v, want empty", got)
	}
}

// TestMarkReceived_TrimsToMaxReceivedIDs mirrors the delivered-ledger's own
// bounded-tail contract for the received ledger.
func TestMarkReceived_TrimsToMaxReceivedIDs(t *testing.T) {
	p := &InputPoller{stateDir: t.TempDir()}
	for i := 0; i < maxReceivedIDs+1; i++ {
		p.MarkReceived("inp-" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if got := len(p.readReceivedIDs()); got != maxReceivedIDs {
		t.Fatalf("ledger length = %d, want %d (trimmed)", got, maxReceivedIDs)
	}
}

// TestReadReceivedIDs_MalformedFileReturnsNil mirrors readDeliveredIDs'
// own parse guard.
func TestReadReceivedIDs_MalformedFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, receivedIDsFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (&InputPoller{stateDir: dir}).readReceivedIDs(); got != nil {
		t.Errorf("readReceivedIDs on a malformed file = %v, want nil", got)
	}
}
