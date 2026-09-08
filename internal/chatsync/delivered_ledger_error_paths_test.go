package chatsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAlreadyDelivered_EmptyStateDirOrIDIsFalse pins the early guard: no
// state dir configured, or an empty id, is never "delivered" rather than
// touching disk to find out.
func TestAlreadyDelivered_EmptyStateDirOrIDIsFalse(t *testing.T) {
	if (&InputPoller{}).alreadyDelivered("inp-1") {
		t.Error("alreadyDelivered with no state dir must be false")
	}
	if (&InputPoller{stateDir: t.TempDir()}).alreadyDelivered("") {
		t.Error("alreadyDelivered with an empty id must be false")
	}
}

// TestReadDeliveredIDs_MalformedFileReturnsNil pins readDeliveredIDs' parse
// guard: a ledger file that is not a JSON string array is treated as absent
// rather than propagating the parse error - recordDelivered/alreadyDelivered
// have no error return to carry it, and a corrupt ledger must not wedge
// either.
func TestReadDeliveredIDs_MalformedFileReturnsNil(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, deliveredIDsFileName)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &InputPoller{stateDir: stateDir}
	if got := p.readDeliveredIDs(); got != nil {
		t.Errorf("readDeliveredIDs on a malformed file = %v, want nil", got)
	}
}

// TestRecordDelivered_TrimsToMaxDeliveredIDs pins the bounded-tail contract:
// once the ledger holds maxDeliveredIDs entries, an older one is dropped
// rather than growing the file forever.
func TestRecordDelivered_TrimsToMaxDeliveredIDs(t *testing.T) {
	stateDir := t.TempDir()
	p := &InputPoller{stateDir: stateDir}
	for i := 0; i < maxDeliveredIDs+1; i++ {
		p.recordDelivered("inp-" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	ids := p.readDeliveredIDs()
	if len(ids) != maxDeliveredIDs {
		t.Fatalf("ledger length = %d, want %d (trimmed)", len(ids), maxDeliveredIDs)
	}
}

// TestWriteFileDurably_RenameErrorSurfaces pins the rename guard directly: a
// directory already occupying the final path makes the rename fail, distinct
// from the existing open-tmp failure test.
func TestWriteFileDurably_RenameErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileDurably(dir, "target", []byte("x")); err == nil {
		t.Fatal("writeFileDurably accepted a target path occupied by a non-empty semantics directory")
	} else if !strings.Contains(err.Error(), "rename") {
		t.Fatalf("err = %v, want the rename wrap", err)
	}
}
