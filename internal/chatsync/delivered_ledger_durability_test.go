package chatsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRecordDelivered_FailedWriteDoesNotCorruptExistingLedger pins the
// durability fix: recordDelivered must write through a temp file and rename,
// like writePendingInput already does, not a bare os.WriteFile straight to
// the final path. A bare os.WriteFile truncates the destination before
// writing it back; if the write is interrupted (disk full, process killed),
// the file at the final path is left corrupt or empty - losing PREVIOUSLY
// durable ledger entries recoverPendingInput depends on to avoid redelivering
// an instruction across a crash. The temp+rename shape never touches the
// final path until the new content is completely written and fsynced, so an
// interrupted write leaves the old, still-valid ledger in place.
func TestRecordDelivered_FailedWriteDoesNotCorruptExistingLedger(t *testing.T) {
	stateDir := t.TempDir()

	seed, err := json.Marshal([]string{"inp-already-durable"})
	if err != nil {
		t.Fatalf("marshal seed ledger: %v", err)
	}
	ledgerPath := filepath.Join(stateDir, deliveredIDsFileName)
	if err := os.WriteFile(ledgerPath, seed, 0o600); err != nil {
		t.Fatalf("seed ledger file: %v", err)
	}

	poller := &InputPoller{stateDir: stateDir}

	// Make the tmp file recordDelivered must write to unwritable, forcing
	// the write to fail partway through - without ever touching the final
	// ledger path if the implementation is atomic.
	tmpPath := ledgerPath + ".tmp"
	if err := os.Mkdir(tmpPath, 0o500); err != nil {
		t.Fatalf("pre-create tmp path as a directory to force the write to fail: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpPath) })

	poller.recordDelivered("inp-new")

	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("ledger file unreadable after a failed write: %v", err)
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		t.Fatalf("ledger file corrupted after a failed write: %v (content: %q)", err, data)
	}
	if len(ids) != 1 || ids[0] != "inp-already-durable" {
		t.Errorf("ledger content after a failed write = %v, want the untouched seed [inp-already-durable]", ids)
	}
}
