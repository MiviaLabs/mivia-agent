package chat

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestLoadContextCatalogCorruptPayloadDoesNotMutateSessionOrReclaim verifies that
// a corrupted message payload fails decode BEFORE reclaiming ownership or mutating
// in-memory session identity.
func TestLoadContextCatalogCorruptPayloadDoesNotMutateSessionOrReclaim(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "resume_corrupt.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Session 1: commits turn
	sess1 := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "hi from sess1"})
	if _, err := sess1.SendUser(context.Background(), "hello from session 1", io.Discard); err != nil {
		t.Fatal(err)
	}
	sess1Principal := sess1.ContextPrincipal()

	// Corrupt the checkpoint payload in context_checkpoints table
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE context_checkpoints SET active_context = X'FFFEFDFCFB' WHERE session_id=?`, sess1.SessionID); err != nil {
		t.Fatal(err)
	}

	// Session 2: separate session with its own messages
	sess2 := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "hi from sess2"})
	if _, err := sess2.SendUser(context.Background(), "hello from session 2", io.Discard); err != nil {
		t.Fatal(err)
	}
	sess2OrigID := sess2.SessionID
	sess2OrigPrincipal := sess2.ContextPrincipal()
	sess2OrigMsgs := sess2.MessagesCopy()

	// Session 2 attempts to load Session 1 (which has corrupt data)
	err = sess2.Load(sess1.SessionID)
	if err == nil {
		t.Fatal("expected Load to fail on corrupted message payload, but it succeeded")
	}

	// Verify Session 2 in-memory state remains untouched
	if sess2.SessionID != sess2OrigID {
		t.Fatalf("sess2.SessionID was mutated to %q, want original %q", sess2.SessionID, sess2OrigID)
	}
	if sess2.ContextPrincipal().CapabilityDigest() != sess2OrigPrincipal.CapabilityDigest() {
		t.Fatal("sess2.contextPrincipal was mutated on failed load")
	}
	if len(sess2.MessagesCopy()) != len(sess2OrigMsgs) {
		t.Fatalf("sess2.Messages was corrupted: len = %d, want %d", len(sess2.MessagesCopy()), len(sess2OrigMsgs))
	}

	// Verify Session 1's capability digest in store was NOT stolen/overwritten by Session 2
	var capDigest string
	if err := db.QueryRow(`SELECT capability_digest FROM context_sessions WHERE session_id=?`, sess1.SessionID).Scan(&capDigest); err != nil {
		t.Fatalf("query session 1 capability digest: %v", err)
	}
	if capDigest != sess1Principal.CapabilityDigest() {
		t.Fatalf("session 1 capability digest was stolen: got %q, want %q", capDigest, sess1Principal.CapabilityDigest())
	}
}

// TestLoadContextCatalogInvalidBindingFactoryDoesNotMutateSessionOrReclaim verifies that
// a model binding failure fails closed BEFORE reclaiming ownership or mutating identity.
func TestLoadContextCatalogInvalidBindingFactoryDoesNotMutateSessionOrReclaim(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "resume_binding.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Session 1: commits turn under a custom model
	sess1 := wireCatalogSession(t, store, &config.Resolved{ProviderName: "custom-provider", Model: "custom-model"}, &fakeCompleter{out: "hi from sess1"})
	if _, err := sess1.SendUser(context.Background(), "hello from session 1", io.Discard); err != nil {
		t.Fatal(err)
	}
	sess1Principal := sess1.ContextPrincipal()

	// Session 2: fresh session whose binding factory explicitly rejects "custom-provider"
	sess2 := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "hi from sess2"})
	if _, err := sess2.SendUser(context.Background(), "hello from session 2", io.Discard); err != nil {
		t.Fatal(err)
	}
	sess2.SetBindingFactory(func(providerName, model string) (ModelBinding, error) {
		if providerName == "custom-provider" {
			return ModelBinding{}, errors.New("unsupported provider")
		}
		return catalogBindingFactory()(providerName, model)
	})

	sess2OrigID := sess2.SessionID
	sess2OrigPrincipal := sess2.ContextPrincipal()
	sess2OrigMsgs := sess2.MessagesCopy()

	// Session 2 attempts to load Session 1 (which requires custom-provider)
	err = sess2.Load(sess1.SessionID)
	if err == nil {
		t.Fatal("expected Load to fail on unsupported provider binding, but it succeeded")
	}

	// Verify Session 2 in-memory state remains untouched
	if sess2.SessionID != sess2OrigID {
		t.Fatalf("sess2.SessionID was mutated to %q, want original %q", sess2.SessionID, sess2OrigID)
	}
	if sess2.ContextPrincipal().CapabilityDigest() != sess2OrigPrincipal.CapabilityDigest() {
		t.Fatal("sess2.contextPrincipal was mutated on failed load")
	}
	if len(sess2.MessagesCopy()) != len(sess2OrigMsgs) {
		t.Fatalf("sess2.Messages was corrupted: len = %d, want %d", len(sess2.MessagesCopy()), len(sess2OrigMsgs))
	}

	// Verify Session 1's capability digest in store was NOT stolen/overwritten by Session 2
	snap1, err := store.Load(context.Background(), sess1Principal, sess1.SessionID)
	if err != nil {
		t.Fatalf("sess1 snapshot could not be read using sess1's principal: %v (capability stolen!)", err)
	}
	if snap1.Revision.Session == 0 {
		t.Fatal("sess1 head revision is invalid")
	}
}
