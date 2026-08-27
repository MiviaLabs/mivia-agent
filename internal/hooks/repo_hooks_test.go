package hooks

import (
	"context"
	"encoding/json"
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
			if runtime.GOOS == "windows" {
				// Git checkouts on Windows carry no permission bits, so argv
				// executability cannot be asserted; the scripts still exist
				// and are invoked through an interpreter on that platform.
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
		t.Fatal("expected at least one PreToolUse gate configured in .mivia/mivia.toml for INV-AG-34")
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

// The destructive-command half of the same gate, executed.
//
// ONE principle is under test: committed work is recoverable, uncommitted work
// is not. The gate blocks the second kind and stays out of the way otherwise.
//
// The allow rows are not padding - they are the whole design. A gate that
// refuses everything is trivially "safe" and useless, and each row here is a
// case an over-broad pattern gets wrong: `reset --hard` against a plain
// `reset`, `branch -D` against `branch -d` (which differ only by CASE, so a
// careless (?i) collapses them), `stash drop` against `stash pop`, and the
// rebase/force-push pair that an agent cannot finish a rebase without. Blocking
// a recovery path, or a normal day's work, is its own way of losing work.
func TestRepoDestructiveGateBlocksLossAndAllowsWork(t *testing.T) {
	requirePOSIX(t)
	groups := repoHookGroups(t)
	runner := Runner{WorkspaceRoot: repoRoot(t)}

	decide := func(t *testing.T, argv ...string) Outcome {
		t.Helper()
		encoded, err := json.Marshal(argv)
		if err != nil {
			t.Fatalf("marshal argv: %v", err)
		}
		return runner.Run(context.Background(), groups, Payload{
			Event: EventPreToolUse,
			Tool:  "run_command",
			Input: []byte(`{"argv":` + string(encoded) + `}`),
		})
	}

	// Work git cannot give back: the working tree, the index, the stash, and
	// the reflog that makes everything else recoverable.
	for _, argv := range [][]string{
		{"git", "reset", "--hard", "HEAD~1"},
		{"git", "checkout", "--", "."},
		{"git", "checkout", "HEAD", "--", "src/x.go"},
		{"git", "checkout", "main", "--", "README.md"},
		{"git", "restore", "src/x.go"},
		{"git", "restore", "--staged", "x"},
		{"git", "clean", "-fd"},
		{"git", "stash", "drop"},
		{"git", "reflog", "expire"},
		{"git", "filter-branch"},
	} {
		out := decide(t, argv...)
		if !out.Denied {
			t.Errorf("%v was allowed; it destroys work that was never committed", argv)
			continue
		}
		if !strings.Contains(out.Reason, "destructive-commands.json") {
			t.Errorf("%v blocked by the wrong policy: %q", argv, out.Reason)
		}
	}

	// Everything an agent needs to finish a job unattended. Each of these
	// either creates a commit or moves a ref, and reflog can undo it.
	for _, argv := range [][]string{
		{"git", "commit", "-m", "msg"},
		{"git", "push", "origin", "master"},
		{"git", "push", "--force", "origin", "master"}, // a rebase you cannot push is a rebase you cannot finish
		{"git", "rebase", "master"},
		{"git", "rebase", "--abort"},    // the way OUT of a bad rebase
		{"git", "rebase", "--continue"}, //
		{"git", "branch", "-D", "old"},  // ref deletion; reflog still has it
		{"git", "branch", "-d", "merged"},
		{"git", "stash"},
		{"git", "stash", "pop"}, // recovery, not loss
		{"git", "reset", "HEAD~1"},
		{"git", "checkout", "-b", "feature"},
		{"git", "merge", "main"},
		{"git", "cherry-pick", "abc"},
		{"git", "revert", "abc"},
		{"make", "verify"},
	} {
		if out := decide(t, argv...); out.Denied {
			t.Errorf("%v was blocked, which stops an agent working unattended: %q", argv, out.Reason)
		}
	}
}
