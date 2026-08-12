package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// End-to-end trust contract for the write tools, at the level that matters
// most: what the agent LOOP executes for a tool call must be the literal
// application of that call's arguments - nothing more. A harness layer that
// re-derives or "completes" an edit from surrounding intent (extra content,
// extra files) violates the contract; this test walks the real loop, the
// real dispatcher, and the real filesystem, and asserts the entire workspace
// changed by exactly the literal edit and nothing else.
//
// The tool-level half of the contract is pinned in
// internal/tools/literal_application_test.go.

func snapshotTree(t *testing.T, abs string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		snap[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestIntegration_SearchReplaceMutatesExactlyItsLiteralArgs(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "applying the edit",
			toolCalls: []provider.ToolCall{toolCall("call_edit", "search_replace",
				`{"path":"f.go","old_string":"return one","new_string":"return two"}`)},
		},
		{content: "edit applied"},
	})
	writeWorkspaceFile(t, h.ws.Abs, "f.go", "package x\n\nfunc foo() {\n\treturn one\n}\n", 0o644)
	writeWorkspaceFile(t, h.ws.Abs, "untouched.go", "package x\n\nvar keep = 1\n", 0o644)

	before := snapshotTree(t, h.ws.Abs)
	runToolStep(t, h, "update the function", "call_edit")

	want := "package x\n\nfunc foo() {\n\treturn two\n}\n"
	if got := readWorkspaceFile(t, filepath.Join(h.ws.Abs, "f.go")); got != want {
		t.Fatalf("f.go after the turn = %q, want the literal result %q", got, want)
	}

	after := snapshotTree(t, h.ws.Abs)
	for rel, sum := range before {
		if rel == "f.go" {
			continue
		}
		if after[rel] != sum {
			t.Fatalf("file %q changed during the turn without a matching write call", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Fatalf("unexpected new file %q appeared during the turn", rel)
		}
	}
}

func TestIntegration_MultiEditMutatesExactlyItsLiteralArgs(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "applying the batch",
			toolCalls: []provider.ToolCall{toolCall("call_batch", "multi_edit",
				`{"path":"seq.txt","edits":[`+
					`{"old_string":"one","new_string":"ONE"},`+
					`{"old_string":"two","new_string":"TWO"}]}`)},
		},
		{content: "batch applied"},
	})
	writeWorkspaceFile(t, h.ws.Abs, "seq.txt", "one\ntwo\n", 0o644)
	writeWorkspaceFile(t, h.ws.Abs, "other.txt", "keep me\n", 0o644)

	before := snapshotTree(t, h.ws.Abs)
	runToolStep(t, h, "apply the batch", "call_batch")

	want := "ONE\nTWO\n"
	if got := readWorkspaceFile(t, filepath.Join(h.ws.Abs, "seq.txt")); got != want {
		t.Fatalf("seq.txt after the turn = %q, want the literal result %q", got, want)
	}

	after := snapshotTree(t, h.ws.Abs)
	for rel, sum := range before {
		if rel == "seq.txt" {
			continue
		}
		if after[rel] != sum {
			t.Fatalf("file %q changed during the turn without a matching write call", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Fatalf("unexpected new file %q appeared during the turn", rel)
		}
	}
}
