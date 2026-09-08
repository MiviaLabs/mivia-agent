package chatsync

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// receivedIDsFileName persists the bounded tail of input ids that have
// reached UI custody - NOT the same checkpoint as deliveredIDsFileName.
// The delivered-ids ledger (delivered_ledger.go) guards "placed on
// Inputs()" for crash-recovery replay; this ledger is a separate, LATER
// checkpoint recording that some downstream consumer actually took
// possession of the instruction (enqueued it, buffered it while a session
// mounts, or attempted to hand it to Send), regardless of whether that
// attempt ultimately succeeds. See ports.RemoteInputEvent.AckReceived's doc
// comment for the full custody-not-execution scope this ledger backs.
const receivedIDsFileName = "received_input_ids.json"

// maxReceivedIDs bounds the ledger, mirroring maxDeliveredIDs's reasoning: a
// small tail is generous for the single input actually in flight at a time.
const maxReceivedIDs = 50

// MarkReceived records that id reached UI custody. Idempotent: a second
// call for the same id is a no-op, since AckReceived may be invoked more
// than once by design (see its doc comment). Uses the same synchronous,
// fsync-before-return durable write delivered_ledger.go uses
// (writeFileDurably) rather than an async/best-effort one, because the
// guarantee this ledger is FOR - "this id definitely reached custody, even
// across a crash" - requires it. Callers must never invoke this from the
// bubbletea Update goroutine directly; internal/ui/screen/conversation's
// ackCmd wraps it in a tea.Cmd instead.
//
// Serialized on p.mu: internal/ui/screen/conversation's mount.go batches
// one ackCmd per buffered remote-input event for the SAME session into one
// tea.Batch, and bubbletea's runtime executes every Cmd in a batch
// concurrently (execBatchMsg). Without a lock, two concurrent calls on this
// same poller instance can both read the ledger before either write lands,
// and the loser's write silently drops the id the winner already recorded
// - see TestInputPoller_MarkReceived_ConcurrentCallsDoNotLoseIDs.
func (p *InputPoller) MarkReceived(id string) {
	if p.stateDir == "" || id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := p.readReceivedIDs()
	for _, existing := range ids {
		if existing == id {
			return
		}
	}
	ids = append(ids, id)
	if len(ids) > maxReceivedIDs {
		ids = ids[len(ids)-maxReceivedIDs:]
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return
	}
	_ = writeFileDurably(p.stateDir, receivedIDsFileName, data)
}

func (p *InputPoller) readReceivedIDs() []string {
	data, err := os.ReadFile(filepath.Join(p.stateDir, receivedIDsFileName))
	if err != nil {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil
	}
	return ids
}
