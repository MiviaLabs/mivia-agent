package newtui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
	"github.com/charmbracelet/x/ansi"
)

// gatingProviderServer serves one OpenAI-compatible chat completion that
// blocks until release is closed, so a turn driven through the REAL provider
// stack stays genuinely in flight (the state an automation run is in while
// the operator opens /resume and picks its session). Once released it
// STREAMS the reply as SSE chunks, the way a real provider does - so text
// events (not just turn end) flow to any live viewer after release.
func gatingProviderServer(t *testing.T) (srv *httptest.Server, entered func() <-chan struct{}, release func()) {
	t.Helper()
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	enteredOnce := sync.Once{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		enteredOnce.Do(func() { close(enteredCh) })
		select {
		case <-releaseCh:
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, chunk := range []string{"AUTOMATION ", "LIVE ", "PROGRESS"} {
			payload, _ := json.Marshal(map[string]any{
				"id":      "chatcmpl-test",
				"object":  "chat.completion.chunk",
				"model":   "m1",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": chunk}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if fl != nil {
				fl.Flush()
			}
		}
		payload, _ := json.Marshal(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion.chunk",
			"model":   "m1",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() <-chan struct{} { return enteredCh }, func() { close(releaseCh) }
}

// TestRepro_ResumeAutomationSessionShowsLiveProgress reproduces the reported
// flow end to end through the production wiring: an automation session is
// spawned through the TUI's own spawner (pool.CreateFreshBackgroundInDir) and
// saved under its own id (automation.spawnRunSession's contract), its turn is
// left in flight, then the operator runs /resume and selects that session.
// The switched-to conversation must be the SAME pooled conversation the run
// drives, and its live turn events must reach the transcript - both for a
// turn already in flight when the view attaches (self-heal at text.end) and
// for a turn started while the screen watches (streams from turn.start).
//
// ROOT-CAUSE NOTE (the investigation this test closes out): the resumed
// session shows NO live progress exactly when the spawned automation session
// lacks the tool surface (chat.Session.AgentTurnEnabled() == false, e.g. the
// root TUI session ran --no-tools so SessionPool.toolsOn was false, or a
// background spawn's per-entry registry attach failed - a failure that is
// silent by design because the tool-scope notice slot is foreground-owned,
// session_pool_worktree.go). Such turns take chat's PLAIN path
// (sendPlain), which publishes only synthetic turn.start/turn.end - the
// reply text never becomes an event (it goes to the io.Writer the caller
// passed, and Conversation.Send passes io.Discard). The live view renders
// events only and never repaints from history on a live turn.end (only
// handleLiveStale reloads), so the transcript stays empty until a tab switch
// forces replayOrResumeLive's LoadHistory. With the tool surface present,
// this test proves the whole chain works.
func TestRepro_ResumeAutomationSessionShowsLiveProgress(t *testing.T) {
	env := newReproLiveEnv(t)

	// In-flight turn, driven like automation.sendIntentHeadless drives it.
	firstErr := env.startTurn("automation step one")
	select {
	case <-env.entered():
	case err := <-firstErr:
		t.Fatalf("automation turn ended before reaching the provider: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("automation turn never reached the provider")
	}

	assertResumeHandsBackLiveConversation(t, env)

	// Part B - the screen contract: drive the real model through a mini
	// event loop that mirrors tea's runtime, as the user would: /resume,
	// filter the picker to the automation session, enter.
	rt := newMiniRuntime(env.root)
	resumeThroughPicker(t, rt, env.boundSess.SessionID)

	// Independent live viewer: splits "the tee delivers nothing" (upstream)
	// from "the screen drops what the tee delivers" (screen side).
	events, sub := env.subscribeLive(t)
	defer sub.Close()

	// Release the in-flight turn and wait it out: the mid-turn attach case.
	// The SECOND turn below starts while the screen is already attached.
	env.release()
	waitTurnDone(t, firstErr, "first automation turn")
	drainEvents(events)

	// Mid-turn attach verdict: the reply must already be on screen
	// (self-heal at text.end per the live-view contract), before any second
	// turn could paint it.
	//
	// Polled, not asserted once: waitTurnDone returns when the turn's own
	// goroutine finishes, but the settled text still has to cross the
	// viewer tee, be queued on the mini runtime, and be applied by an
	// Update before it can appear in a View. Those hops are scheduler
	// work, not turn work, so a bare check here fails whenever the test
	// process is starved - deterministically under `-cpu 1`, and
	// intermittently under a loaded full-suite run, where it also
	// truncated the coverage profile. The deadline is generous because
	// the only thing being waited on is delivery; a genuine regression
	// (the reply never reaching the transcript) still fails, just after
	// the timeout instead of instantly.
	waitForView(t, rt, liveProgressText, 15*time.Second,
		"first (mid-turn attach) turn's reply never reached the transcript")

	secondErr := env.startTurn("automation step two")
	assertTranscriptShowsTurn(t, rt, events, secondErr)
}

// liveProgressText is the streamed reply every turn of this repro produces.
const liveProgressText = "AUTOMATION LIVE PROGRESS"

// reproLiveEnv carries the shared wiring the resume-live repro drives: the
// built app, the runner whose pool spawned the automation session, the
// pooled conversation itself, and the gating provider's controls.
type reproLiveEnv struct {
	root      tea.Model
	runner    *uiadapter.CommandRunner
	conv      ports.Conversation
	boundSess *chat.Session
	entered   func() <-chan struct{}
	release   func()
}

// newReproLiveEnv builds the production wiring (buildApp) with a TOOLFUL root
// session - the faithful production posture, since SessionPool.toolsOn is
// seeded from the root session's UseTools - then spawns and saves the
// automation session exactly as automation.spawnRunSession does.
func newReproLiveEnv(t *testing.T) *reproLiveEnv {
	t.Helper()
	srv, entered, release := gatingProviderServer(t)

	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "m1",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {ProviderName: "ollama", BaseURL: srv.URL},
		},
	}
	seed := newContextBoundSession(t, res, db, "pool-seed-main")
	seed.UseTools = true
	seed.Tools = tools.NewRegistry()
	agentState := &cli.AgentSessionState{WorkspaceRoot: t.TempDir()}

	root, _, runner, closeAutomations, err := buildApp(seed, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}
	t.Cleanup(closeAutomations)

	var boundSess *chat.Session
	bind := func(s *chat.Session) (string, error) {
		boundSess = s
		return "", nil
	}
	conv, err := newAutomationSpawner(runner.Pool()).CreateFreshInDir(bind, "")
	if err != nil {
		t.Fatalf("spawn automation session: %v", err)
	}
	if _, ok := conv.(*uiadapter.Conversation); !ok {
		t.Fatalf("spawner returned %T, want *uiadapter.Conversation", conv)
	}
	if !boundSess.ContextEnabled() {
		t.Fatal("spawned automation session has no context store; spawnRunSession's Save contract could not hold")
	}
	t.Logf("spawned automation session: AgentTurnEnabled=%v UseTools=%v Tools!=nil=%v", boundSess.AgentTurnEnabled(), boundSess.UseTools, boundSess.Tools != nil)
	if !boundSess.AgentTurnEnabled() {
		t.Fatal("spawned automation session lacks the tool surface; its turns would run the plain path, which emits no content events - see TestRepro_ResumeAutomationSessionShowsLiveProgress's doc comment")
	}
	if err := boundSess.Save(boundSess.SessionID); err != nil {
		t.Fatalf("save run session: %v", err)
	}
	return &reproLiveEnv{
		root: root, runner: runner, conv: conv, boundSess: boundSess,
		entered: entered, release: release,
	}
}

// startTurn sends one headless turn (Send + drain to close, exactly
// sendIntentHeadless's contract) and reports its outcome on the returned
// channel.
func (e *reproLiveEnv) startTurn(text string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		h, err := e.conv.Send(context.Background(), intent.Send{Text: text})
		if err != nil {
			errCh <- err
			return
		}
		for range h.Events() { // drain to close
		}
		errCh <- nil
	}()
	return errCh
}

// subscribeLive attaches an independent live viewer the way the screen does.
func (e *reproLiveEnv) subscribeLive(t *testing.T) (<-chan uievent.Event, ports.LiveSubscription) {
	t.Helper()
	le, ok := e.conv.(ports.LiveEventsConversation)
	if !ok {
		t.Fatal("pooled conversation does not implement ports.LiveEventsConversation")
	}
	events, sub := le.SubscribeLive()
	c := e.conv.(*uiadapter.Conversation)
	t.Logf("attached viewer: conversation background=%v foreground=%v", c.IsBackground(), c.IsForeground())
	return events, sub
}

// assertResumeHandsBackLiveConversation is Part A of the repro: /resume's
// listing includes the RUNNING automation session, and selecting it hands
// back the SAME pooled conversation the run is driving.
func assertResumeHandsBackLiveConversation(t *testing.T, env *reproLiveEnv) {
	t.Helper()
	choices := env.runner.Run(context.Background(), "resume", "")
	if choices.Err != "" {
		t.Fatalf("/resume listing error: %s", choices.Err)
	}
	var row *ports.SessionSummary
	for i := range choices.SessionChoices {
		if choices.SessionChoices[i].ID == env.boundSess.SessionID {
			row = &choices.SessionChoices[i]
		}
	}
	if row == nil {
		t.Fatalf("running automation session %q not listed by /resume; rows: %+v", env.boundSess.SessionID, choices.SessionChoices)
	}
	sel := env.runner.SelectSession(context.Background(), env.boundSess.SessionID)
	if sel.Err != "" {
		t.Fatalf("SelectSession(%q): %s", env.boundSess.SessionID, sel.Err)
	}
	if sel.Conversation != env.conv {
		t.Fatalf("/resume handed back %p (%T), want the live pooled conversation %p the automation run drives", sel.Conversation, sel.Conversation, env.conv)
	}
}

// resumeThroughPicker drives the UI the way the user would: /resume, wait for
// the picker, filter it to the automation session's id prefix, enter.
func resumeThroughPicker(t *testing.T, rt *miniRuntime, sessionID string) {
	t.Helper()
	rt.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	rt.typeText("/resume")
	// The real composer has slash completion wired: the first enter accepts
	// the highlighted "/resume" candidate, the second submits it (rule 5.6,
	// commands_test.go's sendLine note).
	rt.key(tea.KeyEnter)
	rt.key(tea.KeyEnter)
	deadlineOpen := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadlineOpen) {
		if strings.Contains(rt.view(), "resume session") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rt.view(), "resume session") {
		t.Fatalf("/resume did not open the session picker; view:\n%s", rt.view())
	}
	rt.typeText(sessionID[:8])
	rt.key(tea.KeyEnter)
	// Give the selection a round to switch the conversation and arm the
	// live view before the turn's events start flowing.
	time.Sleep(200 * time.Millisecond)
}

// waitTurnDone blocks until the turn's outcome arrives, failing the test on
// error or timeout.
func waitTurnDone(t *testing.T, errCh <-chan error, what string) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("%s errored: %v", what, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("%s never finished", what)
	}
}

// drainEvents empties a live viewer's channel without blocking.
func drainEvents(events <-chan uievent.Event) {
	for {
		select {
		case <-events:
			continue
		default:
		}
		return
	}
}

// waitForView polls the runtime's rendered view until it contains want, or
// fails with the rendered view attached once the deadline passes. Rendering
// is asynchronous with respect to a turn finishing (tee -> runtime queue ->
// Update -> View), so any assertion about on-screen content after a turn
// must wait for delivery rather than sample once.
func waitForView(t *testing.T, rt *miniRuntime, want string, timeout time.Duration, whatFailed string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		view := rt.view()
		if strings.Contains(view, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s; view:\n%s", whatFailed, view)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertTranscriptShowsTurn polls until the turn's reply appears in the
// on-screen transcript, distinguishing "the screen dropped what the tee
// delivered" (sawEvents) from an errored turn in the failure message.
func assertTranscriptShowsTurn(t *testing.T, rt *miniRuntime, events <-chan uievent.Event, errCh <-chan error) {
	t.Helper()
	var sawEvent bool
	var noticeText string
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case ev := <-events:
			sawEvent = true
			if nb, ok := ev.Body.(uievent.NoticeBody); ok {
				noticeText = nb.Text
			}
			t.Logf("independent viewer received event: kind=%v turnID=%q body=%T %+v", ev.Kind, ev.TurnID, ev.Body, ev.Body)
		default:
		}
		view := rt.view()
		if strings.Contains(view, liveProgressText) {
			return // live progress reached the chat
		}
		if time.Now().After(deadline) {
			select {
			case sendFailure := <-errCh:
				t.Fatalf("turn errored (notice=%q): %v; view:\n%s", noticeText, sendFailure, view)
			default:
			}
			t.Fatalf("live progress never reached the transcript after /resume (independent viewer saw events: %v, notice=%q); view:\n%s", sawEvent, noticeText, view)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// miniRuntime is a single-owner event loop over a tea.Model: messages are
// applied under a lock, and every Cmd Update returns is executed on its own
// goroutine with its message fed back - the contract bubbletea's own runtime
// honors and a naive sequential pump does not (a blocking Cmd like the live
// read loop would starve the whole chain).
type miniRuntime struct {
	mu   sync.Mutex
	m    tea.Model
	msgs chan tea.Msg
}

func newMiniRuntime(root tea.Model) *miniRuntime {
	rt := &miniRuntime{m: root, msgs: make(chan tea.Msg, 256)}
	go func() {
		for msg := range rt.msgs {
			rt.mu.Lock()
			next, cmd := rt.m.Update(msg)
			rt.m = next
			rt.mu.Unlock()
			rt.exec(cmd)
		}
	}()
	return rt
}

func (rt *miniRuntime) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		rt.deliver(msg)
	}()
}

// deliver feeds one Cmd result back the way bubbletea's runtime does: a
// tea.BatchMsg fans out into its child Cmds (each executed on its own
// goroutine); anything else becomes a message. Without the BatchMsg arm a
// tea.Batch returned from Update was pushed into msgs as an opaque unknown
// message and its children - the live read loop's awaitLiveEvent, ticks,
// ClearScreen - never ran, silently disabling exactly the paths this test
// exercises.
func (rt *miniRuntime) deliver(msg tea.Msg) {
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			rt.exec(c)
		}
		return
	}
	rt.msgs <- msg
}

func (rt *miniRuntime) send(msg tea.Msg) { rt.msgs <- msg }

func (rt *miniRuntime) typeText(text string) {
	for _, r := range text {
		rt.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (rt *miniRuntime) key(code rune) {
	rt.send(tea.KeyPressMsg{Code: code})
}

func (rt *miniRuntime) view() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return ansi.Strip(rt.m.View().Content)
}
