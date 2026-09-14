package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

func TestToolVerbMap_KnownTools(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"read_file":      "Reading",
		"write_file":     "Writing",
		"search_replace": "Editing",
		"grep":           "Searching",
		"search":         "Searching the web",
		"glob":           "Finding files",
		"list_dir":       "Listing",
		"run_command":    "Running",
		"delegate":       "Delegating",
		"dispatch_tasks": "Dispatching tasks",
		"parallel":       "Running tools in parallel",
		"prune":          "Pruning context",
	}
	for name, want := range cases {
		if got := toolVerb(name); got != want {
			t.Errorf("toolVerb(%q)=%q want %q", name, got, want)
		}
	}
}

func TestToolStatusLine_ReadFile(t *testing.T) {
	t.Parallel()
	got := ToolStatusLine("read_file", `{"path":"internal/foo.go"}`)
	if !strings.Contains(got, "Reading") {
		t.Fatalf("expected Reading verb, got %q", got)
	}
	if !strings.Contains(got, "foo.go") {
		t.Fatalf("expected path fragment, got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}

// TestToolStatusLine_RedactsSecrets backs INV-TUI-7, which now reads "redacts
// secrets when a redaction policy is configured". Not parallel: the policy is
// process-wide.
func TestToolStatusLine_RedactsSecrets(t *testing.T) {
	installTestRedactionPolicy(t)
	got := ToolStatusLine("run_command", `{"argv":["echo"],"password":"super-secret-token-value"}`)
	// Status must not leak the secret token value.
	if strings.Contains(got, "super-secret-token-value") {
		t.Fatalf("secret leaked into status: %q", got)
	}
	// Also exercise redactPreview path via free-form detail.
	got2 := ToolStatusLine("grep", `password=hunter2 pattern=auth`)
	if strings.Contains(got2, "hunter2") {
		t.Fatalf("password leaked: %q", got2)
	}
}

// TestToolStatusLine_WithoutPolicyShowsSecrets is the other half of INV-TUI-7:
// with no policy configured the status line redacts nothing.
func TestToolStatusLine_WithoutPolicyShowsSecrets(t *testing.T) {
	redact.SetPolicy(nil)
	got := ToolStatusLine("grep", `password=hunter2 pattern=auth`)
	if !strings.Contains(got, "hunter2") {
		t.Fatalf("unconfigured workspace redacted status line: %q", got)
	}
}

func TestInterimRejectedWhenTooShort(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "  ", "OK.", "…", "a", "queued", "running", "!!!"} {
		if ShouldCommitInterim(s) {
			t.Errorf("should reject %q", s)
		}
	}
}

func TestInterimAcceptedWhenRealProse(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"I'll search the codebase first.",
		"Next I'll read the entrypoint.",
		"Looking into auth next.",
	} {
		if !ShouldCommitInterim(s) {
			t.Errorf("should accept %q", s)
		}
	}
}
