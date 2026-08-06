package delivery

import (
	"strings"
	"testing"

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
		t.Run("newline rejected in title", func(t *testing.T) {
			p := Policy{TitleTemplate: "{{ inputs.task }}"}
			if _, err := p.RenderTitle(map[string]string{"task": "line1\nline2"}); err == nil {
				t.Fatal("RenderTitle: nil error for newline")
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

	t.Run("configurable high default", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}", MaxTitleBytes: 0}
		got, err := p.RenderTitle(map[string]string{"task": strings.Repeat("x", 10000)})
		if err != nil {
			t.Fatalf("RenderTitle: unexpected error: %v", err)
		}
		if want := strings.Repeat("x", 10000); got != want {
			t.Errorf("got %d bytes, want %d bytes (high default)", len(got), len(want))
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
