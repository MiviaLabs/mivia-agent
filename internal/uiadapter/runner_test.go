package uiadapter_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// TestCommandRunner_PoolExposesSameSubagentThreadsWiredToInitialConversation
// guards the buildApp wiring bug where the screen's SubagentThreads
// registry and the CommandRunner's own pooled Conversation for the same
// session were two different, separately-constructed objects: the pool
// must be reachable from the runner, and its initial Conversation must
// already be wired to pool.Threads() (see SessionPool tests for the
// deeper History()-driven regression coverage).
func TestCommandRunner_PoolExposesSameSubagentThreadsWiredToInitialConversation(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "session-1"

	runner := uiadapter.NewCommandRunner(sess, res, nil)

	pool := runner.Pool()
	if pool == nil {
		t.Fatal("expected runner.Pool() to be non-nil")
	}
	conv, err := pool.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if conv == nil {
		t.Fatal("expected a pooled conversation for the initial session")
	}
	if pool.Threads() == nil {
		t.Fatal("expected pool.Threads() to be non-nil")
	}
}

func TestCommandRunner_RunBasicCommands(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)

	// Theme
	if out := runner.Run(context.Background(), "theme", ""); !out.OpenTheme {
		t.Errorf("expected OpenTheme=true, got %+v", out)
	}

	// Help
	if out := runner.Run(context.Background(), "help", ""); !out.OpenHelp {
		t.Errorf("expected OpenHelp=true, got %+v", out)
	}

	// Queue
	if out := runner.Run(context.Background(), "queue", ""); !out.OpenQueue {
		t.Errorf("expected OpenQueue=true, got %+v", out)
	}

	// Settings
	if out := runner.Run(context.Background(), "settings", "models"); !out.OpenSettings || out.SettingsSection != "models" {
		t.Errorf("expected OpenSettings=true with models section, got %+v", out)
	}

	// Quit
	if out := runner.Run(context.Background(), "quit", ""); !out.Quit {
		t.Errorf("expected Quit=true, got %+v", out)
	}
	if out := runner.Run(context.Background(), "exit", ""); !out.Quit {
		t.Errorf("expected Quit=true on exit, got %+v", out)
	}

	// Unknown
	if out := runner.Run(context.Background(), "foobar", "baz"); out.Err == "" {
		t.Errorf("expected Err on unknown command, got %+v", out)
	}
}

func TestCommandRunner_NilSessionErrors(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)

	if out := runner.Run(context.Background(), "clear", ""); out.Err == "" {
		t.Errorf("expected Err on nil session clear, got %+v", out)
	}
	if out := runner.Run(context.Background(), "compact", ""); out.Err == "" {
		t.Errorf("expected Err on nil session compact, got %+v", out)
	}
	if out := runner.Run(context.Background(), "context", ""); out.Err == "" {
		t.Errorf("expected Err on nil session context, got %+v", out)
	}
	if out := runner.Run(context.Background(), "cost", ""); out.Err == "" {
		t.Errorf("expected Err on nil session cost, got %+v", out)
	}
	if out := runner.Run(context.Background(), "model", ""); out.Err == "" {
		t.Errorf("expected Err on nil session model, got %+v", out)
	}
	if out := runner.SelectModel(context.Background(), "m1"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectModel, got %+v", out)
	}
	if out := runner.SelectAgent(context.Background(), "a1"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectAgent, got %+v", out)
	}
	if out := runner.Run(context.Background(), "resume", ""); out.Err == "" {
		t.Errorf("expected Err on nil session resume, got %+v", out)
	}
	if out := runner.Run(context.Background(), "effort", ""); out.Err == "" {
		t.Errorf("expected Err on nil session effort, got %+v", out)
	}
	if out := runner.SelectEffort(context.Background(), "low"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectEffort, got %+v", out)
	}
	if out := runner.Run(context.Background(), "yolo", ""); out.Err == "" {
		t.Errorf("expected Err on nil session yolo, got %+v", out)
	}
	if out := runner.SelectSession(context.Background(), "s1"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectSession, got %+v", out)
	}
}

func TestCommandRunner_New_NilSessionReturnsError(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)
	out := runner.Run(context.Background(), "new", "")
	if out.Err == "" {
		t.Errorf("expected Err on nil session /new, got %+v", out)
	}
}

func TestCommandRunner_New_SavesAndReturnsNewConversation(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	sess.SessionID = "original-sess"
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "new", "")
	if out.Err != "" {
		t.Fatalf("/new returned error: %s", out.Err)
	}
	if out.Conversation == nil {
		t.Fatal("/new must return a non-nil Conversation")
	}
	if !out.ClearTranscript {
		t.Error("/new must set ClearTranscript=true")
	}
	if out.Notice == "" {
		t.Error("/new must set a non-empty Notice")
	}
	// The returned conversation must have a different ID from the original
	if out.Conversation.ID() == "original-sess" || out.Conversation.ID() == "" {
		t.Errorf("/new conversation ID must be fresh, got %q", out.Conversation.ID())
	}
}

func TestCommandRunner_New_InDefaultCommands(t *testing.T) {
	cmds := uiadapter.DefaultCommands()
	for _, c := range cmds {
		if c.Name == "new" {
			return
		}
	}
	t.Error("/new must appear in DefaultCommands()")
}

func TestCommandRunner_ClearAndUsage(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Clear
	if out := runner.Run(context.Background(), "clear", ""); !out.ClearTranscript {
		t.Errorf("expected ClearTranscript=true, got %+v", out)
	}

	// Context
	if out := runner.Run(context.Background(), "context", ""); out.Notice == "" {
		t.Errorf("expected Notice for context, got %+v", out)
	}

	// Cost
	if out := runner.Run(context.Background(), "cost", ""); out.Notice == "" {
		t.Errorf("expected Notice for cost, got %+v", out)
	}
}

func TestCommandRunner_ModelSwitching(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{
		ProviderName: "zai",
		Model:        "glm-5.2",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"zai": {
				ProviderName: "zai",
				APIKey:       "test-key",
				APIKeySet:    true,
				Models: []config.ModelSpec{
					{Name: "glm-5.2", ContextWindowTokens: 2000},
				},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider:   "zai",
			Selectable: true,
			Active:     true,
			Models: []config.ModelSpec{
				{Name: "glm-5.2", ContextWindowTokens: 2000},
			},
		},
	})
	sess := chat.NewSession(res, comp)
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return chat.ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    comp,
			Profile:      config.ModelSpec{Name: model, ContextWindowTokens: 2000},
		}, nil
	})

	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Open model picker
	out := runner.Run(context.Background(), "model", "")
	if len(out.ModelChoiceGroups) == 0 {
		t.Fatalf("expected ModelChoiceGroups, got %+v", out)
	}

	// Direct switch via /model glm-5.2
	out = runner.Run(context.Background(), "model", "glm-5.2")
	if out.Err != "" {
		t.Fatalf("Run model glm-5.2 error: %s", out.Err)
	}
	if sess.CurrentModel() != "glm-5.2" {
		t.Errorf("got current model %q, want %q", sess.CurrentModel(), "glm-5.2")
	}

	// SelectModel direct
	out = runner.SelectModel(context.Background(), "glm-5.2")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %s", out.Err)
	}
	if out.Notice == "" {
		t.Errorf("expected confirmation notice on SelectModel, got %+v", out)
	}

	// SelectModel error
	out = runner.SelectModel(context.Background(), "invalid-model")
	if out.Err == "" {
		t.Errorf("expected error on invalid model switch, got %+v", out)
	}
}

func TestCommandRunner_AgentSwitching(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{
		Name:         "reviewer",
		Description:  "code reviewer",
		SystemPrompt: "you are a reviewer",
	})
	_ = reg.Publish(agents.ResolvedAgent{
		Name:         "coder",
		Description:  "software engineer",
		SystemPrompt: "you are a coder",
	})

	state := &cliagents.AgentSessionState{
		Registry: reg,
		ToolBase: tools.NewRegistry(),
	}

	runner := uiadapter.NewCommandRunner(sess, res, state)

	// List choices
	out := runner.Run(context.Background(), "agents", "")
	if len(out.AgentChoices) < 2 {
		t.Fatalf("expected at least 2 AgentChoices, got %+v", out)
	}

	// Direct switch via /agent reviewer
	out = runner.Run(context.Background(), "agent", "reviewer")
	if out.Err != "" {
		t.Fatalf("Run agent reviewer error: %s", out.Err)
	}

	// SelectAgent
	out = runner.SelectAgent(context.Background(), "reviewer")
	if out.Err != "" {
		t.Fatalf("SelectAgent error: %s", out.Err)
	}
	if out.Notice == "" {
		t.Errorf("expected confirmation notice, got %+v", out)
	}

	// SelectAgent empty name
	if out := runner.SelectAgent(context.Background(), ""); out.Err == "" {
		t.Errorf("expected error on empty agent name, got %+v", out)
	}

	// SelectAgent unknown
	if out := runner.SelectAgent(context.Background(), "unknown"); out.Err == "" {
		t.Errorf("expected error on unknown agent, got %+v", out)
	}
}

func setupSessionStoreFixture(t *testing.T) (*chat.Session, *config.Resolved, *storage.SQLite, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "uiadapter-runner-test-*")
	if err != nil {
		t.Fatal(err)
	}
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)

	store, err := storage.OpenSQLite(tmpDir + "/context.db")
	if err != nil {
		t.Fatal(err)
	}

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

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return sess, res, store, cleanup
}

// TestCommandRunner_ResumeSession_ReflectsRealActivity guards the fix for
// the /resume status dot: SessionChoices must not hardcode the current
// session as Active ("LIVE" regardless of whether anything is running),
// and must still mark it as IsCurrent so the picker can distinguish "this
// is where you are" from "this session has a turn running".
// TestCommandRunner_SessionActive proves the method the /resume picker's
// live-refresh loop polls: cheap, per-session, backed directly by the
// pool - no DB access, no dependency on listSessionSummaries.
func TestCommandRunner_SessionActive(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	if runner.SessionActive(sess.SessionID) {
		t.Error("SessionActive=true with no turn in flight")
	}
	if runner.SessionActive("never-loaded") {
		t.Error("SessionActive=true for an ID the pool never loaded")
	}
}

func TestCommandRunner_ResumeSession_ReflectsRealActivity(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	if err := sess.Save("saved-1"); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}
	// Saving under the session's own ID is the live-projection path
	// (context_catalog.go: "a save under the live session's own id ...
	// names the live context session it projects") - it is what makes
	// ListSessions surface a row whose SessionID equals sess.SessionID,
	// which is what IsCurrent keys off.
	if err := sess.Save(sess.SessionID); err != nil {
		t.Fatalf("failed to save live session: %v", err)
	}

	out := runner.Run(context.Background(), "resume", "")
	if len(out.SessionChoices) == 0 {
		t.Fatalf("expected SessionChoices, got %+v", out)
	}

	sawCurrent := false
	for _, c := range out.SessionChoices {
		if c.Active {
			t.Errorf("session %q reported Active=true with no turn in flight", c.ID)
		}
		if c.ID == sess.SessionID {
			sawCurrent = true
			if !c.IsCurrent {
				t.Errorf("current session %q reported IsCurrent=false", c.ID)
			}
		} else if c.IsCurrent {
			t.Errorf("non-current session %q reported IsCurrent=true", c.ID)
		}
	}
	if !sawCurrent {
		t.Fatalf("current session %q not found in SessionChoices: %+v", sess.SessionID, out.SessionChoices)
	}
}

func TestCommandRunner_ResumeSession(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Resume on empty store
	if out := runner.Run(context.Background(), "resume", ""); out.Err == "" {
		t.Errorf("expected error or no sessions on empty store, got %+v", out)
	}

	// Save a session so resume has content
	if err := sess.Save("saved-1"); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Run resume list
	out := runner.Run(context.Background(), "resume", "")
	if len(out.SessionChoices) == 0 {
		t.Fatalf("expected SessionChoices, got %+v", out)
	}

	// Direct resume via /resume saved-1
	if out := runner.Run(context.Background(), "resume", "saved-1"); out.Err != "" {
		t.Fatalf("Run resume saved-1 error: %s", out.Err)
	}

	// SelectSession empty
	if out := runner.SelectSession(context.Background(), ""); out.Err == "" {
		t.Errorf("expected error on empty session ID, got %+v", out)
	}

	// SelectSession unknown
	if out := runner.SelectSession(context.Background(), "nonexistent"); out.Err == "" {
		t.Errorf("expected error on nonexistent session, got %+v", out)
	}

	// SelectSession
	out = runner.SelectSession(context.Background(), "saved-1")
	if out.Err != "" {
		t.Fatalf("SelectSession error: %s", out.Err)
	}
	if !out.ClearTranscript {
		t.Error("SelectSession must set ClearTranscript=true")
	}
}

func TestCommandRunner_Compact(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Send a turn and add history for compaction
	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		sess.Messages = append(sess.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("long question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("long answer ", 20)},
		)
	}

	// Compact
	out := runner.Run(context.Background(), "compact", "")
	if out.Err != "" {
		t.Fatalf("compact error: %s", out.Err)
	}
	if out.Notice == "" {
		t.Errorf("expected Notice for compact, got %+v", out)
	}
}

func TestDefaultCommands(t *testing.T) {
	cmds := uiadapter.DefaultCommands()
	if len(cmds) == 0 {
		t.Fatal("expected non-empty DefaultCommands")
	}
	var foundModel, foundAgents, foundResume, foundEffort, foundHooks bool
	for _, c := range cmds {
		switch c.Name {
		case "model":
			foundModel = true
		case "agents":
			foundAgents = true
		case "resume":
			foundResume = true
		case "effort":
			foundEffort = true
		case "hooks":
			foundHooks = true
		}
	}
	if !foundModel || !foundAgents || !foundResume || !foundEffort || !foundHooks {
		t.Errorf("missing core commands in DefaultCommands: %+v", cmds)
	}
}

// /hooks must be reachable from the new TUI without a session configured -
// unwired is exactly the failure mode this command exists to fix (a
// silent, unrouted "unknown command").
func TestCommandRunner_HooksWithNoneConfigured(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)
	out := runner.Run(context.Background(), "hooks", "")
	if out.Err != "" {
		t.Fatalf("hooks: unexpected error %q", out.Err)
	}
	if !strings.Contains(strings.ToLower(out.Notice), "no lifecycle hooks") {
		t.Fatalf("expected a no-hooks-configured notice, got %+v", out)
	}
}

// /hooks must reflect a real installed session, so the listing an operator
// sees is what the dispatcher actually runs - not a static help string.
func TestCommandRunner_HooksListsArmedHooks(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(ws+"/.mivia", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws+"/.mivia/mivia.toml", []byte(`[[hooks]]
event = "PostToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./fmt.sh"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := hooksession.Load(ws)
	if err != nil {
		t.Fatalf("hooksession.Load: %v", err)
	}
	t.Cleanup(hooksession.SetForTest(session))

	runner := uiadapter.NewCommandRunner(nil, nil, nil)
	out := runner.Run(context.Background(), "hooks", "")
	if !strings.Contains(out.Notice, "fmt.sh") {
		t.Fatalf("expected the armed hook in the listing, got %+v", out)
	}
}

// /hooks trust must still answer with the removal notice on the new TUI,
// matching the old clichat surface.
func TestCommandRunner_HooksTrustArgument(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)
	out := runner.Run(context.Background(), "hooks", "trust 1")
	if !strings.Contains(strings.ToLower(out.Notice), "removed") {
		t.Fatalf("expected the trust-removed notice, got %+v", out)
	}
}

func TestCommandRunner_Effort(t *testing.T) {
	// Model without reasoning
	resNoReasoning := &config.Resolved{
		ProviderName: "test",
		Model:        "basic-model",
	}
	sessNoReasoning := chat.NewSession(resNoReasoning, nil)
	runnerNoReasoning := uiadapter.NewCommandRunner(sessNoReasoning, resNoReasoning, nil)

	out := runnerNoReasoning.Run(context.Background(), "effort", "")
	if !strings.Contains(out.Notice, "declares no reasoning efforts") {
		t.Errorf("expected no reasoning efforts notice, got %+v", out)
	}

	// Model with reasoning
	resWithReasoning := &config.Resolved{
		ProviderName: "anthropic",
		Model:        "claude-3-7-sonnet",
		Models:       []string{"claude-3-7-sonnet"},
		ModelProfiles: []config.ModelSpec{
			{
				Name:             "claude-3-7-sonnet",
				ReasoningEfforts: []reasoning.Level{"low", "medium", "high", "max"},
				Reasoning:        "medium",
			},
		},
	}
	sessWithReasoning := chat.NewSession(resWithReasoning, nil)
	runnerWithReasoning := uiadapter.NewCommandRunner(sessWithReasoning, resWithReasoning, nil)

	// List choices
	outChoices := runnerWithReasoning.Run(context.Background(), "effort", "")
	if len(outChoices.EffortChoices) == 0 {
		t.Fatalf("expected EffortChoices, got %+v", outChoices)
	}

	// Direct arg with level
	outDirect := runnerWithReasoning.Run(context.Background(), "effort", "low")
	if !strings.Contains(outDirect.Notice, "low") {
		t.Errorf("expected direct arg notice, got %+v", outDirect)
	}

	// Direct invalid arg
	outInvalid := runnerWithReasoning.Run(context.Background(), "effort", "super-ultra-high")
	if outInvalid.Err == "" {
		t.Errorf("expected error on invalid effort, got %+v", outInvalid)
	}

	// Apply choice with default marker suffix stripped
	outSet := runnerWithReasoning.SelectEffort(context.Background(), "high")
	if !strings.Contains(outSet.Notice, "high") {
		t.Errorf("expected high notice, got %+v", outSet)
	}

	// Apply unset
	outUnset := runnerWithReasoning.SelectEffort(context.Background(), "unset")
	if !strings.Contains(outUnset.Notice, "cleared") {
		t.Errorf("expected cleared notice, got %+v", outUnset)
	}
}

func TestCommandRunner_YoloToggle(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "test",
		Model:        "test-model",
	}
	sess := chat.NewSession(res, nil)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// First toggle: enable YOLO
	out1 := runner.Run(context.Background(), "yolo", "")
	if out1.Err != "" {
		t.Fatalf("unexpected err: %v", out1.Err)
	}
	if !strings.Contains(out1.Notice, "enabled") {
		t.Errorf("expected enabled notice, got %q", out1.Notice)
	}
	if sess.ApprovalPolicy != config.ApprovalPolicyAuto {
		t.Errorf("expected sess.ApprovalPolicy = %q, got %q", config.ApprovalPolicyAuto, sess.ApprovalPolicy)
	}

	// Second toggle: disable YOLO
	out2 := runner.Run(context.Background(), "yolo", "")
	if out2.Err != "" {
		t.Fatalf("unexpected err: %v", out2.Err)
	}
	if !strings.Contains(out2.Notice, "disabled") {
		t.Errorf("expected disabled notice, got %q", out2.Notice)
	}
	if sess.ApprovalPolicy != config.ApprovalPolicyWriteOnly {
		t.Errorf("expected sess.ApprovalPolicy = %q, got %q", config.ApprovalPolicyWriteOnly, sess.ApprovalPolicy)
	}
}

// twoModelOllamaCatalogConfig returns a config.Resolved advertising models m1
// and m2 on the ollama provider - the fixture shape
// TestCommandRunner_SelectModel_InResumedSession needs to make SelectModel's
// cross-session resolution actually see both models as selectable.
func twoModelOllamaCatalogConfig() *config.Resolved {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "m1",
		Models:       []string{"m1", "m2"},
		ModelProfiles: []config.ModelSpec{
			{Name: "m1", ContextWindowTokens: 128000},
			{Name: "m2", ContextWindowTokens: 128000},
		},
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models: []config.ModelSpec{
					{Name: "m1", ContextWindowTokens: 128000},
					{Name: "m2", ContextWindowTokens: 128000},
				},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider:   "ollama",
			Selectable: true,
			Active:     true,
			Models: []config.ModelSpec{
				{Name: "m1", ContextWindowTokens: 128000},
				{Name: "m2", ContextWindowTokens: 128000},
			},
		},
	})
	return res
}

// bindRunnerTestContextSession wires sess to a shared durable context store,
// the fixture two or more sessions in the same test need to resume each
// other via the same catalog.
func bindRunnerTestContextSession(t *testing.T, sess *chat.Session, store *storage.SQLite) {
	t.Helper()
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
}

func TestCommandRunner_SelectModel_InResumedSession(t *testing.T) {
	dir := t.TempDir()
	res := twoModelOllamaCatalogConfig()

	store, err := storage.OpenSQLite(dir + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess1 := chat.NewSession(res, nil)
	sess1.SessionID = "session-1"
	bindRunnerTestContextSession(t, sess1, store)

	sess2 := chat.NewSession(res, nil)
	sess2.SessionID = "session-2"
	bindRunnerTestContextSession(t, sess2, store)
	sess2.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "Hello in session 2"},
	}
	if err := sess2.Save("session-2"); err != nil {
		t.Fatalf("failed to save session-2: %v", err)
	}

	pool := uiadapter.NewSessionPool(sess1, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess1, pool, res, nil)

	// Resume session-2
	out := runner.SelectSession(context.Background(), "session-2")
	if out.Err != "" {
		t.Fatalf("SelectSession error: %v", out.Err)
	}
	resumedConv := out.Conversation
	if resumedConv == nil {
		t.Fatal("expected non-nil resumed Conversation")
	}

	// Switch model to m2
	out = runner.SelectModel(context.Background(), "m2")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}

	// Verify the resumed conversation's model was updated to m2
	if got := resumedConv.Model().Name; got != "m2" {
		t.Errorf("resumed conversation model = %q, want %q", got, "m2")
	}
}

// TestCommandRunner_SelectModel_AmbiguousNameAcrossProvidersRefuses pins the
// fix for a confirmed bug: resolveProviderAndModel's cross-provider search
// used to return the FIRST provider whose catalog happened to contain the
// requested name, in catalog order, with no check for a second match - a
// silent, order-dependent provider switch (different auth, base URL, and
// wire behavior) on nothing but a name coincidence. This reproduces the
// real-world collision (a model name, e.g. "claude-sonnet-5", declared
// under two different providers) and asserts SelectModel refuses rather
// than guessing, naming both providers so the user can pick explicitly.
func TestCommandRunner_SelectModel_AmbiguousNameAcrossProvidersRefuses(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmproxycli": {
				ProviderName: "llmproxycli",
				APIKey:       "test-key",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "claude-sonnet-5"}},
			},
			"anthropic": {
				ProviderName: "anthropic",
				APIKey:       "sk-ant-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "claude-sonnet-5"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "llmproxycli", Selectable: true, Models: []config.ModelSpec{{Name: "claude-sonnet-5"}}},
		{Provider: "anthropic", Selectable: true, Models: []config.ModelSpec{{Name: "claude-sonnet-5"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "claude-sonnet-5")

	if out.Err == "" {
		t.Fatalf("SelectModel(ambiguous name) must refuse, got a successful switch: %+v", out)
	}
	if sess.CurrentSelection().ProviderName == "llmproxycli" || sess.CurrentSelection().ProviderName == "anthropic" {
		t.Fatalf("SelectModel must not silently switch to either ambiguous provider, session provider = %q", sess.CurrentSelection().ProviderName)
	}
	if !strings.Contains(out.Err, "llmproxycli") || !strings.Contains(out.Err, "anthropic") {
		t.Fatalf("error must name both ambiguous providers, got %q", out.Err)
	}
	if !strings.Contains(out.Err, "/model <provider> claude-sonnet-5") {
		t.Fatalf("error must give the exact disambiguating command, got %q", out.Err)
	}
}

// TestCommandRunner_SelectModel_UniqueNameAcrossProvidersStillSwitches
// confirms the fix doesn't regress the working case: a name found in
// exactly one Selectable provider still resolves and switches normally.
func TestCommandRunner_SelectModel_UniqueNameAcrossProvidersStillSwitches(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"anthropic": {
				ProviderName: "anthropic",
				APIKey:       "sk-ant-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "claude-sonnet-5"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "anthropic", Selectable: true, Models: []config.ModelSpec{{Name: "claude-sonnet-5"}}},
	})
	sess := chat.NewSession(res, nil)
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return chat.ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    &nullCompleter{},
			Profile:      config.ModelSpec{Name: model},
		}, nil
	})
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "claude-sonnet-5")

	if out.Err != "" {
		t.Fatalf("SelectModel(unique name) must succeed, got error: %v", out.Err)
	}
	if got := sess.CurrentSelection().ProviderName; got != "anthropic" {
		t.Fatalf("session provider = %q, want anthropic", got)
	}
}

func TestCommandRunner_SelectModel_ProviderPrefix(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models: []config.ModelSpec{
					{Name: "model-a"},
				},
			},
			"openrouter": {
				ProviderName: "openrouter",
				APIKey:       "sk-or-v1-test",
				APIKeySet:    true,
				Models: []config.ModelSpec{
					{Name: "model-b"},
				},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider:   "ollama",
			Selectable: true,
			Active:     true,
			Models: []config.ModelSpec{
				{Name: "model-a"},
			},
		},
		{
			Provider:   "openrouter",
			Selectable: true,
			Models: []config.ModelSpec{
				{Name: "model-b"},
			},
		},
	})
	sess := chat.NewSession(res, nil)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Switch with explicit provider prefix
	out := runner.SelectModel(context.Background(), "openrouter/model-b")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	if got := sess.CurrentModel(); got != "model-b" {
		t.Errorf("got current model %q, want %q", got, "model-b")
	}
	if got := sess.CurrentSelection().ProviderName; got != "openrouter" {
		t.Errorf("got provider name %q, want %q", got, "openrouter")
	}
}

func TestCommandRunner_SkillsIntegration(t *testing.T) {
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name:             "feature-delivery",
		ShortDescription: "Deliver an end-to-end feature with tests",
		UserInvocable:    true,
		Instructions:     "Follow feature delivery guidelines carefully.",
	})
	_ = skillReg.Register(skills.Definition{
		Name:          "internal-helper",
		Description:   "internal background skill",
		UserInvocable: false, // Not user invocable
		Instructions:  "internal instructions",
	})

	state := &cliagents.AgentSessionState{
		SkillRegFull: skillReg,
	}

	runner := uiadapter.NewCommandRunner(nil, nil, state)

	// 1. Check runner.Commands() includes invocable skill and excludes non-invocable
	cmds := runner.Commands()
	var foundFeature, foundInternal bool
	for _, c := range cmds {
		if c.Name == "feature-delivery" {
			foundFeature = true
			if c.Desc != "Deliver an end-to-end feature with tests" {
				t.Errorf("got desc %q, want expected short description", c.Desc)
			}
		}
		if c.Name == "internal-helper" {
			foundInternal = true
		}
	}
	if !foundFeature {
		t.Error("expected feature-delivery in runner.Commands()")
	}
	if foundInternal {
		t.Error("internal-helper should NOT appear in runner.Commands() because UserInvocable=false")
	}

	// 2. Execute invocable skill slash command
	out := runner.Run(context.Background(), "feature-delivery", "add auth module")
	if out.Err != "" {
		t.Fatalf("unexpected error for feature-delivery: %v", out.Err)
	}
	if out.SubmitPrompt == "" {
		t.Fatal("expected non-empty SubmitPrompt")
	}
	if !strings.Contains(out.SubmitPrompt, "Follow feature delivery guidelines") {
		t.Errorf("SubmitPrompt missing instructions: %s", out.SubmitPrompt)
	}
	if !strings.Contains(out.SubmitPrompt, `name="feature-delivery"`) {
		t.Errorf("SubmitPrompt missing skill name tag: %s", out.SubmitPrompt)
	}
	if !strings.Contains(out.SubmitPrompt, "add auth module") {
		t.Errorf("SubmitPrompt missing arguments: %s", out.SubmitPrompt)
	}

	// 3. Execute non-invocable skill slash command
	outNonInvocable := runner.Run(context.Background(), "internal-helper", "")
	if outNonInvocable.Err == "" || !strings.Contains(outNonInvocable.Err, "cannot be invoked directly") {
		t.Errorf("expected cannot be invoked directly error, got %+v", outNonInvocable)
	}

	// 4. Execute unknown command
	outUnknown := runner.Run(context.Background(), "nonexistent-skill", "")
	if outUnknown.Err == "" || !strings.Contains(outUnknown.Err, "unknown command") {
		t.Errorf("expected unknown command error, got %+v", outUnknown)
	}
}

// TestCommandRunner_SkillInvocationPersistsTheShortForm pins the fix for a
// real context-bloat bug: a skill invocation must persist the short command
// the user typed, not the full expanded instructions body - see
// intent.Send.PersistedText's doc comment for why (the full body is
// thousands of tokens replayed on every later turn otherwise).
func TestCommandRunner_SkillInvocationPersistsTheShortForm(t *testing.T) {
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name:             "feature-delivery",
		ShortDescription: "Deliver an end-to-end feature with tests",
		UserInvocable:    true,
		Instructions:     "Follow feature delivery guidelines carefully.",
	})
	runner := uiadapter.NewCommandRunner(nil, nil, &cliagents.AgentSessionState{SkillRegFull: skillReg})

	out := runner.Run(context.Background(), "feature-delivery", "add auth module")
	if out.Err != "" {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if out.SubmitPersistedText != "/feature-delivery add auth module" {
		t.Errorf("SubmitPersistedText = %q, want the short slash-command form", out.SubmitPersistedText)
	}
	if strings.Contains(out.SubmitPersistedText, "Follow feature delivery guidelines") {
		t.Error("SubmitPersistedText leaked the full instructions body")
	}
}
