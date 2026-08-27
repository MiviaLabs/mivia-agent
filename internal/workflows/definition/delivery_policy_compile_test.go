package definition

import (
	"strings"
	"testing"
)

// TestCompile_AcceptsDeliveryFailureBinding pins the host-injected context
// source: a step declaring delivery.failure compiles, and malformed delivery
// sources stay rejected (the grammar widens by exactly one binding).
func TestCompile_AcceptsDeliveryFailureBinding(t *testing.T) {
	wf := newMinimalWorkflow("delivery-failure-binding")
	wf.Steps[0].Context = []ContextBinding{
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
		wf.Delivery = &Delivery{
			Kind:                "pull_request",
			Mode:                "draft",
			Provider:            "github",
			Base:                "main",
			PRTitlePolicy:       "policy/pr-title.toml",
			OnPRMetadataFailure: "plan",
		}
		// The re-entry step must bind delivery.failure (compiler-enforced), so
		// a PR-metadata rejection deterministically reaches the repair agent.
		wf.Steps[0].Context = []ContextBinding{
			{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
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
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
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
	base := func() *WorkflowFile {
		wf := newMinimalWorkflow("delivery-pr-title-policy")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
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
	base := func() *WorkflowFile {
		wf := newMinimalWorkflow("delivery-on-pr-metadata-failure")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
		return wf
	}

	t.Run("existing step id compiles", func(t *testing.T) {
		wf := base()
		wf.Delivery.OnPRMetadataFailure = "plan"
		// The re-entry step must bind delivery.failure (compiler-enforced).
		wf.Steps[0].Context = []ContextBinding{
			{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true},
		}
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

// TestCompile_DeliveryMaxRepairsNegativeRejected pins admission validation:
// delivery.max_repairs is a repair-cycle budget and cannot be negative
// (unbounded repair cycles would burn the run deadline).
func TestCompile_DeliveryMaxRepairsNegativeRejected(t *testing.T) {
	base := func() *WorkflowFile {
		wf := newMinimalWorkflow("delivery-max-repairs-negative")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main"}
		return wf
	}
	wf := base()
	wf.Delivery.MaxRepairs = -1
	assertCompileError(t, wf, "negative max_repairs", "max_repairs must be >= 0")
}

// TestCompile_DeliveryProviderValue pins the admission-time provider check:
// only "github" is supported, and the refusal says so plainly instead of
// implying a provider seam that does not exist. An inactive block (kind "")
// never runs, so its provider value stays unchecked.
func TestCompile_DeliveryProviderValue(t *testing.T) {
	t.Run("non-github provider rejected with support message", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-provider-foreign")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "draft", Provider: "gitlab", Base: "main"}
		_, err := Compile(wf)
		if err == nil {
			t.Fatal(`Compile accepted provider "gitlab", want a refusal`)
		}
		if !strings.Contains(err.Error(), `provider "gitlab" is not supported (only "github" is currently supported)`) {
			t.Fatalf("Compile error = %q, want the only-github support message", err)
		}
	})
	t.Run("mode none with foreign provider rejected on fresh compile", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-provider-mode-none")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "none", Provider: "gitlab", Base: "main"}
		if _, err := Compile(wf); err == nil {
			t.Fatal(`Compile accepted provider "gitlab" with mode "none", want a refusal`)
		}
	})
	t.Run("empty provider still rejected as non-empty", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-provider-empty")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "draft", Base: "main"}
		_, err := Compile(wf)
		if err == nil || !strings.Contains(err.Error(), "provider must be non-empty") {
			t.Fatalf("Compile error = %v, want the non-empty provider refusal", err)
		}
	})
	t.Run("inactive block with foreign provider still compiles", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-provider-inactive")
		wf.Delivery = &Delivery{Kind: "", Mode: "", Provider: "gitlab"}
		if _, err := Compile(wf); err != nil {
			t.Fatalf("Compile rejected an inactive delivery block: %v", err)
		}
	})
	t.Run("resume of an admitted foreign-provider definition is not stranded", func(t *testing.T) {
		wf := newMinimalWorkflow("delivery-provider-resume")
		wf.Delivery = &Delivery{Kind: "pull_request", Mode: "none", Provider: "gitlab", Base: "main"}
		if _, err := CompileForResume(wf); err != nil {
			t.Fatalf("CompileForResume rejected an admitted definition: %v", err)
		}
	})
}
