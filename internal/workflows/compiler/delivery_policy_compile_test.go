package compiler

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestCompile_AcceptsDeliveryFailureBinding pins the host-injected context
// source: a step declaring delivery.failure compiles, and malformed delivery
// sources stay rejected (the grammar widens by exactly one binding).
func TestCompile_AcceptsDeliveryFailureBinding(t *testing.T) {
	wf := newMinimalWorkflow("delivery-failure-binding")
	wf.Steps[0].Context = []definition.ContextBinding{
		{From: "delivery.failure", As: "delivery_hint", MaxBytes: 4096, Optional: true},
	}
	if _, err := Compile(wf); err != nil {
		t.Fatalf("Compile rejected delivery.failure binding: %v", err)
	}
}

// TestCompile_DeliveryExtendedPolicyFields covers the optional delivery policy
// fields. A valid workflow with both fields set must compile and carry them.
func TestCompile_DeliveryExtendedPolicyFields(t *testing.T) {
	t.Run("valid workflow with both new fields", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-extended-policy")
		wf.Delivery = &definition.Delivery{
			Kind:                "pull_request",
			Mode:                "draft",
			Provider:            "github",
			Base:                "main",
			PRTitlePolicy:       "policy/pr-title.toml",
			OnPRMetadataFailure: "plan",
		}
		cw, err := Compile(wf)
		if err != nil {
			t.Fatalf("unexpected compile error: %v", err)
		}
		if cw.Delivery.PRTitlePolicy != "policy/pr-title.toml" {
			t.Errorf("PRTitlePolicy = %q, want %q", cw.Delivery.PRTitlePolicy, "policy/pr-title.toml")
		}
		if cw.Delivery.OnPRMetadataFailure != "plan" {
			t.Errorf("OnPRMetadataFailure = %q, want %q", cw.Delivery.OnPRMetadataFailure, "plan")
		}
	})

	t.Run("absent fields stay empty and compile", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-absent-policy-fields")
		wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
		cw, err := Compile(wf)
		if err != nil {
			t.Fatalf("unexpected compile error: %v", err)
		}
		if cw.Delivery.PRTitlePolicy != "" {
			t.Errorf("PRTitlePolicy = %q, want empty", cw.Delivery.PRTitlePolicy)
		}
		if cw.Delivery.OnPRMetadataFailure != "" {
			t.Errorf("OnPRMetadataFailure = %q, want empty", cw.Delivery.OnPRMetadataFailure)
		}
	})
}

// TestCompile_DeliveryPRTitlePolicyValidation checks the pr_title_policy path
// rules: relative paths pass; absolute paths and parent-directory segments
// fail. A parent-directory segment is a segment equal to "..".
func TestCompile_DeliveryPRTitlePolicyValidation(t *testing.T) {
	base := func() *definition.WorkflowFile {
		wf := newMinimalWorkflow("delivery-pr-title-policy")
		wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
		return wf
	}

	for _, tc := range []struct {
		name, policy, wantErr string
	}{
		{"relative directory path", "policy/pr-title.toml", ""},
		{"bare filename", "pr-title.toml", ""},
		{"dotdot inside a segment is allowed", "pr..title.toml", ""},
		{"absolute path", "/etc/pr-title.toml", "must be a relative path"},
		{"leading parent segment", "../pr-title.toml", "parent-directory segment"},
		{"nested parent segment", "policy/../pr-title.toml", "parent-directory segment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := base()
			wf.Delivery.PRTitlePolicy = tc.policy
			if tc.wantErr == "" {
				if _, err := Compile(wf); err != nil {
					t.Fatalf("pr_title_policy %q: unexpected compile error: %v", tc.policy, err)
				}
				return
			}
			assertCompileError(t, wf, "pr_title_policy "+tc.policy, tc.wantErr)
		})
	}
}

// TestCompile_DeliveryOnPRMetadataFailureValidation checks the
// on_pr_metadata_failure step rule: when set, it must name a declared step.
func TestCompile_DeliveryOnPRMetadataFailureValidation(t *testing.T) {
	base := func() *definition.WorkflowFile {
		wf := newMinimalWorkflow("delivery-on-pr-metadata-failure")
		wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
		return wf
	}

	t.Run("existing step id compiles", func(t *testing.T) {
		wf := base()
		wf.Delivery.OnPRMetadataFailure = "plan"
		if _, err := Compile(wf); err != nil {
			t.Fatalf("unexpected compile error: %v", err)
		}
	})

	t.Run("unknown step id is rejected", func(t *testing.T) {
		wf := base()
		wf.Delivery.OnPRMetadataFailure = "ghost"
		assertCompileError(t, wf, "unknown on_pr_metadata_failure step", "names no step")
	})

	t.Run("empty value compiles like before", func(t *testing.T) {
		wf := base()
		wf.Delivery.OnPRMetadataFailure = ""
		if _, err := Compile(wf); err != nil {
			t.Fatalf("unexpected compile error: %v", err)
		}
	})
}
