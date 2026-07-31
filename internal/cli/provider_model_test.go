package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

func TestModelSlashEnforcesAllowlist(t *testing.T) {
	res := &config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}}
	sess := chat.NewSession(res, nil)
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	if _, _, err := handleSlashInfo("/model", []string{"/model", "Z"}, sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if got := sess.CurrentModel(); got != "A" {
		t.Fatalf("rejected model changed to %q", got)
	}
	if !strings.Contains(buf.String(), "available: A, B") {
		t.Fatalf("rejection = %q", buf.String())
	}
	if _, _, err := handleSlashInfo("/model", []string{"/model", "B"}, sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if got := sess.CurrentModel(); got != "B" {
		t.Fatalf("accepted model = %q", got)
	}
}

func TestTUIModelSlashEnforcesAllowlist(t *testing.T) {
	res := &config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}}
	m := newTUIModel(chat.NewSession(res, nil), res, true)
	m.mode = modeChat
	m.handleSlash("/model Z")
	if got := m.session.CurrentModel(); got != "A" {
		t.Fatalf("rejected model changed to %q", got)
	}
	m.handleSlash("/model B")
	if got := m.session.CurrentModel(); got != "B" || m.modelName != "B" {
		t.Fatalf("accepted = %q, label=%q", got, m.modelName)
	}
}

func TestModelSwitchRebuildsSkillRegistryWhenFactoryIsInstalled(t *testing.T) {
	res := &config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}}
	sess := chat.NewSession(res, welcomeStubCompleter{})
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		registry := skills.NewRegistry()
		if err := registry.Register(skills.Definition{
			Name: "review", Run: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.Marshal(model)
			},
		}); err != nil {
			return chat.ModelBinding{}, err
		}
		return chat.ModelBinding{ProviderName: providerName, Model: model, Completer: welcomeStubCompleter{}, SkillRegistry: registry}, nil
	})
	if err := switchModelCommand(sess, res, "p", "B"); err != nil {
		t.Fatal(err)
	}
	definition, ok := sess.CurrentBinding().SkillRegistry.Get("review")
	if !ok {
		t.Fatal("rebuilt skill missing")
	}
	result, err := definition.Run(context.Background(), nil)
	if err != nil || string(result) != `"B"` {
		t.Fatalf("skill runner result=%s err=%v, want model B", result, err)
	}
}

func TestLoadSurfacesModelRestoreNotice(t *testing.T) {
	store, err := chat.NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("saved", nil, "removed", "p"); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{ProviderName: "p", Model: "current", Models: []string{"current"}}
	newSession := func() *chat.Session {
		s := chat.NewSession(res, welcomeStubCompleter{})
		s.SetSessionStore(store, nil)
		return s
	}

	t.Run("repl load", func(t *testing.T) {
		buf := new(bytes.Buffer)
		term := &Terminal{out: buf}
		sess := newSession()
		if _, _, err := handleSlashSessions("/load", "/load saved", sess, term); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "was saved with model") {
			t.Fatalf("output=%q", buf.String())
		}
	})
	t.Run("tui load", func(t *testing.T) {
		m := newTUIModel(newSession(), res, true)
		m.mode = modeChat
		m.handleSlash("/load saved")
		found := false
		for _, b := range m.blocks {
			if strings.Contains(stripANSI(b.Text), "was saved with model") {
				found = true
			}
		}
		if !found || m.modelName != "current" {
			t.Fatalf("blocks=%+v label=%q", m.blocks, m.modelName)
		}
	})
	t.Run("welcome load", func(t *testing.T) {
		m := newTUIModel(newSession(), res, true)
		m.sessions = []chat.SessionInfo{{Name: "saved"}}
		if err := m.openSessionByName("saved"); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, b := range m.blocks {
			if strings.Contains(stripANSI(b.Text), "was saved with model") {
				found = true
			}
		}
		if !found || m.modelName != "current" {
			t.Fatalf("blocks=%+v label=%q", m.blocks, m.modelName)
		}
	})
}

func TestREPLAutoRestoreSurfacesModelNotice(t *testing.T) {
	store, err := chat.NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(chat.AutoSaveName, []provider.Message{{Role: provider.RoleUser, Content: "saved"}}, "removed", "p"); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{ProviderName: "p", Model: "current", Models: []string{"current"}}
	sess := chat.NewSession(res, welcomeStubCompleter{})
	sess.SessionDir = store.Dir()
	sess.SetSessionStore(store, nil)
	sess.SetSessionStore(store, nil)
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	r := &replRuntime{sess: sess, config: res, term: term, renderer: NewChatRenderer(term, "current"), input: NewInputBuffer(" current > ")}
	r.restore()
	if !strings.Contains(buf.String(), "was saved with model") || r.modelShort != "current" {
		t.Fatalf("output=%q short=%q", buf.String(), r.modelShort)
	}
}
