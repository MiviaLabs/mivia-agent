package chatsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// deliveredIDsFileName persists the bounded tail of input ids this poller has
// confirmed delivered (placed on Inputs()). It exists to close the crash
// window pollOnce's delivery select and clearPendingInput leave open: if the
// process dies after a successful send but before clearPendingInput removes
// pending_input.json, a naive restart would recover that file and redeliver
// an instruction the UI (very likely) already ran once. Checking this ledger
// first tells recovery "this one already went out" instead.
const deliveredIDsFileName = "delivered_input_ids.json"

// maxDeliveredIDs bounds the ledger. Only the crash-recovery race above ever
// needs it, and that race can only ever concern the single input actually in
// flight at any moment, so a small tail is generous, not tight.
const maxDeliveredIDs = 50

// recordDelivered appends id to the bounded, persisted delivered-ids ledger.
// A no-op when no stateDir is configured, matching writePendingInput's own
// contract. Called AFTER a successful send on Inputs() and BEFORE
// clearPendingInput, so the ledger entry exists for any crash that happens
// in between.
//
// Written with the same open-tmp/write/fsync/close/rename sequence
// writePendingInput uses (poller.go), not a bare os.WriteFile: a crash
// mid-write to the final path would truncate or corrupt whatever ledger
// content was already durable, which is exactly the state
// recoverPendingInput's alreadyDelivered check depends on to avoid
// redelivering an instruction across a restart. Writing through a temp file
// and renaming means a crash before the rename leaves the PREVIOUS durable
// ledger intact rather than a half-written one.
func (p *InputPoller) recordDelivered(id string) {
	if p.stateDir == "" || id == "" {
		return
	}
	ids := p.readDeliveredIDs()
	ids = append(ids, id)
	if len(ids) > maxDeliveredIDs {
		ids = ids[len(ids)-maxDeliveredIDs:]
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return
	}
	_ = writeFileDurably(p.stateDir, deliveredIDsFileName, data)
}

// writeFileDurably writes data to name under dir via a temp file that is
// fsynced and renamed into place, so a crash mid-write leaves either the
// old complete file or nothing at the final path - never a truncated or
// corrupt one. Mirrors poller.go's writePendingInput, factored out so
// recordDelivered uses the identical durable-write shape instead of a bare
// os.WriteFile.
func writeFileDurably(dir, name string, data []byte) error {
	tmpPath := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write tmp %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync tmp %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp %s: %w", name, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("rename %s: %w", name, err)
	}
	return nil
}

// alreadyDelivered reports whether id is in the persisted delivered-ids
// ledger.
func (p *InputPoller) alreadyDelivered(id string) bool {
	if p.stateDir == "" || id == "" {
		return false
	}
	for _, d := range p.readDeliveredIDs() {
		if d == id {
			return true
		}
	}
	return false
}

func (p *InputPoller) readDeliveredIDs() []string {
	data, err := os.ReadFile(filepath.Join(p.stateDir, deliveredIDsFileName))
	if err != nil {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil
	}
	return ids
}
