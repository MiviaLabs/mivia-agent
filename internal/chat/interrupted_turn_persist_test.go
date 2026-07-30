package chat

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// interruptedCompleter streams a partial answer, then reports the turn was
// cancelled - what Ctrl+C or a request deadline produces mid-answer.
type interruptedCompleter struct{ partial string }

func (interruptedCompleter) Name() string { return "interrupted" }
func (interruptedCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", context.Canceled
}
func (interruptedCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return "", context.Canceled
}
func (c interruptedCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.partial)
	}
	return nil, context.Canceled
}

// TestNoMessageLossInterruptedTurnIsPersisted locks the defect where an errored
// or cancelled turn was never written to disk: SaveAfterTurn sat below an early
// `if err != nil { return }`. The user's question and the answer they had already
// read on screen both vanished from the transcript, so restarting the session
// rebuilt a history missing both - and the model repeated itself.
func TestNoMessageLossInterruptedTurnIsPersisted(t *testing.T) {
	const partial = "Both fixes work. Here is the pro"
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sess := NewSession(&config.Resolved{
		Model:        "test-model",
		SystemPrompt: "sys",
	}, interruptedCompleter{partial: partial})
	sess.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	sess.UseTools = true
	sess.SetSessionStore(store, NewSaveManager(store, "test-model", "test-provider"))

	var sink strings.Builder
	if _, err := sess.SendUser(context.Background(), "prove it", &sink); err == nil {
		t.Fatal("cancelled turn must still report its error")
	}

	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("cancelled turn was never persisted: no session on disk")
	}

	loaded, err := store.Load(names[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	var sawUser, sawPartial bool
	for _, msg := range loaded {
		switch msg.Role {
		case provider.RoleUser:
			if strings.Contains(msg.Content, "prove it") {
				sawUser = true
			}
		case provider.RoleAssistant:
			if strings.Contains(msg.Content, partial) {
				sawPartial = true
			}
		}
	}
	if !sawUser {
		t.Error("the question the user asked is missing from the persisted transcript")
	}
	if !sawPartial {
		t.Error("the answer the user already read is missing from the persisted transcript")
	}
}
