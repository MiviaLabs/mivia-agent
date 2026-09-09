package cliworkflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Discovery no longer fails a whole directory when one file is unusable - it
// reports that file and keeps going, so one oversized or symlinked definition
// cannot take `workflow run` and every unrelated workflow down with it. That
// leaves `workflows validate` responsible for SURFACING the broken file: it
// must name it, fail the command, and still validate its healthy siblings.
// Nothing covered that, so the branch was dead weight the sweep could not kill.
func TestWorkflowsValidateReportsAnUnusableDefinition(t *testing.T) {
	root := t.TempDir()
	wfDir := workspace.NamespacePath(root, "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A healthy sibling, to prove the broken one does not hide it.
	healthy := `version = 1
name = "healthy"
initial_step = "one"

[[steps]]
id = "one"
kind = "human_gate"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfDir, "healthy.toml"), []byte(healthy), 0o600); err != nil {
		t.Fatal(err)
	}
	// Oversized: readRegularWorkflowFile refuses it, so discovery reports it
	// with a reason and no bytes.
	big := make([]byte, 70000)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(wfDir, "oversized.toml"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	err := runWorkflowsValidate([]string{"--workspace", root}, &stdout, &stderr)
	if err == nil {
		t.Fatal("workflows validate passed with an unusable definition in the directory")
	}
	out := stdout.String()
	if !strings.Contains(out, "oversized") {
		t.Errorf("output does not name the broken definition:\n%s", out)
	}
	if !strings.Contains(out, "healthy") {
		t.Errorf("the healthy sibling was not validated; one bad file still hides the rest:\n%s", out)
	}
}

// TestWorkflowsValidatePassesOnAHealthyDirectory is the complement, so the
// assertion above cannot be satisfied by failing everything.
func TestWorkflowsValidatePassesOnAHealthyDirectory(t *testing.T) {
	root := t.TempDir()
	wfDir := workspace.NamespacePath(root, "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	healthy := `version = 1
name = "healthy"
initial_step = "one"

[[steps]]
id = "one"
kind = "human_gate"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfDir, "healthy.toml"), []byte(healthy), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if err := runWorkflowsValidate([]string{"--workspace", root}, &stdout, &stderr); err != nil {
		t.Fatalf("workflows validate on a healthy directory = %v\n%s", err, stdout.String())
	}
}
