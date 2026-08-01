package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// fixedSession builds a session with two confirmed hooks.
func fixedSession(t *testing.T) *hookSession {
	t.Helper()
	userGroup := hooks.Group{
		Event:   hooks.EventPreToolUse,
		Matcher: "run_command",
		Source:  "/home/u/.mivia/mivia.toml",
		Hash:    "user-hash",
		Handlers: []hooks.Handler{{
			Type: hooks.HandlerTypeCommand, Argv: []string{"./gate.sh"},
			Timeout: 10 * time.Second, OnTimeout: hooks.OnTimeoutBlock,
		}},
	}
	secondGroup := hooks.Group{
		Event:  hooks.EventPostToolUse,
		Source: "/home/u/.mivia/mivia.toml",
		Hash:   "second-hash",
		Handlers: []hooks.Handler{{
			Type: hooks.HandlerTypeCommand, Argv: []string{"./audit.sh"},
			Timeout: 10 * time.Second, OnTimeout: hooks.OnTimeoutAllow,
		}},
	}
	return &hookSession{
		store: hooks.OpenStore(filepath.Join(t.TempDir(), "hook-trust.json")),
		decisions: []hooks.Decision{
			{Group: userGroup, Status: hooks.StatusActive},
			{Group: secondGroup, Status: hooks.StatusActive},
		},
	}
}

// Headless does NOT inherit an interactive confirmation. With no TTY there is
// nobody to ask, and "headless implies trusted" would make a cloned repo's
// hooks execute on any build machine that ever runs mivia non-interactively.
//
// v1 has no operator tier, so the rule has no exception: a non-interactive run
// executes ZERO hooks unless --bypass-hook-trust is passed.
func TestHeadlessRunsZeroHooksEvenWhenConfirmed(t *testing.T) {
	session := fixedSession(t)
	session.applyGate(hookGate{headless: true})

	if runnable := session.runnable(); len(runnable) != 0 {
		t.Fatalf("a non-interactive session runs no hooks at all, got %d", len(runnable))
	}
}

// A silent no-op here is the failure mode that produces "hooks are broken" bug
// reports, so the run says why and names the flag.
func TestHeadlessSuppressionNamesTheFlag(t *testing.T) {
	session := fixedSession(t)
	messages := session.applyGate(hookGate{headless: true})
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "--bypass-hook-trust") {
		t.Fatalf("the suppression message must name the flag, got %v", messages)
	}
	if !strings.Contains(joined, "gate.sh") {
		t.Fatalf("the message must name what did not run, got %v", messages)
	}
}

func TestHeadlessWithNoUserHooksSaysNothing(t *testing.T) {
	session := &hookSession{store: hooks.OpenStore(filepath.Join(t.TempDir(), "s.json"))}
	if messages := session.applyGate(hookGate{headless: true}); len(messages) != 0 {
		t.Fatalf("nothing was suppressed, so nothing is reported; got %v", messages)
	}
}

// A bypass that leaves no record is indistinguishable from no gate at all.
func TestBypassRunsUnconfirmedHooksAndLogsEachOne(t *testing.T) {
	session := fixedSession(t)
	session.decisions[0].Status = hooks.StatusPending

	messages := session.applyGate(hookGate{headless: true, bypass: true})
	if len(session.runnable()) != 2 {
		t.Fatalf("bypass runs unconfirmed hooks; got %d runnable", len(session.runnable()))
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "gate.sh") {
		t.Fatalf("bypass must log each hook it ran unconfirmed, got %v", messages)
	}
	if !strings.Contains(joined, "PreToolUse") {
		t.Fatalf("the record must name the event, got %v", messages)
	}
	if strings.Contains(joined, "audit.sh") {
		t.Fatalf("an already-confirmed hook needed no bypass and must not be logged as one, got %v", messages)
	}
}

// The flag bypasses TRUST, and nothing else. argv-only execution, timeouts,
// on_timeout and the output bound all still apply, so the groups that reach the
// runner are the ones the parser produced, unmodified.
func TestBypassRelaxesTrustOnly(t *testing.T) {
	session := fixedSession(t)
	session.decisions[0].Status = hooks.StatusPending
	session.applyGate(hookGate{headless: true, bypass: true})

	for _, group := range session.runnable() {
		for _, handler := range group.Handlers {
			if handler.Type != hooks.HandlerTypeCommand {
				t.Errorf("handler type changed to %q", handler.Type)
			}
			if handler.Timeout != 10*time.Second {
				t.Errorf("timeout changed to %v", handler.Timeout)
			}
			if len(handler.Argv) == 0 {
				t.Error("argv was cleared")
			}
		}
	}
	pre := session.runnable()[0]
	if pre.Handlers[0].OnTimeout != hooks.OnTimeoutBlock {
		t.Errorf("on_timeout changed to %q; bypass relaxes trust, not the gate's failure mode", pre.Handlers[0].OnTimeout)
	}
}

func TestInteractiveSessionIsUnaffectedByTheFlagsAbsence(t *testing.T) {
	session := fixedSession(t)
	if messages := session.applyGate(hookGate{}); len(messages) != 0 {
		t.Fatalf("an interactive session reports no suppression; got %v", messages)
	}
	if len(session.runnable()) != 2 {
		t.Fatalf("an interactive session runs its confirmed hooks; got %d", len(session.runnable()))
	}
}

// An interactive session that passes the flag still gets it: the flag says what
// it does, and honouring it only headless would be a surprise.
func TestBypassAppliesInteractivelyToo(t *testing.T) {
	session := fixedSession(t)
	session.decisions[0].Status = hooks.StatusPending
	session.applyGate(hookGate{bypass: true})
	if len(session.runnable()) != 2 {
		t.Fatalf("bypass applies wherever it is passed; got %d runnable", len(session.runnable()))
	}
}

func TestBypassHookTrustFlagIsParsed(t *testing.T) {
	invocation, err := parseChatInvocation([]string{"--bypass-hook-trust", "--no-tools"})
	if err != nil {
		t.Fatalf("parseChatInvocation: %v", err)
	}
	if !invocation.bypassHookTrust {
		t.Fatal("--bypass-hook-trust must be parsed")
	}
	if !invocation.noTools {
		t.Fatal("--bypass-hook-trust must not swallow other flags")
	}

	plain, err := parseChatInvocation(nil)
	if err != nil {
		t.Fatalf("parseChatInvocation: %v", err)
	}
	if plain.bypassHookTrust {
		t.Fatal("the bypass must never be the default")
	}
}

// The flag name carries "bypass" so it is greppable and never reads as a
// feature: "trust my hooks" is what gets pasted into a CI config unexamined.
func TestBypassFlagIsDocumentedAsDangerous(t *testing.T) {
	help := usageText()
	if !strings.Contains(help, "--bypass-hook-trust") {
		t.Fatal("--bypass-hook-trust must appear in the usage text")
	}
	if !strings.Contains(strings.ToLower(help), "dangerous") {
		t.Fatalf("the flag must be documented as dangerous; got:\n%s", help)
	}
}

// A one-shot -p run is headless whether or not a terminal is attached: there is
// no prompt in it to answer a confirmation.
func TestOneShotIsAlwaysHeadless(t *testing.T) {
	if !hookGateFor(chatInvocation{prompt: "do a thing"}, true).headless {
		t.Fatal("-p is headless even with a TTY attached")
	}
	if hookGateFor(chatInvocation{}, true).headless {
		t.Fatal("an interactive session with a TTY is not headless")
	}
	if !hookGateFor(chatInvocation{}, false).headless {
		t.Fatal("no TTY means nobody to confirm, so it is headless")
	}
	if !hookGateFor(chatInvocation{bypassHookTrust: true}, true).bypass {
		t.Fatal("the flag must reach the gate")
	}
}

// With the bypass active, every hook runs regardless of its trust status. A
// listing that still shows "pending" for a hook that is in fact running would
// describe a session that does not exist.
func TestListingSaysSoWhenTheBypassIsActive(t *testing.T) {
	session := fixedSession(t)
	session.decisions[0].Status = hooks.StatusPending
	session.applyGate(hookGate{bypass: true})

	out := renderHookList(session)
	if !strings.Contains(out, "--bypass-hook-trust") {
		t.Fatalf("the listing must say the bypass is active; got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "regardless") {
		t.Fatalf("the listing must say status is not being honoured; got:\n%s", out)
	}
}

func TestListingSaysSoWhenHeadlessSuppressesHooks(t *testing.T) {
	session := fixedSession(t)
	session.applyGate(hookGate{headless: true})

	out := renderHookList(session)
	if !strings.Contains(out, "--bypass-hook-trust") {
		t.Fatalf("a suppressed listing must name the flag; got:\n%s", out)
	}
}

// v1 ships no operator tier. The managed path the first implementation invented
// was a filesystem convention nothing in this product installs, created outside
// internal/workspace, which owns every namespace path. Until a plan owns the
// install story there is no auto-trusted source at all - and no code pretending
// to verify one.
func TestNoAutoTrustedHookSourceExists(t *testing.T) {
	entries, err := os.ReadDir("../hooks")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join("../hooks", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "/etc/mivia") {
			t.Errorf("%s names /etc/mivia: no such install path exists, and only internal/workspace may name a namespace directory", name)
		}
	}
}

func TestListingIsUnadornedInAnOrdinaryInteractiveSession(t *testing.T) {
	session := fixedSession(t)
	session.applyGate(hookGate{})
	if strings.Contains(renderHookList(session), "--bypass-hook-trust") {
		t.Fatal("an ordinary session must not advertise the bypass flag in its listing")
	}
}

// The dispatcher wiring must be nil when nothing is configured: nil is the
// contract that keeps the no-hook path one nil compare.
func TestHookPolicyFuncsAreNilWithoutConfiguredHooks(t *testing.T) {
	previous := sessionHookState.Load()
	t.Cleanup(func() { sessionHookState.Store(previous) })

	sessionHookState.Store(nil)
	if pre, post := hookPolicyFuncs("/ws"); pre != nil || post != nil {
		t.Fatal("no session means no hook funcs")
	}

	sessionHookState.Store(&hookSession{})
	if pre, post := hookPolicyFuncs("/ws"); pre != nil || post != nil {
		t.Fatal("a session with zero hooks must install no hook funcs")
	}

	sessionHookState.Store(fixedSession(t))
	if pre, post := hookPolicyFuncs(""); pre != nil || post != nil {
		t.Fatal("no workspace root means no hooks")
	}
	if pre, post := hookPolicyFuncs("/ws"); pre == nil || post == nil {
		t.Fatal("configured hooks must install the dispatcher funcs")
	}
}

// A tool call reads the decisions while /hooks trust mutates them, on different
// goroutines. Under -race this is what proves the session is safe to share.
func TestHookSessionIsSafeUnderConcurrentTrustAndRead(t *testing.T) {
	session := fixedSession(t)
	session.decisions[0].Status = hooks.StatusPending

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			_ = session.runnable()
			session.noteRunWarnings([]string{"a hook warned"})
			_ = renderHookList(session)
		}
	}()
	for range 200 {
		_ = session.trust("1")
	}
	<-done
}

// Retained run-time diagnostics are bounded: a hook that warns on every tool
// call must not grow the session without limit.
func TestRunWarningsAreBounded(t *testing.T) {
	session := fixedSession(t)
	for i := range 100 {
		session.noteRunWarnings([]string{string(rune('a' + i%26))})
	}
	session.mu.Lock()
	got := len(session.runWarnings)
	session.mu.Unlock()
	if got > maxRunWarnings {
		t.Fatalf("retained %d run warnings, bound is %d", got, maxRunWarnings)
	}
}

// MIVIA_FILE is derived from a top-level "path" and nothing else, so a hook
// author can reason about when it is set.
func TestHookFileComesFromATopLevelPath(t *testing.T) {
	cases := map[string]string{
		`{"path":"cmd/main.go"}`:        "cmd/main.go",
		`{"path":"a b; rm -rf /"}`:      "a b; rm -rf /",
		`{"argv":["git","commit"]}`:     "",
		`{"nested":{"path":"deep.go"}}`: "",
		`not json`:                      "",
		``:                              "",
	}
	for input, want := range cases {
		if got := hookFileFromInput([]byte(input)); got != want {
			t.Errorf("hookFileFromInput(%s) = %q, want %q", input, got, want)
		}
	}
}
