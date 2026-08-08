package delivery

import (
	"errors"
	"strings"
	"testing"
)

// TestProbePRToolFailsFastOnMissingTool pins the reason the probe exists:
// without it a missing gh is discovered only at delivery, after every gate has
// passed and the whole run is spent.
func TestProbePRToolFailsFastOnMissingTool(t *testing.T) {
	original := prToolProbe
	t.Cleanup(func() { prToolProbe = original })
	sentinel := errors.New("executable file not found in $PATH")
	prToolProbe = func(string) error { return sentinel }

	err := ProbePRTool("github")
	if err == nil {
		t.Fatal("ProbePRTool error = nil, want a missing-tool error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap the probe failure", err)
	}
	if !strings.Contains(err.Error(), "gh") {
		t.Errorf("error %q does not name the tool the operator must install", err)
	}
}

func TestProbePRToolAcceptsUsableTool(t *testing.T) {
	original := prToolProbe
	t.Cleanup(func() { prToolProbe = original })
	var got string
	prToolProbe = func(program string) error { got = program; return nil }

	if err := ProbePRTool("github"); err != nil {
		t.Fatalf("ProbePRTool error = %v, want nil", err)
	}
	if got != "gh" {
		t.Errorf("probed %q, want gh", got)
	}
}

// TestProbePRToolRefusesUnknownProvider pins fail-closed behaviour: an
// unrecognised provider is refused, never assumed to be gh.
func TestProbePRToolRefusesUnknownProvider(t *testing.T) {
	original := prToolProbe
	t.Cleanup(func() { prToolProbe = original })
	prToolProbe = func(string) error { t.Fatal("probe must not run for an unknown provider"); return nil }

	if err := ProbePRTool("gitlab"); err == nil {
		t.Fatal("ProbePRTool error = nil, want an unknown-provider refusal")
	}
}

// TestPrToolProbeDefaultRunsRealBinary exercises the default probe closure so
// the real exec path is covered, not only the injected double.
func TestPrToolProbeDefaultRunsRealBinary(t *testing.T) {
	if err := prToolProbe("true"); err != nil {
		t.Fatalf("probe of /usr/bin/true failed: %v", err)
	}
	if err := prToolProbe("mivia-no-such-binary-probe"); err == nil {
		t.Fatal("probe of a missing binary returned nil")
	}
}
