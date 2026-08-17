package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// newCompiledPRWorkflow builds a minimal admitted workflow with a
// pull_request delivery section, mirroring the compiler test fixtures.
func newCompiledPRWorkflow(t *testing.T, mode string) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Name:        "delivery-policy-test",
		Version:     1,
		InitialStep: "plan",
		Inputs: map[string]definition.InputDef{
			"task": {Type: "string", Required: true},
		},
		Steps: []definition.Step{{ID: "plan", Kind: "agent", Agent: "planner"}},
		Transitions: []definition.Transition{{
			From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"},
		}},
		Delivery: &definition.Delivery{
			Kind:                  "pull_request",
			Mode:                  mode,
			Provider:              "github",
			Base:                  "main",
			TitleTemplate:         "feat: {{ inputs.task }}",
			CommitMessageTemplate: "feat: {{ inputs.task }}\n\nBody.",
		},
	}
	cw, err := compiler.Compile(wf)
	if err != nil {
		t.Fatalf("compiling fixture workflow: %v", err)
	}
	return cw
}

func TestFromCompiled(t *testing.T) {
	testFromCompiledRejects(t, "nil workflow", nil)
	testFromCompiledRejects(t, "nil delivery", &compiler.CompiledWorkflow{Delivery: nil})
	testFromCompiledRejects(t, "empty kind", &compiler.CompiledWorkflow{Delivery: &definition.Delivery{Kind: "", Mode: "draft"}})
	testFromCompiledRejects(t, "mode none", &compiler.CompiledWorkflow{Delivery: &definition.Delivery{Kind: "pull_request", Mode: "none"}})
	testFromCompiledRejects(t, "empty mode", &compiler.CompiledWorkflow{Delivery: &definition.Delivery{Kind: "pull_request", Mode: ""}})

	t.Run("mode draft snapshots policy", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "draft")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false for draft mode, want true")
		}
		assertPolicyFields(t, p, "pull_request", "draft", "github", "main")
		assertPolicyTemplate(t, p, "TitleTemplate", "feat: {{ inputs.task }}")
		assertPolicyTemplate(t, p, "CommitMessageTemplate", "feat: {{ inputs.task }}\n\nBody.")
	})

	t.Run("mode ready", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "ready")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false for ready mode, want true")
		}
		if p.Mode != "ready" {
			t.Errorf("Mode = %q, want ready", p.Mode)
		}
	})

	t.Run("default size limits flow through", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "draft")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false for draft mode, want true")
		}
		assertSizeLimits(t, p, DefaultMaxTitleBytes, DefaultMaxCommitMessageBytes)
	})

	t.Run("explicit size limits flow through", func(t *testing.T) {
		cw := newCompiledWithLimits(t, 999, 7777)
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		assertSizeLimits(t, p, 999, 7777)
	})

	t.Run("zero TOML values get defaults", func(t *testing.T) {
		cw := newCompiledWithLimits(t, 0, 0)
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		assertSizeLimits(t, p, DefaultMaxTitleBytes, DefaultMaxCommitMessageBytes)
	})
}

// TestFromCompiledMaxRepairs pins the repair-cycle budget plumbing: the
// workflow TOML's delivery.max_repairs is snapshotted into Policy.MaxRepairs,
// and zero or negative values select the package default (negative values are
// rejected at admission; the policy clamps defensively).
func TestFromCompiledMaxRepairs(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "draft")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false for draft mode, want true")
		}
		if p.MaxRepairs != DefaultMaxDeliveryRepairs {
			t.Fatalf("MaxRepairs = %d, want the default %d", p.MaxRepairs, DefaultMaxDeliveryRepairs)
		}
	})

	t.Run("configured value flows through", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "draft")
		cw.Delivery.MaxRepairs = 7
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		if p.MaxRepairs != 7 {
			t.Fatalf("MaxRepairs = %d, want 7", p.MaxRepairs)
		}
	})

	t.Run("negative clamps to default", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "draft")
		cw.Delivery.MaxRepairs = -1
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false")
		}
		if p.MaxRepairs != DefaultMaxDeliveryRepairs {
			t.Fatalf("MaxRepairs = %d, want the default %d for a negative config", p.MaxRepairs, DefaultMaxDeliveryRepairs)
		}
	})
}

// testFromCompiledRejects is a table-driven helper that verifies FromCompiled
// returns ok=false for degenerate workflow inputs.
func testFromCompiledRejects(t *testing.T, name string, wf *compiler.CompiledWorkflow) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if _, ok := FromCompiled(wf); ok {
			t.Error("ok = true, want false")
		}
	})
}

func newCompiledWithLimits(t *testing.T, maxTitle, maxMsg int) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Name: "custom-limits", Version: 1, InitialStep: "plan",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps:  []definition.Step{{ID: "plan", Kind: "agent", Agent: "planner"}},
		Transitions: []definition.Transition{{
			From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"},
		}},
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
			TitleTemplate: "feat: {{ inputs.task }}", CommitMessageTemplate: "feat: {{ inputs.task }}",
			MaxTitleBytes: maxTitle, MaxCommitMessageBytes: maxMsg,
		},
	}
	cw, err := compiler.Compile(wf)
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	return cw
}

func assertPolicyFields(t *testing.T, p Policy, kind, mode, provider, base string) {
	t.Helper()
	if p.Kind != kind || p.Mode != mode || p.Provider != provider || p.Base != base {
		t.Errorf("policy = %+v, want %s/%s/%s/%s", p, kind, mode, provider, base)
	}
}

func assertPolicyTemplate(t *testing.T, p Policy, field, want string) {
	t.Helper()
	switch field {
	case "TitleTemplate":
		if p.TitleTemplate != want {
			t.Errorf("TitleTemplate = %q, want %q", p.TitleTemplate, want)
		}
	case "CommitMessageTemplate":
		if p.CommitMessageTemplate != want {
			t.Errorf("CommitMessageTemplate = %q, want %q", p.CommitMessageTemplate, want)
		}
	}
}

func assertSizeLimits(t *testing.T, p Policy, wantTitle, wantMsg int) {
	t.Helper()
	if p.MaxTitleBytes != wantTitle {
		t.Errorf("MaxTitleBytes = %d, want %d", p.MaxTitleBytes, wantTitle)
	}
	if p.MaxCommitMessageBytes != wantMsg {
		t.Errorf("MaxCommitMessageBytes = %d, want %d", p.MaxCommitMessageBytes, wantMsg)
	}
}

func TestPolicyValidate(t *testing.T) {
	t.Run("valid policy passes", func(t *testing.T) {
		p := Policy{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("ready mode passes", func(t *testing.T) {
		p := Policy{Kind: "pull_request", Mode: "ready", Provider: "github", Base: "main"}
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	cases := []struct {
		name   string
		policy Policy
		substr string
	}{
		{"unknown kind", Policy{Kind: "merge", Mode: "draft", Provider: "github", Base: "main"}, "kind"},
		{"invalid mode", Policy{Kind: "pull_request", Mode: "urgent", Provider: "github", Base: "main"}, "mode"},
		{"unsupported provider", Policy{Kind: "pull_request", Mode: "draft", Provider: "gitlab", Base: "main"}, "provider"},
		{"empty provider", Policy{Kind: "pull_request", Mode: "draft", Provider: "", Base: "main"}, "provider"},
		{"empty base", Policy{Kind: "pull_request", Mode: "draft", Provider: "github", Base: ""}, "base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error %q should mention %q", err.Error(), tc.substr)
			}
		})
	}

	t.Run("empty mode treated as none", func(t *testing.T) {
		p := Policy{Kind: "", Mode: "", Provider: "github"}
		if err := p.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil for empty mode", err)
		}
	})

	t.Run("mode none is valid", func(t *testing.T) {
		p := Policy{Kind: "", Mode: "none", Provider: "github"}
		if err := p.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil for mode none", err)
		}
	})
}

func TestRenderTitle(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		p := Policy{TitleTemplate: "feat: {{ inputs.task }}"}
		got, err := p.RenderTitle(map[string]string{"task": "add delivery policy"})
		if err != nil {
			t.Fatalf("RenderTitle: %v", err)
		}
		if want := "feat: add delivery policy"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("missing binding", func(t *testing.T) {
		assertMissingBinding(t, func(inputs map[string]string) (string, error) {
			return Policy{TitleTemplate: "{{ inputs.task }}"}.RenderTitle(inputs)
		}, map[string]string{"other": "x"})
	})

	t.Run("control characters rejected", func(t *testing.T) {
		assertRejectedInput(t, "title", "{{ inputs.task }}", []string{
			"bad\x00title", "bad\x1btitle", "bad\u0085title", "bad\u009btitle",
		})
		// A newline folds to a space instead of failing. A title field holds
		// one line, and a multi-line task input must not stop delivery. The
		// invariant that matters is that the result carries no newline.
		t.Run("newline folded in title", func(t *testing.T) {
			p := Policy{TitleTemplate: "{{ inputs.task }}"}
			got, err := p.RenderTitle(map[string]string{"task": "line1\nline2"})
			if err != nil {
				t.Fatalf("RenderTitle: %v", err)
			}
			if got != "line1 line2" {
				t.Fatalf("RenderTitle = %q, want %q", got, "line1 line2")
			}
		})
	})

	t.Run("size cap truncates gracefully", func(t *testing.T) {
		cap := 256
		big := strings.Repeat("a", cap)
		p := Policy{TitleTemplate: "{{ inputs.a }} {{ inputs.b }}", MaxTitleBytes: cap}
		got, err := p.RenderTitle(map[string]string{"a": big, "b": big})
		if err != nil {
			t.Fatalf("RenderTitle: unexpected error: %v", err)
		}
		if len(got) > cap {
			t.Errorf("truncated title %d bytes exceeds cap %d", len(got), cap)
		}
		if len(got) == 2*cap+1 {
			t.Errorf("title was not truncated, got %d bytes", len(got))
		}
	})

	t.Run("github 256-rune ceiling beats the high byte default", func(t *testing.T) {
		// MaxTitleBytes=0 resolves to DefaultMaxTitleBytes (65536 bytes), but
		// GitHub's 256-character title limit is the effective ceiling, so a
		// 10000-character task renders truncated to MaxTitleRunes runes.
		p := Policy{TitleTemplate: "{{ inputs.task }}", MaxTitleBytes: 0}
		got, err := p.RenderTitle(map[string]string{"task": strings.Repeat("x", 10000)})
		if err != nil {
			t.Fatalf("RenderTitle: unexpected error: %v", err)
		}
		if want := strings.Repeat("x", MaxTitleRunes); got != want {
			t.Errorf("got %d runes, want %d runes under the GitHub 256-character ceiling", utf8.RuneCountInString(got), MaxTitleRunes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("title is not valid UTF-8: %q", got)
		}
	})
}

func TestRenderCommitMessage(t *testing.T) {
	t.Run("happy path with newline", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "feat: {{ inputs.task }}\n\nBody."}
		got, err := p.RenderCommitMessage(map[string]string{"task": "add delivery"})
		if err != nil {
			t.Fatalf("RenderCommitMessage: %v", err)
		}
		if want := "feat: add delivery\n\nBody."; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("newline from binding allowed", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}"}
		got, err := p.RenderCommitMessage(map[string]string{"task": "line1\nline2"})
		if err != nil {
			t.Fatalf("RenderCommitMessage: %v", err)
		}
		if want := "line1\nline2"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("control characters rejected", func(t *testing.T) {
		assertRejectedInput(t, "commit message", "{{ inputs.task }}", []string{
			"bad\x00message", "bad\x07message", "bad\u0085message", "bad\u009bmessage",
		})
	})

	t.Run("missing binding", func(t *testing.T) {
		assertMissingBinding(t, func(inputs map[string]string) (string, error) {
			return Policy{CommitMessageTemplate: "{{ inputs.task }}"}.RenderCommitMessage(inputs)
		}, nil)
	})

	t.Run("size cap truncates gracefully", func(t *testing.T) {
		cap := 512
		big := strings.Repeat("a", cap)
		p := Policy{CommitMessageTemplate: "{{ inputs.a }}\n{{ inputs.b }}", MaxCommitMessageBytes: cap}
		got, err := p.RenderCommitMessage(map[string]string{"a": big, "b": big})
		if err != nil {
			t.Fatalf("RenderCommitMessage: unexpected error: %v", err)
		}
		if len(got) > cap {
			t.Errorf("truncated commit message %d bytes exceeds cap %d", len(got), cap)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("truncated commit should end with \"...\", got tail %q", got[len(got)-5:])
		}
	})

	t.Run("configurable high default", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}", MaxCommitMessageBytes: 0}
		got, err := p.RenderCommitMessage(map[string]string{"task": strings.Repeat("x", 50000)})
		if err != nil {
			t.Fatalf("RenderCommitMessage: unexpected error: %v", err)
		}
		if want := strings.Repeat("x", 50000); got != want {
			t.Errorf("got %d bytes, want %d bytes (high default)", len(got), len(want))
		}
	})
}

func TestTruncateRenderedWordBoundary(t *testing.T) {
	t.Run("space at exact cutoff is excluded from scan", func(t *testing.T) {
		// "abc def " = 8 bytes: a(0) b(1) c(2) (3) d(4) e(5) f(6) (7)
		// maxBytes=7 → cut=7 → findLastSpace scans 0-6 (inclusive).
		// Space at index 3 is found within the scan range.
		// Before the fix (passing cut=7 to findLastSpace), it would scan 0-7
		// and find the trailing space at index 7, cutting to "abc def " (8 bytes)
		// which exceeds maxBytes=7.
		s := "abc def "
		got, err := truncateRendered(s, 7, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "abc"; got != want {
			t.Errorf("got %q (%d bytes), want %q (%d bytes)", got, len(got), want, len(want))
		}
		if len(got) > 7 {
			t.Errorf("result %d bytes exceeds maxBytes 7", len(got))
		}
	})

	t.Run("non-space at limit with space one byte before", func(t *testing.T) {
		// "hello worlx" = 11 bytes. maxBytes=10: bytes 0-9 included, byte 10 excluded.
		// The space at index 5 is the last space within bytes 0-9.
		s := "hello worlx"
		got, err := truncateRendered(s, 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "hello"; got != want {
			t.Errorf("got %q (%d bytes), want %q (%d bytes)", got, len(got), want, len(want))
		}
		if len(got) > 10 {
			t.Errorf("result %d bytes exceeds maxBytes 10", len(got))
		}
	})

	t.Run("no space in range uses byte boundary", func(t *testing.T) {
		// "abcdefghijk" = 11 bytes, no spaces. maxBytes=10.
		// No space to break at, so it should cut at byte 10.
		s := "abcdefghijk"
		got, err := truncateRendered(s, 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "abcdefghij"; got != want {
			t.Errorf("got %q (%d bytes), want %q (%d bytes)", got, len(got), want, len(want))
		}
	})

	t.Run("under limit returns unchanged", func(t *testing.T) {
		s := "hello"
		got, err := truncateRendered(s, 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != s {
			t.Errorf("got %q, want %q", got, s)
		}
	})
}

// assertRejectedInput verifies that each bad input value is rejected by
// RenderTitle or RenderCommitMessage.
func assertRejectedInput(t *testing.T, label, tmpl string, bad []string) {
	t.Helper()
	for _, v := range bad {
		t.Run(v, func(t *testing.T) {
			_, err := Policy{TitleTemplate: tmpl}.RenderTitle(map[string]string{"task": v})
			if err == nil {
				t.Fatalf("RenderTitle accepted %q in %s", v, label)
			}
			_, err2 := Policy{CommitMessageTemplate: tmpl}.RenderCommitMessage(map[string]string{"task": v})
			if err2 == nil {
				t.Fatalf("RenderCommitMessage accepted %q in %s", v, label)
			}
		})
	}
}

// assertMissingBinding verifies that a render function fails with a "missing" error.
func assertMissingBinding(t *testing.T, render func(map[string]string) (string, error), inputs map[string]string) {
	t.Helper()
	_, err := render(inputs)
	if err == nil {
		t.Fatal("nil error for missing binding")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error %q should mention missing binding", err.Error())
	}
}

// TestRenderTitleGitHubRuneCeiling pins the 256-character (rune count) ceiling
// GitHub applies to pull-request titles: a rendered title longer than
// MaxTitleRunes runes must be truncated rune-safely, never splitting a rune.
func TestRenderTitleGitHubRuneCeiling(t *testing.T) {
	t.Run("300 ascii characters truncate to 256 runes", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		got, err := p.RenderTitle(map[string]string{"task": strings.Repeat("a", 300)})
		if err != nil {
			t.Fatalf("RenderTitle: %v", err)
		}
		if n := utf8.RuneCountInString(got); n > MaxTitleRunes {
			t.Errorf("title has %d runes, want <= %d", n, MaxTitleRunes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("title is not valid UTF-8: %q", got)
		}
	})

	t.Run("300 CJK characters truncate to 256 runes, valid UTF-8", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		got, err := p.RenderTitle(map[string]string{"task": strings.Repeat("日", 300)})
		if err != nil {
			t.Fatalf("RenderTitle: %v", err)
		}
		if n := utf8.RuneCountInString(got); n > MaxTitleRunes {
			t.Errorf("title has %d runes, want <= %d", n, MaxTitleRunes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("title is not valid UTF-8: %q", got)
		}
	})

	t.Run("configured byte cap below the rune ceiling still wins", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}", MaxTitleBytes: 100}
		got, err := p.RenderTitle(map[string]string{"task": strings.Repeat("a", 300)})
		if err != nil {
			t.Fatalf("RenderTitle: %v", err)
		}
		if len(got) > 100 {
			t.Errorf("title %d bytes exceeds MaxTitleBytes 100", len(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("title is not valid UTF-8: %q", got)
		}
	})
}

// TestTruncateRunesNeverSplitsAGrapheme pins a real gap an adversarial review
// pass caught: truncateRunes was rune-safe (never splits a multi-byte UTF-8
// sequence) but not GRAPHEME-safe. A base character followed by a combining
// mark - e.g. 'e' (U+0065) + COMBINING ACUTE ACCENT (U+0301), which together
// render as "é" but are two separate runes - could get split exactly between
// them: the truncated result kept the bare base character and silently
// dropped its diacritic, which is valid UTF-8 but visibly wrong text.
func TestTruncateRunesNeverSplitsAGrapheme(t *testing.T) {
	// 150 decomposed "e"+combining-acute-accent pairs (300 runes). A cut at
	// exactly 243 runes (the affix "[stack 22/3]" is 12 runes, room =
	// 256-12-1 = 243) used to land after the 122nd pair's base character but
	// before its combining mark.
	base := strings.Repeat("é", 150)
	affix := "[stack 22/3]"
	title := deriveTitle(base, affix, MaxTitleRunes)
	if !strings.HasSuffix(title, " "+affix) {
		t.Fatalf("title = %q, want it to end with the untruncated affix %q", title, affix)
	}
	baseTrunc := strings.TrimSuffix(title, " "+affix)
	runes := []rune(baseTrunc)
	if len(runes)%2 != 0 {
		t.Fatalf("truncated base has %d runes (odd) - an odd count means the cut landed on a lone base character missing its combining mark; base=%q", len(runes), baseTrunc)
	}
	// The last rune of a complete "e"+combining-acute-accent run must be the
	// mark itself (U+0301) - proof the cut ended on a whole pair, not a bare
	// base character.
	if len(runes) > 0 && runes[len(runes)-1] != '́' {
		t.Fatalf("truncated base ends with %U, want the combining mark U+0301 (a complete pair)", runes[len(runes)-1])
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is not valid UTF-8: %q", title)
	}
}

// TestRenderTemplateAppliesRedaction pins that renderTemplate applies the
// process-wide redaction policy (redact.Text) to the rendered title and the
// rendered commit message, so a credential-shaped input never reaches GitHub
// verbatim. The policy is a process-wide global; no test here may run in
// parallel while depending on it.
func TestRenderTemplateAppliesRedaction(t *testing.T) {
	previous := redact.Current()
	policy, err := redact.Compile(
		[]string{`(?:sk-ant-|sk-|ghp_|github_pat_)[A-Za-z0-9._~-]+`},
		nil, "[redacted]",
	)
	if err != nil {
		t.Fatalf("compile redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	const credential = "ghp_1234567890abcdef"
	t.Run("title is redacted", func(t *testing.T) {
		got, err := Policy{TitleTemplate: "feat: {{ inputs.task }}"}.RenderTitle(map[string]string{"task": "use " + credential})
		if err != nil {
			t.Fatalf("RenderTitle: %v", err)
		}
		if strings.Contains(got, credential) {
			t.Errorf("title leaks credential: %q", got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("title lacks redaction placeholder: %q", got)
		}
	})

	t.Run("commit message is redacted", func(t *testing.T) {
		got, err := Policy{CommitMessageTemplate: "feat: {{ inputs.task }}\n\nBody."}.RenderCommitMessage(map[string]string{"task": "use " + credential})
		if err != nil {
			t.Fatalf("RenderCommitMessage: %v", err)
		}
		if strings.Contains(got, credential) {
			t.Errorf("commit message leaks credential: %q", got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("commit message lacks redaction placeholder: %q", got)
		}
	})
}

// TestRenderTemplateRedactsNothingWithoutPolicy pins the fail-open posture: an
// unconfigured workspace redacts nothing, so rendering is an identity.
func TestRenderTemplateRedactsNothingWithoutPolicy(t *testing.T) {
	previous := redact.Current()
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	const credential = "sk-ant-aaaabbbbccccdddd"
	got, err := Policy{TitleTemplate: "{{ inputs.task }}"}.RenderTitle(map[string]string{"task": credential})
	if err != nil {
		t.Fatalf("RenderTitle: %v", err)
	}
	if got != credential {
		t.Errorf("unconfigured title was altered: %q", got)
	}
}

// TestValidateCommitSubject pins the optional workspace commit-message
// policy (.mivia/policy/commit-message.json): absent file validates nothing;
// a scope-less or oversized subject is a repairable PRMetadataError (the
// subject is always the agent's own pr_title - see buildCommitMessage - so a
// shape violation is always fixable by editing pr_title); a conforming
// subject passes; an unreadable/malformed policy file is a permanent
// RefusalError (a workspace config defect, not something pr_title can fix).
// Only the generic requireScope/maxSubjectLength fields are enforced - no
// workspace's type or scope lists are compiled in.
func TestValidateCommitSubject(t *testing.T) {
	writeCommitPolicy := func(t *testing.T, root, jsonContent string) {
		t.Helper()
		dir := filepath.Join(root, ".mivia", "policy")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "commit-message.json"), []byte(jsonContent), 0o644); err != nil {
			t.Fatalf("write commit-message.json: %v", err)
		}
	}

	t.Run("absent policy file validates nothing", func(t *testing.T) {
		p := Policy{}
		if err := p.ValidateCommitSubject(t.TempDir(), "fix: no scope here"); err != nil {
			t.Fatalf("ValidateCommitSubject with absent policy = %v, want nil", err)
		}
	})

	t.Run("scope-less subject is a repairable PRMetadataError", func(t *testing.T) {
		root := t.TempDir()
		writeCommitPolicy(t, root, `{"requireScope": true, "maxSubjectLength": 72}`)
		p := Policy{}
		err := p.ValidateCommitSubject(root, "fix: no scope here")
		if err == nil || !IsPRMetadataError(err) {
			t.Fatalf("ValidateCommitSubject = %v, want PRMetadataError", err)
		}
		if !strings.Contains(err.Error(), "type(scope)") {
			t.Errorf("error %q should mention the type(scope) shape", err.Error())
		}
	})

	t.Run("oversized subject is a repairable PRMetadataError", func(t *testing.T) {
		root := t.TempDir()
		writeCommitPolicy(t, root, `{"requireScope": true, "maxSubjectLength": 10}`)
		p := Policy{}
		err := p.ValidateCommitSubject(root, "fix(delivery): this subject is way too long")
		if err == nil || !IsPRMetadataError(err) {
			t.Fatalf("ValidateCommitSubject = %v, want PRMetadataError", err)
		}
		if !strings.Contains(err.Error(), "maxSubjectLength") {
			t.Errorf("error %q should mention maxSubjectLength", err.Error())
		}
	})

	t.Run("conforming subject passes", func(t *testing.T) {
		root := t.TempDir()
		writeCommitPolicy(t, root, `{"requireScope": true, "maxSubjectLength": 72}`)
		p := Policy{}
		if err := p.ValidateCommitSubject(root, "fix(delivery): add validation"); err != nil {
			t.Fatalf("ValidateCommitSubject = %v, want nil", err)
		}
	})

	t.Run("requireScope false allows a scope-less subject", func(t *testing.T) {
		root := t.TempDir()
		writeCommitPolicy(t, root, `{"requireScope": false, "maxSubjectLength": 72}`)
		p := Policy{}
		if err := p.ValidateCommitSubject(root, "fix: no scope needed"); err != nil {
			t.Fatalf("ValidateCommitSubject = %v, want nil", err)
		}
	})

	t.Run("malformed policy file is a permanent refusal", func(t *testing.T) {
		root := t.TempDir()
		writeCommitPolicy(t, root, `{not json`)
		p := Policy{}
		err := p.ValidateCommitSubject(root, "fix(delivery): x")
		if err == nil || !IsRefusal(err) {
			t.Fatalf("ValidateCommitSubject = %v, want RefusalError", err)
		}
	})
}

// TestDeliverValidatesCommitMessageAgainstWorkspacePolicy pins the
// admission-time validation end to end: when .mivia/policy/commit-message.json
// is present in the workspace, a non-conforming title (the commit subject)
// is a repairable PRMetadataError BEFORE any commit or push; when absent,
// delivery proceeds.
func TestDeliverValidatesCommitMessageAgainstWorkspacePolicy(t *testing.T) {
	t.Run("absent policy file: delivery proceeds", func(t *testing.T) {
		res, err, _, _, _ := deliverWithCommitPolicy(t, false, "fix: no scope here")
		if err != nil {
			t.Fatalf("Deliver with absent policy file = %v, want success", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
	})

	t.Run("scope-less title is a repairable PRMetadataError", func(t *testing.T) {
		_, err, worktreeRoot, baseCommit, pr := deliverWithCommitPolicy(t, true, "fix: no scope here")
		if err == nil || !IsPRMetadataError(err) {
			t.Fatalf("Deliver = %v, want PRMetadataError", err)
		}
		if !strings.Contains(err.Error(), "scope") {
			t.Errorf("error %q should mention scope", err.Error())
		}
		assertZeroCreates(t, pr)
		if got := runGitOut(t, worktreeRoot, "rev-parse", "HEAD"); got != baseCommit {
			t.Fatalf("HEAD = %s, want untouched base %s", got, baseCommit)
		}
	})

	t.Run("oversized title is a repairable PRMetadataError", func(t *testing.T) {
		_, err, _, _, pr := deliverWithCommitPolicy(t, true, "fix(delivery): "+strings.Repeat("x", 80))
		if err == nil || !IsPRMetadataError(err) {
			t.Fatalf("Deliver = %v, want PRMetadataError", err)
		}
		if !strings.Contains(err.Error(), "maxSubjectLength") {
			t.Errorf("error %q should mention maxSubjectLength", err.Error())
		}
		assertZeroCreates(t, pr)
	})

	t.Run("conforming title passes with policy present", func(t *testing.T) {
		res, err, worktreeRoot, _, _ := deliverWithCommitPolicy(t, true, "fix(delivery): add validation")
		if err != nil {
			t.Fatalf("Deliver with conforming title = %v, want success", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
		if msg := runGitOut(t, worktreeRoot, "log", "-1", "--format=%s"); msg != "fix(delivery): add validation" {
			t.Fatalf("commit subject = %q, want %q", msg, "fix(delivery): add validation")
		}
		if tree := runGitOut(t, worktreeRoot, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(tree, ".mivia") {
			t.Errorf("delivery commit includes .mivia policy file:\n%s", tree)
		}
	})
}

// deliverWithCommitPolicy runs one Deliver attempt against a fresh fixture,
// writing the workspace commit-message policy file when writePolicy is true.
// title becomes both the resolved PR title (title_template has no {{ }}
// bindings, so it renders verbatim) and, per buildCommitMessage, the commit
// subject - the single field the workspace commit-message policy now
// validates. It returns the delivery outcome plus the fixture values
// subtests need for their post-conditions: worktree root, base commit, and
// the PR client used for the attempt.
func deliverWithCommitPolicy(t *testing.T, writePolicy bool, title string) (Result, error, string, string, *fakePRClient) {
	t.Helper()
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	if writePolicy {
		writeWorkspacePolicy(t, repoRoot, worktreeRoot, `{"version": 1, "requireScope": true, "maxSubjectLength": 72}`)
	}
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pol := defaultPolicy("draft")
	pol.TitleTemplate = title
	pol.CommitMessageTemplate = ""
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, pol, map[string]string{"task": "x"}))
	return res, err, worktreeRoot, baseCommit, pr
}

// writeWorkspacePolicy writes an optional workspace policy file under the
// worktree's .mivia/policy directory and excludes .mivia/ from the fixture's
// index (common exclude file), so delivery reads the policy file but never
// commits it into the delivered diff.
func writeWorkspacePolicy(t *testing.T, repoRoot, worktreeRoot, jsonContent string) {
	t.Helper()
	exclude := filepath.Join(repoRoot, ".git", "info", "exclude")
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open git exclude: %v", err)
	}
	if _, err := f.WriteString("\n.mivia/\n"); err != nil {
		f.Close()
		t.Fatalf("append git exclude: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close git exclude: %v", err)
	}
	dir := filepath.Join(worktreeRoot, ".mivia", "policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	writeWorktreeFile(t, worktreeRoot, filepath.Join(".mivia", "policy", "commit-message.json"), jsonContent)
}
