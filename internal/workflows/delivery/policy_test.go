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
	t.Run("nil workflow", func(t *testing.T) {
		p, ok := FromCompiled(nil)
		if ok {
			t.Fatalf("ok = true for nil workflow, want false")
		}
		if p != (Policy{}) {
			t.Errorf("policy = %+v, want zero Policy", p)
		}
	})

	t.Run("nil delivery", func(t *testing.T) {
		wf := &compiler.CompiledWorkflow{Delivery: nil}
		if _, ok := FromCompiled(wf); ok {
			t.Error("ok = true for nil delivery, want false")
		}
	})

	t.Run("empty kind", func(t *testing.T) {
		wf := &compiler.CompiledWorkflow{Delivery: &definition.Delivery{Kind: "", Mode: "draft"}}
		if _, ok := FromCompiled(wf); ok {
			t.Error("ok = true for empty kind, want false")
		}
	})

	t.Run("mode none", func(t *testing.T) {
		wf := &compiler.CompiledWorkflow{Delivery: &definition.Delivery{Kind: "pull_request", Mode: "none"}}
		if _, ok := FromCompiled(wf); ok {
			t.Error("ok = true for mode none, want false")
		}
	})

	t.Run("empty mode", func(t *testing.T) {
		wf := &compiler.CompiledWorkflow{Delivery: &definition.Delivery{Kind: "pull_request", Mode: ""}}
		if _, ok := FromCompiled(wf); ok {
			t.Error("ok = true for empty mode, want false")
		}
	})

	t.Run("mode draft snapshots policy", func(t *testing.T) {
		cw := newCompiledPRWorkflow(t, "draft")
		p, ok := FromCompiled(cw)
		if !ok {
			t.Fatal("ok = false for draft mode, want true")
		}
		if p.Kind != "pull_request" || p.Mode != "draft" || p.Provider != "github" || p.Base != "main" {
			t.Errorf("policy = %+v, want pull_request/draft/github/main", p)
		}
		if p.TitleTemplate != "feat: {{ inputs.task }}" {
			t.Errorf("TitleTemplate = %q, want %q", p.TitleTemplate, "feat: {{ inputs.task }}")
		}
		if p.CommitMessageTemplate != "feat: {{ inputs.task }}\n\nBody." {
			t.Errorf("CommitMessageTemplate = %q", p.CommitMessageTemplate)
		}
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
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		_, err := p.RenderTitle(map[string]string{"other": "x"})
		if err == nil {
			t.Fatal("RenderTitle: nil error for missing binding")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error %q should mention missing binding", err.Error())
		}
	})

	t.Run("NUL rejected", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		if _, err := p.RenderTitle(map[string]string{"task": "bad\x00title"}); err == nil {
			t.Fatal("RenderTitle: nil error for NUL byte")
		}
	})

	t.Run("control character rejected", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		if _, err := p.RenderTitle(map[string]string{"task": "bad\x1btitle"}); err == nil {
			t.Fatal("RenderTitle: nil error for ESC control char")
		}
	})

	t.Run("C1 control characters rejected", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		for _, bad := range []string{"bad\u0085title", "bad\u009btitle"} {
			if _, err := p.RenderTitle(map[string]string{"task": bad}); err == nil {
				t.Fatalf("RenderTitle: nil error for C1 control char %q", bad)
			}
		}
	})

	t.Run("newline rejected in title", func(t *testing.T) {
		p := Policy{TitleTemplate: "{{ inputs.task }}"}
		if _, err := p.RenderTitle(map[string]string{"task": "line1\nline2"}); err == nil {
			t.Fatal("RenderTitle: nil error for newline")
		}
	})

	t.Run("size cap", func(t *testing.T) {
		big := strings.Repeat("a", MaxTitleBytes/2+1)
		p := Policy{TitleTemplate: "{{ inputs.a }}{{ inputs.b }}"}
		_, err := p.RenderTitle(map[string]string{"a": big, "b": big})
		if err == nil {
			t.Fatal("RenderTitle: nil error for oversized render")
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error %q should mention size", err.Error())
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

	t.Run("NUL rejected", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}"}
		if _, err := p.RenderCommitMessage(map[string]string{"task": "bad\x00message"}); err == nil {
			t.Fatal("RenderCommitMessage: nil error for NUL byte")
		}
	})

	t.Run("other control characters rejected", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}"}
		if _, err := p.RenderCommitMessage(map[string]string{"task": "bad\x07message"}); err == nil {
			t.Fatal("RenderCommitMessage: nil error for bell control char")
		}
	})

	t.Run("C1 control characters rejected", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}"}
		for _, bad := range []string{"bad\u0085message", "bad\u009bmessage"} {
			if _, err := p.RenderCommitMessage(map[string]string{"task": bad}); err == nil {
				t.Fatalf("RenderCommitMessage: nil error for C1 control char %q", bad)
			}
		}
	})

	t.Run("missing binding", func(t *testing.T) {
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}"}
		_, err := p.RenderCommitMessage(nil)
		if err == nil {
			t.Fatal("RenderCommitMessage: nil error for missing binding")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error %q should mention missing binding", err.Error())
		}
	})

	t.Run("size cap", func(t *testing.T) {
		big := strings.Repeat("a", MaxCommitBytes/2+1)
		p := Policy{CommitMessageTemplate: "{{ inputs.a }}{{ inputs.b }}"}
		_, err := p.RenderCommitMessage(map[string]string{"a": big, "b": big})
		if err == nil {
			t.Fatal("RenderCommitMessage: nil error for oversized render")
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error %q should mention size", err.Error())
		}
	})
}
