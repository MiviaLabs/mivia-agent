package chat

import (
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestConcurrentAutosavesDoNotCorruptOrLoseHistory closes a real gap:
// TestSaveManager_Concurrent (save_manager_test.go) drives SaveManager at the
// I/O layer with different session names per goroutine, and the sequential
// tests in manual_autosave_overwrite_test.go pin the fencing gate's ordering
// decisions one call at a time - neither launches real goroutines calling the
// session's own public autosave entry point, Session.SaveAfterTurn(),
// concurrently on the SAME live session with the SAME rolling snapshot name.
// That is the actual shape a host produces: the TUI's periodic save and a
// turn-boundary autosave can both fire for the one open session.
//
// This does not hand-simulate an interleaving; it runs N real goroutines
// under the race detector and then verifies the on-disk result is a whole,
// coherent snapshot - not a torn/interleaved mix of two writes - by
// reloading it via the same store the session used to persist it.
func TestConcurrentAutosavesDoNotCorruptOrLoseHistory(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	sess := &Session{
		model:      "test-model",
		SessionDir: store.Dir(),
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "sys"},
			{Role: provider.RoleUser, Content: "question"},
			{Role: provider.RoleAssistant, Content: "answer"},
		},
	}
	sess.SetSessionStore(store, mgr)

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			sess.SaveAfterTurn()
		}()
	}
	wg.Wait()

	// No fencing rejection is expected here: nothing mutates the session's
	// epoch/revision/binding/turnID between captures, so every concurrent
	// call's token stays current and every write targets the same rolling
	// snapshot name (SaveAfterTurn's own uniqAutoSaveName call is made once,
	// under s.mu, and cached on the manager).
	infos, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("snapshot count = %d, want exactly 1 rolling autosave (got %+v)", len(infos), infos)
	}

	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := historyBlob(sess)
	var got string
	for i, m := range loaded {
		if i > 0 {
			got += "|"
		}
		got += m.Role + ":" + m.Content
	}
	if got != want {
		t.Fatalf("persisted snapshot is not a coherent copy of in-memory history:\n got:  %s\n want: %s", got, want)
	}
	if len(loaded) != 3 || loaded[0].Content != "sys" || loaded[1].Content != "question" || loaded[2].Content != "answer" {
		t.Fatalf("persisted snapshot corrupted or torn: %+v", loaded)
	}
}
