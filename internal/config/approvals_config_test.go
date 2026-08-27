package config

import (
	"testing"
)

func TestApprovalsConfigDefaults(t *testing.T) {
	var a ApprovalsConfig
	if a.ApprovalPolicy() != ApprovalPolicyAuto {
		t.Fatalf("expected default %q (accept all tools out of the box), got %q", ApprovalPolicyAuto, a.ApprovalPolicy())
	}
	if !a.IsAuto() {
		t.Fatalf("expected IsAuto=true for default (unset) policy")
	}
}

func TestApprovalsConfigDefaultModeWinsOverLegacyPolicy(t *testing.T) {
	a := ApprovalsConfig{Policy: "auto", DefaultMode: "deny"}
	if got := a.ApprovalPolicy(); got != ApprovalPolicyDeny {
		t.Fatalf("ApprovalPolicy() = %q, want %q (DefaultMode must win over legacy Policy)", got, ApprovalPolicyDeny)
	}
}

func TestApprovalsConfigNormalization(t *testing.T) {
	tests := []struct {
		input  string
		want   string
		isAuto bool
	}{
		{"auto", ApprovalPolicyAuto, true},
		{"never", ApprovalPolicyAuto, true},
		{"none", ApprovalPolicyAuto, true},
		{"yolo", ApprovalPolicyAuto, true},
		{"YOLO", ApprovalPolicyAuto, true},
		{"always", ApprovalPolicyAlways, false},
		{"paranoid", ApprovalPolicyAlways, false},
		{"deny", ApprovalPolicyDeny, false},
		{"deny_always", ApprovalPolicyDeny, false},
		{"write-only", ApprovalPolicyWriteOnly, false},
		{"writes", ApprovalPolicyWriteOnly, false},
		{"once", ApprovalPolicyWriteOnly, false},
		{"", ApprovalPolicyAuto, true},
		{"unknown", ApprovalPolicyWriteOnly, false},
	}

	for _, tc := range tests {
		cfg := ApprovalsConfig{Policy: tc.input}
		if got := cfg.ApprovalPolicy(); got != tc.want {
			t.Errorf("ApprovalPolicy(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if got := cfg.IsAuto(); got != tc.isAuto {
			t.Errorf("IsAuto(%q) = %v, want %v", tc.input, got, tc.isAuto)
		}
	}
}

func TestApprovalsConfigFromTOML(t *testing.T) {
	cfgPath := writeMinimalConfig(t, "\n[approvals]\npolicy = \"auto\"\n")
	res, err := Load(LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !res.Approvals.IsAuto() {
		t.Fatalf("expected Approvals.IsAuto() == true, got policy %q", res.Approvals.ApprovalPolicy())
	}
}

func TestApprovalsConfigDefaultModeFromTOML(t *testing.T) {
	cfgPath := writeMinimalConfig(t, "\n[approvals]\ndefault_mode = \"deny\"\n")
	res, err := Load(LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if got := res.Approvals.ApprovalPolicy(); got != ApprovalPolicyDeny {
		t.Fatalf("Approvals.ApprovalPolicy() = %q, want %q", got, ApprovalPolicyDeny)
	}
}

func TestPolicyPredicates(t *testing.T) {
	if !IsAutoPolicy("yolo") || !IsAutoPolicy("auto") || !IsAutoPolicy("none") || !IsAutoPolicy("never") {
		t.Errorf("IsAutoPolicy failed for auto aliases")
	}
	if IsAutoPolicy("always") || IsAutoPolicy("write-only") || IsAutoPolicy("deny") {
		t.Errorf("IsAutoPolicy returned true for non-auto policy")
	}

	if !IsAlwaysPolicy("always") || !IsAlwaysPolicy("paranoid") || !IsAlwaysPolicy("all") {
		t.Errorf("IsAlwaysPolicy failed for always aliases")
	}
	if IsAlwaysPolicy("auto") || IsAlwaysPolicy("write-only") || IsAlwaysPolicy("deny") {
		t.Errorf("IsAlwaysPolicy returned true for non-always policy")
	}

	if !IsDenyPolicy("deny") || !IsDenyPolicy("deny_always") {
		t.Errorf("IsDenyPolicy failed for deny aliases")
	}
	if IsDenyPolicy("auto") || IsDenyPolicy("always") || IsDenyPolicy("write-only") {
		t.Errorf("IsDenyPolicy returned true for non-deny policy")
	}
}
