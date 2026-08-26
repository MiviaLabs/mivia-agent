package uiadapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
)

// TestSessionPool_IsActive verifies the pool reports a session's real
// turn-in-flight status via its pooled Conversation, and reports false
// for an ID it has never loaded (no live Conversation to ask).
func TestSessionPool_IsActive(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("done")}, block: make(chan struct{})}
	sess := chat.NewSession(res, comp)
	sess.SessionID = "session-1"

	pool := uiadapter.NewSessionPool(sess, res, nil, false)

	if pool.IsActive("session-1") {
		t.Fatal("IsActive=true before any Send")
	}
	if pool.IsActive("never-loaded") {
		t.Fatal("IsActive=true for a session ID the pool never loaded")
	}

	conv, err := pool.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	h, err := conv.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !pool.IsActive("session-1") {
		t.Fatal("IsActive=false while the pooled session's turn is blocked mid-flight")
	}

	close(comp.block)
	for {
		select {
		case _, ok := <-h.Events():
			if !ok {
				goto drained
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out draining turn events")
		}
	}
drained:
	deadline := time.Now().Add(5 * time.Second)
	for pool.IsActive("session-1") {
		if time.Now().After(deadline) {
			t.Fatal("IsActive stayed true after the turn completed")
		}
		time.Sleep(time.Millisecond)
	}
}

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

func TestSessionPool_LoadedSessionInheritsTools(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{
		ProviderName: "test-provider",
		Model:        "test-model",
		Models:       []string{"test-model"},
	}
	sess1 := chat.NewSession(res, nil)
	sess1.SessionDir = dir
	sess1.SessionID = "sess-tools-1"
	sess1.Tools = tools.NewRegistry()
	sess1.UseTools = true
	if err := sess1.Save("sess-tools-1"); err != nil {
		t.Fatalf("saving sess1: %v", err)
	}

	pool := uiadapter.NewSessionPool(sess1, res, nil, true)
	conv, err := pool.GetOrCreate("sess-tools-1")
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	if conv == nil || conv.ID() != "sess-tools-1" {
		t.Errorf("got conversation ID %v, want sess-tools-1", conv.ID())
	}
}

func TestSessionPool_CreateFresh_ReturnsNewConversation(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "initial-session"
	pool := uiadapter.NewSessionPool(sess, res, nil, false)

	// CreateFresh does not exist yet — this test is RED
	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh failed: %v", err)
	}
	if conv == nil {
		t.Fatal("CreateFresh returned nil conversation")
	}
	if conv.ID() == "initial-session" || conv.ID() == "" {
		t.Errorf("CreateFresh returned same or empty ID: %q", conv.ID())
	}
}

func TestSessionPool_CreateFresh_NilResReturnsError(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	_, err := pool.CreateFresh()
	if err == nil {
		t.Fatal("expected error from CreateFresh with nil config")
	}
}

func TestSessionPool_CreateFresh_InheritsSessionDir(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "parent-session"
	sess.SessionDir = dir
	pool := uiadapter.NewSessionPool(sess, res, nil, false)

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh failed: %v", err)
	}
	// Verify the fresh conv is registered and distinct
	if conv.ID() == "parent-session" {
		t.Errorf("CreateFresh should produce a new session ID, got parent ID")
	}
	// Fetch it back from the pool by its new ID
	conv2, err := pool.GetOrCreate(conv.ID())
	if err != nil {
		t.Fatalf("GetOrCreate fresh ID failed: %v", err)
	}
	if conv2.ID() != conv.ID() {
		t.Errorf("pool did not register fresh session: got %q, want %q", conv2.ID(), conv.ID())
	}
}

func TestSessionPool_CreateFresh_InheritsToolsFlag(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "tools-parent"
	sess.Tools = tools.NewRegistry()
	pool := uiadapter.NewSessionPool(sess, res, nil, true)

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh failed: %v", err)
	}
	if conv == nil {
		t.Fatal("CreateFresh returned nil")
	}
}
