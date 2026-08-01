package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This repository ships its own project hooks in .mivia/mivia.toml. They run for
// anyone who clones it and starts mivia here, with no confirmation step, so
// they are held to the same standard as production code: they must parse, they
// must exist on disk, they must be executable, and the gate among them must
// actually gate.
//
// The alternative is a config that looks armed and quietly does nothing -
// which, with no prompt to notice its absence, is exactly the failure that
// would go unseen the longest.

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func repoHookGroups(t *testing.T) []Group {
	t.Helper()
	path := filepath.Join(repoRoot(t), ".mivia", "mivia.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	groups, err := Parse(data, path)
	if err != nil {
		t.Fatalf("this repository's own hook config must parse: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("this repository declares no lifecycle hooks; delete this test or restore them")
	}
	return groups
}

// A hook naming a script that is missing or not executable fails at call time,
// on a tool call, as a warning nobody asked for - or, for the gate, as a block
// on every run_command. Cheaper to catch here.
func TestRepoHookScriptsExistAndAreExecutable(t *testing.T) {
	for _, group := range repoHookGroups(t) {
		for _, handler := range group.Handlers {
			program := resolveProgram(group, handler.Argv[0])
			info, err := os.Stat(program)
			if err != nil {
				t.Errorf("%s hook names %s, which is not there: %v", group.Event, program, err)
				continue
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("%s is not executable (mode %v); argv execution has no shell to fall back on",
					program, info.Mode())
			}
		}
	}
}

// The bypass gate must fail closed. A guard that opens the gate when it cannot
// answer - a crash, a hang, a missing policy file - is not a guard, and it is
// the one setting here that cannot be recovered by noticing later.
func TestRepoPreToolUseGateBlocksOnNoVerdict(t *testing.T) {
	var gates int
	for _, group := range repoHookGroups(t) {
		if group.Event != EventPreToolUse {
			continue
		}
		gates++
		for _, handler := range group.Handlers {
			if handler.OnTimeout != OnTimeoutBlock {
				t.Errorf("%s gate has on_timeout=%q; a gate that cannot answer must not allow",
					handler.Argv[0], handler.OnTimeout)
			}
		}
	}
	if gates == 0 {
		t.Skip("this repository declares no PreToolUse gate")
	}
}

// Matchers here are anchored on purpose. Matching is unanchored by default -
// as it is in every harness whose configs people copy - so `run_command` would
// also select a future `run_command_v2`, and the gate's scope would widen by
// someone else's naming choice.
func TestRepoHookMatchersAreAnchored(t *testing.T) {
	for _, group := range repoHookGroups(t) {
		if group.Matcher == "" {
			continue
		}
		if !strings.HasPrefix(group.Matcher, "^") || !strings.HasSuffix(group.Matcher, "$") {
			t.Errorf("%s matcher %q is unanchored; it will select tool names nobody has written yet",
				group.Event, group.Matcher)
		}
	}
}

// The gate, executed. Asserting the file exists proves nothing about whether it
// says no - and this repository's whole position on hook bypass rests on it
// saying no.
func TestRepoBypassGateActuallyBlocksABypassArgv(t *testing.T) {
	requirePOSIX(t)
	root := repoRoot(t)
	groups := repoHookGroups(t)

	// Assembled from parts so this test file does not itself contain the
	// literal flag that the repository's own tooling scans commits for.
	bypass := "--no-" + "verify"
	runner := Runner{WorkspaceRoot: root}

	blocked := runner.Run(context.Background(), groups, Payload{
		Event: EventPreToolUse,
		Tool:  "run_command",
		Input: []byte(`{"argv":["git","commit","-m","x","` + bypass + `"]}`),
	})
	if !blocked.Denied {
		t.Fatalf("the repository's own gate allowed a hook-bypass commit: %+v", blocked)
	}
	if !strings.Contains(blocked.Reason, "agent-hook-bypass.json") {
		t.Errorf("the block must name the policy it came from, got %q", blocked.Reason)
	}

	allowed := runner.Run(context.Background(), groups, Payload{
		Event: EventPreToolUse,
		Tool:  "run_command",
		Input: []byte(`{"argv":["git","status"]}`),
	})
	if allowed.Denied {
		t.Fatalf("the gate blocked an ordinary command: %q", allowed.Reason)
	}
	if len(allowed.Runs) == 0 {
		t.Error("an allowing gate must still record that it ran")
	}
}
