package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestCompile_CommandGateCompiles verifies that an evidence_gate step may
// declare a sandboxed command instead of a named verifier profile, and that
// the compiled step keeps the command declaration.
func TestCompile_CommandGateCompiles(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "test-command-gate",
		Version:     1,
		InitialStep: "verify",
		Steps: []definition.Step{{
			ID: "verify", Kind: "evidence_gate",
			Command: &definition.StepCommand{Check: "gate", Program: "python3", Args: []string{"scripts/gate.py"}},
		}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := Compile(wf)
	if err != nil {
		t.Fatalf("unexpected compile error for command gate: %v", err)
	}
	step := compiled.Steps[0]
	if step.Command == nil || step.Command.Program != "python3" || step.Verifier != "" {
		t.Fatalf("compiled command gate = %#v, want command program python3 and no verifier", step)
	}
}

// TestCompile_CommandGatePathProgramRejected verifies that a command gate
// whose program is a path (not a bare executable name) fails compilation.
func TestCompile_CommandGatePathProgramRejected(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "test-command-gate-path",
		Version:     1,
		InitialStep: "verify",
		Steps: []definition.Step{{
			ID: "verify", Kind: "evidence_gate",
			Command: &definition.StepCommand{Check: "gate", Program: "/usr/bin/python3"},
		}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for command gate with a path program")
	}
	if !strings.Contains(err.Error(), "bare executable name") {
		t.Errorf("error %q should mention bare executable name", err.Error())
	}
}

// TestCompile_EvidenceGateRequiresVerifierOrCommand verifies that an
// evidence_gate with neither a named verifier nor a command fails compilation.
func TestCompile_EvidenceGateRequiresVerifierOrCommand(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "test-gate-bare",
		Version:     1,
		InitialStep: "verify",
		Steps:       []definition.Step{{ID: "verify", Kind: "evidence_gate"}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for evidence gate with neither verifier nor command")
	}
	if !strings.Contains(err.Error(), "requires a verifier or command") {
		t.Errorf("error %q should mention verifier or command", err.Error())
	}
}
