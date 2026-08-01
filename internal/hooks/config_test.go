package hooks

import (
	"strings"
	"testing"
	"time"
)

const goodConfig = `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./hooks/gate.sh", "--strict"]
  timeout = 20
  on_timeout = "allow"

[[hooks]]
event = "PostToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./hooks/fmt.sh"]
`

func parseOne(t *testing.T, body string) []Group {
	t.Helper()
	groups, err := Parse([]byte(body), "/home/u/.mivia/mivia.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return groups
}

func TestParseAcceptedShapeRoundTrips(t *testing.T) {
	groups := parseOne(t, goodConfig)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	g := groups[0]
	if g.Event != EventPreToolUse || g.Matcher != "run_command" {
		t.Fatalf("group 0 = %+v", g)
	}
	if len(g.Handlers) != 1 {
		t.Fatalf("want 1 handler, got %d", len(g.Handlers))
	}
	h := g.Handlers[0]
	if h.Type != HandlerTypeCommand {
		t.Errorf("type = %q", h.Type)
	}
	if len(h.Argv) != 2 || h.Argv[0] != "./hooks/gate.sh" || h.Argv[1] != "--strict" {
		t.Errorf("argv = %v", h.Argv)
	}
	if h.Timeout != 20*time.Second {
		t.Errorf("timeout = %v, want 20s", h.Timeout)
	}
	if h.OnTimeout != OnTimeoutAllow {
		t.Errorf("on_timeout = %q, want an explicit allow to survive", h.OnTimeout)
	}
	if g.Source != "/home/u/.mivia/mivia.toml" {
		t.Errorf("source = %q", g.Source)
	}
	if g.Hash == "" {
		t.Error("definition hash must be computed at parse")
	}
}

func TestParseNoHooksTableIsEmptyNotError(t *testing.T) {
	groups, err := Parse([]byte("[provider]\nname = \"openai\"\n"), "cfg.toml")
	if err != nil {
		t.Fatalf("a config with no hooks must parse: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("want no groups, got %d", len(groups))
	}
}

// Defaults are computed FROM the event so an author who omits on_timeout gets
// the safe one, and /hooks can display the resolved value rather than a blank.
func TestPerEventDefaults(t *testing.T) {
	cases := []struct {
		event     Event
		timeout   time.Duration
		onTimeout TimeoutVerdict
	}{
		{EventPreToolUse, 10 * time.Second, OnTimeoutBlock},
		{EventPostToolUse, 10 * time.Second, OnTimeoutAllow},
		{EventStop, 5 * time.Second, OnTimeoutAllow},
	}
	for _, tc := range cases {
		body := "[[hooks]]\nevent = \"" + string(tc.event) + "\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n"
		groups := parseOne(t, body)
		h := groups[0].Handlers[0]
		if h.Timeout != tc.timeout {
			t.Errorf("%s: timeout = %v, want %v", tc.event, h.Timeout, tc.timeout)
		}
		if h.OnTimeout != tc.onTimeout {
			t.Errorf("%s: on_timeout = %q, want %q", tc.event, h.OnTimeout, tc.onTimeout)
		}
	}
}

func TestMatcherAbsentMatchesAllToolNames(t *testing.T) {
	groups := parseOne(t, goodConfig)
	if groups[1].Matcher != "" {
		t.Fatalf("absent matcher must normalise to empty (match all), got %q", groups[1].Matcher)
	}
	for _, name := range []string{"run_command", "write_file", ""} {
		if !groups[1].Matches(name) {
			t.Errorf("an absent matcher must match %q", name)
		}
	}
}

// The matcher is compiled once at load and carried on the group. Recompiling
// per invocation would put a parse that can fail on the hot path of a security
// gate, where a failure has no honest verdict to return.
func TestMatcherIsCompiledAtLoad(t *testing.T) {
	groups := parseOne(t, "[[hooks]]\nevent = \"PreToolUse\"\nmatcher = \"^(run_command|write_file)$\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n")
	g := groups[0]
	for _, name := range []string{"run_command", "write_file"} {
		if !g.Matches(name) {
			t.Errorf("matcher must match %q", name)
		}
	}
	for _, name := range []string{"read_file", "run_commands", "xrun_command"} {
		if g.Matches(name) {
			t.Errorf("anchored matcher must not match %q", name)
		}
	}
}

// A copied Group keeps its compiled matcher: groups are passed by value through
// the trust and dispatch layers, and a copy that silently matched everything
// would widen a gate rather than narrow it.
func TestMatcherSurvivesGroupCopy(t *testing.T) {
	groups := parseOne(t, "[[hooks]]\nevent = \"PreToolUse\"\nmatcher = \"^run_command$\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n")
	copied := groups[0]
	if !copied.Matches("run_command") || copied.Matches("write_file") {
		t.Fatal("a copied group must keep its compiled matcher")
	}
}

// rejectionCase is one config that must fail loudly.
type rejectionCase struct {
	name string
	body string
	want []string
}

const testHandler = "\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n"

// rejectionCases are configs a user will plausibly write by copying from the
// Claude Code or Codex docs. Each must fail loudly, naming what mivia supports
// instead, and no value is ever coerced onto the permissive branch.
var rejectionCases = []rejectionCase{
	{
		"deferred event",
		"[[hooks]]\nevent = \"SessionStart\"\n" + testHandler,
		[]string{"deferred", "SessionStart"},
	},
	{
		"deferred event UserPromptSubmit",
		"[[hooks]]\nevent = \"UserPromptSubmit\"\n" + testHandler,
		[]string{"deferred"},
	},
	{
		"unknown event",
		"[[hooks]]\nevent = \"PreToolUsee\"\n" + testHandler,
		[]string{"unknown event", "PreToolUse", "PostToolUse", "Stop"},
	},
	{
		"missing event",
		"[[hooks]]\nmatcher = \"x\"\n" + testHandler,
		[]string{"event"},
	},
	{
		"handler type prompt",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"prompt\"\n  argv = [\"./h.sh\"]\n",
		[]string{"prompt", "command"},
	},
	{
		"handler type agent",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"agent\"\n  argv = [\"./h.sh\"]\n",
		[]string{"agent", "command"},
	},
	{
		"handler type http",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"http\"\n  argv = [\"./h.sh\"]\n",
		[]string{"http", "command"},
	},
	{
		"handler type mcp_tool",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"mcp_tool\"\n  argv = [\"./h.sh\"]\n",
		[]string{"mcp_tool", "command"},
	},
	{
		"trust key",
		"[[hooks]]\nevent = \"PreToolUse\"\ntrust = \"managed\"\n" + testHandler,
		[]string{"trust", "derived"},
	},
	{
		"run string",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  run = \"gofmt -w $FILE\"\n",
		[]string{"run", "argv", "shell"},
	},
	{
		"updatedInput in handler",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n  updatedInput = true\n",
		[]string{"updatedInput"},
	},
	{
		"unknown group key",
		"[[hooks]]\nevent = \"PreToolUse\"\nmatchers = \"x\"\n" + testHandler,
		[]string{"matchers", "unknown key"},
	},
	{
		"unknown handler key",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n  retries = 3\n",
		[]string{"retries", "unknown key"},
	},
	{
		"absent argv",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n",
		[]string{"argv"},
	},
	{
		"empty argv",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = []\n",
		[]string{"argv"},
	},
	{
		"empty argv[0]",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"\"]\n",
		[]string{"argv"},
	},
	{
		"no handlers",
		"[[hooks]]\nevent = \"PreToolUse\"\n",
		[]string{"handlers"},
	},
	{
		"uncompilable matcher",
		"[[hooks]]\nevent = \"PreToolUse\"\nmatcher = \"a(\"\n" + testHandler,
		[]string{"matcher", "regular expression"},
	},
	{
		"zero timeout",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n  timeout = 0\n",
		[]string{"timeout", "600"},
	},
	{
		"negative timeout",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n  timeout = -1\n",
		[]string{"timeout"},
	},
	{
		"absurd timeout",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n  timeout = 100000\n",
		[]string{"timeout", "600"},
	},
	{
		"bad on_timeout",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n  on_timeout = \"warn\"\n",
		[]string{"on_timeout", "block", "allow"},
	},
	{
		"argv not strings",
		"[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [1, 2]\n",
		[]string{"argv"},
	},
}

func TestRejectionTable(t *testing.T) {
	for _, tc := range rejectionCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body), "cfg.toml")
			if err != nil {
				assertMessage(t, err.Error(), tc.want)
				assertMessage(t, err.Error(), []string{"cfg.toml", "hooks[0]"})
				return
			}
			t.Fatalf("want rejection, got nil error")
		})
	}
}

func assertMessage(t *testing.T, msg string, want []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("message must mention %q; got %q", w, msg)
		}
	}
}

// The second group's error must point at hooks[1]; [[hooks]] arrays give no
// other handle to name the offending table.
func TestErrorNamesTheOffendingTableIndex(t *testing.T) {
	body := "[[hooks]]\nevent = \"Stop\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./ok.sh\"]\n\n[[hooks]]\nevent = \"Nope\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n"
	_, err := Parse([]byte(body), "cfg.toml")
	if err == nil {
		t.Fatal("want rejection")
	}
	if !strings.Contains(err.Error(), "hooks[1]") {
		t.Fatalf("error must name hooks[1], got %v", err)
	}
}
