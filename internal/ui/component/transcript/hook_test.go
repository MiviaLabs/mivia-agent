package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// An advisory hook run - one that fired and did not block anything - stays
// collapsed by default, matching the reasoning block's contract: worth
// knowing it happened, not worth taking up screen space unasked.
func TestHookAdvisoryRunCollapsesByDefault(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)
	m = drain(t, m, []uievent.Event{{Kind: uievent.KindHook, Body: uievent.HookBody{
		Event: "PostToolUse", Program: "fmt.sh", Tool: "write_file",
		Input: `{"path":"a.go"}`, Output: "reformatted 2 files",
	}}})
	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(m.blocks))
	}
	if !m.blocks[0].Collapsible || !m.blocks[0].Collapsed {
		t.Fatalf("an advisory hook run must be collapsible and collapsed by default, got %+v", m.blocks[0])
	}
	got := ansi.Strip(m.Dump())
	if !strings.Contains(got, "fmt.sh") || !strings.Contains(got, "write_file") {
		t.Fatalf("the header must name the program and the tool, got:\n%s", got)
	}
}

// A denied hook run is the one outcome where the operator most needs the
// reason immediately visible - it stays uncollapsed and carries the
// error role so it visually stands apart from an advisory run.
func TestHookDeniedRunStaysUncollapsedWithErrorRole(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)
	m = drain(t, m, []uievent.Event{{Kind: uievent.KindHook, Body: uievent.HookBody{
		Event: "PreToolUse", Program: "guard.sh", Tool: "run_command",
		Input: `{"argv":["rm","-rf","/"]}`, Output: "policy forbids this argv", Denied: true,
	}}})
	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(m.blocks))
	}
	blk := m.blocks[0]
	if blk.Collapsed {
		t.Fatalf("a denied hook run must not start collapsed, got %+v", blk)
	}
	if blk.Header.Role != theme.RoleDanger {
		t.Fatalf("a denied hook run must carry the danger role, got %v", blk.Header.Role)
	}
	got := ansi.Strip(m.Dump())
	for _, want := range []string{"guard.sh", "run_command", "rm", "policy forbids this argv"} {
		if !strings.Contains(got, want) {
			t.Errorf("the denied row must show %q, got:\n%s", want, got)
		}
	}
}

// A hook that ran and said nothing is still a row: the whole point of
// hook-run visibility is answering "did my hook fire?" even when it had
// nothing to say.
func TestHookSilentRunStillRendersARow(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)
	m = drain(t, m, []uievent.Event{{Kind: uievent.KindHook, Body: uievent.HookBody{
		Event: "PostToolUse", Program: "fmt.sh", Tool: "write_file",
	}}})
	if len(m.blocks) != 1 {
		t.Fatalf("a silent hook run must still commit a block, got %d", len(m.blocks))
	}
}

// A hook that both prints and warns produces multi-line Output
// unconditionally (hookRunOutput joins them with "\n"). Each line must
// become its own Body element - an unsplit multi-line string under-counts
// Height() against what actually renders, corrupting scroll offsets and
// eviction budgeting.
func TestHookBlockValueSplitsMultiLineOutput(t *testing.T) {
	blk := hookBlockValue(uievent.HookBody{
		Event: "PreToolUse", Program: "guard.sh", Tool: "run_command", Denied: true,
		Output: "reformatted a.go\nreformatted b.go\nreformatted c.go",
	})
	if len(blk.Body) != 3 {
		t.Fatalf("want 3 body lines, got %d: %+v", len(blk.Body), blk.Body)
	}
	// A denied run stays uncollapsed, so Height must count every body line
	// it actually renders: header + 3 body rows + the trailing blank
	// separator every block gets.
	if got, want := blk.Height(80), 5; got != want {
		t.Fatalf("Height(80) = %d, want %d (header + 3 body rows + separator)", got, want)
	}
}

// Same fix, for Input.
func TestHookBlockValueSplitsMultiLineInput(t *testing.T) {
	blk := hookBlockValue(uievent.HookBody{
		Event: "PreToolUse", Program: "guard.sh", Tool: "run_command", Denied: true,
		Input: "line one\nline two",
	})
	if len(blk.Body) != 2 {
		t.Fatalf("want 2 body lines, got %d: %+v", len(blk.Body), blk.Body)
	}
}

// redactToolInput (unlike hookRunOutput) does not trim its output, so a
// trailing newline in pretty-printed JSON input must not become a
// padding-only body row.
func TestHookBlockValueTrimsTrailingNewlineFromInput(t *testing.T) {
	blk := hookBlockValue(uievent.HookBody{
		Event: "PreToolUse", Program: "guard.sh", Tool: "run_command",
		Input: "{\"argv\":[\"ls\"]}\n",
	})
	if len(blk.Body) != 1 {
		t.Fatalf("want 1 body line, got %d: %+v", len(blk.Body), blk.Body)
	}
	if strings.TrimSpace(blk.Body[0]) == "" {
		t.Fatalf("the body line must not be padding-only, got %q", blk.Body[0])
	}
}

// A multi-line notice (e.g. the /hooks listing) must keep every line, not
// just the first - previously everything past the first line was silently
// dropped because the block carried no Body at all.
func TestMultiLineNoticeKeepsEveryLine(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)
	m = drain(t, m, []uievent.Event{{Kind: uievent.KindNotice, Body: uievent.NoticeBody{
		Text: "lifecycle hooks (2)\n  [1] [user] active PreToolUse\n  [2] [user] active PostToolUse",
	}}})
	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(m.blocks))
	}
	if !m.blocks[0].Collapsible {
		t.Fatalf("a multi-line notice must be collapsible so its detail can be shown or hidden")
	}
	got := ansi.Strip(m.Dump())
	for _, want := range []string{"lifecycle hooks (2)", "[1] [user] active PreToolUse", "[2] [user] active PostToolUse"} {
		if !strings.Contains(got, want) {
			t.Errorf("a multi-line notice must keep %q, got:\n%s", want, got)
		}
	}
}

// A single-line notice is unaffected: no Body, same shape as before this
// change (every non-prose block is made Collapsible by push() regardless of
// this package's own Collapsible field, so that part is unchanged; what
// must NOT regress is that a single-line notice carries no Body).
func TestSingleLineNoticeCarriesNoBody(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)
	m = drain(t, m, []uievent.Event{{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "context 80% full"}}})
	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(m.blocks))
	}
	if len(m.blocks[0].Body) != 0 {
		t.Fatalf("a single-line notice must carry no body, got %+v", m.blocks[0])
	}
}
