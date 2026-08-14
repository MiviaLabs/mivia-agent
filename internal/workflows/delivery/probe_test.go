package delivery

import (
	"errors"
	"runtime"
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
	probe := "true"
	if runtime.GOOS == "windows" {
		// No `true` binary exists on Windows; cmd.exe is the always-present
		// probe target and exercises the same LookPath path.
		probe = "cmd"
	}
	if err := prToolProbe(probe); err != nil {
		t.Fatalf("probe of %s failed: %v", probe, err)
	}
	if err := prToolProbe("mivia-no-such-binary-probe"); err == nil {
		t.Fatal("probe of a missing binary returned nil")
	}
}

// TestPrToolProgramUnknownProviderMessage pins the support-framed refusal:
// the error states that only "github" is supported, not that a tool is
// missing for a provider that could otherwise work.
func TestPrToolProgramUnknownProviderMessage(t *testing.T) {
	_, err := prToolProgram("gitlab")
	if err == nil {
		t.Fatal(`prToolProgram("gitlab") error = nil, want a refusal`)
	}
	if !strings.Contains(err.Error(), `provider "gitlab" is not supported (only "github" is currently supported)`) {
		t.Fatalf("prToolProgram error = %q, want the only-github support message", err)
	}
}
