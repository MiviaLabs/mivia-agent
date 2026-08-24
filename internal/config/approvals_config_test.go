package config

import (
	"testing"
)

func TestApprovalsConfigDefaults(t *testing.T) {
	var a ApprovalsConfig
	if a.ApprovalPolicy() != ApprovalPolicyWriteOnly {
		t.Fatalf("expected default %q, got %q", ApprovalPolicyWriteOnly, a.ApprovalPolicy())
	}
	if a.IsAuto() {
		t.Fatalf("expected IsAuto=false for default policy")
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
		{"write-only", ApprovalPolicyWriteOnly, false},
		{"writes", ApprovalPolicyWriteOnly, false},
		{"", ApprovalPolicyWriteOnly, false},
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
