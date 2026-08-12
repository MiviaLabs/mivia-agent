package chat

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestManualAutosaveCannotOverwriteCompletedTurnSnapshot locks the defect
// where a benign host autosave whose transcript was captured before a turn
// completed overwrote the completed turn's rolling crash-recovery snapshot.
// OperationToken.sameFence ignores IdempotencyKey, so a TUI periodic save
// token ("manual-save") and a turn token ("turn:1") at the same
// epoch/revision/binding/turnID share a fence; saveAfterTurnWithToken gated
// only on tokenStale before writing, so a pre-turn manual save landing after
// the turn's save (in the session path, the window between the manager write
// and markDurableRevision; on an unwired manager, unconditionally) regressed
// the snapshot. The manager must refuse the same-fence non-turn write with
// ErrStaleAutosave and leave the snapshot at the newest completed turn.
func TestManualAutosaveCannotOverwriteCompletedTurnSnapshot(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	binding := BindingFence{ProviderName: "test-provider", Model: "test-model", ModelGeneration: 1}
	fence := contextstate.Revision{Session: 1, Durable: 1}

	turnToken := OperationToken{Epoch: 1, Revision: fence, Binding: binding, TurnID: 1, IdempotencyKey: "turn:1"}
	turnMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "completed question"},
		{Role: provider.RoleAssistant, Content: "completed answer"},
	}
	if err := mgr.SaveAfterTurnWithRevision(turnMsgs, turnToken); err != nil {
		t.Fatalf("turn save: %v", err)
	}

	// A benign autosave at the identical fence carrying a transcript captured
	// before the turn completed (the assistant reply is missing).
	manualToken := OperationToken{Epoch: 1, Revision: fence, Binding: binding, TurnID: 1, IdempotencyKey: "manual-save"}
	preTurnMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "completed question"},
	}
	if err := mgr.SaveAfterTurnWithRevision(preTurnMsgs, manualToken); !errors.Is(err, ErrStaleAutosave) {
		t.Fatalf("manual autosave error = %v, want ErrStaleAutosave", err)
	}

	// The rolling snapshot still holds the completed turn's transcript.
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(infos))
	}
	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[1].Content != "completed answer" {
		t.Fatalf("manual autosave regressed the completed-turn snapshot: %+v", loaded)
	}
}

// TestTurnSaveMayFollowSameFenceManualAutosave pins the positive ordering: a
// benign autosave may land first, and the turn save that follows at the same
// fence is still allowed (turn-lifecycle writes are never blocked by the
// manager gate; their staleness is enforced by registerToken/tokenStale). The
// snapshot must end up owning the completed turn's transcript.
func TestTurnSaveMayFollowSameFenceManualAutosave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	binding := BindingFence{ProviderName: "test-provider", Model: "test-model", ModelGeneration: 1}
	fence := contextstate.Revision{Session: 1, Durable: 1}

	manualToken := OperationToken{Epoch: 1, Revision: fence, Binding: binding, TurnID: 1, IdempotencyKey: "manual-save"}
	preTurnMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "completed question"},
	}
	if err := mgr.SaveAfterTurnWithRevision(preTurnMsgs, manualToken); err != nil {
		t.Fatalf("manual autosave: %v", err)
	}

	turnToken := OperationToken{Epoch: 1, Revision: fence, Binding: binding, TurnID: 1, IdempotencyKey: "turn:1"}
	turnMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "completed question"},
		{Role: provider.RoleAssistant, Content: "completed answer"},
	}
	if err := mgr.SaveAfterTurnWithRevision(turnMsgs, turnToken); err != nil {
		t.Fatalf("turn save after manual autosave: %v", err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(infos))
	}
	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[1].Content != "completed answer" {
		t.Fatalf("turn snapshot lost the completed turn: %+v", loaded)
	}
}

// TestNewerFenceManualAutosaveMayRefreshAfterTurnSnapshot pins that the gate
// does not over-block: a benign autosave captured under a strictly newer
// fence (a later epoch/revision/turn) may overwrite an older turn snapshot.
// The rolling snapshot must end up holding the newer transcript.
func TestNewerFenceManualAutosaveMayRefreshAfterTurnSnapshot(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	binding := BindingFence{ProviderName: "test-provider", Model: "test-model", ModelGeneration: 1}

	olderTurnToken := OperationToken{Epoch: 1, Revision: contextstate.Revision{Session: 1, Durable: 1}, Binding: binding, TurnID: 1, IdempotencyKey: "turn:1"}
	olderTurnMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "older question"},
		{Role: provider.RoleAssistant, Content: "older answer"},
	}
	if err := mgr.SaveAfterTurnWithRevision(olderTurnMsgs, olderTurnToken); err != nil {
		t.Fatalf("older turn save: %v", err)
	}

	newerManualToken := OperationToken{Epoch: 2, Revision: contextstate.Revision{Session: 2, Durable: 2}, Binding: binding, TurnID: 2, IdempotencyKey: "manual-save"}
	newerMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "newer question"},
		{Role: provider.RoleAssistant, Content: "newer answer"},
	}
	if err := mgr.SaveAfterTurnWithRevision(newerMsgs, newerManualToken); err != nil {
		t.Fatalf("newer-fence manual autosave was blocked: %v", err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(infos))
	}
	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Content != "newer question" {
		t.Fatalf("snapshot does not hold the newer transcript: %+v", loaded)
	}
}
