package clichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// unregisterableTool cannot be installed on a dispatcher: a handler needs a
// name, and this one has none. It stands in for any tool whose registration
// fails during dispatcher construction.
type unregisterableTool struct{}

func (unregisterableTool) Name() string        { return "  " }
func (unregisterableTool) Description() string { return "unregisterable" }
func (unregisterableTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (unregisterableTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// A session whose dispatcher cannot be built reports the failure instead of
// binding a half-wired session. Attaching is the step that makes tools
// executable, so a session that survived it with no dispatcher would offer the
// model tools nothing can run.
func TestAttachSessionDispatcherReportsConstructionFailure(t *testing.T) {
	root := t.TempDir()
	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, welcomeStubCompleter{})
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(unregisterableTool{})

	cleanup, err := attachSessionDispatcher(sess, root, "test-model",
		config.DefaultSubagentConfig, &AgentSessionState{}, nil, sessionRouting{})
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("attach reported success although the dispatcher could not be built")
	}
	if !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("error %q does not name the failing step", err)
	}
	if sess.Dispatcher != nil {
		t.Fatal("a failed attach left a dispatcher on the session")
	}
}
