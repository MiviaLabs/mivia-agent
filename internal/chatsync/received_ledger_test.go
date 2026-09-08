package chatsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestInputPoller_MarkReceived_ConcurrentCallsDoNotLoseIDs drives two
// concurrent MarkReceived calls for distinct ids against the SAME poller
// instance - exactly the shape internal/ui/screen/conversation/mount.go's
// handleSessionMountedMsg produces when it batches one ackCmd per buffered
// remote-input event for one background session into a single tea.Batch:
// bubbletea's runtime (charm.land/bubbletea/v2's execBatchMsg) runs every
// Cmd in that batch on its own goroutine concurrently. MarkReceived's
// read-modify-write (readReceivedIDs -> append -> writeFileDurably) has no
// serialization, so two concurrent calls can both read the ledger before
// either write lands, and the loser's write clobbers the winner's -
// silently losing a previously recorded id. This must not happen: the
// ledger's own doc comment promises "this id definitely reached custody,
// even across a crash" for EVERY id MarkReceived was called with.
func TestInputPoller_MarkReceived_ConcurrentCallsDoNotLoseIDs(t *testing.T) {
	stateDir := t.TempDir()
	client := newTestClient(t, ClientOptions{BaseURL: "http://unused.invalid"})
	poller := NewInputPoller(client, "sess-mr-race", 1, fixedAuthorUserIDProvider("user-1"), stateDir)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			poller.MarkReceived(fmt.Sprintf("inp-race-%d", i))
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(stateDir, receivedIDsFileName))
	if err != nil {
		t.Fatalf("read %s: %v", receivedIDsFileName, err)
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		t.Fatalf("decode %s: %v", receivedIDsFileName, err)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	var missing []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("inp-race-%d", i)
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d concurrently-marked ids were lost from the ledger: %v (ledger has %d entries: %v)",
			len(missing), n, missing, len(ids), ids)
	}
}

// TestInputPoller_MarkReceived_Idempotent calls MarkReceived(id) twice and
// asserts the persisted ledger contains id exactly once.
func TestInputPoller_MarkReceived_Idempotent(t *testing.T) {
	stateDir := t.TempDir()
	client := newTestClient(t, ClientOptions{BaseURL: "http://unused.invalid"})
	poller := NewInputPoller(client, "sess-mr-1", 1, fixedAuthorUserIDProvider("user-1"), stateDir)

	poller.MarkReceived("inp-1")
	poller.MarkReceived("inp-1")

	data, err := os.ReadFile(filepath.Join(stateDir, receivedIDsFileName))
	if err != nil {
		t.Fatalf("read %s: %v", receivedIDsFileName, err)
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		t.Fatalf("decode %s: %v", receivedIDsFileName, err)
	}
	count := 0
	for _, id := range ids {
		if id == "inp-1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("received-ids ledger contains inp-1 %d times, want 1 (%v)", count, ids)
	}
}

// TestInputPoller_MarkReceived_DoesNotAffectDeliveredLedger asserts the
// received-ids and delivered-ids ledgers are independent files with
// independent contents.
func TestInputPoller_MarkReceived_DoesNotAffectDeliveredLedger(t *testing.T) {
	stateDir := t.TempDir()
	client := newTestClient(t, ClientOptions{BaseURL: "http://unused.invalid"})
	poller := NewInputPoller(client, "sess-mr-2", 1, fixedAuthorUserIDProvider("user-1"), stateDir)

	poller.recordDelivered("inp-delivered")
	poller.MarkReceived("inp-received")

	deliveredPath := filepath.Join(stateDir, deliveredIDsFileName)
	receivedPath := filepath.Join(stateDir, receivedIDsFileName)

	if deliveredPath == receivedPath {
		t.Fatalf("delivered and received ledger paths must differ")
	}

	deliveredData, err := os.ReadFile(deliveredPath)
	if err != nil {
		t.Fatalf("read %s: %v", deliveredIDsFileName, err)
	}
	var deliveredIDs []string
	if err := json.Unmarshal(deliveredData, &deliveredIDs); err != nil {
		t.Fatalf("decode %s: %v", deliveredIDsFileName, err)
	}
	if len(deliveredIDs) != 1 || deliveredIDs[0] != "inp-delivered" {
		t.Errorf("delivered ledger = %v, want [inp-delivered]", deliveredIDs)
	}

	receivedData, err := os.ReadFile(receivedPath)
	if err != nil {
		t.Fatalf("read %s: %v", receivedIDsFileName, err)
	}
	var receivedIDs []string
	if err := json.Unmarshal(receivedData, &receivedIDs); err != nil {
		t.Fatalf("decode %s: %v", receivedIDsFileName, err)
	}
	if len(receivedIDs) != 1 || receivedIDs[0] != "inp-received" {
		t.Errorf("received ledger = %v, want [inp-received]", receivedIDs)
	}
}
