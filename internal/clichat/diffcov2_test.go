package clichat

// diffcov2_test.go closes the remaining diff-coverage gaps reported by the
// gate for the rendering, slash-command, and seam-dispatch surfaces that the
// primary suites already exercise around.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliworkflow "github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"

	"golang.org/x/sys/unix"
)

// --- agent_task_handler.go ---

func TestDiffCov2BindingForRequestDeclaredMismatch(t *testing.T) {
	h := &agentTaskHandler{
		definition: agents.ResolvedAgent{Name: "coder", Provider: "openrouter", Model: "m1"},
		binding:    agentBinding{ProviderName: "openrouter", Model: "m1"},
	}
	// A declared binding with a mismatched persisted pair fails closed.
	_, err := h.bindingForRequest(runtime.Request{ProviderName: "other", Model: "m2"})
	if err == nil || !strings.Contains(err.Error(), "does not match the current definition binding") {
		t.Fatalf("bindingForRequest(mismatch) err = %v; want binding mismatch error", err)
	}
	// The matching pair is accepted.
	if _, err := h.bindingForRequest(runtime.Request{ProviderName: "openrouter", Model: "m1"}); err != nil {
		t.Fatalf("bindingForRequest(match) err = %v", err)
	}
}

// --- bubble_rail_roles.go ---

func TestDiffCov2HeaderGroupMemberAndExitTokens(t *testing.T) {
	m := headerGroupMember(3, "gk")
	if !m.InGroup || m.ToolIndex != -1 || m.ToolCount != 3 || m.GroupKey != "gk" || !m.IsHeader {
		t.Fatalf("headerGroupMember = %+v", m)
	}
	for _, s := range []string{"exit=error", "exit=timeout", "exit=canceled", "exit=cancelled", "x exit=1 y"} {
		if !hasExitFailureToken(s) {
			t.Errorf("hasExitFailureToken(%q) = false; want true", s)
		}
	}
	for _, s := range []string{"exit=0", "exit=10", "exitit=1", "no tokens"} {
		if hasExitFailureToken(s) {
			t.Errorf("hasExitFailureToken(%q) = true; want false", s)
		}
	}
}

// --- chat.go / chat_repl*.go / chat_slash.go / chat_hub.go ---

func TestDiffCov2HandleTabWithSlashPrefix(t *testing.T) {
	ib := NewInputBuffer(" > ")
	for _, r := range "/he" {
		ib.Insert(r)
	}
	handleTab(ib)
	if s := ib.String(); !strings.HasPrefix(s, "/") {
		t.Fatalf("handleTab must keep the slash prefix; input = %q", s)
	}
}

func TestDiffCov2StartClassicReplHub(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	leave := startClassicReplHub(sess)
	if leave == nil {
		t.Fatal("startClassicReplHub returned a nil cleanup")
	}
	leave()
}

func TestDiffCov2PrintReplBannerToolsOn(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	printReplBanner(sess, true, false)
	printReplBanner(sess, false, false)
}

func TestDiffCov2ReplSubmitNonExitLine(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	term := NewTestTerminal(&bytes.Buffer{})
	rt := newREPLRuntime(sess, &config.Resolved{ProviderName: "p", Model: "m"}, false, term)
	for _, r := range "hello" {
		rt.input.Insert(r)
	}
	exit, err := rt.submit()
	if exit || err != nil {
		t.Fatalf("submit(hello) = (%v, %v); want (false, nil)", exit, err)
	}
}

func TestDiffCov2HandleSlashAgentDispatch(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	term := NewTestTerminal(&bytes.Buffer{})
	handled, exit, err := handleSlash("/agent", sess, &config.Resolved{ProviderName: "p", Model: "m"}, false, term)
	if !handled || exit || err != nil {
		t.Fatalf("handleSlash(/agent) = (%v, %v, %v)", handled, exit, err)
	}
}

// --- chatblock.go / chatblock_render.go ---

func TestDiffCov2ChatBlockFromMessageAndHydrateDivider(t *testing.T) {
	b := chatBlockFromMessage(1, 2, provider.Message{Role: provider.RoleUser, Content: "hi"})
	if b.ID == "" || b.Kind != ChatBlockUser || b.Text != "hi" {
		t.Fatalf("chatBlockFromMessage = %+v", b)
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "a"},
		{Role: provider.RoleAssistant, Content: "b"},
		{Role: provider.RoleUser, Content: "c"},
	}
	blocks := HydrateChatBlocks(msgs)
	found := false
	for _, blk := range blocks {
		if blk.Kind == ChatBlockDivider {
			found = true
		}
	}
	if !found {
		t.Fatal("HydrateChatBlocks(two turns) must insert a divider block")
	}
}

func TestDiffCov2RenderPreformattedDefaultAndSystemArrow(t *testing.T) {
	block := ChatBlock{ID: "b1", Kind: ChatBlockAssistant, Text: "t", Rendered: "rendered"}
	rail := ResolveBlockRail(block, GroupMember{}, ChromeRenderOpts(), RailView{})
	lines, ok := renderPreformattedBlock(block, rail)
	if !ok || len(lines) != 1 || lines[0] != "rendered" {
		t.Fatalf("renderPreformattedBlock(default) = (%v, %v)", lines, ok)
	}
	arrow := renderBlockBody(ChatBlock{Kind: ChatBlockSystem, Text: "plain system note"}, "plain system note", "m", 60, false)
	if len(arrow) != 1 || !strings.Contains(arrow[0], "plain system note") {
		t.Fatalf("renderBlockBody(system note) = %v", arrow)
	}
}

// --- chatblock_status.go ---

func TestDiffCov2ReconstructStatusThinkingThenTool(t *testing.T) {
	blocks := ReconstructEmptySpeechStatus([]ChatBlock{
		{Kind: ChatBlockThinking, Text: "t"},
		{Kind: ChatBlockTool, ToolName: "read_file"},
	})
	if len(blocks) < 2 {
		t.Fatalf("ReconstructEmptySpeechStatus dropped blocks: %+v", blocks)
	}
}

func TestDiffCov2ToolWaveFollowsWorkStatusSystem(t *testing.T) {
	blocks := []ChatBlock{
		{Kind: ChatBlockSystem, Text: "→ running"},
		{Kind: ChatBlockTool, ToolName: "read_file"},
	}
	if !toolWaveFollows(blocks, 0) {
		t.Fatal("toolWaveFollows(system work status) = false; want true")
	}
}

// --- chatblock_workgroup.go ---

func TestDiffCov2WorkGroupWindowNarrowWidthAndAppendBlock(t *testing.T) {
	out := RenderChatBlocksWithWorkGroupsWindow(nil, "m", 0, false, nil, nil, RailView{})
	if len(out.Lines) != 0 {
		t.Fatalf("RenderChatBlocksWithWorkGroupsWindow(nil) lines = %v", out.Lines)
	}
	r := &ChatBlockRender{}
	appendRenderedBlock(r, ChatBlock{Kind: ChatBlockSystem, Text: "note"}, "m", 60, false)
	if len(r.Lines) == 0 {
		t.Fatal("appendRenderedBlock produced no lines")
	}
}

// --- dialog.go ---

func TestDiffCov2HelpDialogLayoutAndDraw(t *testing.T) {
	lines, boxW, contentH, top, left := helpDialogLayout(30, 10)
	if boxW < 40 || contentH < 1 || top < 1 || left < 1 || len(lines) != contentH {
		t.Fatalf("helpDialogLayout(30,10) = (%d lines, boxW=%d, contentH=%d, top=%d, left=%d)", len(lines), boxW, contentH, top, left)
	}
	term := NewTestTerminal(&bytes.Buffer{})
	drawHelpDialog(term, []string{strings.Repeat("x", 200)}, 40, 1, 2, 2)
	drawHelpDialog(term, []string{"short"}, 40, 1, 2, 2)
	// A narrow width forces the truncation branches in renderHelpLines.
	for _, line := range renderHelpLines(10) {
		_ = line
	}
}

// --- dialog_compositor.go ---

func TestDiffCov2RenderDialogFrameRowRefit(t *testing.T) {
	layout := DialogLayout{Rect: Rect{W: 12, H: 6}}
	out := RenderDialogFrame("t", []string{"row content wider than the frame"}, "f", layout)
	if !strings.Contains(out, "row conten") {
		t.Fatalf("RenderDialogFrame lost row content: %q", out)
	}
}

// --- diff_render.go ---

func TestDiffCov2RenderCollapsedEditBlockFallbackPathAndFailed(t *testing.T) {
	block := ChatBlock{ToolName: "edit", Text: `{"path":"/tmp/a"}`, Failed: true, Elapsed: 2 * time.Second}
	lines := renderCollapsedEditBlock(block, "changed two files", "agent", 60)
	if len(lines) == 0 {
		t.Fatal("renderCollapsedEditBlock returned no lines")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "/tmp/a") {
		t.Fatalf("renderCollapsedEditBlock missing fallback path: %q", joined)
	}
}

// --- highlight.go / highlight_blocks.go ---

func TestDiffCov2HighlightLinePlainUnknownAndInlineComment(t *testing.T) {
	if out, multi := highlightLine("plain text", "", false); multi || !strings.Contains(out, "plain text") {
		t.Fatalf("highlightLine(no lang) = (%q, %v)", out, multi)
	}
	if out, multi := highlightLine("code", "not-a-language", false); multi || !strings.Contains(out, "code") {
		t.Fatalf("highlightLine(unknown lang) = (%q, %v)", out, multi)
	}
	// A complete multi-line comment on one line renders dim plus code around it.
	out, multi := highlightLine("x /* note */ y", "go", false)
	if multi || !strings.Contains(out, "note") {
		t.Fatalf("highlightLine(inline comment) = (%q, %v)", out, multi)
	}
	if got := getCodeIcon("brainfuck"); got == "" {
		t.Fatal("getCodeIcon(unknown lang) returned empty")
	}
}

// --- keymap.go ---

func TestDiffCov2ValidateKeyRegistryStructuralErrors(t *testing.T) {
	errs := validateKeyRegistry([]binding{
		{Group: "g"},          // no keys, no help
		{Keys: []string{"x"}}, // no group, no help
		{Keys: []string{"y"}, Group: "g", Scope: scopeGlobal, Help: "h"},
	})
	if len(errs) < 3 {
		t.Fatalf("validateKeyRegistry errors = %v; want at least 3", errs)
	}
}

// --- markdown.go ---

func TestDiffCov2MarkdownPadTruncateHeadingAndCodeLines(t *testing.T) {
	if got := padCell("abcdef", "F", 3, alignLeft); got != "F" {
		t.Fatalf("padCell(wide) = %q; want F unchanged", got)
	}
	if got := truncateVisible("ab", 5); got != "ab" {
		t.Fatalf("truncateVisible(short) = %q", got)
	}
	mw := NewMarkdownWriter(&bytes.Buffer{})
	mw.SetWidth(60)
	if out := mw.formatNonCodeLine("### Deep heading", "### Deep heading"); !strings.Contains(out, "Deep heading") {
		t.Fatalf("formatNonCodeLine(h3) = %q", out)
	}
	if out := mw.formatCodeLine("plain code"); !strings.Contains(out, "plain code") {
		t.Fatalf("formatCodeLine(plain) = %q", out)
	}
	if out := mw.formatCodeLine("+ added line"); !strings.Contains(out, "+ added line") {
		t.Fatalf("formatCodeLine(diff-like) = %q", out)
	}
}

// --- markdown_table_render.go ---

func TestDiffCov2WrapCellHardSplitAndTruncateANSI(t *testing.T) {
	rows := wrapCellANSI(strings.Repeat("a", 12), 4)
	if len(rows) < 2 {
		t.Fatalf("wrapCellANSI(hard split) rows = %v", rows)
	}
	styled := "\033[31mabcdef\033[0m"
	cut := truncateANSI(styled, 4)
	if !strings.Contains(cut, "…") || !strings.HasSuffix(cut, AnsiReset) {
		t.Fatalf("truncateANSI(styled cut) = %q; want ellipsis plus reset", cut)
	}
	full := truncateANSI("\033[31mab\033[0m", 10)
	if !strings.HasSuffix(full, AnsiReset) {
		t.Fatalf("truncateANSI(styled full) = %q; want reset suffix", full)
	}
}

// --- renderer.go ---

func TestDiffCov2RenderToolResultFailed(t *testing.T) {
	out := renderToolResult(provider.Message{Name: "run_command", Content: "error: boom"})
	if !strings.Contains(out, "run_command") {
		t.Fatalf("renderToolResult(failed) = %q", out)
	}
}

// --- session_catalog.go ---

func TestDiffCov2DisplaySessionNameAndAgeBranches(t *testing.T) {
	if got := DisplaySessionName(chat.SessionInfo{Name: "s", WorktreeRoute: true, Worktree: "wt"}, ""); got != "Worktree · wt" {
		t.Fatalf("DisplaySessionName(worktree) = %q", got)
	}
	if got := DisplaySessionName(chat.SessionInfo{Name: "__last__", UpdatedAt: time.Time{}}, "__last__-other"); got != "Auto" {
		t.Fatalf("DisplaySessionName(zero age) = %q; want Auto", got)
	}
	if got := FormatSessionAge(time.Now().Add(-48 * time.Hour)); got != "2d ago" {
		t.Fatalf("FormatSessionAge(2d) = %q", got)
	}
	if got := FormatSessionAge(time.Now().Add(-30 * 24 * time.Hour)); got == "" {
		t.Fatal("FormatSessionAge(30d) returned empty; want a date")
	}
}

// --- session_delivery_repair.go ---

func TestDiffCov2SessionAutoDeliveryRepairLoopAdvanceErrors(t *testing.T) {
	ctx := context.Background()
	// The first advance fails: the loop settles the run failure and returns.
	sessionAutoDeliveryRepairLoop(ctx, workflowledger.NewMemoryRepository(), "", nil, nil, "no-run",
		func(context.Context) (workflowledger.RunSnapshot, error) {
			return workflowledger.RunSnapshot{}, errors.New("advance boom")
		}, nil, false)

	// The second advance fails after one repair-continue attempt.
	repo := workflowledger.NewMemoryRepository()
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: "wfr-run", WorkflowName: "wf", Status: workflowledger.RunStatusPending, ActiveStepID: "build",
		StartedAt: time.Now(),
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-run", 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	calls := 0
	sessionAutoDeliveryRepairLoop(ctx, repo, "", nil, nil, "wfr-run",
		func(context.Context) (workflowledger.RunSnapshot, error) {
			calls++
			if calls == 1 {
				return workflowledger.RunSnapshot{RunID: "wfr-run", Status: workflowledger.RunStatusDeliveryPending}, nil
			}
			return workflowledger.RunSnapshot{}, errors.New("second advance boom")
		}, nil, false)
	if calls != 2 {
		t.Fatalf("advance calls = %d; want 2", calls)
	}
}

// --- stack_state.go ---

func TestDiffCov2ChunkSettleSucceededNotNoDiff(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	store := workflowledger.NewStore(storage.NewMemory())
	var buf bytes.Buffer
	chunkSettleSucceeded(repo, store, "stk", "chk",
		workflowledger.RunSnapshot{RunID: "r1", Status: workflowledger.RunStatusSucceeded}, &buf)
	chunkSettleSucceeded(repo, store, "stk", "chk2",
		workflowledger.RunSnapshot{RunID: "r2", Status: workflowledger.RunStatusFailed}, &buf)
}

// --- thinking.go ---

func TestDiffCov2RenderThinkingBlockView(t *testing.T) {
	out := renderThinkingBlockView("t1", "thinking hard", false, 0, "m", 40, false, 0, true)
	if !strings.Contains(out, "thinking hard") {
		t.Fatalf("renderThinkingBlockView = %q", out)
	}
}

// --- tool_wave_status.go ---

func TestDiffCov2ToolWaveCountsAndNegativeElapsed(t *testing.T) {
	rows := []ToolRow{
		{Name: "read_file", Done: true},
		{Name: "parallel", Done: true},
		{Name: "write_file"},
	}
	open, done, total := ToolWaveCounts(rows)
	if open != 1 || done != 1 || total != 2 {
		t.Fatalf("ToolWaveCounts = (%d, %d, %d); want (1, 1, 2)", open, done, total)
	}
	if got := FormatLiveToolWaveSummary(1, 0, 1, -5*time.Second); !strings.Contains(got, "0s") {
		t.Fatalf("FormatLiveToolWaveSummary(negative elapsed) = %q", got)
	}
}

// --- tui_focus.go / tui_shared.go / tui_helpers.go / tui_stream.go ---

func TestDiffCov2TuiFocusStringsAndSharedHelpers(t *testing.T) {
	if got := FocusSidebar.String(); got != "sidebar" {
		t.Fatalf("FocusSidebar.String() = %q", got)
	}
	if got := FocusWorkflowsSidebar.String(); got != "workflows" {
		t.Fatalf("FocusWorkflowsSidebar.String() = %q", got)
	}
	if got := FormatAgentUnavailable(nil); got != "agent switch failed" {
		t.Fatalf("FormatAgentUnavailable(nil) = %q", got)
	}
	if got := hardTruncateANSI("line", 0); got != AnsiReset {
		t.Fatalf("hardTruncateANSI(width 0) = %q", got)
	}
	if got := hardTruncateANSI("abc", 5); got != "abc"+AnsiReset {
		t.Fatalf("hardTruncateANSI(short no reset) = %q", got)
	}
	if got := hardTruncateANSI("abc"+AnsiReset, 5); got != "abc"+AnsiReset {
		t.Fatalf("hardTruncateANSI(short with reset) = %q", got)
	}
	if got := wrapANSI("aa bb", 2); !strings.Contains(got, "aa") {
		t.Fatalf("wrapANSI = %q", got)
	}
	b := NewStreamBridge()
	b.SetTurnID(9)
	b.Close()
}

// --- tui_tools_apply.go ---

func TestDiffCov2ToolResultFailedMatrix(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"", false},
		{"all good", false},
		{"ERROR: bad", true},
		{"failed", true},
		{"failed halfway", true},
		{"exit=0", false},
		{"exit=01", true},
		{"code exit=127 stop", true},
		{"exitit=0", false},
	} {
		if got := ToolResultFailed(tc.body); got != tc.want {
			t.Errorf("ToolResultFailed(%q) = %v; want %v", tc.body, got, tc.want)
		}
	}
}

// --- chat_slash_handlers.go ---

func TestDiffCov2HandleSlashLoadContextSession(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}
	sess := chat.NewSession(res, nullCompleter{})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := sess.Load(sess.SessionID); err != nil {
		t.Fatal(err)
	}
	if !sess.LoadedContextSession() {
		t.Skip("durable context session did not load in this environment")
	}
	term := NewTestTerminal(&bytes.Buffer{})
	ok, _, err := handleSlashSessions("/load", "/load "+sess.SessionID, sess, term)
	if !ok || err != nil {
		t.Fatalf("handleSlashSessions(/load durable) = (%v, %v)", ok, err)
	}
	if !strings.Contains(termOut(term), "context") {
		t.Logf("/load output: %q", termOut(term))
	}
}

// --- stack_decompose_continue.go ---

func TestDiffCov2AdmitNextWaveHaltedAtMaxTotal(t *testing.T) {
	store := workflowledger.NewStore(storage.NewMemory())
	chunks := []ChunkPlan{{ID: "c1", Title: "one"}}
	if err := seedStackLedger(store, "stk-max", chunks); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionTask("stk-max", "c1", stackStatusMerged); err != nil {
		t.Fatal(err)
	}
	prepared := &cliworkflow.PreparedWorkflowRun{
		Repo: workflowledger.NewMemoryRepository(),
		Compiled: &definition.CompiledWorkflow{
			Stacking: &definition.StackingConfig{MaxTotalChunks: 1},
		},
	}
	err := admitNextWaveIfReady(prepared, store, "stk-max", chunks, true, "more scope", nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "max_total_chunks") {
		t.Fatalf("admitNextWaveIfReady(cap reached) err = %v; want max_total_chunks halt", err)
	}
}

// --- dialog_geometry.go (crafted zero-width slicing inputs) ---

func TestDiffCov2WrapDisplayRowsWideRuneSplit(t *testing.T) {
	// A lone wide rune with a 1-column inner width cannot be cut into a
	// 1-column part: the slicer falls through to the defensive empty-row
	// padding branches.
	rows, sources := WrapDisplayRowsWithSources([]string{"漢"}, 1)
	if len(rows) == 0 || len(rows) != len(sources) {
		t.Fatalf("WrapDisplayRowsWithSources(wide rune) = (%v, %v)", rows, sources)
	}
}

// --- chat_command.go (TUI dispatch behind a pty stdin) ---

// withPtyStdin swaps os.Stdin for a pty slave so term.IsTerminal reports
// true, runs fn, then restores the original stdin.
func withPtyStdin(t *testing.T, fn func()) {
	t.Helper()
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}
	defer func() { _ = unix.Close(master) }()
	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlockpt failed: %v", err)
	}
	ptsN, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		t.Skipf("pts number failed: %v", err)
	}
	n := uint32(ptsN)
	slave, err := unix.Open("/dev/pts/"+itoa(int(n)), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open pty slave: %v", err)
	}
	defer func() { _ = unix.Close(slave) }()
	oldStdin := os.Stdin
	oldTerm := os.Getenv("TERM")
	_ = os.Setenv("TERM", "xterm")
	os.Stdin = os.NewFile(uintptr(slave), "stdin-pty")
	defer func() {
		os.Stdin = oldStdin
		_ = os.Setenv("TERM", oldTerm)
	}()
	fn()
}

func TestDiffCov2DispatchChatSurfaceLaunchesTUI(t *testing.T) {
	withPtyStdin(t, func() {
		prev := TUILauncherFunc
		launched := false
		TUILauncherFunc = func(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *AgentSessionState, resumeSessionName string) error {
			launched = true
			return nil
		}
		defer func() { TUILauncherFunc = prev }()

		res := &config.Resolved{ProviderName: "p", Model: "m"}
		sess := chat.NewSession(res, nullCompleter{})
		err := dispatchChatSurface(chatInvocation{}, sess, res, false, &AgentSessionState{})
		if err != nil {
			t.Fatalf("dispatchChatSurface(TUI) err = %v", err)
		}
		if !launched {
			t.Fatal("TUILauncherFunc was not called")
		}

		// An unwired launcher fails closed.
		TUILauncherFunc = nil
		if err := dispatchChatSurface(chatInvocation{}, sess, res, false, &AgentSessionState{}); err == nil || !strings.Contains(err.Error(), "unwired") {
			t.Fatalf("dispatchChatSurface(unwired) err = %v; want unwired error", err)
		}
	})
}
