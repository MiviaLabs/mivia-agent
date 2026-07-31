package chat

import (
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestAutoSaveUsesCurrentModel(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{model: "startup-model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}, {Role: provider.RoleAssistant, Content: "hi"}}, SessionDir: store.Dir()}
	s.SetSessionStore(store, NewSaveManager(store, "startup-model", "test-provider"))
	s.model = "selected-model"
	s.SaveAfterTurn()
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Model != "selected-model" {
		t.Fatalf("saved infos = %+v", infos)
	}
}

func TestLoadModelPolicy(t *testing.T) {
	for _, storeBacked := range []bool{false, true} {
		t.Run(fmt.Sprintf("store_backed=%v", storeBacked), func(t *testing.T) {
			store, err := NewFileSessionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save("saved", []provider.Message{{Role: provider.RoleUser, Content: "saved"}}, "removed", "p"); err != nil {
				t.Fatal(err)
			}
			s := NewSession(&config.Resolved{Model: "current", Models: []string{"current", "listed"}}, nil)
			s.SessionDir = store.Dir()
			if storeBacked {
				s.SetSessionStore(store, nil)
			}
			if err := s.Load("saved"); err != nil {
				t.Fatal(err)
			}
			saved, current, ok := s.ModelRestoreNotice()
			if s.CurrentModel() != "current" || !ok || saved != "removed" || current != "current" {
				t.Fatalf("model=%q notice=(%q,%q,%v)", s.CurrentModel(), saved, current, ok)
			}
		})
	}
}

func TestLoadRestoresListedAndUnrestrictedModels(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "saved"}}
	if err := store.Save("listed", msgs, "listed", "p"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("free", msgs, "anything", "p"); err != nil {
		t.Fatal(err)
	}
	managed := NewSession(&config.Resolved{Model: "current", Models: []string{"current", "listed"}}, nil)
	managed.SetSessionStore(store, nil)
	if err := managed.Load("listed"); err != nil {
		t.Fatal(err)
	}
	if managed.CurrentModel() != "listed" {
		t.Fatalf("managed=%q", managed.CurrentModel())
	}
	free := NewSession(&config.Resolved{Model: "current"}, nil)
	free.SetSessionStore(store, nil)
	if err := free.Load("free"); err != nil {
		t.Fatal(err)
	}
	if free.CurrentModel() != "anything" {
		t.Fatalf("free=%q", free.CurrentModel())
	}
}
