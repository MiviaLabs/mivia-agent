package presentation

import (
	"sort"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestFormatWorkflowExplain_Basic(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "test-workflow",
		Description: "A test workflow",
		Version:     1,
		Digest:      "abc123",
		InitialStep: "step1",
		Steps: []definition.Step{
			{ID: "step1", Kind: "agent", Agent: "worker"},
			{ID: "step2", Kind: "agent", Agent: "reviewer"},
		},
		Transitions: []definition.Transition{
			{From: "step1", To: "step2", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "step2", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Agents:     []string{"reviewer", "worker"},
		References: []string{"template: templates/plan.md"},
	}

	out := FormatWorkflowExplain(cw)

	// Check header
	if !strings.Contains(out, "Workflow: test-workflow (v1)") {
		t.Fatalf("missing workflow name/version in output:\n%s", out)
	}
	if !strings.Contains(out, "A test workflow") {
		t.Fatalf("missing description in output:\n%s", out)
	}
	if !strings.Contains(out, "Digest: abc123") {
		t.Fatalf("missing digest in output:\n%s", out)
	}

	// Check state graph
	if !strings.Contains(out, "State Graph") {
		t.Fatalf("missing state graph section:\n%s", out)
	}
	if !strings.Contains(out, "→ [agent] step1") {
		t.Fatalf("missing initial step marker:\n%s", out)
	}
	if !strings.Contains(out, "[agent] step2") {
		t.Fatalf("missing step2:\n%s", out)
	}
	if !strings.Contains(out, "when status=succeeded → step2") {
		t.Fatalf("missing transition step1→step2:\n%s", out)
	}
	if !strings.Contains(out, "when status=succeeded → success") {
		t.Fatalf("missing transition step2→success:\n%s", out)
	}

	// Check declared authority
	if !strings.Contains(out, "Declared Authority") {
		t.Fatalf("missing declared authority section:\n%s", out)
	}
	if !strings.Contains(out, "agent: worker") {
		t.Fatalf("missing worker agent:\n%s", out)
	}
	if !strings.Contains(out, "agent: reviewer") {
		t.Fatalf("missing reviewer agent:\n%s", out)
	}

	// Check references
	if !strings.Contains(out, "References") {
		t.Fatalf("missing references section:\n%s", out)
	}
	if !strings.Contains(out, "template: templates/plan.md") {
		t.Fatalf("missing template reference:\n%s", out)
	}
}

func TestFormatWorkflowExplain_WithLoops(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "loop-workflow",
		Description: "",
		Version:     1,
		Digest:      "def456",
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{
				From: "implement", To: "implement",
				Match:         definition.MatchCriteria{Status: "failed"},
				Loop:          "fix-loop",
				MaxIterations: 3,
			},
			{
				From: "implement", To: "success",
				Match: definition.MatchCriteria{Status: "succeeded"},
			},
		},
		LoopNames: []string{"fix-loop"},
		Agents:    []string{"go-engineer"},
	}

	out := FormatWorkflowExplain(cw)

	// Check loop caps section
	if !strings.Contains(out, "Loop Caps") {
		t.Fatalf("missing loop caps section:\n%s", out)
	}
	if !strings.Contains(out, "fix-loop: max 3 iterations") {
		t.Fatalf("missing loop cap:\n%s", out)
	}

	// Check loop in transition
	if !strings.Contains(out, "loop=fix-loop, max 3") {
		t.Fatalf("missing loop info in transition:\n%s", out)
	}
}

func TestFormatWorkflowExplain_WithDelivery(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "delivery-workflow",
		Description: "",
		Version:     1,
		Digest:      "ghi789",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind:     "pull_request",
			Mode:     "draft",
			Provider: "github",
			Base:     "main",
		},
		Agents: []string{"planner"},
	}

	out := FormatWorkflowExplain(cw)

	if !strings.Contains(out, "Delivery") {
		t.Fatalf("missing delivery section:\n%s", out)
	}
	if !strings.Contains(out, "kind:    pull_request") {
		t.Fatalf("missing delivery kind:\n%s", out)
	}
	if !strings.Contains(out, "mode:    draft") {
		t.Fatalf("missing delivery mode:\n%s", out)
	}
	if !strings.Contains(out, "provider: github") {
		t.Fatalf("missing delivery provider:\n%s", out)
	}
	if !strings.Contains(out, "base:    main") {
		t.Fatalf("missing delivery base:\n%s", out)
	}
}

func TestFormatWorkflowExplain_WithLimits(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:               "limited-workflow",
		Version:            1,
		Digest:             "lmn101",
		InitialStep:        "s1",
		MaxStepAttempts:    12,
		MaxDurationSeconds: 7200,
		Steps: []definition.Step{
			{ID: "s1", Kind: "agent", Agent: "worker"},
		},
		Transitions: []definition.Transition{
			{From: "s1", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Agents: []string{"worker"},
	}

	out := FormatWorkflowExplain(cw)

	if !strings.Contains(out, "Limits") {
		t.Fatalf("missing limits section:\n%s", out)
	}
	if !strings.Contains(out, "max_step_attempts:    12") {
		t.Fatalf("missing max_step_attempts:\n%s", out)
	}
	if !strings.Contains(out, "max_duration_seconds: 7200") {
		t.Fatalf("missing max_duration_seconds:\n%s", out)
	}
}

func TestFormatWorkflowExplain_WithMatchOutput(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "match-workflow",
		Version:     1,
		Digest:      "match123",
		InitialStep: "gate1",
		Steps: []definition.Step{
			{ID: "gate1", Kind: "evidence_gate", Verifier: "go-test"},
		},
		Transitions: []definition.Transition{
			{
				From: "gate1", To: "success",
				Match: definition.MatchCriteria{
					Status: "succeeded",
					Output: map[string]string{"verdict": "approved"},
				},
			},
			{
				From: "gate1", To: "failure",
				Match: definition.MatchCriteria{
					Status: "succeeded",
					Output: map[string]string{"verdict": "rejected"},
				},
			},
		},
		Agents: []string{},
	}

	out := FormatWorkflowExplain(cw)

	if !strings.Contains(out, "status=succeeded, verdict=approved → success") {
		t.Fatalf("missing approved transition:\n%s", out)
	}
	if !strings.Contains(out, "status=succeeded, verdict=rejected → failure") {
		t.Fatalf("missing rejected transition:\n%s", out)
	}
}

func TestFormatWorkflowExplain_Minimal(t *testing.T) {
	// Minimal workflow with no loops, no delivery, no limits, no refs
	cw := &CompiledWorkflowExplain{
		Name:        "minimal",
		Version:     1,
		Digest:      "min000",
		InitialStep: "s1",
		Steps: []definition.Step{
			{ID: "s1", Kind: "human_gate"},
		},
		Transitions: []definition.Transition{
			{From: "s1", To: "success", Match: definition.MatchCriteria{Status: "approved"}},
		},
	}

	out := FormatWorkflowExplain(cw)

	// Should NOT have sections for loops, delivery, limits, authority, refs
	if strings.Contains(out, "Loop Caps") {
		t.Fatalf("unexpected loop caps section:\n%s", out)
	}
	if strings.Contains(out, "Delivery") {
		t.Fatalf("unexpected delivery section:\n%s", out)
	}
	if strings.Contains(out, "Limits") {
		t.Fatalf("unexpected limits section:\n%s", out)
	}
	if strings.Contains(out, "Declared Authority") {
		t.Fatalf("unexpected authority section:\n%s", out)
	}
	if strings.Contains(out, "References") {
		t.Fatalf("unexpected references section:\n%s", out)
	}
}

func TestFormatWorkflowExplain_ReferencesSorted(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "refs-test",
		Version:     1,
		Digest:      "ref000",
		InitialStep: "s1",
		Steps: []definition.Step{
			{ID: "s1", Kind: "agent", Agent: "worker"},
		},
		Transitions: []definition.Transition{
			{From: "s1", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		References: []string{"schema: b.json", "template: a.md", "schema: c.json"},
		Agents:     []string{"worker"},
	}

	out := FormatWorkflowExplain(cw)

	// Extract reference lines and verify sorted order
	var refLines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "template:") || strings.HasPrefix(line, "schema:") {
			refLines = append(refLines, line)
		}
	}

	sorted := make([]string, len(refLines))
	copy(sorted, refLines)
	sort.Strings(sorted)
	for i := range refLines {
		if refLines[i] != sorted[i] {
			t.Fatalf("references not sorted: got %v, expected %v", refLines, sorted)
		}
	}
}

func TestFormatWorkflowExplain_UnlimitedLoop(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "unlimited-loop",
		Version:     1,
		Digest:      "unl000",
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{
				From: "implement", To: "implement",
				Match:         definition.MatchCriteria{Status: "failed"},
				Loop:          "fix-loop",
				MaxIterations: -1,
			},
			{
				From: "implement", To: "success",
				Match: definition.MatchCriteria{Status: "succeeded"},
			},
		},
		LoopNames: []string{"fix-loop"},
		Agents:    []string{"go-engineer"},
	}

	out := FormatWorkflowExplain(cw)

	if !strings.Contains(out, "unlimited") {
		t.Fatalf("expected 'unlimited' in output for MaxIterations=-1:\n%s", out)
	}
	if strings.Contains(out, "max -1") {
		t.Fatalf("should not render 'max -1' for unlimited loop:\n%s", out)
	}
}

func TestFormatWorkflowExplain_AgentsSorted(t *testing.T) {
	cw := &CompiledWorkflowExplain{
		Name:        "agents-test",
		Version:     1,
		Digest:      "agt000",
		InitialStep: "s1",
		Steps: []definition.Step{
			{ID: "s1", Kind: "agent", Agent: "zebra"},
			{ID: "s2", Kind: "agent_gate", Agent: "alpha"},
		},
		Transitions: []definition.Transition{
			{From: "s1", To: "s2", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "s2", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Agents: []string{"zebra", "alpha"}, // unsorted input
	}

	out := FormatWorkflowExplain(cw)

	// Agents should appear sorted in output
	alphaIdx := strings.Index(out, "agent: alpha")
	zebraIdx := strings.Index(out, "agent: zebra")
	if alphaIdx == -1 || zebraIdx == -1 {
		t.Fatalf("missing agent entries in output:\n%s", out)
	}
	if alphaIdx > zebraIdx {
		t.Fatalf("agents not sorted: alpha should come before zebra")
	}
}
