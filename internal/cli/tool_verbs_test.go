package cli

import (
	"strings"
	"testing"
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
	got := toolStatusLine("read_file", `{"path":"internal/foo.go"}`)
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

func TestToolStatusLine_RedactsSecrets(t *testing.T) {
	t.Parallel()
	got := toolStatusLine("run_command", `{"argv":["echo"],"password":"super-secret-token-value"}`)
	// Status must not leak the secret token value.
	if strings.Contains(got, "super-secret-token-value") {
		t.Fatalf("secret leaked into status: %q", got)
	}
	// Also exercise redactPreview path via free-form detail.
	got2 := toolStatusLine("grep", `password=hunter2 pattern=auth`)
	if strings.Contains(got2, "hunter2") {
		t.Fatalf("password leaked: %q", got2)
	}
}

func TestToolBatchStatusLine_ParallelNotSpam(t *testing.T) {
	t.Parallel()
	starts := []bridgeToolEvt{
		{Start: true, Name: "parallel", Detail: "2 tools"},
		{Start: true, ToolCallID: "a", Name: "list_dir", Detail: `{"path":"."}`},
		{Start: true, ToolCallID: "b", Name: "glob", Detail: `{"pattern":"*"}`},
	}
	got := toolBatchStatusLine(starts)
	if !strings.Contains(got, "2 tools") && !strings.Contains(got, "Running") {
		t.Fatalf("expected batch summary, got %q", got)
	}
	// Single real tool falls through to toolStatusLine.
	one := toolBatchStatusLine([]bridgeToolEvt{
		{Start: true, ToolCallID: "a", Name: "read_file", Detail: `{"path":"a.go"}`},
	})
	if !strings.Contains(one, "Reading") {
		t.Fatalf("single tool status=%q", one)
	}
}

func TestToolBatchStatusDetailListsTools(t *testing.T) {
	t.Parallel()
	starts := []bridgeToolEvt{
		{Start: true, ToolCallID: "a", Name: "list_dir", Detail: `{"path":"."}`},
		{Start: true, ToolCallID: "b", Name: "glob", Detail: `{"pattern":"*"}`},
	}
	got := toolBatchStatusDetail(starts)
	if !strings.Contains(got, "Running 2 tools") {
		t.Fatalf("summary missing: %q", got)
	}
	if !strings.Contains(got, "Listing") || !strings.Contains(got, "Finding") {
		t.Fatalf("per-tool lines missing: %q", got)
	}
	if strings.Count(got, "\n") < 2 {
		t.Fatalf("want multi-line detail, got %q", got)
	}
	// Single tool stays one line.
	one := toolBatchStatusDetail([]bridgeToolEvt{
		{Start: true, ToolCallID: "a", Name: "read_file", Detail: `{"path":"a.go"}`},
	})
	if strings.Contains(one, "\n") {
		t.Fatalf("single tool must be one line: %q", one)
	}
}

func TestInterimRejectedWhenTooShort(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "  ", "OK.", "…", "a", "queued", "running", "!!!"} {
		if shouldCommitInterim(s) {
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
		if !shouldCommitInterim(s) {
			t.Errorf("should accept %q", s)
		}
	}
}

func TestShouldFollowOutput(t *testing.T) {
	t.Parallel()
	if shouldFollowOutput(true, false, true) {
		t.Fatal("scrolled up must drop follow")
	}
	if !shouldFollowOutput(false, true, false) {
		t.Fatal("at bottom must re-enable follow")
	}
	if !shouldFollowOutput(true, false, false) {
		t.Fatal("sticky follow without scroll keeps follow")
	}
	if shouldFollowOutput(false, false, false) {
		t.Fatal("not follow and not at bottom stays unfollowed")
	}
}
