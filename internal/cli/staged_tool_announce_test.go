package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestCallingAStagedToolBeforePublicationSaysSo: a tool staged by load_tools
// becomes callable only after the turn boundary publishes it. When the boundary
// defers (R2-2), a later turn calling the staged tool must receive a precise
// pending-publication message instead of the unknown-tool denial.
func TestCallingAStagedToolBeforePublicationSaysSo(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetSwitchGuard(func() error { return fmt.Errorf("background run active") })

	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v while background work held the dispatcher", got)
	}
	if _, ok := fixture.sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending for the next qualifying boundary")
	}

	completer.mu.Lock()
	completer.turns = []provider.Response{
		toolCallResponse(namedCall("c2", "grep", `{}`)),
		{Content: "done"},
	}
	completer.calls = 0
	completer.mu.Unlock()
	if _, err := fixture.sess.SendUser(context.Background(), "use it", io.Discard); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	var stagedContent string
	for _, msg := range fixture.sess.MessagesCopy() {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "c2" {
			stagedContent = msg.Content
			if strings.Contains(msg.Content, "not available to this agent") {
				t.Fatalf("staged tool got the unknown-tool denial: %q", msg.Content)
			}
		}
	}
	if !strings.Contains(stagedContent, "staged for loading") {
		t.Fatalf("the staged tool call did not report pending publication: %q", stagedContent)
	}
	if !strings.Contains(stagedContent, "background orchestration is active") {
		t.Fatalf("the staged denial did not announce the deferral reason: %q", stagedContent)
	}
}

// TestLoadToolsAnnouncesDeferredPublication: when a stage from an earlier turn
// is still pending because the boundary deferred, a re-request of the same name
// announces the cause mid-turn instead of repeating the availability promise.
func TestLoadToolsAnnouncesDeferredPublication(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetSwitchGuard(func() error { return fmt.Errorf("background run active") })
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if _, ok := fixture.sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending while the guard refuses")
	}
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err != nil {
		t.Fatalf("re-request: %v", err)
	}
	if !strings.Contains(out, "already staged") {
		t.Fatalf("re-request did not report the pending stage: %q", out)
	}
	if !strings.Contains(out, "background orchestration is active") {
		t.Fatalf("re-request did not announce the deferral reason: %q", out)
	}
}
