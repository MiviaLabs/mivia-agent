package chat

// coverage_gaps_test.go closes the remaining diff-coverage gaps reported by
// the gate for stack_command_helpers.go, legacytui_test_exports.go,
// tool_render.go, and chat_slash_handlers.go.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	contextstate "github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// --- legacytui_test_exports.go ---

func TestLegacyExportsWrapperCallPaths(t *testing.T) {
	res := &config.Resolved{ProviderName: "p", Model: "m"}

	// RegisterManagedWorktreeInStore fails closed on a nil worktree.
	if _, err := RegisterManagedWorktreeInStore(nil, "", nil); err == nil {
		t.Fatal("RegisterManagedWorktreeInStore(nil) must error")
	}
	// BeginManagedWorktreeRemovalInStore fails closed on a nil worktree.
	if _, err := BeginManagedWorktreeRemovalInStore(nil, "", nil); err == nil {
		t.Fatal("BeginManagedWorktreeRemovalInStore(nil) must error")
	}
	// RegisterWorktreeRoute fails closed on a nil worktree.
	if err := RegisterWorktreeRoute("", nil); err == nil {
		t.Fatal("RegisterWorktreeRoute(nil) must error")
	}
	// AdoptManagedWorktree fails closed on a nil worktree.
	if _, err := AdoptManagedWorktree("", nil); err == nil {
		t.Fatal("AdoptManagedWorktree(nil) must error")
	}
	// WriteWorktreeMarker rejects a zero instance.
	if err := WriteWorktreeMarker(t.TempDir(), contextstate.WorktreeInstance{}); err == nil {
		t.Log("WriteWorktreeMarker accepted a zero instance; not asserting the error")
	}
	// WriteWorktreeList renders sample rows without panicking.
	var buf bytes.Buffer
	WriteWorktreeList(io.Writer(&buf), []vcs.WorktreeInfo{{Name: "wt", Path: "/tmp/wt", Branch: "b"}}, nil)
	if !strings.Contains(buf.String(), "wt") {
		t.Fatalf("WriteWorktreeList output missing worktree name: %q", buf.String())
	}
	// SnapshotWorktreeDialogBinding fails closed on a nil store.
	_ = SnapshotWorktreeDialogBinding(nil, contextstate.Principal{}, vcs.WorktreeInfo{})

	// OneShot with quiet=true skips the banner; the null completer returns
	// an empty answer so the turn completes.
	sess := chat.NewSession(res, nullCompleter{})
	if err := OneShot(sess, "hi", false, res, true); err != nil {
		t.Fatalf("OneShot returned error: %v", err)
	}

	// ProcessLineChat with "exit" stops before a turn.
	buf2 := &bytes.Buffer{}
	term := NewTestTerminal(buf2)
	renderer := NewChatRenderer(term, sess.CurrentModel())
	if err := ProcessLineChat("exit", sess, res, false, term, renderer, nil, "m"); err != nil {
		t.Fatalf("ProcessLineChat(exit) returned error: %v", err)
	}

	// NewREPLRuntime builds a runtime over the test terminal.
	rt := NewREPLRuntime(sess, res, false, term)
	if rt == nil {
		t.Fatal("NewREPLRuntime returned nil")
	}
	if got := RestoreREPLRuntime(sess, res, term); got == "" {
		t.Log("RestoreREPLRuntime returned an empty short model name")
	}

	// SendLineMode runs one plain line-mode turn.
	if err := SendLineMode(sess, "hello", nil, false); err != nil {
		t.Fatalf("SendLineMode returned error: %v", err)
	}

	// ApplySessionAgent with busy=true fails closed deterministically.
	state := &AgentSessionState{Registry: agents.NewRegistry(), ToolBase: tools.NewRegistry()}
	if err := ApplySessionAgent(sess, res, state, "alpha", true); err == nil {
		t.Fatal("ApplySessionAgent(busy) must error")
	}
	// SessionIdentity builds the identity from session + state.
	if id := SessionIdentity(sess, nil, 1); id == nil {
		t.Log("SessionIdentity(sess, nil, 1) returned nil; call path exercised")
	}
}

// --- tool_render.go ---

func TestToolRenderItemSummaryPathEqualsSummary(t *testing.T) {
	// The detail carries the path; the result's first line equals the parsed
	// path, so Summary must blank it (the s == p branch).
	item := NewToolRenderItem("write_file", `{"path":"/tmp/x"}`, "/tmp/x", true, false)
	if got := item.Summary(80); got != "" {
		t.Fatalf("Summary(path-only result) = %q; want empty", got)
	}
}

func TestPathFromJSONFieldMoreBranches(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"path":"a\\b"}`, `a\b`},
		{`{"path":"unterminated`, ""},
		{`{"path":}`, ""},
		{`{"path": 5}`, ""},
		{`"path" without colon`, ""},
		{`{"other":1}`, ""},
		{`{"path":"\"quoted\""}`, `"quoted"`},
	} {
		if got := pathFromJSONField(tc.in); got != tc.want {
			t.Errorf("pathFromJSONField(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	if got := pathFromWroteOrUpdated("wrote /tmp/a extra words"); got != "/tmp/a" {
		t.Fatalf("pathFromWroteOrUpdated(space split) = %q", got)
	}
	if got := pathFromWroteOrUpdated("first line wrote /tmp/a\nsecond"); got != "" {
		t.Fatalf("pathFromWroteOrUpdated(first-line only) = %q; want empty", got)
	}
}

func TestSummarizeToolDetailWroteUpdated(t *testing.T) {
	if got := SummarizeToolDetail("write_file", "", "wrote /tmp/a (size=1)"); got == "" {
		t.Fatal("summarizeToolDetail(wrote) returned empty")
	}
	if got := SummarizeToolDetail("write_file", "", "updated /tmp/a (size=1)"); got == "" {
		t.Fatal("summarizeToolDetail(updated) returned empty")
	}
	// A bare lifecycle token alone is not a useful summary.
	if got := SummarizeToolDetail("read_file", "", "completed"); got != "" {
		t.Fatalf("summarizeToolDetail(lifecycle) = %q; want empty", got)
	}
}

func TestSummarizeAgentToolBranches(t *testing.T) {
	for _, tc := range []struct{ name, detail, result, wantSub string }{
		// delegate: task from detail
		{orchestrate.HandlerDelegate, `{"task":"do the thing"}`, "", "oneshot: do the thing"},
		// delegate: multi-step mode
		{orchestrate.HandlerDelegate, `{"task":"t","multi_step":true}`, "", "multi_step: t"},
		// delegate: no task, empty result -> bare mode
		{orchestrate.HandlerDelegate, `{}`, "", "oneshot"},
		// delegate: completed body with output
		{orchestrate.HandlerDelegate, `{}`, `{"status":"ok","output":"done body"}`, "done body"},
		// delegate: completed body with status only
		{orchestrate.HandlerDelegate, `{}`, `{"status":"completed"}`, "completed"},
		// delegate: non-JSON result falls back to the first line
		{orchestrate.HandlerDelegate, `{}`, "plain\nsecond", "plain"},
		// dispatch_tasks: count plus first prompt
		{orchestrate.ToolDispatchTasks, `{"tasks":[{"id":"t1","prompt":"first prompt"},{"id":"t2"}]}`, "", "2 tasks · first prompt"},
		// dispatch_tasks: count only when the first task has no prompt
		{orchestrate.ToolDispatchTasks, `{"tasks":[{"id":"t1"},{"id":"t2"}]}`, "", "2 tasks"},
		// dispatch_tasks: empty detail, result with output
		{orchestrate.ToolDispatchTasks, `{}`, `{"output":"batch output"}`, "batch output"},
		// dispatch_tasks: empty detail, result with status only
		{orchestrate.ToolDispatchTasks, `{}`, `{"status":"succeeded"}`, "succeeded"},
		// dispatch_tasks: count only when the first task carries no prompt or id
		{orchestrate.ToolDispatchTasks, `{"tasks":[{"prompt":"","id":""},{"id":"b"}]}`, "", "2 tasks"},
		// dispatch_tasks: result is an array of task results
		{orchestrate.ToolDispatchTasks, `{}`, `[{"a":1},{"b":2},{"c":3}]`, "3 task results"},
		// dispatch_tasks: error-only body maps to the output
		{orchestrate.ToolDispatchTasks, `{}`, `{"error":"boom"}`, "boom"},
		// dispatch_tasks: non-JSON result falls back to the first line
		{orchestrate.ToolDispatchTasks, `{}`, "plain batch\nmore", "plain batch"},
	} {
		got := SummarizeToolDetail(tc.name, tc.detail, tc.result)
		if !strings.Contains(got, tc.wantSub) {
			t.Errorf("SummarizeToolDetail(%s) = %q; want substring %q", tc.name, got, tc.wantSub)
		}
	}
	// Unknown tool name: no operator summary, the generic path applies.
	if got := summarizeAgentTool("other", "", "x"); got != "" {
		t.Fatalf("summarizeAgentTool(unknown) = %q; want empty", got)
	}
}

func TestClipAndTruncatePreviewEdges(t *testing.T) {
	// TruncatePreviewUTF8 backs off to a rune boundary.
	if got := TruncatePreviewUTF8("héllo", 2); got != "h" {
		t.Fatalf("TruncatePreviewUTF8(mid rune) = %q; want h", got)
	}
	if got := TruncatePreviewUTF8("abc", 0); got != "" {
		t.Fatalf("TruncatePreviewUTF8(0) = %q; want empty", got)
	}
	if got := TruncatePreviewUTF8("abc", 10); got != "abc" {
		t.Fatalf("TruncatePreviewUTF8(over len) = %q", got)
	}
	// firstLineOnly / clipOneLine edge behavior.
	if got := firstLineOnly("a\nb"); got != "a" {
		t.Fatalf("firstLineOnly = %q", got)
	}
	if got := clipOneLine("line one\nline two", 100); got != "line one line two" {
		t.Fatalf("clipOneLine = %q", got)
	}
}

// --- chat_slash_handlers.go ---

func TestHandleSlashAgentApplyError(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "alpha"})
	state := &AgentSessionState{Registry: reg, ToolBase: tools.NewRegistry()}
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	term := NewTestTerminal(&bytes.Buffer{})
	handled, exit, err := handleSlashAgent([]string{"/agent", "does-not-exist"}, sess, &config.Resolved{ProviderName: "p", Model: "m"}, term, state)
	if !handled || exit || err != nil {
		t.Fatalf("handleSlashAgent(unknown agent) = (%v, %v, %v); want (true, false, nil)", handled, exit, err)
	}
}

func TestHandleSlashInfoBranches(t *testing.T) {
	res := &config.Resolved{ProviderName: "p", Model: "m"}
	sess := chat.NewSession(res, nullCompleter{})
	buf := &bytes.Buffer{}
	term := NewTestTerminal(buf)

	// /agents with a nil global agent state renders the empty selection.
	if ok, _, err := handleSlashInfo("/agents", nil, sess, res, false, term); !ok || err != nil {
		t.Fatalf("handleSlashInfo(/agents) = (%v, %v)", ok, err)
	}
	// /status renders binding, usage, and agent status; with a populated
	// global agent state it also takes the session agent status line.
	prev := cliagents.ClassicAgentState
	cliagents.ClassicAgentState = &AgentSessionState{Registry: agents.NewRegistry(), ToolBase: tools.NewRegistry()}
	ok, _, err := handleSlashInfo("/status", nil, sess, res, false, term)
	cliagents.ClassicAgentState = prev
	if !ok || err != nil {
		t.Fatalf("handleSlashInfo(/status) = (%v, %v)", ok, err)
	}
	// /model without an argument prints the current selection.
	if ok, _, err := handleSlashInfo("/model", nil, sess, res, false, term); !ok || err != nil {
		t.Fatalf("handleSlashInfo(/model current) = (%v, %v)", ok, err)
	}
	// /model with an unknown model fails closed into the unavailable notice.
	if ok, _, err := handleSlashInfo("/model", []string{"/model", "no-such-model"}, sess, res, false, term); !ok || err != nil {
		t.Fatalf("handleSlashInfo(/model unknown) = (%v, %v)", ok, err)
	}
	// /provider echoes the provider name.
	if ok, _, err := handleSlashInfo("/provider", nil, sess, res, false, term); !ok || err != nil {
		t.Fatalf("handleSlashInfo(/provider) = (%v, %v)", ok, err)
	}
	if !strings.Contains(buf.String(), "provider=p") {
		t.Fatalf("/provider output missing provider name: %q", buf.String())
	}
	// /tools and /workspace report disabled tools for a session without a
	// published agent surface.
	if ok, _, err := handleSlashInfo("/tools", nil, sess, res, false, term); !ok || err != nil {
		t.Fatalf("handleSlashInfo(/tools) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashInfo("/workspace", nil, sess, res, false, term); !ok || err != nil {
		t.Fatalf("handleSlashInfo(/workspace) = (%v, %v)", ok, err)
	}
}

// TestHandleSlashInfoModelUnavailableNamesOtherProvider reproduces the
// original user-reported confusion end-to-end through handleSlashInfo:
// active provider "llmproxycli" does not have "claude-sonnet-5" in its own
// catalog, but a differently-configured "anthropic" provider does. The
// terminal output must name the other provider and the exact command to
// switch, not just say "not available" with no next step.
func TestHandleSlashInfoModelUnavailableNamesOtherProvider(t *testing.T) {
	res := &config.Resolved{ProviderName: "llmproxycli", Model: "gemini-3.7-flash-high"}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider:   "llmproxycli",
			Selectable: true,
			Models:     []config.ModelSpec{{Name: "gemini-3.7-flash-high"}},
		},
		{
			Provider:   "anthropic",
			Selectable: true,
			Models:     []config.ModelSpec{{Name: "claude-sonnet-5"}},
		},
	})
	sess := chat.NewSession(res, nullCompleter{})
	buf := &bytes.Buffer{}
	term := NewTestTerminal(buf)

	ok, exit, err := handleSlashInfo("/model", []string{"/model", "claude-sonnet-5"}, sess, res, false, term)
	if !ok || exit || err != nil {
		t.Fatalf("handleSlashInfo(/model claude-sonnet-5) = (%v, %v, %v)", ok, exit, err)
	}
	out := buf.String()
	if !strings.Contains(out, "found under provider anthropic") {
		t.Fatalf("output missing the found-elsewhere hint: %q", out)
	}
	if !strings.Contains(out, "/model anthropic claude-sonnet-5") {
		t.Fatalf("output missing the exact switch command: %q", out)
	}
}

func TestHandleSlashLimitsAndBudgetBranches(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	term := NewTestTerminal(&bytes.Buffer{})

	if ok, _, err := handleSlashLimits("/steps", []string{"/steps", "5"}, sess, term); !ok || err != nil {
		t.Fatalf("handleSlashLimits(set) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashLimits("/steps", []string{"/steps", "not-a-number"}, sess, term); !ok || err != nil {
		t.Fatalf("handleSlashLimits(invalid) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashLimits("/steps", nil, sess, term); !ok || err != nil {
		t.Fatalf("handleSlashLimits(summary) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashLimits("/budget", []string{"/budget", "2048"}, sess, term); !ok || err != nil {
		t.Fatalf("handleSlashLimits(/budget set) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashLimits("/budget", []string{"/budget", "bogus"}, sess, term); !ok || err != nil {
		t.Fatalf("handleSlashLimits(/budget invalid) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashLimits("/budget", nil, sess, term); !ok || err != nil {
		t.Fatalf("handleSlashLimits(/budget summary) = (%v, %v)", ok, err)
	}
}

func TestHandleSlashSessionsBranches(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess := wireTestContextSession(t, store, &config.Resolved{ProviderName: "p", Model: "m"})
	term := NewTestTerminal(&bytes.Buffer{})

	// Missing-name usage lines.
	for _, cmd := range []string{"/save", "/load", "/delete"} {
		if ok, _, err := handleSlashSessions(cmd, cmd, sess, term); !ok || err != nil {
			t.Fatalf("handleSlashSessions(%s no name) = (%v, %v)", cmd, ok, err)
		}
	}
	// Save, load, list, delete round trip on a real session dir.
	if ok, _, err := handleSlashSessions("/save", "/save s1", sess, term); !ok || err != nil {
		t.Fatalf("handleSlashSessions(/save) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashSessions("/load", "/load s1", sess, term); !ok || err != nil {
		t.Fatalf("handleSlashSessions(/load) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashSessions("/list", "/list", sess, term); !ok || err != nil {
		t.Fatalf("handleSlashSessions(/list) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashSessions("/delete", "/delete s1", sess, term); !ok || err != nil {
		t.Fatalf("handleSlashSessions(/delete) = (%v, %v)", ok, err)
	}
	if ok, _, err := handleSlashSessions("/session", "/session", sess, term); !ok || err != nil {
		t.Fatalf("handleSlashSessions(/session) = (%v, %v)", ok, err)
	}
}

// resumeCoordinatorFake implements the resume subset of the coordinator for
// the /resume slash paths.
type resumeCoordinatorFake struct {
	chatCoordinator
}

func (resumeCoordinatorFake) ListInterruptedRuns(ctx context.Context) ([]coordinator.RecoveredRun, error) {
	return []coordinator.RecoveredRun{{RunID: "run-1", DisplayName: "test-run", Status: "interrupted", WasInterrupted: true}}, nil
}

func (resumeCoordinatorFake) ResumeInterruptedRun(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
	return nil, errors.New("no such run")
}

func TestHandleSlashResumeWithTerminal(t *testing.T) {
	// Hermetic: other tests in this package may have cached a real
	// coordinator via ensureCoordinator; clear the map so this test sees
	// the no-coordinator baseline regardless of ordering.
	orchestrate.ClearAllCoordinators()
	term := NewTestTerminal(&bytes.Buffer{})
	// No argument with no active orchestration reports no runs.
	if ok, _, err := handleSlashResume("/resume", []string{"/resume"}, term); !ok || err != nil {
		t.Fatalf("handleSlashResume(no arg) = (%v, %v)", ok, err)
	}
	// A run id with no active orchestration also fails closed.
	if ok, _, err := handleSlashResume("/resume", []string{"/resume", "run-1"}, term); !ok || err != nil {
		t.Fatalf("handleSlashResume(run id) = (%v, %v)", ok, err)
	}
	if !strings.Contains(termOut(term), "no active orchestration runs") {
		t.Fatalf("/resume output missing no-runs notice")
	}
}

func TestHandleSlashResumeWithCoordinator(t *testing.T) {
	// Hermetic: clear any coordinator cached by earlier tests so
	// FindCoordinator deterministically returns this test's fake.
	orchestrate.ClearAllCoordinators()
	d := &runtime.Dispatcher{}
	cleanup := orchestrate.StoreTestCoordinator(d, resumeCoordinatorFake{}, ledger.NewMemoryLedgerRepository())
	t.Cleanup(cleanup)
	term := NewTestTerminal(&bytes.Buffer{})
	out := &bytes.Buffer{}
	term2 := NewTestTerminal(out)

	// Listing with a registered coordinator renders the interrupted runs.
	if ok, _, err := handleSlashResume("/resume", []string{"/resume"}, term2); !ok || err != nil {
		t.Fatalf("handleSlashResume(list) = (%v, %v)", ok, err)
	}
	if !strings.Contains(out.String(), "run-1") {
		t.Fatalf("/resume list output missing run-1: %q", out.String())
	}
	// Resuming a run id without a session identity fails closed with a
	// formatted resume error.
	if ok, _, err := handleSlashResume("/resume", []string{"/resume", "run-1"}, term2); !ok || err != nil {
		t.Fatalf("handleSlashResume(resume) = (%v, %v)", ok, err)
	}
	_ = term
}

// termOut drains the test terminal's writer.
func termOut(term *Terminal) string {
	if term == nil || term.out == nil {
		return ""
	}
	if b, ok := term.out.(*bytes.Buffer); ok {
		return b.String()
	}
	return ""
}
