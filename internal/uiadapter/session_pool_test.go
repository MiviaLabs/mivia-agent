package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func TestSessionPool_GetOrCreateInitial(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "session-1"

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	conv, err := pool.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conv == nil || conv.ID() != "session-1" {
		t.Errorf("got conversation ID %v, want session-1", conv.ID())
	}
}

func TestSessionPool_GetOrCreateLoadsPersistedSession(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{Model: "test-model"}

	sess1 := chat.NewSession(res, nil)
	sess1.SessionDir = dir
	sess1.SessionID = "sess-alpha"
	sess1.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "Hello from Alpha"},
	}
	if err := sess1.Save("sess-alpha"); err != nil {
		t.Fatalf("saving sess1: %v", err)
	}

	// Create sess2 in persistence
	sess2 := chat.NewSession(res, nil)
	sess2.SessionDir = dir
	sess2.SessionID = "sess-beta"
	sess2.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "Hello from Beta"},
	}
	if err := sess2.Save("sess-beta"); err != nil {
		t.Fatalf("saving sess2: %v", err)
	}

	// Create pool initialized with sess1
	pool := uiadapter.NewSessionPool(sess1, res, nil, false)

	// Fetch sess-beta from pool
	convBeta, err := pool.GetOrCreate("sess-beta")
	if err != nil {
		t.Fatalf("GetOrCreate sess-beta failed: %v", err)
	}
	if len(convBeta.History()) == 0 || convBeta.History()[0].Text != "Hello from Beta" {
		t.Errorf("convBeta history mismatch: got %+v", convBeta.History())
	}

	// Verify sess1 was not mutated or replaced
	if sess1.SessionID != "sess-alpha" {
		t.Errorf("sess1 mutated in place: SessionID = %q, want sess-alpha", sess1.SessionID)
	}
	convAlpha, err := pool.GetOrCreate("sess-alpha")
	if err != nil {
		t.Fatalf("GetOrCreate sess-alpha failed: %v", err)
	}
	if convAlpha.ID() != "sess-alpha" {
		t.Errorf("convAlpha.ID() = %q, want sess-alpha", convAlpha.ID())
	}
}

func TestSessionPool_NilConfigReturnsError(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	_, err := pool.GetOrCreate("nonexistent")
	if err == nil {
		t.Fatal("expected error on GetOrCreate with nil config")
	}
}

func TestSessionPool_InheritsStore(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{Model: "test-model"}
	store, err := chat.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	sess1 := chat.NewSession(res, nil)
	sess1.SetSessionStore(store, nil)
	sess1.SessionID = "stored-1"
	if err := sess1.Save("stored-1"); err != nil {
		t.Fatal(err)
	}

	pool := uiadapter.NewSessionPool(sess1, res, nil, false)
	conv, err := pool.GetOrCreate("stored-1")
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	if conv.ID() != "stored-1" {
		t.Errorf("got ID %q, want stored-1", conv.ID())
	}
}

func TestSessionPool_GetOrCreateWithModelCatalog(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{
		ProviderName: "test-provider",
		Model:        "test-model",
		Models:       []string{"test-model"},
	}
	sess1 := chat.NewSession(res, nil)
	sess1.SessionDir = dir
	sess1.SessionID = "sess-catalog-1"
	sess1.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "Hello with catalog"},
	}
	if err := sess1.Save("sess-catalog-1"); err != nil {
		t.Fatalf("saving sess1: %v", err)
	}

	pool := uiadapter.NewSessionPool(sess1, res, nil, false)
	conv, err := pool.GetOrCreate("sess-catalog-1")
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	if conv == nil || conv.ID() != "sess-catalog-1" {
		t.Errorf("got conversation ID %v, want sess-catalog-1", conv.ID())
	}
}
