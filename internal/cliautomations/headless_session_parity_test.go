package cliautomations

// A headless automation session must reach the model with the same surface
// an interactive session gets: the workspace's configured tool registry,
// its skills, its agent roles and the session tool catalog. It used to get
// none of that - composition.BuildSession was called with an empty
// RegistryInput and a zero DispatcherInput - so [tools] policy was ignored,
// lifecycle hooks never ran, and there was no delegation verb at all.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// errStubHookInstall is the failure a stubbed hook install returns.
var errStubHookInstall = errors.New("stub hook install failure")

// spawnFixtureSession builds one session through the real spawner against a
// fixture workspace, and returns it with the resolved config it was built
// from.
func spawnFixtureSession(t *testing.T, id string) (*chat.Session, *config.Resolved, *HeadlessSpawner) {
	t.Helper()
	root := writeAutomationsFixture(t, id)
	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	sess := spawn.lastSession
	if sess == nil {
		t.Fatal("spawner recorded no session")
	}
	return sess, res, spawn
}

func advertisedNames(sess *chat.Session) map[string]string {
	out := map[string]string{}
	for _, spec := range sess.AdvertisedToolSpecs() {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		out[name] = desc
	}
	return out
}

// TestHeadlessSessionCarriesTheSessionToolCatalog is the parity assertion
// that matters most: a headless run gets the delegation and orchestration
// tools the session dispatcher owns. A plain runtime.NewToolDispatcher -
// what this path used to build - registers none of them, so the model had
// no way to delegate at all.
func TestHeadlessSessionCarriesTheSessionToolCatalog(t *testing.T) {
	sess, _, _ := spawnFixtureSession(t, "parity-catalog")

	advertised := advertisedNames(sess)
	if len(advertised) == 0 {
		t.Fatal("session advertised no tools")
	}
	for _, name := range []string{"dispatch_tasks", "inspect_agents", "post_message", "ledger_read"} {
		if _, ok := advertised[name]; !ok {
			t.Errorf("advertised tools are missing session tool %q; got %v", name, sortedKeys(advertised))
		}
	}
	// The registry must carry them too, not just the wire array: advertised
	// without registered is a call that resolves to nothing.
	if _, ok := sess.Tools.Get("dispatch_tasks"); !ok {
		t.Error("session registry has no dispatch_tasks; the surface advertised a tool it cannot execute")
	}
}

// TestHeadlessSessionHonoursWorkspaceToolPolicy pins that [tools] config
// reaches the registry. Before, composition.RegistryInput was
// {Workspace: dir} and every other field was zero, so disable_tools,
// run_blocklist, write_path_denylist and the secret-path patterns were all
// silently inert for an automation run.
func TestHeadlessSessionHonoursWorkspaceToolPolicy(t *testing.T) {
	root := writeAutomationsFixture(t, "parity-policy")
	appendWorkspaceConfig(t, root, "\n[tools]\ndisable_tools = ['fetch_url']\n")

	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if _, ok := spawn.lastSession.Tools.Get("fetch_url"); ok {
		t.Fatal("fetch_url is registered despite [tools] disable_tools; the workspace's tool policy did not reach the registry")
	}
	if _, ok := spawn.lastSession.Tools.Get("read_file"); !ok {
		t.Fatal("read_file is missing; the policy was applied but the registry is not the configured one")
	}
}

// TestHeadlessSessionPromptCarriesTheRoster pins that the compiled root
// prompt AND the subagent roster reach the session. The roster is what
// makes dispatch_tasks usable: without it the model has the verb and no
// idea which agents exist.
func TestHeadlessSessionPromptCarriesTheRoster(t *testing.T) {
	root := writeAutomationsFixture(t, "parity-roster")
	writeAgentRole(t, root, "reviewer", "Reviews a change for defects.")

	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	prompt := spawn.lastSession.BaseSystemPrompt
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("session has an empty system prompt: the run would send no system message")
	}
	if !strings.Contains(prompt, "reviewer") {
		t.Fatalf("system prompt carries no subagent roster entry for the workspace's own agent role:\n%s", prompt)
	}
}

// TestHeadlessSpawnNeverMutatesTheSharedConfig pins the copy-on-write rule.
// A daemon builds every session from ONE *config.Resolved; a per-run
// adjustment written through it (the workspace prompt gate blanks
// SystemPrompt) would leak into every later run, and the second fire would
// silently lose its system prompt.
func TestHeadlessSpawnNeverMutatesTheSharedConfig(t *testing.T) {
	sess, res, spawn := spawnFixtureSession(t, "parity-cow")
	first := sess.BaseSystemPrompt
	if res.SystemPrompt != "" {
		t.Fatalf("the shared config was written through: SystemPrompt = %q", res.SystemPrompt)
	}

	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("CloseLastRun: %v", err)
	}
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("second CreateFreshInDir: %v", err)
	}
	if second := spawn.lastSession.BaseSystemPrompt; second != first {
		t.Fatalf("second fire's prompt differs from the first:\nfirst  %q\nsecond %q", first, second)
	}
}

// TestHeadlessSessionAdvertisesToolDescriptions pins that the wire array
// carries each tool's real description. The agent loop's own fallback
// restates the SDK's registry definitions, which are name and schema only.
func TestHeadlessSessionAdvertisesToolDescriptions(t *testing.T) {
	sess, _, _ := spawnFixtureSession(t, "parity-desc")

	described := 0
	for _, desc := range advertisedNames(sess) {
		if strings.TrimSpace(desc) != "" {
			described++
		}
	}
	if described == 0 {
		t.Fatal("every advertised spec has an empty description")
	}
}

// writeAgentRole writes one Markdown agent role under root's .agents/agents.
func writeAgentRole(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\ntools: [read_file]\n---\n\nDo the work.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write agent role: %v", err)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestInstallAutomationHooksArmsThisProcess pins the hook install. Without
// it, clichat.NewHeadlessSession builds every dispatcher with
// HooksConfigured false, and the workspace's PreToolUse and PostToolUse
// handlers never run for an automation - the security-relevant half of this
// parity gap.
func TestInstallAutomationHooksArmsThisProcess(t *testing.T) {
	var gotRoot string
	var released bool
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = func(root string, staleBypass, quiet bool) (func(), error) {
		gotRoot = root
		if staleBypass {
			t.Error("headless install asked for a stale bypass")
		}
		if !quiet {
			t.Error("headless install was not quiet")
		}
		return func() { released = true }, nil
	}
	t.Cleanup(func() { clichat.InstallHookSessionFunc = prev })

	release, err := installAutomationHooks("/tmp/example-root")
	if err != nil {
		t.Fatalf("installAutomationHooks: %v", err)
	}
	if gotRoot != "/tmp/example-root" {
		t.Fatalf("installed against %q", gotRoot)
	}
	release()
	if !released {
		t.Fatal("the returned release did not reach the hook session's own release")
	}
}

// TestInstallAutomationHooksPropagatesAFailure pins that a hook session
// that cannot be installed fails the command. Continuing would run the
// automation with its guards silently absent.
func TestInstallAutomationHooksPropagatesAFailure(t *testing.T) {
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = func(string, bool, bool) (func(), error) {
		return nil, errStubHookInstall
	}
	t.Cleanup(func() { clichat.InstallHookSessionFunc = prev })

	release, err := installAutomationHooks("/tmp/example-root")
	if err == nil {
		t.Fatal("installAutomationHooks succeeded against a failing install")
	}
	if release != nil {
		t.Fatal("a failed install returned a release func")
	}
}

// TestInstallAutomationHooksWithoutSeam pins the unwired-seam case: a
// binary that never imports internal/cli keeps its previous posture - no
// hooks - rather than failing to start.
func TestInstallAutomationHooksWithoutSeam(t *testing.T) {
	prev := clichat.InstallHookSessionFunc
	clichat.InstallHookSessionFunc = nil
	t.Cleanup(func() { clichat.InstallHookSessionFunc = prev })

	release, err := installAutomationHooks("/tmp/example-root")
	if err != nil {
		t.Fatalf("installAutomationHooks with no seam: %v", err)
	}
	if release == nil {
		t.Fatal("no release func returned")
	}
	release()
}

// TestCreateFreshInDirReportsACompleterFailure pins that a run whose
// provider cannot be built fails loudly. A nil completer would make the
// surface attach a silent no-op.
func TestCreateFreshInDirReportsACompleterFailure(t *testing.T) {
	spawn, err := NewHeadlessSpawner(t.TempDir(), &config.Resolved{ProviderName: "openrouter"})
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	conv, err := spawn.CreateFreshInDir(nil, "")
	if err == nil {
		t.Fatalf("CreateFreshInDir succeeded with an unconfigured provider: %v", conv)
	}
	if !strings.Contains(err.Error(), "completer") {
		t.Fatalf("error = %v, want it naming the completer", err)
	}
}

// TestBuildServiceBoundsAStepByTheConfiguredRunBudget pins the WIRING, not
// the helper: it reads the deadline off the Service buildService actually
// constructs. An earlier version of this test called the resolver in
// isolation and passed with the production wiring deleted.
//
// Nothing had ever set Config.TurnTimeout, so a step doing real work was
// cut at internal/automation's hardcoded 10 minutes with a bare "context
// deadline exceeded" and no knob to raise it.
func TestBuildServiceBoundsAStepByTheConfiguredRunBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure string
		want      time.Duration
	}{
		{"configured", "\ndefault_total_timeout_seconds = 7200\n", 2 * time.Hour},
		{"unset", "", time.Duration(config.DefaultSubagentTotalTimeoutSec) * time.Second},
		// Negative is the documented opt-out. It must NOT resolve to the
		// executor's own 10-minute fallback, which is what handing it a
		// zero did: the operator who disabled the ceiling got the
		// tightest one available.
		{"disabled", "\ndefault_total_timeout_seconds = -1\n", clichat.StepTimeout(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeAutomationsFixture(t, "turn-budget-"+tc.name)
			if tc.configure != "" {
				appendWorkspaceConfig(t, root, tc.configure)
			}
			svc, _, cleanup, err := buildService(root, "")
			if err != nil {
				t.Fatalf("buildService: %v", err)
			}
			t.Cleanup(cleanup)
			if got := svc.TurnTimeoutForTest(); got != tc.want {
				t.Fatalf("step deadline = %s, want %s", got, tc.want)
			}
			if got := svc.TurnTimeoutForTest(); got == 10*time.Minute {
				t.Fatal("the step deadline fell back to the executor's hardcoded 10 minutes")
			}
		})
	}
}

// TestGetOrResumeInDirRestoresASavedRun is the round trip
// `mivia automations resume` performs. It could never work: the headless
// session left composition.BuildSession to default the checkpoint
// principal's subject to the session's OWN id, fresh on every fire, and
// every catalog read is subject-scoped - so the row a run saved was
// unreadable the moment that run ended. A missing binding factory then
// blocked the publish even once the row resolved.
func TestGetOrResumeInDirRestoresASavedRun(t *testing.T) {
	root := writeAutomationsFixture(t, "resume-round-trip")
	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })

	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	saved := spawn.lastSession
	id := saved.SessionID
	if err := saved.Save(id); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("CloseLastRun: %v", err)
	}

	conv, resumed, err := spawn.GetOrResumeInDir(id, "")
	if err != nil {
		t.Fatalf("GetOrResumeInDir: %v", err)
	}
	if conv == nil || resumed == nil {
		t.Fatal("resume returned no conversation or session")
	}
	if resumed.SessionID != id {
		t.Fatalf("resumed session id = %q, want %q", resumed.SessionID, id)
	}
	// The surface must still be attached after the restore.
	if len(resumed.AdvertisedToolSpecs()) == 0 {
		t.Fatal("the resumed session carries no advertised tools[]")
	}
}

// TestHeadlessSessionInjectsCoreMemoryBlock is the POSITIVE parity proof for
// SLICE S2: a headless session built over a workspace with [memory]
// inject_core enabled and a seeded, promoted core-tier memory must carry
// that memory's text in its composed conversation, exactly like the
// interactive path's ApplySelectedAgentPrompt call
// (chat_command.go:211). Before the production fix, NewHeadlessSession never
// called cliagents.ApplySelectedAgentPrompt at all, so this assertion
// FAILED (no memory-context message existed in the session at all) - proof
// this test is real, not a tautology against boilerplate.
func TestHeadlessSessionInjectsCoreMemoryBlock(t *testing.T) {
	root := writeAutomationsFixture(t, "parity-memory-positive")
	appendWorkspaceConfig(t, root, "\n[memory]\ninject_core = true\nstore_backend = \"markdown\"\n")

	// Seed and promote one project-scope memory directly through the same
	// store-opening path the workspace itself uses, so the file lands where
	// the headless session's own memory wiring will read it back from.
	memRes := config.MemoryConfig{StoreBackend: "markdown", InjectCore: true,
		MaxEntryBytes: memory.DefaultMaxEntryBytes, MaxSearchResults: memory.DefaultMaxSearchResults}
	seedStore, err := cliagents.OpenMemoryStoreWithReadOnly(root, memRes, false)
	if err != nil {
		t.Fatalf("open seed memory store: %v", err)
	}
	saved, err := seedStore.Save(context.Background(), memory.Entry{
		Title: "headless parity fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "core memory must reach a headless session too", Why: "test",
	})
	if err != nil {
		_ = seedStore.Close()
		t.Fatalf("save seed memory: %v", err)
	}
	if err := seedStore.PromoteToCore(context.Background(), saved.ID); err != nil {
		_ = seedStore.Close()
		t.Fatalf("promote seed memory to core: %v", err)
	}
	if err := seedStore.Close(); err != nil {
		t.Fatalf("close seed memory store: %v", err)
	}

	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	sess := spawn.lastSession
	if sess == nil {
		t.Fatal("spawner recorded no session")
	}

	var memoryMsg string
	for _, m := range sess.MessagesCopy() {
		if strings.Contains(m.Content, "<core-memory-context>") {
			memoryMsg = m.Content
			break
		}
	}
	if memoryMsg == "" {
		t.Fatal("headless session carries no core-memory-context message; the memory block never reached it")
	}
	if !strings.Contains(memoryMsg, "headless parity fact") {
		t.Fatalf("injected block missing the seeded memory:\n%s", memoryMsg)
	}
}

// TestHeadlessSessionEmptyMemoryDirYieldsNoBlock is the NEGATIVE proof
// alongside the positive test above: a workspace with inject_core enabled
// but NO promoted core-tier memories must compose no memory-context
// message at all. This rules out the failure mode where the assertion
// above would pass against a hardcoded/boilerplate block regardless of
// what memory actually exists.
func TestHeadlessSessionEmptyMemoryDirYieldsNoBlock(t *testing.T) {
	root := writeAutomationsFixture(t, "parity-memory-negative")
	appendWorkspaceConfig(t, root, "\n[memory]\ninject_core = true\nstore_backend = \"markdown\"\n")

	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	sess := spawn.lastSession
	if sess == nil {
		t.Fatal("spawner recorded no session")
	}

	for _, m := range sess.MessagesCopy() {
		if strings.Contains(m.Content, "<core-memory-context>") {
			t.Fatalf("an empty memory dir must yield no memory-context message, got:\n%s", m.Content)
		}
	}
}

// TestHeadlessSessionMemoryDisabledRegistersNoMemoryTools is the sibling of
// the positive/negative memory-injection proofs above, for the sink's OTHER
// input: a workspace with [memory] enabled = false leaves
// WireSessionMemory's opts.Memory nil, so buildHeadlessRegistry's
// MemoryStoreSink is invoked with a nil store (see
// cliagents.StashMemoryOnState's nil-safe assignment). That must be a
// harmless no-op, not a panic or a phantom tool registration: memory_save
// and memory_search are registered ONLY when a store exists
// (registerMemoryTools), so their absence here is the externally
// observable proof that the nil case was handled cleanly end to end.
func TestHeadlessSessionMemoryDisabledRegistersNoMemoryTools(t *testing.T) {
	root := writeAutomationsFixture(t, "parity-memory-disabled")
	appendWorkspaceConfig(t, root, "\n[memory]\nenabled = false\n")

	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	sess := spawn.lastSession
	if sess == nil {
		t.Fatal("spawner recorded no session")
	}

	for _, name := range []string{"memory_save", "memory_search", "memory_delete"} {
		if _, ok := sess.Tools.Get(name); ok {
			t.Fatalf("%s is registered despite [memory] enabled = false; the nil-store sink path is not truly a no-op", name)
		}
	}
}
